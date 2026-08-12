package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultAddress = "127.0.0.1:8080"
)

type startupConfig struct {
	address                string
	databaseURL            string
	envelopeKeyID          string
	envelopeKey            ed25519.PrivateKey
	siteSessionKey         []byte
	reconciliation         string
	trustProxy             bool
	applyMigrations        bool
	operatorKey            []byte
	observerRPCs           []reconciliation.RPCProvider
	observerConfig         reconciliation.Config
	observerInterval       time.Duration
	observerTimeout        time.Duration
	reconciliationInterval time.Duration
	reconciliationTimeout  time.Duration
	signerReceiptKeys      []controlapi.BroadcastKey
}

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("control plane stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	if cfg.applyMigrations {
		if err := controlapi.ApplyMigrations(startupCtx, db); err != nil {
			return err
		}
	}
	siteSessions, err := controlapi.NewSiteSessionCodec(cfg.siteSessionKey, 2*time.Minute, nil)
	if err != nil {
		return fmt.Errorf("create site session codec: %w", err)
	}
	store, err := controlapi.NewPostgresStore(db, siteSessions)
	if err != nil {
		return err
	}
	eventJournal, err := controlplane.OpenPostgresJournal(startupCtx, db)
	if err != nil {
		return err
	}
	policyProvider, err := controlapi.NewPostgresPolicyProvider(db)
	if err != nil {
		return err
	}
	reconciliationEngine, err := reconciliation.Open(cfg.reconciliation, cfg.observerConfig)
	if err != nil {
		return fmt.Errorf("open reconciliation state: %w", err)
	}
	defer reconciliationEngine.Close()

	lifecycle, err := controlplane.New(controlplane.Config{
		PolicyProvider: policyProvider, Journal: eventJournal,
		FreezeGate: controlapi.AgentFreezeGate{Store: store}, ChainGate: reconciliationEngine,
		ApprovalTTL: 15 * time.Minute, AuthorizationTTL: 5 * time.Minute,
		RequestIDSource:       func() (string, error) { return randomID("req") },
		AuthorizationIDSource: func() (string, error) { return randomID("auth") },
		NonceSource:           func() (string, error) { return randomNonce() },
		EnvelopeKeyID:         cfg.envelopeKeyID, EnvelopePrivateKey: cfg.envelopeKey,
	})
	if err != nil {
		return fmt.Errorf("create lifecycle: %w", err)
	}
	if _, err := lifecycle.SweepExpired(startupCtx); err != nil {
		return fmt.Errorf("sweep expired approvals at startup: %w", err)
	}
	var signerBroadcasts controlapi.BroadcastRegistrar
	if len(cfg.signerReceiptKeys) > 0 {
		keys, err := controlapi.NewStaticBroadcastKeys(cfg.signerReceiptKeys)
		if err != nil {
			return fmt.Errorf("create customer signer receipt key registry: %w", err)
		}
		signerBroadcasts, err = controlapi.NewSignerBroadcastRegistrar(lifecycle, keys, reconciliationEngine, nil)
		if err != nil {
			return fmt.Errorf("create customer signer broadcast registrar: %w", err)
		}
	}
	observers, err := reconciliation.NewObserverSet(cfg.observerConfig.ChainID, cfg.observerRPCs, nil, nil)
	if err != nil {
		return fmt.Errorf("create Base observer set: %w", err)
	}
	observerSupervisor, err := reconciliation.NewSupervisor(observers, reconciliationEngine, reconciliation.SupervisorConfig{
		Interval: cfg.observerInterval, ObservationTimeout: cfg.observerTimeout,
		OnResult: func(status reconciliation.ChainStatus, result reconciliation.SnapshotResult) {
			slog.Info("Base observer snapshot persisted", "state", status.State, "responding", len(result.Observations), "failed", len(result.Failures), "required", status.RequiredObserverQuorum)
		},
	})
	if err != nil {
		return fmt.Errorf("create Base observer supervisor: %w", err)
	}
	reconciliationWorker, err := reconciliation.NewWorker(observers, reconciliationEngine, reconciliation.WorkerConfig{
		Interval: cfg.reconciliationInterval, QueryTimeout: cfg.reconciliationTimeout,
		OnCycle: func(cycle reconciliation.WorkerCycle) {
			slog.Info("Base reconciliation cycle completed", "examined", cycle.Examined, "receiptCandidates", cycle.ReceiptCandidates, "settled", cycle.Settled, "reverted", cycle.Reverted, "finalityConfirmed", cycle.FinalityConfirmed, "reorgsReopened", cycle.ReorgsReopened, "deferred", cycle.Deferred, "skippedForChain", cycle.SkippedForChain)
		},
	})
	if err != nil {
		return fmt.Errorf("create Base reconciliation worker: %w", err)
	}
	api, err := controlapi.NewServer(controlapi.ServerConfig{
		Store: store, Lifecycle: lifecycle, Chain: reconciliationEngine, SiteSessions: siteSessions,
		OperatorControlKey: cfg.operatorKey, SignerBroadcasts: signerBroadcasts,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr: cfg.address, Handler: enforceTransportSecurity(api, cfg.trustProxy),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 * 1024,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("control plane listening", "address", cfg.address, "chainId", cfg.observerConfig.ChainID, "observerCount", len(cfg.observerRPCs))
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	shutdownSignal, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	maintenanceErrors := make(chan error, 1)
	go func() {
		maintenanceErrors <- runExpirySweeper(shutdownSignal, lifecycle, 30*time.Second)
	}()
	observerErrors := make(chan error, 1)
	go func() {
		observerErrors <- observerSupervisor.Run(shutdownSignal)
	}()
	reconciliationErrors := make(chan error, 1)
	go func() {
		reconciliationErrors <- reconciliationWorker.Run(shutdownSignal)
	}()
	select {
	case <-shutdownSignal.Done():
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("serve control-plane API: %w", err)
		}
	case err := <-maintenanceErrors:
		if err != nil {
			return err
		}
	case err := <-observerErrors:
		if err != nil {
			return err
		}
	case err := <-reconciliationErrors:
		if err != nil {
			return err
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	return httpServer.Shutdown(shutdownCtx)
}

func runExpirySweeper(ctx context.Context, lifecycle *controlplane.Lifecycle, interval time.Duration) error {
	if lifecycle == nil || interval <= 0 {
		return errors.New("expiry sweeper requires a lifecycle and positive interval")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := lifecycle.SweepExpired(ctx); err != nil {
				return fmt.Errorf("sweep expired approvals: %w", err)
			}
		}
	}
}

func loadConfig() (startupConfig, error) {
	key, err := decodePrivateKey(os.Getenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	siteSessionKey, err := decodeSymmetricKey("FLOWOPS_SITE_SESSION_KEY_B64", os.Getenv("FLOWOPS_SITE_SESSION_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg := startupConfig{
		address: strings.TrimSpace(os.Getenv("FLOWOPS_CONTROL_ADDR")), databaseURL: strings.TrimSpace(os.Getenv("FLOWOPS_DATABASE_URL")),
		envelopeKeyID: strings.TrimSpace(os.Getenv("FLOWOPS_ENVELOPE_KEY_ID")),
		envelopeKey:   key, siteSessionKey: siteSessionKey,
		reconciliation: strings.TrimSpace(os.Getenv("FLOWOPS_RECONCILIATION_JOURNAL")),
	}
	operatorKey, err := decodeSymmetricKey("FLOWOPS_OPERATOR_CONTROL_KEY_B64", os.Getenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg.operatorKey = operatorKey
	signerReceiptKeys, err := parseSignerKeys(os.Getenv("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg.signerReceiptKeys = signerReceiptKeys
	observerRuntime, err := loadObserverRuntimeConfig()
	if err != nil {
		return startupConfig{}, err
	}
	cfg.observerRPCs = observerRuntime.providers
	cfg.observerConfig = observerRuntime.engine
	cfg.observerInterval = observerRuntime.interval
	cfg.observerTimeout = observerRuntime.timeout
	cfg.reconciliationInterval = observerRuntime.reconciliationInterval
	cfg.reconciliationTimeout = observerRuntime.reconciliationTimeout
	trustProxy, err := parseStrictBool("FLOWOPS_TRUST_PROXY_HEADERS", os.Getenv("FLOWOPS_TRUST_PROXY_HEADERS"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg.trustProxy = trustProxy
	applyMigrations, err := parseStrictBoolDefault("FLOWOPS_APPLY_MIGRATIONS", os.Getenv("FLOWOPS_APPLY_MIGRATIONS"), true)
	if err != nil {
		return startupConfig{}, err
	}
	cfg.applyMigrations = applyMigrations
	if cfg.address == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			cfg.address = defaultAddress
		} else {
			parsedPort, err := strconv.ParseUint(port, 10, 16)
			if err != nil || parsedPort == 0 {
				return startupConfig{}, errors.New("PORT must be a positive TCP port")
			}
			cfg.address = "0.0.0.0:" + port
		}
	}
	if err := validateListenAddress(cfg.address, cfg.trustProxy); err != nil {
		return startupConfig{}, err
	}
	if cfg.databaseURL == "" || cfg.envelopeKeyID == "" || cfg.reconciliation == "" {
		return startupConfig{}, errors.New("database, signing, session, operator-control, observer, and reconciliation configuration are required")
	}
	return cfg, nil
}

func decodeSymmetricKey(name, value string) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("%s must encode exactly 32 bytes", name)
	}
	return append([]byte(nil), raw...), nil
}

func validateListenAddress(address string, trustProxy bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("FLOWOPS_CONTROL_ADDR must be a host:port listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("FLOWOPS_CONTROL_ADDR host must be an IP address")
	}
	if !ip.IsLoopback() && !trustProxy {
		return errors.New("non-loopback FLOWOPS_CONTROL_ADDR requires FLOWOPS_TRUST_PROXY_HEADERS=true")
	}
	return nil
}

func parseStrictBool(name, value string) (bool, error) {
	return parseStrictBoolDefault(name, value, false)
}

func parseStrictBoolDefault(name, value string, defaultValue bool) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	if value == "false" {
		return false, nil
	}
	if value == "true" {
		return true, nil
	}
	return false, fmt.Errorf("%s must be true or false", name)
}

func enforceTransportSecurity(next http.Handler, trustProxy bool) http.Handler {
	if !trustProxy {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" && firstForwardedValue(request.Header.Get("X-Forwarded-Proto")) != "https" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"code":"HTTPS_REQUIRED","message":"secure transport is required"}}`))
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func firstForwardedValue(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return strings.ToLower(strings.TrimSpace(first))
}

func decodePrivateKey(value string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64 is required")
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64 must encode one Ed25519 private key")
	}
	canonical := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(raw, canonical) != 1 {
		return nil, errors.New("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64 is not a canonical Ed25519 private key")
	}
	return ed25519.PrivateKey(append([]byte(nil), raw...)), nil
}

func randomID(prefix string) (string, error) { return controlapiRandomID(prefix) }

func randomNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(raw), nil
}

func controlapiRandomID(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(raw), nil
}
