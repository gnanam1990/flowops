package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpevents"
	"github.com/gnanam1990/flowops/internal/ascprecovery"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type startupConfig struct {
	databaseURL       string
	listenAddress     string
	wormURL           string
	remoteHeadURL     string
	writerKeys        map[string][]byte
	checkpointKeys    map[string]ed25519.PublicKey
	attestationKeyID  string
	attestationKey    ed25519.PrivateKey
	attestationPublic ed25519.PublicKey
	externalTimeout   time.Duration
	verificationLimit time.Duration
	proofTTL          time.Duration
	cacheTTL          time.Duration
}

var configKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP event recovery stopped", "error", err)
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
		return fmt.Errorf("open recovery PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	if err := db.PingContext(startupCtx); err != nil {
		cancel()
		return fmt.Errorf("connect to recovery PostgreSQL: %w", err)
	}
	if err := requirePublicSchema(startupCtx, db); err != nil {
		cancel()
		return err
	}
	cancel()
	store, err := ascpevents.NewPostgresStore(db)
	if err != nil {
		return err
	}
	worm, err := ascprecovery.NewHTTPSWORMReader(config.wormURL, config.externalTimeout)
	if err != nil {
		return fmt.Errorf("create immutable checkpoint reader: %w", err)
	}
	remote, err := ascprecovery.NewHTTPSRemoteHeadReader(config.remoteHeadURL, config.externalTimeout)
	if err != nil {
		return fmt.Errorf("create remote event-head reader: %w", err)
	}
	recoverySource, err := ascprecovery.NewEventRecoverySource(store, worm, remote, config.writerKeys, config.checkpointKeys)
	if err != nil {
		return err
	}
	service, err := ascprecovery.NewService(recoverySource, ascprecovery.Config{KeyID: config.attestationKeyID,
		PrivateKey: config.attestationKey, ProofTTL: config.proofTTL, CacheTTL: config.cacheTTL,
		VerifyTimeout: config.verificationLimit, Clock: time.Now})
	if err != nil {
		return err
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, config.verificationLimit)
	_, err = service.Latest(verifyCtx)
	verifyCancel()
	if err != nil {
		return fmt.Errorf("startup event-recovery verification: %w", err)
	}
	handler, err := ascprecovery.NewHandler(service, func(err error) { slog.Error("event-recovery verification failed", "error", err) })
	if err != nil {
		return err
	}
	server := &http.Server{Addr: config.listenAddress, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: config.verificationLimit + 5*time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", config.listenAddress)
	if err != nil {
		return fmt.Errorf("listen for event-recovery requests: %w", err)
	}
	slog.Info("ASCP event recovery started", "address", config.listenAddress, "attestationKeyId", config.attestationKeyID)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down event-recovery server: %w", err)
		}
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve event-recovery requests: %w", err)
	}
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		databaseURL:      strings.TrimSpace(os.Getenv("FLOWOPS_RECOVERY_DATABASE_URL")),
		listenAddress:    strings.TrimSpace(os.Getenv("FLOWOPS_RECOVERY_LISTEN_ADDRESS")),
		wormURL:          strings.TrimSpace(os.Getenv("FLOWOPS_RECOVERY_WORM_URL")),
		remoteHeadURL:    strings.TrimSpace(os.Getenv("FLOWOPS_RECOVERY_REMOTE_HEAD_URL")),
		attestationKeyID: strings.TrimSpace(os.Getenv("FLOWOPS_RECOVERY_ATTESTATION_KEY_ID")),
		externalTimeout:  5 * time.Second, verificationLimit: 20 * time.Second, proofTTL: 2 * time.Minute, cacheTTL: 2 * time.Second,
	}
	if err := validateDatabaseURL(config.databaseURL); err != nil {
		return startupConfig{}, err
	}
	if err := validateListenAddress(config.listenAddress); err != nil {
		return startupConfig{}, err
	}
	var err error
	if config.writerKeys, err = parseKeyMap(os.Getenv("FLOWOPS_RECOVERY_WRITER_KEYS_JSON"), 32); err != nil {
		return startupConfig{}, fmt.Errorf("FLOWOPS_RECOVERY_WRITER_KEYS_JSON: %w", err)
	}
	checkpointRaw, err := parseKeyMap(os.Getenv("FLOWOPS_RECOVERY_CHECKPOINT_KEYS_JSON"), ed25519.PublicKeySize)
	if err != nil {
		return startupConfig{}, fmt.Errorf("FLOWOPS_RECOVERY_CHECKPOINT_KEYS_JSON: %w", err)
	}
	config.checkpointKeys = make(map[string]ed25519.PublicKey, len(checkpointRaw))
	for keyID, key := range checkpointRaw {
		config.checkpointKeys[keyID] = ed25519.PublicKey(key)
	}
	if !configKeyIDPattern.MatchString(config.attestationKeyID) {
		return startupConfig{}, errors.New("FLOWOPS_RECOVERY_ATTESTATION_KEY_ID is invalid")
	}
	if config.attestationKey, err = readPrivateKeyFile(os.Getenv("FLOWOPS_RECOVERY_ATTESTATION_KEY_FILE")); err != nil {
		return startupConfig{}, err
	}
	if config.attestationPublic, err = parsePublicKey(os.Getenv("FLOWOPS_RECOVERY_ATTESTATION_PUBLIC_KEY_B64")); err != nil {
		return startupConfig{}, err
	}
	if !bytes.Equal(config.attestationKey.Public().(ed25519.PublicKey), config.attestationPublic) {
		return startupConfig{}, errors.New("event-recovery attestation private key does not match the configured public key")
	}
	for name, target := range map[string]*time.Duration{
		"FLOWOPS_RECOVERY_EXTERNAL_TIMEOUT":     &config.externalTimeout,
		"FLOWOPS_RECOVERY_VERIFICATION_TIMEOUT": &config.verificationLimit,
		"FLOWOPS_RECOVERY_PROOF_TTL":            &config.proofTTL,
		"FLOWOPS_RECOVERY_CACHE_TTL":            &config.cacheTTL,
	} {
		if *target, err = parseDuration(name, *target); err != nil {
			return startupConfig{}, err
		}
	}
	if config.externalTimeout < time.Second || config.externalTimeout > 10*time.Second ||
		config.verificationLimit < 2*config.externalTimeout+5*time.Second || config.verificationLimit > time.Minute ||
		config.proofTTL < time.Second || config.proofTTL > 5*time.Minute || config.cacheTTL <= 0 ||
		config.cacheTTL > 5*time.Second || config.cacheTTL >= config.proofTTL {
		return startupConfig{}, errors.New("event-recovery timing configuration is outside safe bounds")
	}
	if _, err := ascprecovery.NewHTTPSWORMReader(config.wormURL, config.externalTimeout); err != nil {
		return startupConfig{}, errors.New("FLOWOPS_RECOVERY_WORM_URL is invalid")
	}
	if _, err := ascprecovery.NewHTTPSRemoteHeadReader(config.remoteHeadURL, config.externalTimeout); err != nil {
		return startupConfig{}, errors.New("FLOWOPS_RECOVERY_REMOTE_HEAD_URL is invalid")
	}
	return config, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return errors.New("FLOWOPS_RECOVERY_DATABASE_URL must be a PostgreSQL URL with a database path")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("FLOWOPS_RECOVERY_DATABASE_URL must set sslmode=verify-full exactly once")
	}
	for _, override := range []string{"host", "hostaddr", "port", "dbname", "database", "user", "password", "search_path", "options"} {
		if parsed.Query().Has(override) {
			return fmt.Errorf("FLOWOPS_RECOVERY_DATABASE_URL must not override %s in query parameters", override)
		}
	}
	return nil
}

func validateListenAddress(raw string) error {
	host, port, err := net.SplitHostPort(raw)
	parsedPort, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || parsedPort < 1024 || parsedPort > 65535 {
		return errors.New("FLOWOPS_RECOVERY_LISTEN_ADDRESS must use an explicit non-privileged port")
	}
	if host != "0.0.0.0" && host != "127.0.0.1" && host != "::" && host != "::1" {
		return errors.New("FLOWOPS_RECOVERY_LISTEN_ADDRESS must bind an explicit local interface")
	}
	return nil
}

func requirePublicSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("recovery PostgreSQL connection is required")
	}
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return fmt.Errorf("read recovery PostgreSQL current schema: %w", err)
	}
	if schema != "public" {
		return errors.New("recovery PostgreSQL current schema must be public")
	}
	return nil
}

func parseKeyMap(raw string, size int) (map[string][]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 64<<10 || size < 1 || rejectDuplicateKeys([]byte(trimmed)) != nil {
		return nil, errors.New("key map must be one strict JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	var encoded map[string]string
	if err := decoder.Decode(&encoded); err != nil || len(encoded) == 0 || len(encoded) > 32 {
		return nil, errors.New("key map must be one strict JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("key map must contain one JSON value")
	}
	result := make(map[string][]byte, len(encoded))
	for keyID, value := range encoded {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if !configKeyIDPattern.MatchString(keyID) || err != nil || len(decoded) != size || base64.StdEncoding.EncodeToString(decoded) != value {
			return nil, errors.New("key map contains an invalid key")
		}
		result[keyID] = decoded
	}
	return result, nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("key map must be an object")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return errors.New("key map key is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("key map key is duplicated")
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

func readPrivateKeyFile(rawPath string) (ed25519.PrivateKey, error) {
	path := strings.TrimSpace(rawPath)
	if !filepath.IsAbs(path) {
		return nil, errors.New("FLOWOPS_RECOVERY_ATTESTATION_KEY_FILE must be an absolute path")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("FLOWOPS_RECOVERY_ATTESTATION_KEY_FILE must be a private regular file")
	}
	defer func() { _ = file.Close() }()
	info, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) ||
		!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > 1024 {
		return nil, errors.New("FLOWOPS_RECOVERY_ATTESTATION_KEY_FILE must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return nil, errors.New("read event-recovery attestation key file")
	}
	encoded := strings.TrimSpace(string(raw))
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PrivateKeySize || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, errors.New("event-recovery attestation key file is invalid")
	}
	return ed25519.PrivateKey(key), nil
}

func parsePublicKey(raw string) (ed25519.PublicKey, error) {
	encoded := strings.TrimSpace(raw)
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, errors.New("FLOWOPS_RECOVERY_ATTESTATION_PUBLIC_KEY_B64 is invalid")
	}
	return ed25519.PublicKey(key), nil
}

func parseDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}
