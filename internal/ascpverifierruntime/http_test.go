package ascpverifierruntime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/ascpverifier"
)

type staticVerifier struct {
	decision ascpverifier.SignedDecision
	err      error
}

type blockingVerifier struct {
	started chan struct{}
	release <-chan struct{}
}

func (v blockingVerifier) VerifyAndSign(ctx context.Context, _ ascpverifier.Input) (ascpverifier.SignedDecision, error) {
	v.started <- struct{}{}
	select {
	case <-v.release:
		return ascpverifier.SignedDecision{}, nil
	case <-ctx.Done():
		return ascpverifier.SignedDecision{}, ctx.Err()
	}
}

func (s staticVerifier) VerifyAndSign(context.Context, ascpverifier.Input) (ascpverifier.SignedDecision, error) {
	return s.decision, s.err
}

type memoryReplayGuard struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (g *memoryReplayGuard) Consume(_ context.Context, keyID, nonce, _ string, _, _ time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := keyID + "\x00" + nonce
	if _, exists := g.seen[key]; exists {
		return ErrReplay
	}
	g.seen[key] = struct{}{}
	return nil
}

func TestHandlerAuthenticatesBindsAndRejectsReplay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := []byte(strings.Repeat("k", 32))
	handler, err := NewHandler(HandlerConfig{
		Verifier:    staticVerifier{decision: ascpverifier.SignedDecision{Verdict: ascpverifier.VerdictPass}},
		ReplayGuard: &memoryReplayGuard{seen: map[string]struct{}{}}, Keys: map[string][]byte{"delivery-key-1": key},
		Clock: func() time.Time { return now }, MaxSkew: 30 * time.Second, ChainID: "8453",
		EscrowContract: "0x1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"requestId":"verifier-request-1","input":{"commitment":{"chainId":"8453","escrowContract":"0x1111111111111111111111111111111111111111"},"spec":"e30=","delivery":{"reference":null,"content":null,"contentDigest":"","httpStatus":0,"contentType":"","capturedAt":0}}}`)
	request := signedRequest(t, now, key, "nonce_for_verifier_0001", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"requestId":"verifier-request-1"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	replay := signedRequest(t, now, key, "nonce_for_verifier_0001", body)
	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, replay)
	if replayed.Code != http.StatusConflict {
		t.Fatalf("replay status=%d", replayed.Code)
	}

	crossChain := bytes.Replace(body, []byte(`"8453"`), []byte(`"84532"`), 1)
	crossResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossResponse, signedRequest(t, now, key, "nonce_for_verifier_0002", crossChain))
	if crossResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-chain status=%d", crossResponse.Code)
	}
	chunked := signedRequest(t, now, key, "nonce_for_verifier_chunked_1", body)
	chunked.ContentLength = -1
	chunked.TransferEncoding = []string{"chunked"}
	chunkedResponse := httptest.NewRecorder()
	handler.ServeHTTP(chunkedResponse, chunked)
	if chunkedResponse.Code != http.StatusOK {
		t.Fatalf("chunked request status=%d body=%s", chunkedResponse.Code, chunkedResponse.Body.String())
	}
}

func TestHandlerRejectsBadMACDuplicateKeysAndUnavailableReplayState(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := []byte(strings.Repeat("k", 32))
	newHandler := func(guard ReplayGuard) *Handler {
		handler, err := NewHandler(HandlerConfig{Verifier: staticVerifier{}, ReplayGuard: guard,
			Keys: map[string][]byte{"delivery-key-1": key}, Clock: func() time.Time { return now }, MaxSkew: 30 * time.Second,
			ChainID: "8453", EscrowContract: "0x1111111111111111111111111111111111111111"})
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	body := []byte(`{"requestId":"verifier-request-1","requestId":"substituted","input":{}}`)
	badMAC := signedRequest(t, now, key, "nonce_for_verifier_0003", body)
	badMAC.Header.Set("X-FlowOps-Verifier-Signature", strings.Repeat("0", 64))
	badResponse := httptest.NewRecorder()
	newHandler(&memoryReplayGuard{seen: map[string]struct{}{}}).ServeHTTP(badResponse, badMAC)
	if badResponse.Code != http.StatusUnauthorized {
		t.Fatalf("bad MAC status=%d", badResponse.Code)
	}
	uppercaseMAC := signedRequest(t, now, key, "nonce_for_verifier_0008", body)
	uppercaseMAC.Header.Set("X-FlowOps-Verifier-Signature", strings.ToUpper(uppercaseMAC.Header.Get("X-FlowOps-Verifier-Signature")))
	uppercaseResponse := httptest.NewRecorder()
	newHandler(&memoryReplayGuard{seen: map[string]struct{}{}}).ServeHTTP(uppercaseResponse, uppercaseMAC)
	if uppercaseResponse.Code != http.StatusUnauthorized {
		t.Fatalf("non-canonical MAC status=%d", uppercaseResponse.Code)
	}
	duplicateHeader := signedRequest(t, now, key, "nonce_for_verifier_0006", body)
	duplicateHeader.Header["X-Flowops-Verifier-Nonce"] = []string{"nonce_for_verifier_0006", "nonce_for_verifier_0007"}
	duplicateHeaderResponse := httptest.NewRecorder()
	newHandler(&memoryReplayGuard{seen: map[string]struct{}{}}).ServeHTTP(duplicateHeaderResponse, duplicateHeader)
	if duplicateHeaderResponse.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate auth header status=%d", duplicateHeaderResponse.Code)
	}

	duplicateResponse := httptest.NewRecorder()
	newHandler(&memoryReplayGuard{seen: map[string]struct{}{}}).ServeHTTP(duplicateResponse, signedRequest(t, now, key, "nonce_for_verifier_0004", body))
	if duplicateResponse.Code != http.StatusBadRequest {
		t.Fatalf("duplicate status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}

	unavailable := replayGuardFunc(func(context.Context, string, string, string, time.Time, time.Time) error {
		return errors.New("database down")
	})
	unavailableResponse := httptest.NewRecorder()
	newHandler(unavailable).ServeHTTP(unavailableResponse, signedRequest(t, now, key, "nonce_for_verifier_0005", []byte(`{"requestId":"verifier-request-1","input":{}}`)))
	if unavailableResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d", unavailableResponse.Code)
	}
	for label, signedAt := range map[string]time.Time{"stale": now.Add(-31 * time.Second), "future": now.Add(31 * time.Second)} {
		response := httptest.NewRecorder()
		newHandler(&memoryReplayGuard{seen: map[string]struct{}{}}).ServeHTTP(response,
			signedRequest(t, signedAt, key, "nonce_for_verifier_time_"+label, []byte(`{"requestId":"verifier-request-1","input":{}}`)))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s timestamp status=%d", label, response.Code)
		}
	}
}

func TestHandlerBindsMACToKeyIDAndRejectsExcessiveJSONDepth(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := []byte(strings.Repeat("k", 32))
	handler, err := NewHandler(HandlerConfig{
		Verifier: staticVerifier{}, ReplayGuard: &memoryReplayGuard{seen: map[string]struct{}{}},
		Keys: map[string][]byte{"delivery-key-1": key, "delivery-key-2": key}, Clock: func() time.Time { return now },
		MaxSkew: 30 * time.Second, ChainID: "8453", EscrowContract: "0x1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"requestId":"verifier-request-1","input":{}}`)
	crossKey := signedRequest(t, now, key, "nonce_for_cross_key_0001", body)
	crossKey.Header.Set("X-FlowOps-Verifier-Key-Id", "delivery-key-2")
	crossKeyResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossKeyResponse, crossKey)
	if crossKeyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("cross-key replay status=%d", crossKeyResponse.Code)
	}

	deepBody := []byte(`{"nested":` + strings.Repeat("[", maxJSONDepth+1) + `0` + strings.Repeat("]", maxJSONDepth+1) + `}`)
	deepResponse := httptest.NewRecorder()
	handler.ServeHTTP(deepResponse, signedRequest(t, now, key, "nonce_for_deep_json_0001", deepBody))
	if deepResponse.Code != http.StatusBadRequest {
		t.Fatalf("deep JSON status=%d body=%s", deepResponse.Code, deepResponse.Body.String())
	}
}

func TestPostgresReplayGuardUsesReviewedPruneRoutine(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT public.prune_ascp_verifier_intake_replays()")).
		WillReturnRows(sqlmock.NewRows([]string{"deleted"}).AddRow(int64(3)))
	guard, err := NewPostgresReplayGuard(db)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := guard.PruneExpired(t.Context())
	if err != nil || deleted != 3 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerBoundsConcurrentVerificationWork(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := []byte(strings.Repeat("k", 32))
	started := make(chan struct{}, maxConcurrent)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	handler, err := NewHandler(HandlerConfig{
		Verifier: blockingVerifier{started: started, release: release}, ReplayGuard: &memoryReplayGuard{seen: map[string]struct{}{}},
		Keys: map[string][]byte{"delivery-key-1": key}, Clock: func() time.Time { return now }, MaxSkew: 30 * time.Second,
		ChainID: "8453", EscrowContract: "0x1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"requestId":"verifier-request-busy","input":{"commitment":{"chainId":"8453","escrowContract":"0x1111111111111111111111111111111111111111"}}}`)
	var workers sync.WaitGroup
	workers.Add(maxConcurrent)
	for index := 0; index < maxConcurrent; index++ {
		go func(index int) {
			defer workers.Done()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, signedRequest(t, now, key, "nonce_for_busy_worker_000"+strconv.Itoa(index), body))
		}(index)
	}
	for index := 0; index < maxConcurrent; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("verification worker did not acquire its slot")
		}
	}
	busyResponse := httptest.NewRecorder()
	handler.ServeHTTP(busyResponse, signedRequest(t, now, key, "nonce_for_busy_overflow_01", body))
	if busyResponse.Code != http.StatusServiceUnavailable || !strings.Contains(busyResponse.Body.String(), `"error":"VERIFIER_BUSY"`) {
		t.Fatalf("busy status=%d body=%s", busyResponse.Code, busyResponse.Body.String())
	}
	close(release)
	workers.Wait()
}

func TestHandlerMapsDurableVerifierStateFailureToServiceUnavailable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := []byte(strings.Repeat("k", 32))
	handler, err := NewHandler(HandlerConfig{
		Verifier:    staticVerifier{err: ascpverifier.ErrStateUnavailable},
		ReplayGuard: &memoryReplayGuard{seen: map[string]struct{}{}}, Keys: map[string][]byte{"delivery-key-1": key},
		Clock: func() time.Time { return now }, MaxSkew: 30 * time.Second, ChainID: "8453",
		EscrowContract: "0x1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"requestId":"verifier-request-state-1","input":{"commitment":{"chainId":"8453","escrowContract":"0x1111111111111111111111111111111111111111"},"spec":"e30=","delivery":{"reference":null,"content":null,"contentDigest":"","httpStatus":0,"contentType":"","capturedAt":0}}}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedRequest(t, now, key, "nonce_for_verifier_state_0001", body))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"error":"VERIFIER_STATE_UNAVAILABLE"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	inactiveHandler, err := NewHandler(HandlerConfig{
		Verifier:    staticVerifier{err: ascpverifier.ErrVerifierInactive},
		ReplayGuard: &memoryReplayGuard{seen: map[string]struct{}{}}, Keys: map[string][]byte{"delivery-key-1": key},
		Clock: func() time.Time { return now }, MaxSkew: 30 * time.Second, ChainID: "8453",
		EscrowContract: "0x1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	inactiveResponse := httptest.NewRecorder()
	inactiveHandler.ServeHTTP(inactiveResponse, signedRequest(t, now, key, "nonce_for_verifier_inactive_1", body))
	if inactiveResponse.Code != http.StatusServiceUnavailable || !strings.Contains(inactiveResponse.Body.String(), `"error":"VERIFIER_INACTIVE"`) {
		t.Fatalf("inactive status=%d body=%s", inactiveResponse.Code, inactiveResponse.Body.String())
	}
}

type replayGuardFunc func(context.Context, string, string, string, time.Time, time.Time) error

func (f replayGuardFunc) Consume(ctx context.Context, keyID, nonce, digest string, signedAt, receivedAt time.Time) error {
	return f(ctx, keyID, nonce, digest, signedAt, receivedAt)
}

func signedRequest(t *testing.T, now time.Time, key []byte, nonce string, body []byte) *http.Request {
	t.Helper()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	digest := sha256.Sum256(body)
	message := "ASCP_VERIFIER_INTAKE_V2\ndelivery-key-1\nPOST\n/v1/verdicts\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(digest[:])
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/v1/verdicts", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-FlowOps-Verifier-Key-Id", "delivery-key-1")
	request.Header.Set("X-FlowOps-Verifier-Timestamp", timestamp)
	request.Header.Set("X-FlowOps-Verifier-Nonce", nonce)
	request.Header.Set("X-FlowOps-Verifier-Signature", hex.EncodeToString(mac.Sum(nil)))
	return request
}
