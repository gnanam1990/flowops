package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpsignerruntime"
)

func TestLoadConfigPinsEverySignerBoundaryAndRejectsSubstitution(t *testing.T) {
	directory := signerTempDir(t)
	setValidEnvironment(t, directory)
	config, err := loadConfig()
	if err != nil || config.keyID != "spend-authorizer-1" || config.epoch != 7 || config.dependencyTimeout != 3*time.Second {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	t.Setenv("FLOWOPS_SIGNER_ARTIFACT_SOCKET", filepath.Join(directory, "signer.sock"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("shared signer and artifact socket was accepted")
	}
	setValidEnvironment(t, directory)
	t.Setenv("FLOWOPS_SIGNER_KEY_EPOCH", "07")
	if _, err := loadConfig(); err == nil {
		t.Fatal("noncanonical key epoch was accepted")
	}
}

func TestLoadArtifactKeyRequiresCanonicalPrivateNonzeroFile(t *testing.T) {
	directory := signerTempDir(t)
	valid := filepath.Join(directory, "valid.key")
	writeKey(t, valid, makeKey(1), 0o600)
	key, err := loadArtifactKey(valid)
	if err != nil || len(key) != 32 || key[0] != 1 {
		t.Fatalf("key=%x err=%v", key, err)
	}
	for name, value := range map[string]struct {
		raw  string
		mode os.FileMode
	}{
		"zero":   {base64.StdEncoding.EncodeToString(make([]byte, 32)), 0o600},
		"short":  {base64.StdEncoding.EncodeToString([]byte("short")), 0o600},
		"public": {base64.StdEncoding.EncodeToString(makeKey(2)), 0o644},
	} {
		path := filepath.Join(directory, name+".key")
		if err := os.WriteFile(path, []byte(value.raw+"\n"), value.mode); err != nil {
			t.Fatal(err)
		}
		if _, err := loadArtifactKey(path); err == nil {
			t.Fatalf("invalid key %s was accepted", name)
		}
	}
	target := signerTempDir(t)
	keyPath := filepath.Join(target, "linked.key")
	writeKey(t, keyPath, makeKey(3), 0o600)
	linkedParent := filepath.Join(directory, "linked-parent")
	if err := os.Symlink(target, linkedParent); err != nil {
		t.Fatal(err)
	}
	if _, err := loadArtifactKey(filepath.Join(linkedParent, "linked.key")); err == nil {
		t.Fatal("artifact key through a symlink parent was accepted")
	}
}

func TestRunStartsRealDualSocketRuntimeAndStopsCleanly(t *testing.T) {
	directory := signerTempDir(t)
	setValidEnvironment(t, directory)
	keyPath := filepath.Join(directory, "artifact.key")
	writeKey(t, keyPath, makeKey(9), 0o600)
	writeKey(t, filepath.Join(directory, "keeper.token"), makeKey(10), 0o600)
	ring := startDependency(t, filepath.Join(directory, "ring.sock"), "ring6")
	defer ring.Close()
	activation := startDependency(t, filepath.Join(directory, "activation.sock"), "activation")
	defer activation.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- run(ctx) }()
	signerPath := filepath.Join(directory, "signer.sock")
	artifactPath := filepath.Join(directory, "artifact.sock")
	waitSocket(t, signerPath)
	waitSocket(t, artifactPath)
	if _, err := os.Stat(filepath.Join(directory, "ledger.jsonl")); err != nil {
		t.Fatalf("durable ledger not created: %v", err)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("signer runtime did not stop")
	}
}

func setValidEnvironment(t *testing.T, directory string) {
	t.Helper()
	t.Setenv("FLOWOPS_SIGNER_KEY_ID", "spend-authorizer-1")
	t.Setenv("FLOWOPS_SIGNER_ADDRESS", "0x1111111111111111111111111111111111111111")
	t.Setenv("FLOWOPS_SIGNER_KEY_EPOCH", "7")
	t.Setenv("FLOWOPS_SIGNER_KEEPER_ID", "keeper-primary")
	t.Setenv("FLOWOPS_SIGNER_LEDGER_PATH", filepath.Join(directory, "ledger.jsonl"))
	t.Setenv("FLOWOPS_SIGNER_ARTIFACT_KEY_FILE", filepath.Join(directory, "artifact.key"))
	t.Setenv("FLOWOPS_SIGNER_KEEPER_TOKEN_FILE", filepath.Join(directory, "keeper.token"))
	t.Setenv("FLOWOPS_SIGNER_RUNTIME_SOCKET", filepath.Join(directory, "signer.sock"))
	t.Setenv("FLOWOPS_SIGNER_ARTIFACT_SOCKET", filepath.Join(directory, "artifact.sock"))
	t.Setenv("FLOWOPS_SIGNER_RING6_SOCKET", filepath.Join(directory, "ring.sock"))
	t.Setenv("FLOWOPS_SIGNER_ACTIVATION_SOCKET", filepath.Join(directory, "activation.sock"))
	t.Setenv("FLOWOPS_SIGNER_DEPENDENCY_TIMEOUT", "")
}

func signerTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "aspcmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func writeKey(t *testing.T, path string, key []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func makeKey(first byte) []byte {
	key := make([]byte, 32)
	key[0] = first
	for index := 1; index < len(key); index++ {
		key[index] = byte(index)
	}
	return key
}

func startDependency(t *testing.T, path, name string) *http.Server {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"protocol": ascpsignerruntime.DependencyProtocol, "boundary": name, "status": "ok"})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return server
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket was not created: %s", path)
}

func TestConfigDoesNotAcceptWhitespaceOrRelativePaths(t *testing.T) {
	directory := signerTempDir(t)
	setValidEnvironment(t, directory)
	t.Setenv("FLOWOPS_SIGNER_LEDGER_PATH", " relative.jsonl ")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path error=%v", err)
	}
}
