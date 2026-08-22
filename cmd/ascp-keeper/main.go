package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/ascpkeeper"
	"github.com/gnanam1990/flowops/internal/ascpleadership"
	"github.com/gnanam1990/flowops/internal/securefile"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type startupConfig struct {
	databaseURL                                            string
	keeperID                                               string
	gasPayer                                               string
	chainID                                                uint64
	sockets                                                map[string]string
	interval, cycleTimeout, leaseDuration, boundaryTimeout time.Duration
	batchSize, expiryLimit, maxFeeBumps                    int
	maxGasLimit                                            uint64
	feeCap                                                 ascpkeeper.Fee
	signerTokenPath                                        string
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP keeper runtime stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	if err := ascpkeeper.ValidateDistinctSockets(config.sockets); err != nil {
		return fmt.Errorf("validate keeper boundary sockets: %w", err)
	}
	signerCapability, err := loadSignerCapability(config.signerTokenPath)
	if err != nil {
		return err
	}
	defer clear(signerCapability)
	db, err := sql.Open("pgx", config.databaseURL)
	if err != nil {
		return fmt.Errorf("open keeper PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("connect keeper PostgreSQL: %w", err)
	}
	var schema string
	if err := db.QueryRowContext(startupCtx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		return errors.New("keeper PostgreSQL current schema must be public")
	}
	boundaries := make(map[string]*ascpkeeper.UnixBoundary, len(config.sockets))
	for name, path := range config.sockets {
		var boundary *ascpkeeper.UnixBoundary
		if name == "artifact" {
			boundary, err = ascpkeeper.NewAuthenticatedUnixBoundary(name, path, config.boundaryTimeout, signerCapability)
		} else {
			boundary, err = ascpkeeper.NewUnixBoundary(name, path, config.boundaryTimeout)
		}
		if err != nil {
			return fmt.Errorf("configure %s boundary: %w", name, err)
		}
		boundaries[name] = boundary
	}
	if err := checkBoundaries(startupCtx, boundaries); err != nil {
		return err
	}
	store, err := ascpkeeper.NewPostgresStore(db)
	if err != nil {
		return err
	}
	leadership, err := ascpleadership.NewPostgres(db, "public")
	if err != nil {
		return fmt.Errorf("create keeper leadership gate: %w", err)
	}
	artifactClient, err := ascpkeeper.NewUnixArtifactClient(boundaries["artifact"])
	if err != nil {
		return err
	}
	artifacts, err := ascpkeeper.NewSignerArtifactSource(artifactClient)
	if err != nil {
		return err
	}
	assembler, err := ascpkeeper.NewUnixAssembler(boundaries["assembler"])
	if err != nil {
		return err
	}
	verifier, err := ascpkeeper.NewUnixBindingVerifier(boundaries["verifier"])
	if err != nil {
		return err
	}
	wallet, err := ascpkeeper.NewUnixWallet(boundaries["wallet"])
	if err != nil {
		return err
	}
	sealer, err := ascpkeeper.NewUnixSealer(boundaries["sealer"])
	if err != nil {
		return err
	}
	chain, err := ascpkeeper.NewUnixChainBoundary(boundaries["chain"])
	if err != nil {
		return err
	}
	broadcaster, err := ascpkeeper.NewUnixBroadcaster(boundaries["broadcast"])
	if err != nil {
		return err
	}
	service, err := ascpkeeper.NewService(store, artifacts, assembler, verifier, wallet, sealer, broadcaster, chain, chain, chain, chain, leadership, ascpkeeper.Config{
		KeeperID: config.keeperID, GasPayer: config.gasPayer, ChainID: config.chainID, LeaseDuration: config.leaseDuration,
		MaxFeeBumps: config.maxFeeBumps, MaxGasLimit: config.maxGasLimit, FeeCap: config.feeCap,
	})
	if err != nil {
		return fmt.Errorf("create keeper service: %w", err)
	}
	expiry, err := ascpkeeper.NewExpiryScanner(store, chain, config.keeperID, config.gasPayer, config.chainID)
	if err != nil {
		return fmt.Errorf("create keeper expiry scanner: %w", err)
	}
	worker, err := ascpkeeper.NewWorker(service, expiry, ascpkeeper.WorkerConfig{
		Interval: config.interval, CycleTimeout: config.cycleTimeout, BatchSize: config.batchSize, ExpiryLimit: config.expiryLimit,
		OnCycle: func(cycle ascpkeeper.WorkerCycle) {
			slog.Info("ASCP keeper cycle completed", "expiryEnqueued", cycle.ExpiryEnqueued, "observed", cycle.Observed,
				"relayed", cycle.Relayed, "submitted", cycle.Submitted, "confirmed", cycle.Confirmed,
				"finalized", cycle.Finalized, "reverted", cycle.Reverted, "reorged", cycle.Reorged,
				"ambiguous", cycle.Ambiguous, "timedOut", cycle.TimedOut, "deadLetter", cycle.DeadLetter)
		},
	})
	if err != nil {
		return fmt.Errorf("create keeper worker: %w", err)
	}
	slog.Info("ASCP keeper runtime started", "keeperId", config.keeperID, "gasPayer", config.gasPayer, "chainId", config.chainID)
	return worker.Run(ctx)
}

func checkBoundaries(ctx context.Context, boundaries map[string]*ascpkeeper.UnixBoundary) error {
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(boundaries))
	for name, boundary := range boundaries {
		name, boundary := name, boundary
		go func() { results <- result{name, boundary.Check(ctx)} }()
	}
	var joined error
	for range boundaries {
		check := <-results
		if check.err != nil {
			joined = errors.Join(joined, fmt.Errorf("check %s boundary: %w", check.name, check.err))
		}
	}
	return joined
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		databaseURL:     strings.TrimSpace(os.Getenv("FLOWOPS_KEEPER_DATABASE_URL")),
		keeperID:        strings.TrimSpace(os.Getenv("FLOWOPS_KEEPER_ID")),
		gasPayer:        strings.TrimSpace(os.Getenv("FLOWOPS_KEEPER_GAS_PAYER")),
		signerTokenPath: os.Getenv("FLOWOPS_KEEPER_SIGNER_TOKEN_FILE"),
		interval:        time.Minute, cycleTimeout: 50 * time.Second, leaseDuration: 55 * time.Second, boundaryTimeout: 3 * time.Second,
		batchSize: 20, expiryLimit: 100, maxFeeBumps: 3, maxGasLimit: 1_000_000,
		feeCap:  ascpkeeper.Fee{MaxFeePerGasWei: strings.TrimSpace(os.Getenv("FLOWOPS_KEEPER_MAX_FEE_PER_GAS_WEI")), MaxPriorityFeePerGasWei: strings.TrimSpace(os.Getenv("FLOWOPS_KEEPER_MAX_PRIORITY_FEE_PER_GAS_WEI"))},
		sockets: map[string]string{},
	}
	if err := validateDatabaseURL(config.databaseURL); err != nil {
		return startupConfig{}, err
	}
	if !identifierPattern.MatchString(config.keeperID) {
		return startupConfig{}, errors.New("FLOWOPS_KEEPER_ID is invalid")
	}
	if strings.TrimSpace(config.signerTokenPath) != config.signerTokenPath || !filepath.IsAbs(config.signerTokenPath) || filepath.Clean(config.signerTokenPath) != config.signerTokenPath || config.signerTokenPath == "/" {
		return startupConfig{}, errors.New("FLOWOPS_KEEPER_SIGNER_TOKEN_FILE must be a clean absolute path")
	}
	if !common.IsHexAddress(config.gasPayer) || strings.ToLower(common.HexToAddress(config.gasPayer).Hex()) != config.gasPayer || common.HexToAddress(config.gasPayer) == (common.Address{}) {
		return startupConfig{}, errors.New("FLOWOPS_KEEPER_GAS_PAYER must be a lowercase nonzero address")
	}
	var err error
	if config.chainID, err = parseUint("FLOWOPS_KEEPER_CHAIN_ID", 0); err != nil || config.chainID != 8453 && config.chainID != 84532 {
		return startupConfig{}, errors.New("FLOWOPS_KEEPER_CHAIN_ID must be 8453 or 84532")
	}
	for _, name := range []string{"artifact", "assembler", "verifier", "wallet", "sealer", "broadcast", "chain"} {
		envName := "FLOWOPS_KEEPER_" + strings.ToUpper(name) + "_SOCKET"
		path := os.Getenv(envName)
		if strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return startupConfig{}, fmt.Errorf("%s must be a clean absolute path", envName)
		}
		config.sockets[name] = path
	}
	seen := map[string]string{}
	for name, path := range config.sockets {
		if previous, ok := seen[path]; ok {
			return startupConfig{}, fmt.Errorf("keeper boundaries %s and %s must not share a socket", previous, name)
		}
		seen[path] = name
	}
	for name, target := range map[string]*time.Duration{
		"FLOWOPS_KEEPER_INTERVAL": &config.interval, "FLOWOPS_KEEPER_CYCLE_TIMEOUT": &config.cycleTimeout,
		"FLOWOPS_KEEPER_LEASE_DURATION": &config.leaseDuration, "FLOWOPS_KEEPER_BOUNDARY_TIMEOUT": &config.boundaryTimeout,
	} {
		if *target, err = parseDuration(name, *target); err != nil {
			return startupConfig{}, err
		}
	}
	if config.batchSize, err = parseInt("FLOWOPS_KEEPER_BATCH_SIZE", config.batchSize); err != nil {
		return startupConfig{}, err
	}
	if config.expiryLimit, err = parseInt("FLOWOPS_KEEPER_EXPIRY_LIMIT", config.expiryLimit); err != nil {
		return startupConfig{}, err
	}
	if config.maxFeeBumps, err = parseInt("FLOWOPS_KEEPER_MAX_FEE_BUMPS", config.maxFeeBumps); err != nil {
		return startupConfig{}, err
	}
	if config.maxGasLimit, err = parseUint("FLOWOPS_KEEPER_MAX_GAS_LIMIT", config.maxGasLimit); err != nil {
		return startupConfig{}, err
	}
	minimumRelayBudget := 10*config.boundaryTimeout + 5*time.Second
	if config.interval < time.Second || config.interval > 5*time.Minute || config.cycleTimeout >= config.interval ||
		config.cycleTimeout/10 < config.boundaryTimeout || config.cycleTimeout*8/10 < minimumRelayBudget ||
		config.leaseDuration < minimumRelayBudget || config.leaseDuration > time.Minute || config.boundaryTimeout < time.Second || config.boundaryTimeout > 10*time.Second ||
		config.batchSize < 1 || config.batchSize > 100 || config.expiryLimit < 1 || config.expiryLimit > 1000 || config.maxFeeBumps < 0 || config.maxFeeBumps > 3 ||
		config.maxGasLimit == 0 || config.maxGasLimit > 30_000_000 || !validFeeCap(config.feeCap) {
		return startupConfig{}, errors.New("ASCP keeper timing, batch, gas, or fee configuration is outside safe bounds")
	}
	return config, nil
}

func loadSignerCapability(path string) ([]byte, error) {
	secret, err := securefile.ReadCanonicalBase64Secret(path)
	if err != nil {
		return nil, fmt.Errorf("load keeper signer capability: %w", err)
	}
	return secret, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("FLOWOPS_KEEPER_DATABASE_URL must be a PostgreSQL URL")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("FLOWOPS_KEEPER_DATABASE_URL must set sslmode=verify-full exactly once")
	}
	for _, override := range []string{"host", "hostaddr", "port", "dbname", "database", "user", "password", "search_path", "options"} {
		if parsed.Query().Has(override) {
			return fmt.Errorf("FLOWOPS_KEEPER_DATABASE_URL must not override %s", override)
		}
	}
	return nil
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

func parseInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func parseUint(name string, fallback uint64) (uint64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer", name)
	}
	return value, nil
}

func validDecimal(value string, zeroAllowed bool) bool {
	if value == "" || len(value) > 78 || value != "0" && value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return zeroAllowed || value != "0"
}

func validFeeCap(fee ascpkeeper.Fee) bool {
	if !validDecimal(fee.MaxFeePerGasWei, false) || !validDecimal(fee.MaxPriorityFeePerGasWei, true) {
		return false
	}
	maximum, maxOK := new(big.Int).SetString(fee.MaxFeePerGasWei, 10)
	priority, priorityOK := new(big.Int).SetString(fee.MaxPriorityFeePerGasWei, 10)
	return maxOK && priorityOK && maximum.BitLen() <= 256 && priority.BitLen() <= 256 && priority.Cmp(maximum) <= 0
}
