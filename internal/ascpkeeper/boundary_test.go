package ascpkeeper

import (
	"bytes"
	"context"
	"encoding/base64"
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

func unixBoundaryServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "flowops-keeper-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "boundary.sock")
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return path
}

func healthHandler(boundary string, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"protocol":"` + boundaryProtocolVersion + `","boundary":"` + boundary + `","status":"ok"}`))
	})
	if next != nil {
		mux.Handle("/", next)
	}
	return mux
}

func TestUnixBoundaryChecksSocketAndExactIdentity(t *testing.T) {
	path := unixBoundaryServer(t, healthHandler("artifact", nil))
	boundary, err := NewUnixBoundary("artifact", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrong, err := NewUnixBoundary("wallet", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := wrong.Check(context.Background()); err == nil {
		t.Fatal("expected boundary identity mismatch")
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(context.Background()); err == nil {
		t.Fatal("expected world-writable socket rejection")
	}
}

func TestUnixBoundaryRejectsGroupWritableParent(t *testing.T) {
	path := unixBoundaryServer(t, healthHandler("artifact", nil))
	if err := os.Chmod(filepath.Dir(path), 0o770); err != nil {
		t.Fatal(err)
	}
	boundary, err := NewUnixBoundary("artifact", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("expected group-writable parent rejection, got %v", err)
	}
}

func TestValidateDistinctSocketsRejectsFilesystemAliases(t *testing.T) {
	path := unixBoundaryServer(t, healthHandler("artifact", nil))
	alias := filepath.Join(filepath.Dir(path), "boundary-alias.sock")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDistinctSockets(map[string]string{"artifact": path, "wallet": alias}); err == nil || !strings.Contains(err.Error(), "same Unix socket") {
		t.Fatalf("expected socket identity alias rejection, got %v", err)
	}
}

func TestUnixBoundaryRejectsUnknownResponseFields(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/nonce" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"nonce":7,"untrusted":true}`))
	})
	path := unixBoundaryServer(t, healthHandler("chain", handler))
	boundary, _ := NewUnixBoundary("chain", path, time.Second)
	chain, _ := NewUnixChainBoundary(boundary)
	if _, err := chain.PendingNonce(context.Background(), 84532, "0x1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("expected strict response rejection")
	}
}

func TestAuthenticatedArtifactBoundaryRejectsCapabilitySubstitution(t *testing.T) {
	capability := bytes.Repeat([]byte{0xa7}, 32)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Header.Get("Authorization") != "Bearer "+base64.StdEncoding.EncodeToString(capability) {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"code":"ARTIFACT_UNAUTHORIZED"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"handle":{"id":"expected-handle"},"artifact":"c2VjcmV0"}`))
	})
	path := unixBoundaryServer(t, healthHandler("artifact", handler))
	wrongCapability := bytes.Repeat([]byte{0xb8}, 32)
	boundary, err := NewAuthenticatedUnixBoundary("artifact", path, time.Second, wrongCapability)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewUnixArtifactClient(boundary)
	if err != nil {
		t.Fatal(err)
	}
	handle, artifact, err := client.Release(context.Background(), "handle", "keeper")
	if err == nil || handle.ID != "" || artifact != nil {
		t.Fatalf("substituted capability output: handle=%+v artifact=%q err=%v", handle, artifact, err)
	}
}

func TestUnixBoundaryErrorNeverReturnsDecodedSecretBuffers(t *testing.T) {
	t.Run("artifact", func(t *testing.T) {
		capability := bytes.Repeat([]byte{0xa7}, 32)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer "+base64.StdEncoding.EncodeToString(capability) {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"handle":{},"artifact":"c2VjcmV0","unknown":true}`))
		})
		path := unixBoundaryServer(t, healthHandler("artifact", handler))
		boundary, _ := NewAuthenticatedUnixBoundary("artifact", path, time.Second, capability)
		client, _ := NewUnixArtifactClient(boundary)
		handle, artifact, err := client.Release(context.Background(), "handle", "keeper")
		if err == nil || handle.ID != "" || artifact != nil {
			t.Fatalf("secret-bearing error output was returned: handle=%+v artifact=%q err=%v", handle, artifact, err)
		}
	})

	t.Run("wallet", func(t *testing.T) {
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"hash":"0x01","raw":"c2VjcmV0","unknown":true}`))
		})
		path := unixBoundaryServer(t, healthHandler("wallet", handler))
		boundary, _ := NewUnixBoundary("wallet", path, time.Second)
		wallet, _ := NewUnixWallet(boundary)
		signed, err := wallet.Sign(context.Background(), UnsignedTransaction{})
		if err == nil || signed.Hash != "" || signed.Raw != nil {
			t.Fatalf("wallet error returned signed bytes: signed=%+v err=%v", signed, err)
		}
	})

	t.Run("assembler", func(t *testing.T) {
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"transaction":{"data":"c2VjcmV0"},"unknown":true}`))
		})
		path := unixBoundaryServer(t, healthHandler("assembler", handler))
		boundary, _ := NewUnixBoundary("assembler", path, time.Second)
		assembler, _ := NewUnixAssembler(boundary)
		transaction, err := assembler.Assemble(context.Background(), Job{}, nil, 0, Fee{})
		if err == nil || transaction.Data != nil {
			t.Fatalf("assembler error returned transaction bytes: transaction=%+v err=%v", transaction, err)
		}
	})

	t.Run("sealer", func(t *testing.T) {
		handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			if request.URL.Path == "/v1/seal" {
				_, _ = writer.Write([]byte(`{"ciphertext":"c2VjcmV0","keyId":"key","unknown":true}`))
				return
			}
			_, _ = writer.Write([]byte(`{"raw":"c2VjcmV0","unknown":true}`))
		})
		path := unixBoundaryServer(t, healthHandler("sealer", handler))
		boundary, _ := NewUnixBoundary("sealer", path, time.Second)
		sealer, _ := NewUnixSealer(boundary)
		ciphertext, keyID, err := sealer.Seal(context.Background(), []byte("raw"), []byte("aad"))
		if err == nil || ciphertext != nil || keyID != "" {
			t.Fatalf("sealer error returned ciphertext: ciphertext=%q keyID=%q err=%v", ciphertext, keyID, err)
		}
		raw, err := sealer.Open(context.Background(), []byte("ciphertext"), "key", []byte("aad"))
		if err == nil || raw != nil {
			t.Fatalf("sealer error returned raw bytes: raw=%q err=%v", raw, err)
		}
	})
}

func TestUnixBoundaryRejectsMissingNonceInsteadOfDefaultingToZero(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	})
	path := unixBoundaryServer(t, healthHandler("chain", handler))
	boundary, _ := NewUnixBoundary("chain", path, time.Second)
	chain, _ := NewUnixChainBoundary(boundary)
	if _, err := chain.PendingNonce(context.Background(), 84532, "0x1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("expected missing nonce rejection")
	}
}

func TestUnixBoundaryRejectsDuplicateEconomicFields(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"nonce":7,"nonce":0}`))
	})
	path := unixBoundaryServer(t, healthHandler("chain", handler))
	boundary, _ := NewUnixBoundary("chain", path, time.Second)
	chain, _ := NewUnixChainBoundary(boundary)
	if _, err := chain.PendingNonce(context.Background(), 84532, "0x1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("expected duplicate nonce rejection")
	}
}

func TestUnixBoundaryRejectsJSONPrefixContentType(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/jsonp")
		_, _ = writer.Write([]byte(`{"nonce":7}`))
	})
	path := unixBoundaryServer(t, healthHandler("chain", handler))
	boundary, _ := NewUnixBoundary("chain", path, time.Second)
	chain, _ := NewUnixChainBoundary(boundary)
	if _, err := chain.PendingNonce(context.Background(), 84532, "0x1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("expected non-JSON media type rejection")
	}
}

func TestUnixChainBoundaryClassifiesOnlyExplicitBroadcastFailures(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`{"code":"UNDERPRICED"}`))
	})
	path := unixBoundaryServer(t, healthHandler("broadcast", handler))
	boundary, _ := NewUnixBoundary("broadcast", path, time.Second)
	broadcaster, _ := NewUnixBroadcaster(boundary)
	if _, err := broadcaster.Broadcast(context.Background(), []byte{1}); !errors.Is(err, ErrBroadcastUnderpriced) || errors.Is(err, ErrBroadcastRejected) {
		t.Fatalf("unexpected broadcast classification: %v", err)
	}
}

func TestBoundaryConstructorsRejectCrossWiring(t *testing.T) {
	boundary, err := NewUnixBoundary("assembler", "/run/flowops/assembler.sock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewUnixWallet(boundary); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected cross-wire rejection, got %v", err)
	}
}

func TestAssemblerBoundaryNeverReceivesLeaseTokenOrSignerHandle(t *testing.T) {
	seen := make(chan map[string]any, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		seen <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"transaction":{"chainId":84532,"from":"0x1111111111111111111111111111111111111111","to":"0x2222222222222222222222222222222222222222","valueWei":"0","nonce":1,"gasLimit":100000,"data":"AQIDBA==","fee":{"maxFeePerGasWei":"100","maxPriorityFeePerGasWei":"2"}}}`))
	})
	path := unixBoundaryServer(t, healthHandler("assembler", handler))
	boundary, _ := NewUnixBoundary("assembler", path, time.Second)
	assembler, _ := NewUnixAssembler(boundary)
	job := Job{SignerHandle: "sensitive_signer_handle", LeaseOwner: "keeper-primary", LeaseToken: "sensitive_lease_token", LastError: "sensitive internal failure"}
	if _, err := assembler.Assemble(context.Background(), job, []byte{1}, 1, Fee{"100", "2"}); err != nil {
		t.Fatal(err)
	}
	request := <-seen
	encoded, err := json.Marshal(request["job"])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive_signer_handle", "sensitive_lease_token", "sensitive internal failure"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("boundary request leaked %q: %s", secret, encoded)
		}
	}
}
