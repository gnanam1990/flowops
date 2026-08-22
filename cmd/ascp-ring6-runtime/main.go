package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpring6"
)

type startupConfig struct {
	keyID, keeperID, signerAddress string
	keyEpoch                       uint64
	journalPath, runtimeSocket     string
	verifierSocket, hsmSocket      string
	dependencyTimeout              time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP Ring 6 runtime stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (returnErr error) {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	verifierBoundary, err := ascpring6.NewComponentBoundary("verifier", config.verifierSocket, config.dependencyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, verifierBoundary.Close()) }()
	hsmBoundary, err := ascpring6.NewComponentBoundary("hsm", config.hsmSocket, config.dependencyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, hsmBoundary.Close()) }()
	if err := ascpring6.ValidateComponentSockets(verifierBoundary, hsmBoundary); err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := checkComponents(healthCtx, verifierBoundary, hsmBoundary); err != nil {
		return err
	}
	journal, err := ascpring6.OpenJournal(ctx, config.journalPath)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, journal.Close()) }()
	verifier, err := ascpring6.NewUnixVerifier(verifierBoundary)
	if err != nil {
		return err
	}
	hsm, err := ascpring6.NewUnixHSM(hsmBoundary)
	if err != nil {
		return err
	}
	service, err := ascpring6.New(ascpring6.Config{
		Store: journal, Verifier: verifier, HSM: hsm, KeyID: config.keyID, KeyEpoch: config.keyEpoch,
		KeeperID: config.keeperID, SignerAddress: config.signerAddress,
	})
	if err != nil {
		return err
	}
	slog.Info("ASCP Ring 6 runtime started", "keyId", config.keyID, "keyEpoch", config.keyEpoch, "keeperId", config.keeperID)
	return service.ServeUnix(ctx, ascpring6.UnixConfig{Socket: config.runtimeSocket, RequestTimeout: config.dependencyTimeout})
}

func checkComponents(ctx context.Context, boundaries ...*ascpring6.ComponentBoundary) error {
	results := make(chan error, len(boundaries))
	for _, boundary := range boundaries {
		boundary := boundary
		go func() { results <- boundary.Check(ctx) }()
	}
	var combined error
	for range boundaries {
		combined = errors.Join(combined, <-results)
	}
	return combined
}

func loadConfig() (startupConfig, error) {
	config := startupConfig{
		keyID:             strings.TrimSpace(os.Getenv("FLOWOPS_RING6_KEY_ID")),
		keeperID:          strings.TrimSpace(os.Getenv("FLOWOPS_RING6_KEEPER_ID")),
		signerAddress:     strings.TrimSpace(os.Getenv("FLOWOPS_RING6_SIGNER_ADDRESS")),
		journalPath:       os.Getenv("FLOWOPS_RING6_JOURNAL_PATH"),
		runtimeSocket:     os.Getenv("FLOWOPS_RING6_RUNTIME_SOCKET"),
		verifierSocket:    os.Getenv("FLOWOPS_RING6_VERIFIER_SOCKET"),
		hsmSocket:         os.Getenv("FLOWOPS_RING6_HSM_SOCKET"),
		dependencyTimeout: 3 * time.Second,
	}
	if !ascpring6.ValidIdentifier(config.keyID) || !ascpring6.ValidIdentifier(config.keeperID) {
		return startupConfig{}, errors.New("Ring 6 key and keeper identifiers must be canonical")
	}
	if !ascpring6.ValidSignerAddress(config.signerAddress) {
		return startupConfig{}, errors.New("FLOWOPS_RING6_SIGNER_ADDRESS must be a canonical nonzero address")
	}
	rawEpoch := strings.TrimSpace(os.Getenv("FLOWOPS_RING6_KEY_EPOCH"))
	epoch, err := strconv.ParseUint(rawEpoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != rawEpoch {
		return startupConfig{}, errors.New("FLOWOPS_RING6_KEY_EPOCH must be a positive canonical integer")
	}
	config.keyEpoch = epoch
	if raw := strings.TrimSpace(os.Getenv("FLOWOPS_RING6_DEPENDENCY_TIMEOUT")); raw != "" {
		config.dependencyTimeout, err = time.ParseDuration(raw)
		if err != nil {
			return startupConfig{}, errors.New("FLOWOPS_RING6_DEPENDENCY_TIMEOUT is invalid")
		}
	}
	if config.dependencyTimeout < time.Second || config.dependencyTimeout > 10*time.Second {
		return startupConfig{}, errors.New("Ring 6 dependency timeout must be between 1s and 10s")
	}
	paths := []string{config.journalPath, config.runtimeSocket, config.verifierSocket, config.hsmSocket}
	seen := map[string]struct{}{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.TrimSpace(path) != path || path == "/" {
			return startupConfig{}, errors.New("all Ring 6 paths must be clean absolute paths")
		}
		if _, exists := seen[path]; exists {
			return startupConfig{}, errors.New("Ring 6 journal and socket paths must be distinct")
		}
		seen[path] = struct{}{}
	}
	if !ascpring6.ValidRuntimeSocketPath(config.runtimeSocket) {
		return startupConfig{}, errors.New("Ring 6 runtime socket path cannot fit its private publish path")
	}
	for _, path := range []string{config.verifierSocket, config.hsmSocket} {
		if !ascpring6.ValidSocketPath(path) {
			return startupConfig{}, errors.New("Ring 6 socket paths must fit the Unix socket address limit")
		}
	}
	return config, nil
}
