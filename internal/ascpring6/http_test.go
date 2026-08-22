package ascpring6

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestHandlerSignsExactRequestAndRejectsProtocolAndJSONSubstitution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
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
	defer journal.Close()
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
	defer journal.Close()
	service := ringService(t, journal, &testVerifier{}, &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}, key, now)
	socket := filepath.Join(directory, "ring6.sock")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.ServeUnix(ctx, UnixConfig{Socket: socket, RequestTimeout: time.Second}) }()
	waitForSocket(t, socket)
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}
	client := unixHTTPClient(socket)
	response, err := client.Get("http://unix/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", response.StatusCode)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
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
}

func serveJSON(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/verify-and-sign", bytes.NewReader(body))
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
