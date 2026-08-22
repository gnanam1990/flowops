package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpsignerruntime"
	"github.com/gnanam1990/flowops/internal/securefile"
)

type startupConfig struct {
	keyID, keeperID, signerAddress string
	epoch                          uint64
	ledgerPath, artifactKeyPath    string
	keeperTokenPath                string
	signerSocket, artifactSocket   string
	ring6Socket, activationSocket  string
	dependencyTimeout              time.Duration
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("ASCP isolated signer runtime stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	ring6Boundary, err := ascpsignerruntime.NewDependencyBoundary("ring6", config.ring6Socket, config.dependencyTimeout)
	if err != nil {
		return err
	}
	activationBoundary, err := ascpsignerruntime.NewDependencyBoundary("activation", config.activationSocket, config.dependencyTimeout)
	if err != nil {
		return err
	}
	if err := ascpsignerruntime.ValidateDependencySockets(ring6Boundary, activationBoundary); err != nil {
		return err
	}
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := checkDependencies(startupCtx, ring6Boundary, activationBoundary); err != nil {
		return err
	}

	activationVerifier, err := ascpsignerruntime.NewUnixActivationVerifier(activationBoundary)
	if err != nil {
		return err
	}
	key, err := loadArtifactKey(config.artifactKeyPath)
	if err != nil {
		return err
	}
	keeperToken, err := loadKeeperToken(config.keeperTokenPath)
	if err != nil {
		clear(key)
		return err
	}
	defer clear(keeperToken)
	if subtle.ConstantTimeCompare(key, keeperToken) == 1 {
		clear(key)
		return errors.New("signer artifact key and keeper capability must be cryptographically distinct")
	}
	cipher, err := ascpbearer.NewAESGCMCipher(key, rand.Reader)
	clear(key)
	if err != nil {
		return err
	}
	ledger, err := ascpbearer.OpenFileSignerStoreContext(startupCtx, config.ledgerPath, cipher, activationVerifier, time.Now, rand.Reader)
	if err != nil {
		return fmt.Errorf("open isolated signer ledger: %w", err)
	}
	defer ledger.Close()
	ring6, err := ascpsignerruntime.NewUnixRing6Engine(ring6Boundary)
	if err != nil {
		return err
	}
	pinned, err := ascpsignerruntime.NewPinnedEngine(ring6, config.keyID, config.epoch, config.keeperID, config.signerAddress)
	if err != nil {
		return err
	}
	prepared, err := ascpbearer.NewLedgerPreparedSigner(ledger, pinned)
	if err != nil {
		return err
	}
	service, err := ascpsignerruntime.NewService(prepared, ledger, keeperToken)
	if err != nil {
		return err
	}
	slog.Info("ASCP isolated signer runtime started", "keyId", config.keyID, "keyEpoch", config.epoch, "keeperId", config.keeperID)
	return service.ServeUnix(ctx, ascpsignerruntime.UnixConfig{
		SignerSocket: config.signerSocket, ArtifactSocket: config.artifactSocket, RequestTimeout: config.dependencyTimeout,
	})
}

func checkDependencies(ctx context.Context, boundaries ...*ascpsignerruntime.DependencyBoundary) error {
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
		keyID: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_KEY_ID")), keeperID: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_KEEPER_ID")), signerAddress: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_ADDRESS")),
		ledgerPath: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_LEDGER_PATH")), artifactKeyPath: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_ARTIFACT_KEY_FILE")),
		keeperTokenPath: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_KEEPER_TOKEN_FILE")),
		signerSocket:    strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_RUNTIME_SOCKET")), artifactSocket: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_ARTIFACT_SOCKET")),
		ring6Socket: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_RING6_SOCKET")), activationSocket: strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_ACTIVATION_SOCKET")),
		dependencyTimeout: 3 * time.Second,
	}
	if !identifierPattern.MatchString(config.keyID) || !identifierPattern.MatchString(config.keeperID) {
		return startupConfig{}, errors.New("signer key and keeper identifiers are required and must be canonical")
	}
	if !common.IsHexAddress(config.signerAddress) || config.signerAddress != strings.ToLower(config.signerAddress) ||
		common.HexToAddress(config.signerAddress) == (common.Address{}) {
		return startupConfig{}, errors.New("FLOWOPS_SIGNER_ADDRESS must be a canonical nonzero address")
	}
	epoch, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_KEY_EPOCH")), 10, 64)
	if err != nil || epoch == 0 {
		return startupConfig{}, errors.New("FLOWOPS_SIGNER_KEY_EPOCH must be a positive canonical integer")
	}
	if strconv.FormatUint(epoch, 10) != strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_KEY_EPOCH")) {
		return startupConfig{}, errors.New("FLOWOPS_SIGNER_KEY_EPOCH must be a positive canonical integer")
	}
	config.epoch = epoch
	if raw := strings.TrimSpace(os.Getenv("FLOWOPS_SIGNER_DEPENDENCY_TIMEOUT")); raw != "" {
		config.dependencyTimeout, err = time.ParseDuration(raw)
		if err != nil {
			return startupConfig{}, errors.New("FLOWOPS_SIGNER_DEPENDENCY_TIMEOUT is invalid")
		}
	}
	if config.dependencyTimeout < time.Second || config.dependencyTimeout > 10*time.Second {
		return startupConfig{}, errors.New("signer dependency timeout must be between 1s and 10s")
	}
	paths := []string{config.ledgerPath, config.artifactKeyPath, config.keeperTokenPath, config.signerSocket, config.artifactSocket, config.ring6Socket, config.activationSocket}
	seen := map[string]struct{}{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.TrimSpace(path) != path || path == "/" {
			return startupConfig{}, errors.New("all signer paths must be clean absolute paths")
		}
		if _, exists := seen[path]; exists {
			return startupConfig{}, errors.New("signer ledger, key, and socket paths must be distinct")
		}
		seen[path] = struct{}{}
	}
	if config.ledgerPath == config.artifactKeyPath || filepath.Dir(config.ledgerPath) != filepath.Dir(config.artifactKeyPath) {
		return startupConfig{}, errors.New("signer ledger and artifact key must be distinct files in the same secured volume")
	}
	return config, nil
}

func loadArtifactKey(path string) ([]byte, error) {
	return loadPrivateSecret(path, "artifact key")
}

func loadKeeperToken(path string) ([]byte, error) {
	return loadPrivateSecret(path, "keeper capability")
}

func loadPrivateSecret(path, purpose string) ([]byte, error) {
	secret, err := securefile.ReadCanonicalBase64Secret(path)
	if err != nil {
		return nil, fmt.Errorf("load signer %s: %w", purpose, err)
	}
	return secret, nil
}
