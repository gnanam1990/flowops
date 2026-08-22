package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpgovernancerelay"
	"github.com/gnanam1990/flowops/internal/ascpkeeper"
	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/internal/securefile"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/sys/unix"
)

type config struct {
	databaseURL                          string
	workerID                             string
	sockets                              map[string]string
	interval, lease, boundaryTimeout     time.Duration
	batch, quorum                        int
	mode                                 string
	authorizeOrg, authorizeWorkflow      string
	authorizeKey, authorizeSignatureFile string
	vaultTokenFile                       string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("ASCP governance relayer stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		return err
	}
	if err := ascpkeeper.ValidateDistinctSockets(activeSocketPaths(cfg)); err != nil {
		return fmt.Errorf("validate governance relay boundaries: %w", err)
	}
	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(startup); err != nil {
		return fmt.Errorf("connect governance relay PostgreSQL: %w", err)
	}
	var schema string
	if err := db.QueryRowContext(startup, `SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		return errors.New("governance relay PostgreSQL current schema must be public")
	}
	directoryBoundary, err := checkedBoundary(startup, "directory", cfg.sockets["directory"], cfg.boundaryTimeout, nil)
	if err != nil {
		return err
	}
	chainBoundary, err := checkedBoundary(startup, "chain", cfg.sockets["chain"], cfg.boundaryTimeout, nil)
	if err != nil {
		return err
	}
	directory, err := ascpgovernancerelay.NewUnixDirectory(directoryBoundary)
	if err != nil {
		return err
	}
	chain, err := ascpgovernancerelay.NewUnixSnapshotSource(chainBoundary)
	if err != nil {
		return err
	}
	store, err := ascpgovernancerelay.NewPostgresStore(db, nil)
	if err != nil {
		return err
	}
	if err := ascpgovernancerelay.VerifyRuntimeRole(startup, db); err != nil {
		return fmt.Errorf("verify governance relay PostgreSQL role: %w", err)
	}
	if cfg.mode == "inspect" {
		request, err := ascpgovernancerelay.InspectSigningRequest(ctx, store, directory, chain,
			cfg.authorizeOrg, cfg.authorizeWorkflow, cfg.quorum, time.Now().UTC())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(request)
	}
	vaultCapability, err := securefile.ReadCanonicalBase64Secret(cfg.vaultTokenFile)
	if err != nil {
		return fmt.Errorf("load governance vault capability: %w", err)
	}
	defer clear(vaultCapability)
	vaultBoundary, err := checkedBoundary(startup, "vault", cfg.sockets["vault"], cfg.boundaryTimeout, vaultCapability)
	if err != nil {
		return err
	}
	vault, err := ascpgovernancerelay.NewUnixVault(vaultBoundary)
	if err != nil {
		return err
	}
	if cfg.mode == "authorize" {
		return authorize(ctx, store, directory, chain, vault, cfg)
	}
	broadcastBoundary, err := checkedBoundary(startup, "broadcast", cfg.sockets["broadcast"], cfg.boundaryTimeout, nil)
	if err != nil {
		return err
	}
	outcomes, err := ascpgovernancerelay.NewUnixOutcomeSource(chainBoundary)
	if err != nil {
		return err
	}
	broadcaster, err := ascpgovernancerelay.NewUnixBroadcaster(broadcastBoundary)
	if err != nil {
		return err
	}
	workflowStore, err := ascpworkflow.NewPostgresStore(db)
	if err != nil {
		return err
	}
	workflows, err := ascpworkflow.New(workflowStore, nil, nil, nil)
	if err != nil {
		return err
	}
	service, err := ascpgovernancerelay.NewService(store, directory, chain, vault, broadcaster, outcomes, workflows,
		ascpgovernancerelay.Config{WorkerID: cfg.workerID, Quorum: cfg.quorum, LeaseDuration: cfg.lease})
	if err != nil {
		return err
	}
	slog.Info("ASCP governance relayer started", "workerId", cfg.workerID, "quorum", cfg.quorum)
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		if err := cycle(ctx, service, cfg.batch); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func activeSocketPaths(cfg config) map[string]string {
	active := map[string]string{"directory": cfg.sockets["directory"], "chain": cfg.sockets["chain"]}
	if cfg.mode != "inspect" {
		active["vault"] = cfg.sockets["vault"]
	}
	if cfg.mode == "run" {
		active["broadcast"] = cfg.sockets["broadcast"]
	}
	return active
}

func checkedBoundary(ctx context.Context, name, path string, timeout time.Duration, capability []byte) (*ascpgovernancerelay.UnixBoundary, error) {
	var boundary *ascpgovernancerelay.UnixBoundary
	var err error
	if len(capability) > 0 {
		boundary, err = ascpgovernancerelay.NewAuthenticatedUnixBoundary(name, path, timeout, capability)
	} else {
		boundary, err = ascpgovernancerelay.NewUnixBoundary(name, path, timeout)
	}
	if err != nil {
		return nil, err
	}
	if err := boundary.Check(ctx); err != nil {
		return nil, fmt.Errorf("check governance %s boundary: %w", name, err)
	}
	return boundary, nil
}

func cycle(ctx context.Context, service *ascpgovernancerelay.Service, batch int) error {
	for range batch {
		if job, err := service.ObserveOnce(ctx); errors.Is(err, ascpgovernancerelay.ErrNoWork) {
			break
		} else if err != nil {
			return fmt.Errorf("observe governance relay: %w", err)
		} else {
			logJob("observed governance relay", job)
		}
	}
	for range batch {
		if job, replayed, err := service.ConsumeOnce(ctx); errors.Is(err, ascpgovernancerelay.ErrNoWork) {
			break
		} else if err != nil {
			return fmt.Errorf("consume governance command: %w", err)
		} else {
			slog.Info("consumed governance command", "workflowId", job.Command.WorkflowID,
				"state", job.State, "replayed", replayed)
		}
	}
	for range batch {
		if job, err := service.RelayOnce(ctx); errors.Is(err, ascpgovernancerelay.ErrNoWork) {
			break
		} else if err != nil {
			return fmt.Errorf("relay governance command: %w", err)
		} else {
			logJob("processed governance relay", job)
		}
	}
	return nil
}

func logJob(message string, job ascpgovernancerelay.Job) {
	slog.Info(message, "workflowId", job.Command.WorkflowID, "state", job.State, "attemptCount", job.AttemptCount)
}

func authorize(ctx context.Context, store ascpgovernancerelay.Store, directory ascpgovernancerelay.SafeDirectory,
	snapshots ascpgovernancerelay.SnapshotSource, vault ascpgovernancerelay.ArtifactVault, cfg config,
) error {
	signatures, err := readSignatures(cfg.authorizeSignatureFile)
	if err != nil {
		return err
	}
	defer clear(signatures)
	job, err := ascpgovernancerelay.AuthorizeSignatures(ctx, store, directory, snapshots, vault,
		cfg.authorizeOrg, cfg.authorizeWorkflow, cfg.authorizeKey, signatures, cfg.quorum, time.Now().UTC())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		WorkflowID string                    `json:"workflowId"`
		State      ascpgovernancerelay.State `json:"state"`
		SafeTxHash string                    `json:"safeTxHash"`
	}{job.Command.WorkflowID, job.State, job.Prepared.SafeTxHash})
}

func loadConfig(args []string) (config, error) {
	mode := "run"
	if len(args) > 1 || len(args) == 1 && args[0] != "run" && args[0] != "inspect" && args[0] != "authorize" {
		return config{}, errors.New("usage: ascp-governance-relayer [run|inspect|authorize]")
	}
	if len(args) == 1 {
		mode = args[0]
	}
	cfg := config{
		databaseURL: strings.TrimSpace(os.Getenv("FLOWOPS_GOVERNANCE_RELAY_DATABASE_URL")),
		workerID:    strings.TrimSpace(os.Getenv("FLOWOPS_GOVERNANCE_RELAY_WORKER_ID")), mode: mode,
		interval: time.Minute, lease: 55 * time.Second, boundaryTimeout: 3 * time.Second, batch: 20, quorum: 2,
		sockets: map[string]string{
			"directory": os.Getenv("FLOWOPS_GOVERNANCE_RELAY_DIRECTORY_SOCKET"),
			"chain":     os.Getenv("FLOWOPS_GOVERNANCE_RELAY_CHAIN_SOCKET"),
			"vault":     os.Getenv("FLOWOPS_GOVERNANCE_RELAY_VAULT_SOCKET"),
			"broadcast": os.Getenv("FLOWOPS_GOVERNANCE_RELAY_BROADCAST_SOCKET"),
		},
		authorizeOrg:           os.Getenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_ORG"),
		authorizeWorkflow:      os.Getenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_WORKFLOW"),
		authorizeKey:           os.Getenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_KEY"),
		authorizeSignatureFile: os.Getenv("FLOWOPS_GOVERNANCE_RELAY_SIGNATURE_FILE"),
		vaultTokenFile:         os.Getenv("FLOWOPS_GOVERNANCE_RELAY_VAULT_TOKEN_FILE"),
	}
	if err := validateDatabaseURL(cfg.databaseURL); err != nil {
		return config{}, err
	}
	if mode == "run" && cfg.workerID == "" {
		return config{}, errors.New("FLOWOPS_GOVERNANCE_RELAY_WORKER_ID is required")
	}
	for name, path := range activeSocketPaths(cfg) {
		if strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return config{}, fmt.Errorf("FLOWOPS_GOVERNANCE_RELAY_%s_SOCKET must be a clean absolute path", strings.ToUpper(name))
		}
	}
	if mode != "inspect" && (strings.TrimSpace(cfg.vaultTokenFile) != cfg.vaultTokenFile || !filepath.IsAbs(cfg.vaultTokenFile) ||
		filepath.Clean(cfg.vaultTokenFile) != cfg.vaultTokenFile || cfg.vaultTokenFile == "/") {
		return config{}, errors.New("FLOWOPS_GOVERNANCE_RELAY_VAULT_TOKEN_FILE must be a clean absolute path")
	}
	var err error
	if cfg.interval, err = envDuration("FLOWOPS_GOVERNANCE_RELAY_INTERVAL", cfg.interval); err != nil {
		return config{}, err
	}
	if cfg.lease, err = envDuration("FLOWOPS_GOVERNANCE_RELAY_LEASE_DURATION", cfg.lease); err != nil {
		return config{}, err
	}
	if cfg.boundaryTimeout, err = envDuration("FLOWOPS_GOVERNANCE_RELAY_BOUNDARY_TIMEOUT", cfg.boundaryTimeout); err != nil {
		return config{}, err
	}
	if cfg.batch, err = envInt("FLOWOPS_GOVERNANCE_RELAY_BATCH_SIZE", cfg.batch); err != nil {
		return config{}, err
	}
	if cfg.quorum, err = envInt("FLOWOPS_GOVERNANCE_RELAY_QUORUM", cfg.quorum); err != nil {
		return config{}, err
	}
	if cfg.interval < time.Second || cfg.interval > 5*time.Minute || cfg.lease < time.Second || cfg.lease > time.Minute ||
		cfg.boundaryTimeout < time.Second || cfg.boundaryTimeout > 10*time.Second || cfg.batch < 1 || cfg.batch > 100 || cfg.quorum < 2 || cfg.quorum > 5 {
		return config{}, errors.New("governance relay timing, batch, or quorum is outside safe bounds")
	}
	if (mode == "inspect" || mode == "authorize") && (cfg.authorizeOrg == "" || cfg.authorizeWorkflow == "") {
		return config{}, errors.New("inspect and authorize modes require organization and workflow")
	}
	if mode == "authorize" && (cfg.authorizeKey == "" || cfg.authorizeSignatureFile == "") {
		return config{}, errors.New("authorize mode requires organization, workflow, key, and signature file")
	}
	return cfg, nil
}

func readSignatures(path string) ([]byte, error) {
	if strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("signature file must be a clean absolute path")
	}
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("signature parent is not securely controlled")
	}
	defer parent.Close()
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 || !securefile.OwnerAllowed(parentInfo) {
		return nil, errors.New("signature parent must be a private owner-controlled directory")
	}
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("signature file is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 8*1024 || !securefile.OwnerAllowed(info) {
		return nil, errors.New("signature file must be a private owner-controlled regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 8*1024+1))
	defer clear(raw)
	if err != nil || len(raw) > 8*1024 {
		return nil, errors.New("signature file is unreadable or too large")
	}
	encoded := raw
	if len(encoded) > 0 && encoded[len(encoded)-1] == '\n' {
		encoded = encoded[:len(encoded)-1]
	}
	if len(encoded) == 0 || !bytes.Equal(bytes.TrimSpace(encoded), encoded) {
		return nil, errors.New("signature file must contain one canonical base64 line")
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	written, err := base64.StdEncoding.Strict().Decode(decoded, encoded)
	decoded = decoded[:written]
	if err != nil || len(decoded) == 0 || len(decoded)%65 != 0 || len(decoded) > 50*65 {
		clear(decoded)
		return nil, errors.New("signature bundle is invalid")
	}
	return decoded, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" ||
		parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("FLOWOPS_GOVERNANCE_RELAY_DATABASE_URL must be a PostgreSQL URL")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("governance relay database must use sslmode=verify-full")
	}
	for _, key := range []string{"host", "hostaddr", "port", "dbname", "database", "user", "password", "search_path", "options"} {
		if parsed.Query().Has(key) {
			return fmt.Errorf("governance relay database URL must not override %s", key)
		}
	}
	return nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return value, nil
}
func envInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return value, nil
}
