// ascp-asset-health continuously verifies the pinned Base USDC proxy and
// Circle control surfaces. It owns no wallet key and cannot broadcast a chain
// transaction.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpassethealth"
	"github.com/gnanam1990/flowops/internal/dbreadiness"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type startupConfig struct {
	databaseURL       string
	chainID           uint64
	asset             string
	implementation    string
	runtimeCodeHash   string
	buyer             string
	escrow            string
	providers         []reconciliation.RPCProvider
	quorum            int
	interval          time.Duration
	queryTimeout      time.Duration
	maxObservationAge time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP asset-health monitor stopped", "error", err)
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
		return fmt.Errorf("open asset-health PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("connect to asset-health PostgreSQL: %w", err)
	}
	var schema string
	if err := db.QueryRowContext(startupCtx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		return errors.New("asset-health PostgreSQL current schema must be public")
	}

	observers, err := reconciliation.NewObserverSet(config.chainID, config.providers, nil, nil)
	if err != nil {
		return fmt.Errorf("create asset-health observers: %w", err)
	}
	source, err := ascpassethealth.NewRPCSource(observers, config.buyer, config.escrow, nil)
	if err != nil {
		return err
	}
	store, err := ascpassethealth.NewPostgresStore(db)
	if err != nil {
		return err
	}
	verifier, err := ascpassethealth.NewPostgresRecoveryVerifier(db, nil)
	if err != nil {
		return err
	}
	service, err := ascpassethealth.New(ascpassethealth.Config{ChainID: config.chainID, Asset: config.asset,
		ProxyImplementation: config.implementation, RuntimeCodeHash: config.runtimeCodeHash,
		Quorum: config.quorum, MaxObservationAge: config.maxObservationAge}, store, verifier, nil)
	if err != nil {
		return err
	}
	monitor, err := ascpassethealth.NewMonitor(source, service)
	if err != nil {
		return err
	}

	slog.Info("ASCP asset-health monitor started", "chainId", config.chainID, "asset", config.asset,
		"rpcProviders", len(config.providers), "quorum", config.quorum)
	ticker := time.NewTicker(config.interval)
	defer ticker.Stop()
	for {
		cycleCtx, cycleCancel := context.WithTimeout(ctx, config.queryTimeout)
		record, failures, observeErr := monitor.RunOnce(cycleCtx)
		cycleCancel()
		if observeErr != nil {
			slog.Error("asset-health observation failed closed", "error", observeErr, "providerFailures", failures)
		} else {
			slog.Info("asset-health observation recorded", "state", record.State, "epoch", record.Epoch,
				"finalizedBlock", record.FinalizedBlock, "providerFailures", failures)
			if record.State == ascpassethealth.Recovering {
				recovered, recoveryErr := service.CompleteRecovery(ctx)
				if recoveryErr == nil {
					slog.Info("asset recovery completed", "state", recovered.State, "epoch", recovered.Epoch)
				} else if !errors.Is(recoveryErr, ascpassethealth.ErrRecoveryIncomplete) {
					return recoveryErr
				} else {
					slog.Info("asset recovery waiting for canonical and accounting reconciliation")
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		databaseURL:     strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_DATABASE_URL")),
		asset:           strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_ASSET")),
		implementation:  strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_PROXY_IMPLEMENTATION")),
		runtimeCodeHash: strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_RUNTIME_CODE_HASH")),
		buyer:           strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_BUYER")),
		escrow:          strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_ESCROW")),
		interval:        30 * time.Second, queryTimeout: 15 * time.Second, maxObservationAge: time.Minute,
	}
	if err := dbreadiness.ValidateRuntimeURL(config.databaseURL); err != nil {
		return startupConfig{}, fmt.Errorf("FLOWOPS_ASSET_HEALTH_DATABASE_URL: %w", err)
	}
	chainID, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_CHAIN_ID")), 10, 64)
	if err != nil || chainID != 8453 && chainID != 84532 {
		return startupConfig{}, errors.New("FLOWOPS_ASSET_HEALTH_CHAIN_ID must be 8453 or 84532")
	}
	config.chainID = chainID
	if err := decodeProviders(os.Getenv("FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON"), &config.providers); err != nil {
		return startupConfig{}, err
	}
	quorum, err := strconv.Atoi(strings.TrimSpace(os.Getenv("FLOWOPS_ASSET_HEALTH_RPC_QUORUM")))
	if err != nil || quorum < 2 || quorum > len(config.providers) {
		return startupConfig{}, errors.New("FLOWOPS_ASSET_HEALTH_RPC_QUORUM must be between 2 and the provider count")
	}
	config.quorum = quorum
	for name, target := range map[string]*time.Duration{
		"FLOWOPS_ASSET_HEALTH_INTERVAL":            &config.interval,
		"FLOWOPS_ASSET_HEALTH_QUERY_TIMEOUT":       &config.queryTimeout,
		"FLOWOPS_ASSET_HEALTH_MAX_OBSERVATION_AGE": &config.maxObservationAge,
	} {
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil {
				return startupConfig{}, fmt.Errorf("%s is invalid: %w", name, err)
			}
			*target = value
		}
	}
	if config.interval < 5*time.Second || config.interval > time.Minute || config.queryTimeout <= 0 ||
		config.queryTimeout >= config.interval || config.maxObservationAge < config.interval || config.maxObservationAge > 5*time.Minute {
		return startupConfig{}, errors.New("asset-health timing configuration is unsafe")
	}
	if _, err := ascpassethealth.New(ascpassethealth.Config{ChainID: config.chainID, Asset: config.asset,
		ProxyImplementation: config.implementation, RuntimeCodeHash: config.runtimeCodeHash,
		Quorum: config.quorum, MaxObservationAge: config.maxObservationAge}, ascpassethealth.NewMemoryStore(), recoveryVerifierConfigStub{}, nil); err != nil {
		return startupConfig{}, fmt.Errorf("asset-health binding configuration: %w", err)
	}
	observers, err := reconciliation.NewObserverSet(config.chainID, config.providers, nil, nil)
	if err != nil {
		return startupConfig{}, err
	}
	if _, err := ascpassethealth.NewRPCSource(observers, config.buyer, config.escrow, nil); err != nil {
		return startupConfig{}, fmt.Errorf("asset-health subject configuration: %w", err)
	}
	return config, nil
}

func decodeProviders(raw string, output *[]reconciliation.RPCProvider) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil || len(*output) < 2 || len(*output) > 5 {
		return errors.New("FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON must contain two to five providers")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON contains trailing data")
	}
	if _, err := reconciliation.NewObserverSet(84532, *output, nil, nil); err != nil {
		return fmt.Errorf("FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON: %w", err)
	}
	return nil
}

type recoveryVerifierConfigStub struct{}

func (recoveryVerifierConfigStub) VerifyRecovery(context.Context, ascpassethealth.Record) (ascpassethealth.RecoveryProof, error) {
	return ascpassethealth.RecoveryProof{}, ascpassethealth.ErrRecoveryIncomplete
}
