package main

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpverifier"
	"github.com/gnanam1990/flowops/internal/ascpverifierruntime"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type startupConfig struct {
	databaseURL      string
	listenAddress    string
	chainID          string
	escrowContract   string
	verifierEpoch    uint64
	softwareHash     string
	attestationTTL   time.Duration
	governanceMaxAge time.Duration
	requestSkew      time.Duration
	intakeKeys       map[string][]byte
	signer           *fileSigner
}

type fileSigner struct {
	keyPath string
	address common.Address
}

func (s *fileSigner) Address() common.Address { return s.address }

func (s *fileSigner) SignDigest(_ context.Context, digest common.Hash) ([]byte, error) {
	key, address, err := readSignerKey(s.keyPath)
	if err != nil || address != s.address {
		return nil, errors.New("verifier signer key is unavailable or changed")
	}
	return crypto.Sign(digest[:], key)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP verifier runtime stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", config.databaseURL)
	if err != nil {
		return fmt.Errorf("open verifier PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("connect verifier PostgreSQL: %w", err)
	}
	var schema string
	if err := db.QueryRowContext(startupCtx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		return errors.New("verifier PostgreSQL current schema must be public")
	}
	journal, err := ascpverifier.NewPostgresDecisionJournal(db)
	if err != nil {
		return err
	}
	nonces, err := ascpverifier.NewPostgresNonceSource(db)
	if err != nil {
		return err
	}
	gate, err := ascpverifier.NewPostgresVerifierKeyGate(db, config.governanceMaxAge)
	if err != nil {
		return err
	}
	service, err := ascpverifier.New(ascpverifier.Config{
		VerifierEpoch: config.verifierEpoch, VerifierSoftwareHash: config.softwareHash,
		AttestationTTL: config.attestationTTL, Clock: time.Now,
		Engines: map[ascpverifier.Class]ascpverifier.Engine{ascpverifier.ClassStructuredData: ascpverifier.StructuredDataEngine{}},
		Signer:  config.signer, Nonces: nonces, VerifierKeyGate: gate, DecisionJournal: journal,
	})
	if err != nil {
		return err
	}
	replays, err := ascpverifierruntime.NewPostgresReplayGuard(db)
	if err != nil {
		return err
	}
	if _, err := replays.PruneExpired(startupCtx); err != nil {
		return err
	}
	handler, err := ascpverifierruntime.NewHandler(ascpverifierruntime.HandlerConfig{
		Verifier: service, ReplayGuard: replays, Keys: config.intakeKeys, Clock: time.Now,
		MaxSkew: config.requestSkew, ChainID: config.chainID, EscrowContract: config.escrowContract,
	})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 5*time.Minute + 10*time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", config.listenAddress)
	if err != nil {
		return fmt.Errorf("listen for verifier requests: %w", err)
	}
	slog.Info("ASCP verifier runtime started", "address", config.listenAddress, "chainId", config.chainID,
		"escrowContract", config.escrowContract, "verifier", strings.ToLower(config.signer.address.Hex()), "epoch", config.verifierEpoch)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	go pruneVerifierReplays(ctx, replays)
	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func pruneVerifierReplays(ctx context.Context, replays *ascpverifierruntime.PostgresReplayGuard) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pruneCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			deleted, err := replays.PruneExpired(pruneCtx)
			cancel()
			if err != nil {
				slog.Error("ASCP verifier replay pruning failed", "error", err)
				continue
			}
			slog.Info("ASCP verifier replay pruning completed", "deleted", deleted)
		}
	}
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		databaseURL:    strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_DATABASE_URL")),
		listenAddress:  strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_LISTEN_ADDRESS")),
		chainID:        strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_CHAIN_ID")),
		escrowContract: strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_ESCROW_CONTRACT"))),
		softwareHash:   strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_SOFTWARE_HASH")),
		attestationTTL: 10 * time.Minute, governanceMaxAge: time.Minute, requestSkew: 30 * time.Second,
	}
	if err := validateDatabaseURL(config.databaseURL); err != nil {
		return startupConfig{}, err
	}
	if err := validateListenAddress(config.listenAddress); err != nil {
		return startupConfig{}, err
	}
	if config.chainID != "8453" && config.chainID != "84532" {
		return startupConfig{}, errors.New("FLOWOPS_VERIFIER_CHAIN_ID must be Base mainnet or Sepolia")
	}
	if !common.IsHexAddress(config.escrowContract) || common.HexToAddress(config.escrowContract) == (common.Address{}) {
		return startupConfig{}, errors.New("FLOWOPS_VERIFIER_ESCROW_CONTRACT is invalid")
	}
	var err error
	if config.verifierEpoch, err = parseUint("FLOWOPS_VERIFIER_EPOCH"); err != nil || config.verifierEpoch == 0 || config.verifierEpoch > math.MaxInt64 {
		return startupConfig{}, errors.New("FLOWOPS_VERIFIER_EPOCH must be positive")
	}
	if !canonicalHash(config.softwareHash) {
		return startupConfig{}, errors.New("FLOWOPS_VERIFIER_SOFTWARE_HASH is invalid")
	}
	for name, target := range map[string]*time.Duration{
		"FLOWOPS_VERIFIER_ATTESTATION_TTL":    &config.attestationTTL,
		"FLOWOPS_VERIFIER_GOVERNANCE_MAX_AGE": &config.governanceMaxAge,
		"FLOWOPS_VERIFIER_REQUEST_SKEW":       &config.requestSkew,
	} {
		if *target, err = parseDuration(name, *target); err != nil {
			return startupConfig{}, err
		}
	}
	if config.attestationTTL < time.Second || config.attestationTTL > 15*time.Minute ||
		config.governanceMaxAge < time.Second || config.governanceMaxAge > 10*time.Minute ||
		config.requestSkew < time.Second || config.requestSkew > time.Minute {
		return startupConfig{}, errors.New("verifier timing configuration is outside safe bounds")
	}
	if config.intakeKeys, err = parseKeyMap(os.Getenv("FLOWOPS_VERIFIER_INTAKE_KEYS_JSON")); err != nil {
		return startupConfig{}, err
	}
	keyPath := strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_SIGNER_KEY_FILE"))
	_, address, err := readSignerKey(keyPath)
	if err != nil {
		return startupConfig{}, err
	}
	expected := strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_VERIFIER_SIGNER_ADDRESS")))
	if !common.IsHexAddress(expected) || common.HexToAddress(expected) != address {
		return startupConfig{}, errors.New("verifier signer address does not match key")
	}
	config.signer = &fileSigner{keyPath: keyPath, address: address}
	return config, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return errors.New("FLOWOPS_VERIFIER_DATABASE_URL must be a PostgreSQL URL")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("FLOWOPS_VERIFIER_DATABASE_URL must set sslmode=verify-full exactly once")
	}
	for _, override := range []string{"host", "hostaddr", "port", "dbname", "database", "user", "password", "search_path", "options"} {
		if parsed.Query().Has(override) {
			return fmt.Errorf("FLOWOPS_VERIFIER_DATABASE_URL must not override %s", override)
		}
	}
	return nil
}

func validateListenAddress(raw string) error {
	host, port, err := net.SplitHostPort(raw)
	value, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || value < 1024 || value > 65535 || host != "127.0.0.1" && host != "::1" {
		return errors.New("FLOWOPS_VERIFIER_LISTEN_ADDRESS must be explicit loopback with a non-privileged port")
	}
	return nil
}

func readSignerKey(rawPath string) (*ecdsa.PrivateKey, common.Address, error) {
	path := strings.TrimSpace(rawPath)
	if !filepath.IsAbs(path) {
		return nil, common.Address{}, errors.New("FLOWOPS_VERIFIER_SIGNER_KEY_FILE must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, common.Address{}, errors.New("verifier signer key must be a private regular file")
	}
	defer func() { _ = file.Close() }()
	info, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) ||
		!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > 256 {
		return nil, common.Address{}, errors.New("verifier signer key must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 257))
	encoded := strings.TrimSpace(string(raw))
	if err != nil || len(encoded) != 64 || encoded != strings.ToLower(encoded) {
		return nil, common.Address{}, errors.New("verifier signer key file is invalid")
	}
	key, err := crypto.HexToECDSA(encoded)
	if err != nil {
		return nil, common.Address{}, errors.New("verifier signer key file is invalid")
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func parseKeyMap(raw string) (map[string][]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if rejectDuplicateObjectKeys([]byte(trimmed)) != nil {
		return nil, errors.New("FLOWOPS_VERIFIER_INTAKE_KEYS_JSON contains duplicate keys")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var encoded map[string]string
	if err := decoder.Decode(&encoded); err != nil || len(encoded) == 0 || len(encoded) > 32 {
		return nil, errors.New("FLOWOPS_VERIFIER_INTAKE_KEYS_JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("FLOWOPS_VERIFIER_INTAKE_KEYS_JSON must contain one value")
	}
	result := make(map[string][]byte, len(encoded))
	for keyID, value := range encoded {
		key, err := base64.StdEncoding.DecodeString(value)
		if len(keyID) < 8 || err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != value {
			return nil, errors.New("FLOWOPS_VERIFIER_INTAKE_KEYS_JSON contains an invalid key")
		}
		result[keyID] = key
	}
	return result, nil
}

func rejectDuplicateObjectKeys(raw []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("expected object")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return errors.New("invalid key")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func parseUint(name string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(os.Getenv(name)), 10, 64)
}

func parseDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func canonicalHash(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	return len(value) == 66 && strings.HasPrefix(value, "0x") && value == strings.ToLower(value) && err == nil && len(decoded) == 32 && common.BytesToHash(decoded) != (common.Hash{})
}
