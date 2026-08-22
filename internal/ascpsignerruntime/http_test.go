package ascpsignerruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

type testEngine struct {
	calls int
	sig   []byte
	err   error
}

var testArtifactToken = bytes.Repeat([]byte{0xa7}, 32)

func (e *testEngine) VerifyAndSign(context.Context, ascpbearer.ActivationInput) ([]byte, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return append([]byte(nil), e.sig...), nil
}

type testVerifier struct{ proveCalls int }

type sequenceReader struct {
	mu   sync.Mutex
	next byte
}

func (r *sequenceReader) Read(output []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	for index := range output {
		output[index] = r.next
	}
	return len(output), nil
}

func (*testVerifier) VerifyActivation(context.Context, ascpbearer.Handle, ascpbearer.ActivationProof) error {
	return nil
}

func (v *testVerifier) ProveUnactivated(context.Context, ascpbearer.Handle, time.Time) error {
	v.proveCalls++
	return nil
}

func TestSignerAndArtifactBoundariesEnforcePrepareActivateRelease(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, engine := testService(t, func() time.Time { return now })
	input := validInput(now, 1)
	prepare := signerRequest(t, service.SignerHandler(), "/v1/prepare", map[string]any{"protocol": SignerProtocol, "input": input})
	if prepare.Code != http.StatusOK || bytes.Contains(prepare.Body.Bytes(), engine.sig) || bytes.Contains(prepare.Body.Bytes(), []byte("artifact")) {
		t.Fatalf("prepare status=%d body=%s", prepare.Code, prepare.Body.String())
	}
	var prepared struct {
		HandleID string `json:"handleId"`
	}
	decodeResponse(t, prepare, &prepared)
	if prepared.HandleID == "" || engine.calls != 1 {
		t.Fatalf("handle=%q engine calls=%d", prepared.HandleID, engine.calls)
	}

	replay := signerRequest(t, service.SignerHandler(), "/v1/prepare", map[string]any{"protocol": SignerProtocol, "input": input})
	var replayed struct {
		HandleID string `json:"handleId"`
	}
	decodeResponse(t, replay, &replayed)
	if replayed.HandleID != prepared.HandleID || engine.calls != 1 {
		t.Fatalf("replay handle=%q calls=%d", replayed.HandleID, engine.calls)
	}

	before := artifactRequest(t, service.ArtifactHandler(), prepared.HandleID, input.KeeperID)
	if before.Code != http.StatusConflict {
		t.Fatalf("pre-activation release status=%d body=%s", before.Code, before.Body.String())
	}
	wrongProof := activationProof(input, prepared.HandleID, now)
	wrongProof.RequestID = hash(99)
	wrongAck := signerRequest(t, service.SignerHandler(), "/v1/acknowledge", map[string]any{"protocol": SignerProtocol, "proof": wrongProof})
	if wrongAck.Code != http.StatusConflict {
		t.Fatalf("wrong request activation status=%d body=%s", wrongAck.Code, wrongAck.Body.String())
	}
	ack := signerRequest(t, service.SignerHandler(), "/v1/acknowledge", map[string]any{"protocol": SignerProtocol, "proof": activationProof(input, prepared.HandleID, now)})
	if ack.Code != http.StatusOK {
		t.Fatalf("activation status=%d body=%s", ack.Code, ack.Body.String())
	}
	wrongKeeper := artifactRequest(t, service.ArtifactHandler(), prepared.HandleID, "keeper-attacker")
	if wrongKeeper.Code != http.StatusForbidden {
		t.Fatalf("wrong keeper status=%d body=%s", wrongKeeper.Code, wrongKeeper.Body.String())
	}
	unauthorized := signerRequest(t, service.ArtifactHandler(), "/v1/release", map[string]any{"protocol": ArtifactProtocol, "handleId": prepared.HandleID, "keeperId": input.KeeperID})
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Body.String() != "{\"code\":\"ARTIFACT_UNAUTHORIZED\"}\n" {
		t.Fatalf("unauthenticated artifact release status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	first := artifactRequest(t, service.ArtifactHandler(), prepared.HandleID, input.KeeperID)
	second := artifactRequest(t, service.ArtifactHandler(), prepared.HandleID, input.KeeperID)
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusOK {
			t.Fatalf("release status=%d body=%s", response.Code, response.Body.String())
		}
		var released struct {
			Handle   ascpbearer.Handle `json:"handle"`
			Artifact []byte            `json:"artifact"`
		}
		decodeResponse(t, response, &released)
		if released.Handle.State != ascpbearer.Released || !bytes.Equal(released.Artifact, engine.sig) {
			t.Fatalf("released=%+v artifact=%x", released.Handle, released.Artifact)
		}
	}
}

func TestSignerBoundaryRejectsDuplicateUnknownAndProtocolSubstitution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, engine := testService(t, func() time.Time { return now })
	inputJSON, _ := json.Marshal(validInput(now, 1))
	requests := []string{
		`{"protocol":"ASCP_BEARER_RUNTIME_V1","protocol":"ASCP_BEARER_RUNTIME_V1","input":` + string(inputJSON) + `}`,
		`{"protocol":"ASCP_BEARER_RUNTIME_V1","input":` + string(inputJSON) + `,"unexpected":true}`,
		`{"protocol":"ASCP_KEEPER_BOUNDARY_V1","input":` + string(inputJSON) + `}`,
	}
	for _, raw := range requests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/prepare", bytes.NewBufferString(raw))
		request.Header.Set("Content-Type", "application/json")
		service.SignerHandler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("request accepted: status=%d body=%s raw=%s", recorder.Code, recorder.Body.String(), raw)
		}
	}
	if engine.calls != 0 {
		t.Fatalf("malformed requests reached engine: %d", engine.calls)
	}
}

func TestSignerBoundaryReturnsOnlyExplicitPermanentRefusalCode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, engine := testService(t, func() time.Time { return now })
	engine.err = ascpbearer.ErrSignerRefused
	response := signerRequest(t, service.SignerHandler(), "/v1/prepare", map[string]any{"protocol": SignerProtocol, "input": validInput(now, 1)})
	if response.Code != http.StatusUnprocessableEntity || response.Body.String() != "{\"code\":\"SIGNER_REFUSED\"}\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestArtifactBoundaryRequiresOneExactCapability(t *testing.T) {
	service, _ := testService(t, time.Now)
	for name, headers := range map[string][]string{
		"missing": nil,
		"wrong":   {"Bearer " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x12}, 32))},
		"short":   {"Bearer YQ=="},
		"duplicate": {
			"Bearer " + base64.StdEncoding.EncodeToString(testArtifactToken),
			"Bearer " + base64.StdEncoding.EncodeToString(testArtifactToken),
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			for _, value := range headers {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			service.ArtifactHandler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(testArtifactToken))
	response := httptest.NewRecorder()
	service.ArtifactHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("exact capability status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSameActionLabelAcrossOperationsDoesNotCollide(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, engine := testService(t, func() time.Time { return now })
	first := validInput(now, 1)
	second := validInput(now, 2)
	if first.ActionID != second.ActionID || first.OperationID == second.OperationID {
		t.Fatal("test inputs do not exercise the operation-scoped replay key")
	}
	var handles []string
	for _, input := range []ascpbearer.ActivationInput{first, second} {
		response := signerRequest(t, service.SignerHandler(), "/v1/prepare", map[string]any{"protocol": SignerProtocol, "input": input})
		if response.Code != http.StatusOK {
			t.Fatalf("prepare status=%d body=%s", response.Code, response.Body.String())
		}
		var prepared struct {
			HandleID string `json:"handleId"`
		}
		decodeResponse(t, response, &prepared)
		handles = append(handles, prepared.HandleID)
	}
	if handles[0] == handles[1] || engine.calls != 2 {
		t.Fatalf("handles=%v engine calls=%d", handles, engine.calls)
	}
}

func TestUnactivatedProofBindsRequestOperationActionAndDurablyExpires(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	current := now
	verifier := &testVerifier{}
	service, _ := testServiceWithVerifier(t, func() time.Time { return current }, verifier)
	input := validInput(now, 1)
	prepared := signerRequest(t, service.SignerHandler(), "/v1/prepare", map[string]any{"protocol": SignerProtocol, "input": input})
	if prepared.Code != http.StatusOK {
		t.Fatalf("prepare status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	inputHash, err := ascpbearer.ActivationInputHash(input)
	if err != nil {
		t.Fatal(err)
	}
	current = input.ValidUntil.Add(time.Second)
	wrong := proofRequest(t, service, input.RequestID, hash(998), input.ActionID, inputHash)
	if wrong.Code != http.StatusOK {
		t.Fatalf("absent operation proof status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	conflict := proofRequest(t, service, input.RequestID, input.OperationID, input.ActionID, hash(997))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("mismatched proof status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	proofResponse := proofRequest(t, service, input.RequestID, input.OperationID, input.ActionID, inputHash)
	if proofResponse.Code != http.StatusOK || verifier.proveCalls != 1 {
		t.Fatalf("proof status=%d body=%s calls=%d", proofResponse.Code, proofResponse.Body.String(), verifier.proveCalls)
	}
	var response struct {
		Proof ascpbearer.UnactivatedProof `json:"proof"`
	}
	decodeResponse(t, proofResponse, &response)
	digest, _ := ascpbearer.UnactivatedProofDigest(response.Proof)
	if response.Proof.RequestID != input.RequestID || response.Proof.OperationID != input.OperationID || response.Proof.HandleID == "" || response.Proof.ProofDigest != digest {
		t.Fatalf("proof=%+v digest=%s", response.Proof, digest)
	}
	replay := proofRequest(t, service, input.RequestID, input.OperationID, input.ActionID, inputHash)
	if replay.Code != http.StatusOK || verifier.proveCalls != 1 {
		t.Fatalf("proof replay status=%d calls=%d body=%s", replay.Code, verifier.proveCalls, replay.Body.String())
	}
}

func testService(t *testing.T, clock func() time.Time) (*Service, *testEngine) {
	return testServiceWithVerifier(t, clock, &testVerifier{})
}

func testServiceWithVerifier(t *testing.T, clock func() time.Time, verifier *testVerifier) (*Service, *testEngine) {
	t.Helper()
	cipher, err := ascpbearer.NewAESGCMCipher(bytes.Repeat([]byte{1}, 32), bytes.NewReader(bytes.Repeat([]byte{2}, 8192)))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ascpbearer.NewSignerStore(cipher, verifier, clock, &sequenceReader{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &testEngine{sig: bytes.Repeat([]byte{0x55}, 65)}
	prepared, err := ascpbearer.NewLedgerPreparedSigner(ledger, engine)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(prepared, ledger, testArtifactToken)
	if err != nil {
		t.Fatal(err)
	}
	return service, engine
}

func validInput(now time.Time, sequence uint64) ascpbearer.ActivationInput {
	payload := []byte("canonical-lock-payload")
	evidence := []byte("complete-independent-evidence")
	return ascpbearer.ActivationInput{
		RequestID: hash(10 + sequence), AuthorizationID: hash(20 + sequence), OperationID: hash(30 + sequence),
		ReservationID: hash(40 + sequence), ActionID: "lock-action-1", CanonicalPayload: payload,
		CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence), Digest: hash(50 + sequence), Nonce: hash(60 + sequence),
		InstrumentType: ascpbearer.InstrumentLockAuthorization, SignerBindingVersion: 1,
		SignerKeyID: "signer-key-1", KeyEpoch: 1, ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress: "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}
}

func activationProof(input ascpbearer.ActivationInput, handle string, now time.Time) ascpbearer.ActivationProof {
	return ascpbearer.ActivationProof{
		RequestID: input.RequestID, HandleID: handle, OperationID: input.OperationID, Digest: input.Digest,
		Nonce: input.Nonce, PrimaryMirrorDigest: hash(500), ActivationOccurredAt: now,
	}
}

func signerRequest(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(value)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	clear(body)
	return recorder
}

func artifactRequest(t *testing.T, handler http.Handler, handleID, keeperID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"protocol": ArtifactProtocol, "handleId": handleID, "keeperId": keeperID})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/release", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(testArtifactToken))
	handler.ServeHTTP(recorder, request)
	clear(body)
	return recorder
}

func proofRequest(t *testing.T, service *Service, requestID, operationID, actionID, inputHash string) *httptest.ResponseRecorder {
	t.Helper()
	return signerRequest(t, service.SignerHandler(), "/v1/prove-unactivated", map[string]any{
		"protocol": SignerProtocol, "requestId": requestID, "operationId": operationID, "actionId": actionID, "inputHash": inputHash,
	})
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, output any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), output); err != nil {
		t.Fatal(err)
	}
}

func hash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
