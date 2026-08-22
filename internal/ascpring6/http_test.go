package ascpring6

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

func TestHandlerSignsExactRequestAndRejectsProtocolAndJSONSubstitution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	service := ringService(t, journal, &testVerifier{}, &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}, key, now)
	input := ringInput(now)
	body, _ := json.Marshal(struct {
		Protocol string `json:"protocol"`
		Input    any    `json:"input"`
	}{Protocol, input})

	response := serveJSON(service.Handler(), body)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "signature") {
		t.Fatalf("success status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	mutations := map[string][]byte{
		"wrong-protocol": bytes.Replace(body, []byte(Protocol), []byte("ASCP_SIGNER_DEPENDENCY_V0"), 1),
		"unknown-field":  bytes.Replace(body, []byte(`{"protocol"`), []byte(`{"extra":true,"protocol"`), 1),
		"duplicate":      bytes.Replace(body, []byte(`{"protocol"`), []byte(`{"protocol":"`+Protocol+`","protocol"`), 1),
		"trailing":       append(append([]byte(nil), body...), []byte(` {}`)...),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			got := serveJSON(service.Handler(), mutation)
			if got.Code != http.StatusBadRequest || strings.Contains(got.Body.String(), "signature") {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
}

func TestHandlerMapsPermanentRefusalWithoutLeakingSignature(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	service := ringService(t, journal, &testVerifier{err: &PermanentRefusal{Code: "EVIDENCE_INVALID"}}, &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}, key, now)
	body, _ := json.Marshal(struct {
		Protocol string `json:"protocol"`
		Input    any    `json:"input"`
	}{Protocol, ringInput(now)})
	response := serveJSON(service.Handler(), body)
	if response.Code != http.StatusUnprocessableEntity || response.Body.String() != "{\"code\":\"SIGNER_REFUSED\"}\n" || strings.Contains(response.Body.String(), "signature") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestServeUnixUsesPrivateSocketAndRefusesExistingPath(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	directory := ringTempDir(t)
	journal, err := OpenJournal(context.Background(), filepath.Join(directory, "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	service := ringService(t, journal, &testVerifier{}, &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}, key, now)
	socket := filepath.Join(directory, "ring6.sock")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.ServeUnix(ctx, UnixConfig{Socket: socket, RequestTimeout: time.Second}) }()
	waitForSocket(t, socket)
	waitForRuntimeHealth(t, socket)
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(privateBindPath(socket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private publish link remained after startup: %v", err)
	}
	client := unixHTTPClient(socket)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", response.StatusCode)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("graceful shutdown left socket path: %v", err)
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restarted := make(chan error, 1)
	go func() {
		restarted <- service.ServeUnix(restartCtx, UnixConfig{Socket: socket, RequestTimeout: time.Second})
	}()
	waitForSocket(t, socket)
	waitForRuntimeHealth(t, socket)
	restartCancel()
	if err := <-restarted; err != nil {
		t.Fatalf("same-path restart failed: %v", err)
	}

	existing := filepath.Join(directory, "existing.sock")
	if err := os.WriteFile(existing, []byte("do-not-remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.ServeUnix(context.Background(), UnixConfig{Socket: existing, RequestTimeout: time.Second}); err == nil {
		t.Fatal("existing path was accepted")
	}
	content, err := os.ReadFile(existing)
	if err != nil || string(content) != "do-not-remove" {
		t.Fatalf("existing path changed: %q err=%v", content, err)
	}
	staleTarget := filepath.Join(directory, "stale.sock")
	staleBind := privateBindPath(staleTarget)
	if err := os.WriteFile(staleBind, []byte("operator-inspection"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.ServeUnix(context.Background(), UnixConfig{Socket: staleTarget, RequestTimeout: time.Second}); err == nil {
		t.Fatal("stale private publish path was accepted")
	}
	content, err = os.ReadFile(staleBind)
	if err != nil || string(content) != "operator-inspection" {
		t.Fatalf("stale private publish path changed: %q err=%v", content, err)
	}
}

func TestServeUnixAllowsSequentialDependencyBudgets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	directory := ringTempDir(t)
	journal, err := OpenJournal(context.Background(), filepath.Join(directory, "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	delay := 600 * time.Millisecond
	verifier := delayedVerifier{delay: delay, delegate: &testVerifier{}}
	hsm := delayedHSM{delay: delay, delegate: &deterministicHSM{
		key: crypto.FromECDSA(key), operations: map[string]HSMResult{},
	}}
	service := ringService(t, journal, verifier, hsm, key, now)
	socket := filepath.Join(directory, "budget.sock")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.ServeUnix(ctx, UnixConfig{Socket: socket, RequestTimeout: time.Second}) }()
	waitForSocket(t, socket)
	waitForRuntimeHealth(t, socket)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("stop budget-test runtime: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("budget-test runtime did not stop")
		}
	})
	body, err := json.Marshal(struct {
		Protocol string `json:"protocol"`
		Input    any    `json:"input"`
	}{Protocol, ringInput(now)})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://unix/v1/verify-and-sign", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := unixHTTPClient(socket)
	client.Timeout = 4 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("sequential verifier and HSM budgets lost the response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(responseBody, []byte(`"signature"`)) {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody)
	}
}

type delayedVerifier struct {
	delay    time.Duration
	delegate IndependentVerifier
}

func (v delayedVerifier) Verify(ctx context.Context, input ascpbearer.ActivationInput, inputHash string) error {
	if err := waitDelay(ctx, v.delay); err != nil {
		return err
	}
	return v.delegate.Verify(ctx, input, inputHash)
}

type delayedHSM struct {
	delay    time.Duration
	delegate HSM
}

func (h delayedHSM) Sign(ctx context.Context, request HSMRequest) (HSMResult, error) {
	if err := waitDelay(ctx, h.delay); err != nil {
		return HSMResult{}, err
	}
	return h.delegate.Sign(ctx, request)
}

func waitDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestPrivateUnixListenerPreservesReplacementPathOnClose(t *testing.T) {
	directory := ringTempDir(t)
	path := filepath.Join(directory, "owned.sock")
	listener, err := listenPrivateUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err == nil {
		t.Fatal("replacement path was not detected")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement path changed: %q err=%v", content, err)
	}
}

func TestServeUnixReportsReplacementPathOnShutdown(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	directory := ringTempDir(t)
	journal, err := OpenJournal(context.Background(), filepath.Join(directory, "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	service := ringService(t, journal, &testVerifier{}, &deterministicHSM{
		key: crypto.FromECDSA(key), operations: map[string]HSMResult{},
	}, key, now)
	socket := filepath.Join(directory, "reported.sock")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.ServeUnix(ctx, UnixConfig{Socket: socket, RequestTimeout: time.Second}) }()
	waitForSocket(t, socket)
	waitForRuntimeHealth(t, socket)
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "no longer identifies this listener") {
			t.Fatalf("replacement shutdown error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not report replacement on shutdown")
	}
	content, err := os.ReadFile(socket)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("reported replacement changed: %q err=%v", content, err)
	}
}

func serveJSON(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/verify-and-sign", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Unix socket was not created")
}

func waitForRuntimeHealth(t *testing.T, path string) {
	t.Helper()
	client := unixHTTPClient(path)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/healthz", nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			cancel()
			if response.StatusCode == http.StatusOK {
				return
			}
		} else {
			cancel()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Ring 6 runtime health did not become ready")
}

func unixHTTPClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
}

func TestDecodeStrictRejectsExcessiveNesting(t *testing.T) {
	raw := []byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))
	var output any
	if err := decodeStrict(raw, &output); err == nil {
		t.Fatal("excessive JSON nesting accepted")
	}
}
