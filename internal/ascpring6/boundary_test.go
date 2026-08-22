package ascpring6

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestComponentBoundariesPinIdentityBindingsAndRefusalClass(t *testing.T) {
	directory := ringTempDir(t)
	inputHash := ringHash(8)
	verifierPath := filepath.Join(directory, "verifier.sock")
	hsmPath := filepath.Join(directory, "hsm.sock")
	stopVerifier := serveComponent(t, verifierPath, "verifier", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, struct {
				Verified  bool   `json:"verified"`
				InputHash string `json:"inputHash"`
			}{true, inputHash})
		})
	})
	defer stopVerifier()
	stopHSM := serveComponent(t, hsmPath, "hsm", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/sign", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, HSMResult{OperationHandle: "op-1", Digest: ringHash(9), Signature: make([]byte, 65)})
		})
	})
	defer stopHSM()

	verifierBoundary, err := NewComponentBoundary("verifier", verifierPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	hsmBoundary, err := NewComponentBoundary("hsm", hsmPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateComponentSockets(verifierBoundary, hsmBoundary); err != nil {
		t.Fatal(err)
	}
	if err := verifierBoundary.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifier, _ := NewUnixVerifier(verifierBoundary)
	if err := verifier.Verify(context.Background(), ringInput(time.Unix(1_800_000_000, 0).UTC()), inputHash); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), ringInput(time.Unix(1_800_000_000, 0).UTC()), ringHash(10)); !errors.Is(err, ErrBinding) {
		t.Fatalf("substituted hash error=%v", err)
	}
	if err := ValidateComponentSockets(verifierBoundary, verifierBoundary); err == nil {
		t.Fatal("shared component socket accepted")
	}
}

func TestUnixVerifierPersistsOnlyCanonicalUnprocessableRefusal(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		code      string
		permanent bool
	}{
		{"permanent", http.StatusUnprocessableEntity, "EVIDENCE_INVALID", true},
		{"transient", http.StatusServiceUnavailable, "EVIDENCE_INVALID", false},
		{"invalid-code", http.StatusUnprocessableEntity, "not valid!", false},
		{"missing-code", http.StatusUnprocessableEntity, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(ringTempDir(t), "verifier.sock")
			stop := serveComponent(t, path, "verifier", func(mux *http.ServeMux) {
				mux.HandleFunc("POST /v1/verify", func(w http.ResponseWriter, _ *http.Request) {
					writeFailure(w, test.status, test.code)
				})
			})
			defer stop()
			boundary, _ := NewComponentBoundary("verifier", path, time.Second)
			verifier, _ := NewUnixVerifier(boundary)
			err := verifier.Verify(context.Background(), ringInput(time.Unix(1_800_000_000, 0).UTC()), ringHash(8))
			if err == nil || errors.Is(err, ErrRefused) != test.permanent {
				t.Fatalf("error=%v permanent=%v", err, errors.Is(err, ErrRefused))
			}
		})
	}
}

func TestComponentBoundaryRejectsSocketReplacementAfterPinning(t *testing.T) {
	path := filepath.Join(ringTempDir(t), "verifier.sock")
	stopFirst := serveComponent(t, path, "verifier", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"verified": true, "inputHash": ringHash(8)})
		})
	})
	boundary, err := NewComponentBoundary("verifier", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stopFirst()
	stopReplacement := serveComponent(t, path, "verifier", func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/verify", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"verified": true, "inputHash": ringHash(8)})
		})
	})
	defer stopReplacement()
	if err := boundary.Check(context.Background()); err == nil {
		t.Fatal("replacement component socket retained the pinned identity")
	}
}

func TestValidateComponentSocketsRejectsDistinctPathsToSameSocket(t *testing.T) {
	directory := ringTempDir(t)
	verifierPath := filepath.Join(directory, "verifier.sock")
	hsmPath := filepath.Join(directory, "hsm.sock")
	stop := serveComponent(t, verifierPath, "verifier", func(*http.ServeMux) {})
	defer stop()
	if err := os.Link(verifierPath, hsmPath); err != nil {
		t.Fatal(err)
	}
	verifier, err := NewComponentBoundary("verifier", verifierPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = verifier.Close() }()
	hsm, err := NewComponentBoundary("hsm", hsmPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hsm.Close() }()
	if err := ValidateComponentSockets(verifier, hsm); err == nil {
		t.Fatal("distinct component paths to one socket were accepted")
	}
}

func TestComponentBoundaryRejectsReplacementBetweenCheckAndDial(t *testing.T) {
	path := filepath.Join(ringTempDir(t), "verifier.sock")
	stopFirst := serveComponent(t, path, "verifier", func(*http.ServeMux) {})
	defer stopFirst()
	boundary, err := NewComponentBoundary("verifier", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var stopReplacement func()
	_, err = boundary.dialPinned(context.Background(), func(ctx context.Context, network, address string) (net.Conn, error) {
		stopFirst()
		stopReplacement = serveComponent(t, path, "verifier", func(*http.ServeMux) {})
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	if stopReplacement != nil {
		defer stopReplacement()
	}
	if err == nil {
		t.Fatal("component replacement between validation and dial was accepted")
	}
}

func TestComponentBoundaryRejectsDuplicateAndWrongHealthIdentity(t *testing.T) {
	path := filepath.Join(ringTempDir(t), "hsm.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"protocol":"ASCP_RING6_COMPONENT_V1","boundary":"verifier","status":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"operationHandle":"one","operationHandle":"two","digest":"` + ringHash(1) + `","signature":"AA=="}`))
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); _ = listener.Close() }()
	boundary, _ := NewComponentBoundary("hsm", path, time.Second)
	if err := boundary.Check(context.Background()); err == nil {
		t.Fatal("wrong health identity accepted")
	}
	hsm, _ := NewUnixHSM(boundary)
	if _, err := hsm.Sign(context.Background(), HSMRequest{}); err == nil {
		t.Fatal("duplicate component response accepted")
	}
}

func TestComponentBoundaryPinLifecycle(t *testing.T) {
	path := filepath.Join(ringTempDir(t), "verifier.sock")
	stop := serveComponent(t, path, "verifier", func(*http.ServeMux) {})
	defer stop()
	boundary, err := NewComponentBoundary("verifier", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if source, err := inspectSocket(path); err != nil || source != boundary.identity {
		t.Fatalf("source identity=%v err=%v", source, err)
	}
	if pin, err := inspectSocket(boundary.pinPath); err != nil || pin != boundary.identity {
		t.Fatalf("pin identity=%v err=%v", pin, err)
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(boundary.pinPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("component pin remained after close: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("component source removed with pin: %v", err)
	}
	if err := os.WriteFile(boundary.pinPath, []byte("operator-inspection"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewComponentBoundary("verifier", path, time.Second); err == nil {
		t.Fatal("stale component pin was accepted")
	}
	content, err := os.ReadFile(boundary.pinPath)
	if err != nil || string(content) != "operator-inspection" {
		t.Fatalf("stale component pin changed: %q err=%v", content, err)
	}
}

func serveComponent(t *testing.T, path, boundary string, register func(*http.ServeMux)) func() {
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
		}{ComponentProtocol, boundary, "ok"})
	})
	register(mux)
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return func() { _ = server.Close(); _ = listener.Close() }
}
