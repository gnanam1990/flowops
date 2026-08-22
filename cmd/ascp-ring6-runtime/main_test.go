package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpring6"
	"github.com/gnanam1990/flowops/internal/ascpsignerruntime"
)

func TestLoadConfigPinsDistinctCanonicalBoundaries(t *testing.T) {
	directory := ringRuntimeTempDir(t)
	setRingRuntimeEnvironment(t, directory)
	config, err := loadConfig()
	if err != nil || config.keyID != "key-1" || config.keyEpoch != 7 || config.dependencyTimeout != 3*time.Second {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	t.Setenv("FLOWOPS_RING6_HSM_SOCKET", filepath.Join(directory, "verifier.sock"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("shared component path accepted")
	}
	setRingRuntimeEnvironment(t, directory)
	t.Setenv("FLOWOPS_RING6_KEY_EPOCH", "07")
	if _, err := loadConfig(); err == nil {
		t.Fatal("noncanonical epoch accepted")
	}
	setRingRuntimeEnvironment(t, directory)
	t.Setenv("FLOWOPS_RING6_KEY_ID", "not canonical!")
	if _, err := loadConfig(); err == nil {
		t.Fatal("noncanonical key identifier accepted")
	}
	setRingRuntimeEnvironment(t, directory)
	t.Setenv("FLOWOPS_RING6_SIGNER_ADDRESS", "1111111111111111111111111111111111111111")
	if _, err := loadConfig(); err == nil {
		t.Fatal("signer address without canonical prefix accepted")
	}
	setRingRuntimeEnvironment(t, directory)
	t.Setenv("FLOWOPS_RING6_HSM_SOCKET", filepath.Join(directory, strings.Repeat("x", 128)+".sock"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("overlong Unix socket path accepted")
	}
}

func TestRunStartsFullRing6WirePathAndStopsCleanly(t *testing.T) {
	directory := ringRuntimeTempDir(t)
	setRingRuntimeEnvironment(t, directory)
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOWOPS_RING6_SIGNER_ADDRESS", strings.ToLower(crypto.PubkeyToAddress(privateKey.PublicKey).Hex()))
	verifier := startRingComponent(t, filepath.Join(directory, "verifier.sock"), "verifier", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify", func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Protocol  string                     `json:"protocol"`
				Input     ascpbearer.ActivationInput `json:"input"`
				InputHash string                     `json:"inputHash"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Protocol != ascpring6.ComponentProtocol {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeRingJSON(w, map[string]any{"verified": true, "inputHash": request.InputHash})
		})
	})
	defer func() { _ = verifier.Close() }()
	hsm := startRingComponent(t, filepath.Join(directory, "hsm.sock"), "hsm", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/sign", func(w http.ResponseWriter, r *http.Request) {
			var envelope struct {
				Protocol string               `json:"protocol"`
				Request  ascpring6.HSMRequest `json:"request"`
			}
			if json.NewDecoder(r.Body).Decode(&envelope) != nil || envelope.Protocol != ascpring6.ComponentProtocol {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			signature, signErr := crypto.Sign(common.HexToHash(envelope.Request.Digest).Bytes(), privateKey)
			if signErr != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeRingJSON(w, ascpring6.HSMResult{OperationHandle: "hsm-operation-1", Digest: envelope.Request.Digest, Signature: signature})
		})
	})
	defer func() { _ = hsm.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- run(ctx) }()
	runtimeSocket := filepath.Join(directory, "ring6.sock")
	waitRingRuntimeSocket(t, runtimeSocket)
	boundary, err := ascpsignerruntime.NewDependencyBoundary("ring6", runtimeSocket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine, err := ascpsignerruntime.NewUnixRing6Engine(boundary)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload, evidence := []byte("wire-payload"), []byte("wire-evidence")
	input := ascpbearer.ActivationInput{
		RequestID: ringCommandHash(1), AuthorizationID: ringCommandHash(2), OperationID: ringCommandHash(3), ReservationID: ringCommandHash(4), ActionID: "wire-action",
		CanonicalPayload: payload, CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload), EvidenceBundle: evidence, EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence),
		Digest: ringCommandHash(5), Nonce: ringCommandHash(6), InstrumentType: ascpbearer.InstrumentLockAuthorization, SignerBindingVersion: 1,
		SignerKeyID: "key-1", KeyEpoch: 7, ModuleAddress: "0x1111111111111111111111111111111111111111", SafeAddress: "0x2222222222222222222222222222222222222222",
		KeeperID: "keeper-1", ValidAfter: now, ValidUntil: now.Add(5 * time.Minute),
	}
	signature, err := engine.VerifyAndSign(context.Background(), input)
	defer clear(signature)
	expected, signErr := crypto.Sign(common.HexToHash(input.Digest).Bytes(), privateKey)
	defer clear(expected)
	if err != nil || signErr != nil || !bytes.Equal(signature, expected) {
		t.Fatalf("wire signature=%x expected=%x err=%v", signature, expected, err)
	}
	if info, err := os.Stat(filepath.Join(directory, "ring6.jsonl")); err != nil || info.Size() == 0 {
		t.Fatalf("journal info=%v err=%v", info, err)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ring 6 runtime did not stop")
	}
}

func setRingRuntimeEnvironment(t *testing.T, directory string) {
	t.Helper()
	t.Setenv("FLOWOPS_RING6_KEY_ID", "key-1")
	t.Setenv("FLOWOPS_RING6_KEY_EPOCH", "7")
	t.Setenv("FLOWOPS_RING6_KEEPER_ID", "keeper-1")
	t.Setenv("FLOWOPS_RING6_SIGNER_ADDRESS", "0x1111111111111111111111111111111111111111")
	t.Setenv("FLOWOPS_RING6_JOURNAL_PATH", filepath.Join(directory, "ring6.jsonl"))
	t.Setenv("FLOWOPS_RING6_RUNTIME_SOCKET", filepath.Join(directory, "ring6.sock"))
	t.Setenv("FLOWOPS_RING6_VERIFIER_SOCKET", filepath.Join(directory, "verifier.sock"))
	t.Setenv("FLOWOPS_RING6_HSM_SOCKET", filepath.Join(directory, "hsm.sock"))
	t.Setenv("FLOWOPS_RING6_DEPENDENCY_TIMEOUT", "")
}

func ringRuntimeTempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(base, "ring6cmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func startRingComponent(t *testing.T, path, boundary string, configure func(*http.ServeMux)) *http.Server {
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
		writeRingJSON(w, map[string]string{"protocol": ascpring6.ComponentProtocol, "boundary": boundary, "status": "ok"})
	})
	configure(mux)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	return server
}

func writeRingJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func waitRingRuntimeSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Ring 6 runtime socket was not created")
}

func ringCommandHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
