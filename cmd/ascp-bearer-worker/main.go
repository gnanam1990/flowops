package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type startupConfig struct {
	databaseURL, workerID, signerKeyID, keeperID string
	signerSocket, mirrorSocket                   string
	keyEpoch                                     uint64
	interval, cycleTimeout, leaseDuration        time.Duration
	boundaryTimeout, retryDelay                  time.Duration
	expiryBatchSize, advanceBatchSize            int
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP bearer worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	sockets := map[string]string{"signer": config.signerSocket, "mirror": config.mirrorSocket}
	if err := ascpbearer.ValidateRuntimeSockets(sockets); err != nil {
		return fmt.Errorf("validate bearer boundary sockets: %w", err)
	}
	boundaries := make(map[string]*ascpbearer.RuntimeUnixBoundary, len(sockets))
	for name, path := range sockets {
		boundary, err := ascpbearer.NewRuntimeUnixBoundary(name, path, config.boundaryTimeout)
		if err != nil {
			return fmt.Errorf("configure bearer %s boundary: %w", name, err)
		}
		boundaries[name] = boundary
	}
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := checkBoundaries(startupCtx, boundaries); err != nil {
		return err
	}

	db, err := sql.Open("pgx", config.databaseURL)
	if err != nil {
		return fmt.Errorf("open bearer PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("connect bearer PostgreSQL: %w", err)
	}
	var schema string
	if err := db.QueryRowContext(startupCtx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		return errors.New("bearer PostgreSQL current schema must be public")
	}
	if err := ascpbearer.VerifyRuntimeRole(startupCtx, db); err != nil {
		return fmt.Errorf("verify bearer PostgreSQL role: %w", err)
	}

	store, err := ascpbearer.NewRuntimeActivationStore(db)
	if err != nil {
		return err
	}
	signer, err := ascpbearer.NewRuntimeUnixSigner(boundaries["signer"])
	if err != nil {
		return err
	}
	mirror, err := ascpbearer.NewRuntimeUnixMirror(boundaries["mirror"])
	if err != nil {
		return err
	}
	service, err := ascpbearer.NewRuntimeService(store, signer, mirror, ascpbearer.RuntimeConfig{
		Claim: ascpbearer.RuntimeClaim{
			WorkerID: config.workerID, SignerKeyID: config.signerKeyID, KeyEpoch: config.keyEpoch,
			KeeperID: config.keeperID, LeaseDuration: config.leaseDuration,
		},
		RetryDelay: config.retryDelay,
	})
	if err != nil {
		return fmt.Errorf("create bearer runtime service: %w", err)
	}
	worker, err := ascpbearer.NewRuntimeWorker(service, ascpbearer.RuntimeWorkerConfig{
		Interval: config.interval, CycleTimeout: config.cycleTimeout, ExpiryPhaseTimeout: config.boundaryTimeout + time.Second,
		ExpiryBatchSize: config.expiryBatchSize, AdvanceBatchSize: config.advanceBatchSize,
		OnCycle: func(cycle ascpbearer.RuntimeCycle) {
			slog.Info("ASCP bearer cycle completed", "processed", cycle.Processed, "advanced", cycle.Advanced, "prepared", cycle.Prepared,
				"activated", cycle.Activated, "mirrored", cycle.Mirrored, "acknowledged", cycle.Acknowledged,
				"expired", cycle.Expired, "refused", cycle.Refused, "retried", cycle.Retried)
		},
	})
	if err != nil {
		return fmt.Errorf("create bearer runtime worker: %w", err)
	}
	slog.Info("ASCP bearer worker started", "workerId", config.workerID, "signerKeyId", config.signerKeyID,
		"keyEpoch", config.keyEpoch, "keeperId", config.keeperID)
	return worker.Run(ctx)
}

func checkBoundaries(ctx context.Context, boundaries map[string]*ascpbearer.RuntimeUnixBoundary) error {
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(boundaries))
	for name, boundary := range boundaries {
		name, boundary := name, boundary
		go func() { results <- result{name: name, err: boundary.Check(ctx)} }()
	}
	var joined error
	for range boundaries {
		result := <-results
		if result.err != nil {
			joined = errors.Join(joined, fmt.Errorf("check bearer %s boundary: %w", result.name, result.err))
		}
	}
	return joined
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		databaseURL:  strings.TrimSpace(os.Getenv("FLOWOPS_BEARER_DATABASE_URL")),
		workerID:     strings.TrimSpace(os.Getenv("FLOWOPS_BEARER_WORKER_ID")),
		signerKeyID:  strings.TrimSpace(os.Getenv("FLOWOPS_BEARER_SIGNER_KEY_ID")),
		keeperID:     strings.TrimSpace(os.Getenv("FLOWOPS_BEARER_KEEPER_ID")),
		signerSocket: strings.TrimSpace(os.Getenv("FLOWOPS_BEARER_SIGNER_SOCKET")),
		mirrorSocket: strings.TrimSpace(os.Getenv("FLOWOPS_BEARER_MIRROR_SOCKET")),
		interval:     30 * time.Second, cycleTimeout: 20 * time.Second, leaseDuration: 10 * time.Second,
		boundaryTimeout: 3 * time.Second, retryDelay: 10 * time.Second,
		expiryBatchSize: 10, advanceBatchSize: 40,
	}
	if err := validateDatabaseURL(config.databaseURL); err != nil {
		return startupConfig{}, err
	}
	if !identifierPattern.MatchString(config.workerID) || !identifierPattern.MatchString(config.signerKeyID) ||
		!identifierPattern.MatchString(config.keeperID) {
		return startupConfig{}, errors.New("bearer worker, signer key, and keeper identifiers are required and must be canonical")
	}
	var err error
	if config.keyEpoch, err = parseUint("FLOWOPS_BEARER_KEY_EPOCH", 0); err != nil || config.keyEpoch == 0 {
		return startupConfig{}, errors.New("FLOWOPS_BEARER_KEY_EPOCH must be a positive integer")
	}
	for name, path := range map[string]string{"FLOWOPS_BEARER_SIGNER_SOCKET": config.signerSocket, "FLOWOPS_BEARER_MIRROR_SOCKET": config.mirrorSocket} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return startupConfig{}, fmt.Errorf("%s must be a clean absolute path", name)
		}
	}
	if config.signerSocket == config.mirrorSocket {
		return startupConfig{}, errors.New("bearer signer and mirror boundaries must not share a socket")
	}
	for name, target := range map[string]*time.Duration{
		"FLOWOPS_BEARER_INTERVAL": &config.interval, "FLOWOPS_BEARER_CYCLE_TIMEOUT": &config.cycleTimeout,
		"FLOWOPS_BEARER_LEASE_DURATION": &config.leaseDuration, "FLOWOPS_BEARER_BOUNDARY_TIMEOUT": &config.boundaryTimeout,
		"FLOWOPS_BEARER_RETRY_DELAY": &config.retryDelay,
	} {
		if *target, err = parseDuration(name, *target); err != nil {
			return startupConfig{}, err
		}
	}
	if config.expiryBatchSize, err = parseInt("FLOWOPS_BEARER_EXPIRY_BATCH_SIZE", config.expiryBatchSize); err != nil {
		return startupConfig{}, err
	}
	if config.advanceBatchSize, err = parseInt("FLOWOPS_BEARER_ADVANCE_BATCH_SIZE", config.advanceBatchSize); err != nil {
		return startupConfig{}, err
	}
	minimumLease := config.boundaryTimeout + 2*time.Second
	if config.interval < time.Second || config.interval > 5*time.Minute || config.cycleTimeout < time.Second || config.cycleTimeout >= config.interval ||
		config.cycleTimeout <= config.boundaryTimeout+time.Second ||
		config.boundaryTimeout < time.Second || config.boundaryTimeout > 10*time.Second || config.leaseDuration < minimumLease || config.leaseDuration > time.Minute ||
		config.retryDelay < time.Second || config.retryDelay > time.Hour || config.expiryBatchSize < 1 || config.expiryBatchSize > 100 ||
		config.advanceBatchSize < 1 || config.advanceBatchSize > 100 {
		return startupConfig{}, errors.New("ASCP bearer timing or batch configuration is outside safe bounds")
	}
	return config, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" ||
		parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("FLOWOPS_BEARER_DATABASE_URL must be a PostgreSQL URL")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("FLOWOPS_BEARER_DATABASE_URL must set sslmode=verify-full exactly once")
	}
	for _, override := range []string{"host", "hostaddr", "port", "dbname", "database", "user", "password", "search_path", "options"} {
		if parsed.Query().Has(override) {
			return fmt.Errorf("FLOWOPS_BEARER_DATABASE_URL must not override %s", override)
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
