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
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	baseSepoliaChainID = 84532
	defaultAddress     = "127.0.0.1:8080"
)

type startupConfig struct {
	address        string
	databaseURL    string
	envelopeKeyID  string
	envelopeKey    ed25519.PrivateKey
	reconciliation string
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
	if err := controlapi.ApplyMigrations(startupCtx, db); err != nil {
		return err
	}
	store, err := controlapi.NewPostgresStore(db)
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
	reconciliationEngine, err := reconciliation.Open(cfg.reconciliation, reconciliation.Config{ChainID: baseSepoliaChainID})
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
	api, err := controlapi.NewServer(controlapi.ServerConfig{Store: store, Lifecycle: lifecycle, Chain: reconciliationEngine})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr: cfg.address, Handler: api,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 * 1024,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("control plane listening", "address", cfg.address, "chainId", baseSepoliaChainID)
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	shutdownSignal, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-shutdownSignal.Done():
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("serve control-plane API: %w", err)
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	return httpServer.Shutdown(shutdownCtx)
}

func loadConfig() (startupConfig, error) {
	key, err := decodePrivateKey(os.Getenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg := startupConfig{
		address: strings.TrimSpace(os.Getenv("FLOWOPS_CONTROL_ADDR")), databaseURL: strings.TrimSpace(os.Getenv("FLOWOPS_DATABASE_URL")),
		envelopeKeyID: strings.TrimSpace(os.Getenv("FLOWOPS_ENVELOPE_KEY_ID")),
		envelopeKey:   key, reconciliation: strings.TrimSpace(os.Getenv("FLOWOPS_RECONCILIATION_JOURNAL")),
	}
	if cfg.address == "" {
		cfg.address = defaultAddress
	}
	if cfg.databaseURL == "" || cfg.envelopeKeyID == "" || cfg.reconciliation == "" {
		return startupConfig{}, errors.New("FLOWOPS_DATABASE_URL, FLOWOPS_ENVELOPE_KEY_ID, FLOWOPS_ENVELOPE_PRIVATE_KEY_B64, and FLOWOPS_RECONCILIATION_JOURNAL are required")
	}
	return cfg, nil
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
