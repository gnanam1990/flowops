package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpleadership"
	"github.com/gnanam1990/flowops/internal/ascprails"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type startupConfig struct {
	databaseURL       string
	workerID          string
	chainID           uint64
	rpcProviders      []reconciliation.RPCProvider
	rpcQuorum         int
	rpcRequestTimeout time.Duration
	maxChainLag       time.Duration
	integrityURL      string
	integrityKeys     map[string]ed25519.PublicKey
	integrityTimeout  time.Duration
	integrityMaxTTL   time.Duration
	interval          time.Duration
	cycleTimeout      time.Duration
	batchSize         int
	leaseDuration     time.Duration
	httpTimeout       time.Duration
	retryDelay        time.Duration
	maxObservationAge time.Duration
}

var workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP seller worker stopped", "error", err)
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
		return fmt.Errorf("open rails PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("connect to rails PostgreSQL: %w", err)
	}
	store, err := ascprails.NewPostgresStore(db)
	if err != nil {
		return err
	}
	operations, err := ascprails.NewPostgresOperationGate(db)
	if err != nil {
		return err
	}
	leadership, err := ascpleadership.NewPostgres(db, "public")
	if err != nil {
		return fmt.Errorf("create leadership gate: %w", err)
	}
	rpcTransport, err := ascprails.NewRestrictedTransport()
	if err != nil {
		return fmt.Errorf("create restricted Base RPC transport: %w", err)
	}
	rpcClient := &http.Client{Timeout: config.rpcRequestTimeout, Transport: rpcTransport}
	observers, err := reconciliation.NewObserverSet(config.chainID, config.rpcProviders, rpcClient, nil)
	if err != nil {
		return fmt.Errorf("create Base observer set: %w", err)
	}
	chainClock, err := ascprails.NewQuorumChainClock(observers, config.chainID, config.rpcQuorum, config.maxChainLag)
	if err != nil {
		return fmt.Errorf("create seller chain clock: %w", err)
	}
	integritySource, err := ascprails.NewHTTPSIntegritySource(config.integrityURL, config.integrityTimeout)
	if err != nil {
		return fmt.Errorf("create event-integrity source: %w", err)
	}
	eventHead, err := ascprails.NewPostgresEventHeadReader(db)
	if err != nil {
		return err
	}
	integrityGate, err := ascprails.NewAttestedIntegrityGate(eventHead, integritySource, config.integrityKeys, config.integrityMaxTTL)
	if err != nil {
		return fmt.Errorf("create event-integrity gate: %w", err)
	}
	if err := integrityGate.Check(startupCtx); err != nil {
		return fmt.Errorf("event-integrity startup gate: %w", err)
	}
	transport, err := ascprails.NewRestrictedTransport()
	if err != nil {
		return fmt.Errorf("create restricted seller transport: %w", err)
	}
	service, err := ascprails.NewService(store, leadership, chainClock, operations, integrityGate, transport, ascprails.Config{
		WorkerID: config.workerID, LeaseDuration: config.leaseDuration, HTTPTimeout: config.httpTimeout,
		RetryDelay: config.retryDelay, MaxAttempts: 3, MaxObservationAge: config.maxObservationAge,
		Recorder: logRecorder{},
	})
	if err != nil {
		return fmt.Errorf("create seller egress service: %w", err)
	}
	worker, err := ascprails.NewWorker(service, ascprails.WorkerConfig{Interval: config.interval,
		CycleTimeout: config.cycleTimeout, BatchSize: config.batchSize, OnCycle: func(cycle ascprails.WorkerCycle) {
			slog.Info("ASCP seller cycle completed", "finalized", cycle.Finalized, "dispatched", cycle.Dispatched,
				"responseStored", cycle.ResponseStored, "retryWait", cycle.RetryWait, "captured", cycle.Captured,
				"missing", cycle.Missing, "deadLetter", cycle.DeadLetter)
		}})
	if err != nil {
		return fmt.Errorf("create seller worker: %w", err)
	}
	slog.Info("ASCP seller worker started", "workerId", config.workerID, "chainId", config.chainID,
		"rpcProviders", len(config.rpcProviders), "rpcQuorum", config.rpcQuorum)
	return worker.Run(ctx)
}

type logRecorder struct{}

func (logRecorder) Record(_ context.Context, event ascprails.Event) {
	slog.Info("ASCP seller transition", "jobId", event.JobID, "operationId", event.OperationID,
		"organizationId", event.OrganizationID, "state", event.State, "attempt", event.Attempt, "code", event.Code)
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		databaseURL:      strings.TrimSpace(os.Getenv("FLOWOPS_RAILS_DATABASE_URL")),
		workerID:         strings.TrimSpace(os.Getenv("FLOWOPS_SELLER_WORKER_ID")),
		integrityURL:     strings.TrimSpace(os.Getenv("FLOWOPS_SELLER_INTEGRITY_URL")),
		integrityTimeout: 5 * time.Second, integrityMaxTTL: 2 * time.Minute, rpcRequestTimeout: 5 * time.Second,
		maxChainLag: 30 * time.Second,
		interval:    50 * time.Second, cycleTimeout: 45 * time.Second, batchSize: 20,
		leaseDuration: 55 * time.Second, httpTimeout: 10 * time.Second,
		retryDelay: 15 * time.Second, maxObservationAge: 30 * time.Second,
	}
	var err error
	if err = validateDatabaseURL(config.databaseURL); err != nil {
		return startupConfig{}, err
	}
	if !workerIDPattern.MatchString(config.workerID) {
		return startupConfig{}, errors.New("FLOWOPS_SELLER_WORKER_ID is invalid")
	}
	if config.chainID, err = parseUint("FLOWOPS_SELLER_CHAIN_ID", 0); err != nil || config.chainID != 8453 && config.chainID != 84532 {
		return startupConfig{}, errors.New("FLOWOPS_SELLER_CHAIN_ID must be 8453 or 84532")
	}
	if config.rpcProviders, err = parseRPCProviders(os.Getenv("FLOWOPS_SELLER_RPC_PROVIDERS_JSON")); err != nil {
		return startupConfig{}, err
	}
	if config.rpcQuorum, err = parseInt("FLOWOPS_SELLER_RPC_QUORUM", 0); err != nil || config.rpcQuorum < 2 || config.rpcQuorum > len(config.rpcProviders) {
		return startupConfig{}, errors.New("FLOWOPS_SELLER_RPC_QUORUM must be between 2 and the provider count")
	}
	if config.integrityURL == "" {
		return startupConfig{}, errors.New("FLOWOPS_SELLER_INTEGRITY_URL is required")
	}
	if config.integrityKeys, err = parseIntegrityKeys(os.Getenv("FLOWOPS_SELLER_INTEGRITY_KEYS_JSON")); err != nil {
		return startupConfig{}, err
	}
	for name, target := range map[string]*time.Duration{
		"FLOWOPS_SELLER_RPC_REQUEST_TIMEOUT": &config.rpcRequestTimeout,
		"FLOWOPS_SELLER_MAX_CHAIN_LAG":       &config.maxChainLag,
		"FLOWOPS_SELLER_INTEGRITY_TIMEOUT":   &config.integrityTimeout,
		"FLOWOPS_SELLER_INTEGRITY_MAX_TTL":   &config.integrityMaxTTL,
		"FLOWOPS_SELLER_INTERVAL":            &config.interval,
		"FLOWOPS_SELLER_CYCLE_TIMEOUT":       &config.cycleTimeout,
		"FLOWOPS_SELLER_LEASE_DURATION":      &config.leaseDuration,
		"FLOWOPS_SELLER_HTTP_TIMEOUT":        &config.httpTimeout,
		"FLOWOPS_SELLER_RETRY_DELAY":         &config.retryDelay,
		"FLOWOPS_SELLER_MAX_OBSERVATION_AGE": &config.maxObservationAge,
	} {
		if *target, err = parseDuration(name, *target); err != nil {
			return startupConfig{}, err
		}
	}
	if config.batchSize, err = parseInt("FLOWOPS_SELLER_BATCH_SIZE", config.batchSize); err != nil {
		return startupConfig{}, err
	}
	// One quorum snapshot performs chain ID, latest block and anchor block
	// requests sequentially per provider. Providers run in parallel, so budget
	// three request timeouts rather than multiplying by provider count.
	minimumEffectBudget := 2*config.integrityTimeout + 3*config.rpcRequestTimeout + config.httpTimeout + 5*time.Second
	if config.interval < time.Second || config.interval > time.Minute || config.cycleTimeout < minimumEffectBudget || config.cycleTimeout >= config.interval ||
		config.batchSize < 1 || config.batchSize > 100 || config.leaseDuration < time.Second || config.leaseDuration > time.Minute ||
		config.httpTimeout <= 0 || config.leaseDuration < minimumEffectBudget || config.retryDelay < time.Second || config.retryDelay > time.Hour ||
		config.rpcRequestTimeout < time.Second || config.rpcRequestTimeout > 10*time.Second || config.maxObservationAge < time.Second ||
		config.maxObservationAge > time.Minute || config.maxChainLag < time.Second || config.maxChainLag > 10*time.Minute ||
		config.integrityMaxTTL < time.Second || config.integrityMaxTTL > 5*time.Minute {
		return startupConfig{}, errors.New("ASCP seller worker durations or batch size are outside safe bounds")
	}
	if _, err := reconciliation.NewObserverSet(config.chainID, config.rpcProviders, nil, nil); err != nil {
		return startupConfig{}, fmt.Errorf("FLOWOPS_SELLER_RPC_PROVIDERS_JSON: %w", err)
	}
	if _, err := ascprails.NewHTTPSIntegritySource(config.integrityURL, config.integrityTimeout); err != nil {
		return startupConfig{}, errors.New("FLOWOPS_SELLER_INTEGRITY_URL or timeout is invalid")
	}
	return config, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" ||
		parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("FLOWOPS_RAILS_DATABASE_URL must be a PostgreSQL URL with a database path")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("FLOWOPS_RAILS_DATABASE_URL must set sslmode=verify-full exactly once")
	}
	for _, override := range []string{"host", "hostaddr", "port", "dbname", "database", "user", "password"} {
		if parsed.Query().Has(override) {
			return fmt.Errorf("FLOWOPS_RAILS_DATABASE_URL must not override %s in query parameters", override)
		}
	}
	return nil
}

func parseRPCProviders(raw string) ([]reconciliation.RPCProvider, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, errors.New("FLOWOPS_SELLER_RPC_PROVIDERS_JSON contains duplicate or invalid JSON fields")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	var providers []reconciliation.RPCProvider
	if err := decoder.Decode(&providers); err != nil || len(providers) < 2 || len(providers) > 5 {
		return nil, errors.New("FLOWOPS_SELLER_RPC_PROVIDERS_JSON must contain two to five strict provider objects")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("FLOWOPS_SELLER_RPC_PROVIDERS_JSON must contain exactly one JSON value")
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &objects); err != nil {
		return nil, errors.New("FLOWOPS_SELLER_RPC_PROVIDERS_JSON must contain valid JSON")
	}
	for _, object := range objects {
		if len(object) != 2 || object["name"] == nil || object["url"] == nil {
			return nil, errors.New("FLOWOPS_SELLER_RPC_PROVIDERS_JSON fields must be exactly name and url")
		}
	}
	for _, provider := range providers {
		if err := ascprails.ValidateRestrictedURLShape(provider.URL); err != nil {
			return nil, fmt.Errorf("FLOWOPS_SELLER_RPC_PROVIDERS_JSON provider %q is unsafe", provider.Name)
		}
	}
	return providers, nil
}

func parseIntegrityKeys(raw string) (map[string]ed25519.PublicKey, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, errors.New("FLOWOPS_SELLER_INTEGRITY_KEYS_JSON contains duplicate or invalid JSON fields")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	var encoded map[string]string
	if err := decoder.Decode(&encoded); err != nil || len(encoded) == 0 || len(encoded) > 8 {
		return nil, errors.New("FLOWOPS_SELLER_INTEGRITY_KEYS_JSON must contain one to eight public keys")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("FLOWOPS_SELLER_INTEGRITY_KEYS_JSON must contain exactly one JSON value")
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for keyID, value := range encoded {
		if !workerIDPattern.MatchString(keyID) {
			return nil, fmt.Errorf("integrity public key ID %q is invalid", keyID)
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != value {
			return nil, fmt.Errorf("integrity public key %q is invalid", keyID)
		}
		keys[keyID] = ed25519.PublicKey(decoded)
	}
	return keys, nil
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

func parseUint(name string, fallback uint64) (uint64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned decimal integer", name)
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
		return 0, fmt.Errorf("%s must be a decimal integer", name)
	}
	return value, nil
}

func rejectDuplicateJSONKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return errors.New("duplicate JSON object key")
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	return visit()
}
