package ascpsignerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

func TestDependencyClientsVerifyIdentityRouteAndExactOutputs(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	directory := shortTempDir(t)
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signerAddress := strings.ToLower(crypto.PubkeyToAddress(privateKey.PublicKey).Hex())
	var ringCalls atomic.Int32
	ringPath := filepath.Join(directory, "ring.sock")
	ringServer := dependencyServer(t, ringPath, "ring6", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify-and-sign", func(w http.ResponseWriter, r *http.Request) {
			ringCalls.Add(1)
			var request struct {
				Protocol string                     `json:"protocol"`
				Input    ascpbearer.ActivationInput `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			digest := common.HexToHash(request.Input.Digest)
			signature, err := crypto.Sign(digest.Bytes(), privateKey)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, struct {
				Signature []byte `json:"signature"`
			}{signature})
		})
	})
	defer ringServer.Close()
	activationPath := filepath.Join(directory, "activation.sock")
	activationServer := dependencyServer(t, activationPath, "activation", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify-activation", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, struct {
				Verified bool `json:"verified"`
			}{true})
		})
		mux.HandleFunc("POST /v1/prove-unactivated", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, struct {
				Unactivated bool `json:"unactivated"`
			}{true})
		})
	})
	defer activationServer.Close()

	ringBoundary, _ := NewDependencyBoundary("ring6", ringPath, time.Second)
	activationBoundary, _ := NewDependencyBoundary("activation", activationPath, time.Second)
	if err := ValidateDependencySockets(ringBoundary, activationBoundary); err != nil {
		t.Fatal(err)
	}
	if err := ringBoundary.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := activationBoundary.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	ring, _ := NewUnixRing6Engine(ringBoundary)
	pinned, _ := NewPinnedEngine(ring, "signer-key-1", 1, "keeper-primary", signerAddress)
	input := validInput(now, 1)
	signature, err := pinned.VerifyAndSign(context.Background(), input)
	if err != nil || len(signature) != 65 || ringCalls.Load() != 1 {
		t.Fatalf("signature=%d calls=%d err=%v", len(signature), ringCalls.Load(), err)
	}
	changed := input
	changed.KeyEpoch++
	if _, err := pinned.VerifyAndSign(context.Background(), changed); err == nil || ringCalls.Load() != 1 {
		t.Fatalf("route substitution reached Ring 6: calls=%d err=%v", ringCalls.Load(), err)
	}
	wrongSigner, _ := NewPinnedEngine(ring, "signer-key-1", 1, "keeper-primary", "0x1111111111111111111111111111111111111111")
	if _, err := wrongSigner.VerifyAndSign(context.Background(), input); err == nil || ringCalls.Load() != 2 {
		t.Fatalf("wrong recovered signer was accepted: calls=%d err=%v", ringCalls.Load(), err)
	}

	verifier, _ := NewUnixActivationVerifier(activationBoundary)
	handle := ascpbearer.Handle{
		ID: "asph_0123456789abcdefghijklmnopqrstuv", RequestID: input.RequestID,
		AuthorizationID: input.AuthorizationID, ReservationID: input.ReservationID,
		ActionID: input.ActionID, OperationID: input.OperationID, SignerRequestHash: hash(100),
		CanonicalPayloadHash: input.CanonicalPayloadHash, Digest: input.Digest, Nonce: input.Nonce,
		SignerKeyID: input.SignerKeyID, KeyEpoch: input.KeyEpoch, KeeperID: input.KeeperID,
		ValidAfter: input.ValidAfter, ValidUntil: input.ValidUntil, State: ascpbearer.Prepared,
	}
	if err := verifier.VerifyActivation(context.Background(), handle, activationProof(input, handle.ID, now)); err != nil {
		t.Fatal(err)
	}
	if err := verifier.ProveUnactivated(context.Background(), handle, input.ValidUntil.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyClientRejectsDuplicateResponseAndSharedSocket(t *testing.T) {
	directory := shortTempDir(t)
	path := filepath.Join(directory, "duplicate.sock")
	server := dependencyServer(t, path, "ring6", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify-and-sign", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signature":"YQ==","signature":"YQ=="}`))
		})
	})
	defer server.Close()
	boundary, _ := NewDependencyBoundary("ring6", path, time.Second)
	if err := ValidateDependencySockets(boundary, boundary); err == nil {
		t.Fatal("shared dependency socket was accepted")
	}
	engine, _ := NewUnixRing6Engine(boundary)
	if _, err := engine.VerifyAndSign(context.Background(), validInput(time.Now().UTC(), 1)); err == nil {
		t.Fatal("duplicate dependency response was accepted")
	}
}

func TestRing6DependencyPreservesOnlyExplicitPermanentRefusal(t *testing.T) {
	directory := shortTempDir(t)
	path := filepath.Join(directory, "refuse.sock")
	server := dependencyServer(t, path, "ring6", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify-and-sign", func(w http.ResponseWriter, _ *http.Request) {
			writeFailure(w, http.StatusUnprocessableEntity, "SIGNER_REFUSED")
		})
	})
	defer server.Close()
	boundary, _ := NewDependencyBoundary("ring6", path, time.Second)
	engine, _ := NewUnixRing6Engine(boundary)
	if _, err := engine.VerifyAndSign(context.Background(), validInput(time.Now().UTC(), 1)); !errors.Is(err, ascpbearer.ErrSignerRefused) {
		t.Fatalf("permanent refusal lost: %v", err)
	}
}

func TestRing6DependencyDoesNotPromoteWrongStatusToPermanentRefusal(t *testing.T) {
	directory := shortTempDir(t)
	path := filepath.Join(directory, "transient.sock")
	server := dependencyServer(t, path, "ring6", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify-and-sign", func(w http.ResponseWriter, _ *http.Request) {
			writeFailure(w, http.StatusServiceUnavailable, "SIGNER_REFUSED")
		})
	})
	defer server.Close()
	boundary, _ := NewDependencyBoundary("ring6", path, time.Second)
	engine, _ := NewUnixRing6Engine(boundary)
	if _, err := engine.VerifyAndSign(context.Background(), validInput(time.Now().UTC(), 1)); err == nil || errors.Is(err, ascpbearer.ErrSignerRefused) {
		t.Fatalf("wrong status was promoted to permanent refusal: %v", err)
	}
}

func dependencyServer(t *testing.T, path, name string, routes func(*http.ServeMux)) *http.Server {
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
		writeJSON(w, http.StatusOK, struct {
			Protocol string `json:"protocol"`
			Boundary string `json:"boundary"`
			Status   string `json:"status"`
		}{DependencyProtocol, name, "ok"})
	})
	routes(mux)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return server
}
