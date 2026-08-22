package ascpbearer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeBoundaryServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "flowops-bearer-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "boundary.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return path
}

func runtimeJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(body))
}

func TestRuntimeBoundaryChecksExactIdentityAndSocketAliases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		runtimeJSON(writer, `{"protocol":"ASCP_BEARER_RUNTIME_V1","boundary":"signer","status":"ok"}`)
	})
	path := runtimeBoundaryServer(t, mux)
	boundary, err := NewRuntimeUnixBoundary("signer", path, time.Second)
	if err != nil || boundary.Check(context.Background()) != nil {
		t.Fatalf("boundary=%+v err=%v check=%v", boundary, err, boundary.Check(context.Background()))
	}
	wrong, _ := NewRuntimeUnixBoundary("mirror", path, time.Second)
	if err := wrong.Check(context.Background()); !errors.Is(err, ErrRuntimeBoundary) {
		t.Fatalf("wrong identity err=%v", err)
	}
	alias := filepath.Join(filepath.Dir(path), "alias.sock")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeSockets(map[string]string{"signer": path, "mirror": alias}); err == nil || !strings.Contains(err.Error(), "same Unix socket") {
		t.Fatalf("alias err=%v", err)
	}
}

func TestRuntimeBoundaryRejectsDuplicateAndUnknownEconomicFields(t *testing.T) {
	for name, response := range map[string]string{
		"duplicate": `{"handleId":"opaque-runtime-handle-0123456789abcdef","handleId":"attacker-runtime-handle-0123456789abcdef"}`,
		"unknown":   `{"handleId":"opaque-runtime-handle-0123456789abcdef","digest":"0x01"}`,
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/prepare", func(writer http.ResponseWriter, _ *http.Request) { runtimeJSON(writer, response) })
			path := runtimeBoundaryServer(t, mux)
			boundary, _ := NewRuntimeUnixBoundary("signer", path, time.Second)
			var output struct {
				HandleID string `json:"handleId"`
			}
			if err := boundary.call(context.Background(), http.MethodPost, "/v1/prepare", struct {
				Protocol string `json:"protocol"`
			}{runtimeBoundaryProtocol}, &output); !errors.Is(err, ErrRuntimeBoundary) {
				t.Fatalf("response accepted: %v", err)
			}
		})
	}
}

func TestRuntimeSignerAndMirrorEnforceExactResponseBindings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/prove-unactivated", func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Protocol    string `json:"protocol"`
			RequestID   string `json:"requestId"`
			OperationID string `json:"operationId"`
			ActionID    string `json:"actionId"`
			InputHash   string `json:"inputHash"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Protocol != runtimeBoundaryProtocol {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		proof := UnactivatedProof{RequestID: body.RequestID, OperationID: body.OperationID, ActionID: body.ActionID, InputHash: body.InputHash, Status: "EXPIRED_UNACTIVATED", ProvenAt: time.Now().UTC()}
		proof.ProofDigest, _ = UnactivatedProofDigest(proof)
		encoded, _ := json.Marshal(struct {
			Proof UnactivatedProof `json:"proof"`
		}{proof})
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(encoded)
		clear(encoded)
	})
	path := runtimeBoundaryServer(t, mux)
	boundary, _ := NewRuntimeUnixBoundary("signer", path, time.Second)
	signer, _ := NewRuntimeUnixSigner(boundary)
	request := runtimeRequest(time.Now().UTC())
	proof, err := signer.ProveUnactivated(context.Background(), request)
	if err != nil || proof.RequestID != request.RequestID || proof.InputHash != request.InputHash {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}

	mirror, _ := NewRuntimeUnixMirror(&RuntimeUnixBoundary{name: "mirror"})
	if err := mirror.PutPrimary(context.Background(), "bearer-registry/../../escape.json", []byte("payload"), bearerHash(80)); !errors.Is(err, ErrActivationInput) {
		t.Fatalf("unsafe object key err=%v", err)
	}
}

func TestRuntimeBoundaryRejectsGroupWritableParent(t *testing.T) {
	mux := http.NewServeMux()
	path := runtimeBoundaryServer(t, mux)
	if err := os.Chmod(filepath.Dir(path), 0o770); err != nil {
		t.Fatal(err)
	}
	boundary, _ := NewRuntimeUnixBoundary("signer", path, time.Second)
	if err := boundary.Check(context.Background()); !errors.Is(err, ErrRuntimeBoundary) {
		t.Fatalf("unsafe socket parent err=%v", err)
	}
}
