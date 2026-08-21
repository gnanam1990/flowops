package ascpactivation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
)

type authorizationStub struct {
	value     ascporchestration.Authorization
	err       error
	identity  ascporchestration.Identity
	operation string
}

func (s *authorizationStub) Authorization(_ context.Context, identity ascporchestration.Identity, operationID string) (ascporchestration.Authorization, error) {
	s.identity, s.operation = identity, operationID
	return s.value, s.err
}

type activationStoreStub struct {
	input    ascpbearer.ActivationInput
	value    ascpbearer.ActivationRequest
	replayed bool
	err      error
	readErr  error
	requests int
	reads    int
	readID   string
}

type bindingStub struct {
	value          ascpsignerbinding.Binding
	err            error
	organizationID string
	agentID        string
	reads          int
}

func (s *bindingStub) Current(_ context.Context, organizationID, agentID string) (ascpsignerbinding.Binding, error) {
	s.reads++
	s.organizationID, s.agentID = organizationID, agentID
	return s.value, s.err
}

func testBinding() *bindingStub {
	return &bindingStub{value: ascpsignerbinding.Binding{
		OrganizationID: "org_a", AgentID: "agent_a", Version: 1, ChainID: 84532,
		SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
	}}
}

func (s *activationStoreStub) Request(_ context.Context, input ascpbearer.ActivationInput) (ascpbearer.ActivationRequest, bool, error) {
	s.requests++
	s.input = input
	if s.value.RequestID == "" {
		s.value = ascpbearer.ActivationRequest{ActivationInput: input, InputHash: testHash(9), State: ascpbearer.SignRequested, CreatedAt: input.ValidAfter}
	}
	return s.value, s.replayed, s.err
}

func (s *activationStoreStub) ForAuthorization(_ context.Context, authorizationID string) (ascpbearer.ActivationRequest, error) {
	s.reads++
	s.readID = authorizationID
	if s.readErr != nil {
		return ascpbearer.ActivationRequest{}, s.readErr
	}
	if s.value.RequestID == "" {
		return ascpbearer.ActivationRequest{}, ascpbearer.ErrActivationNotFound
	}
	return s.value, nil
}

func TestServiceDerivesDurableScopeAndReturnsRedactedProjection(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	authorization := &authorizationStub{value: ascporchestration.Authorization{
		AuthorizationID: testHash(1), OperationID: testHash(2), ReservationID: testHash(3),
		State: ascpexecauth.ValidatedAndReserved,
	}}
	store := &activationStoreStub{}
	binding := testBinding()
	service, err := New(Config{Authorizations: authorization, Bindings: binding, Store: store, Random: bytes.NewReader(bytes.Repeat([]byte{0x44}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(now)
	identity := ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}
	status, err := service.Create(context.Background(), identity, authorization.value.OperationID, request)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.identity != identity || authorization.operation != authorization.value.OperationID {
		t.Fatalf("authorization scope identity=%+v operation=%s", authorization.identity, authorization.operation)
	}
	if store.input.RequestID != testHashByte(0x44) || store.input.AuthorizationID != authorization.value.AuthorizationID ||
		store.input.OperationID != authorization.value.OperationID || store.input.ReservationID != authorization.value.ReservationID {
		t.Fatalf("server-derived activation binding=%+v", store.input)
	}
	if binding.organizationID != identity.OrganizationID || binding.agentID != identity.AgentID ||
		store.input.SignerBindingVersion != binding.value.Version || store.input.SignerKeyID != binding.value.SignerKeyID || store.input.KeyEpoch != binding.value.KeyEpoch ||
		store.input.ModuleAddress != binding.value.ModuleAddress || store.input.SafeAddress != binding.value.SafeAddress ||
		store.input.KeeperID != binding.value.KeeperID {
		t.Fatalf("authoritative signer binding lookup=%+v input=%+v", binding, store.input)
	}
	if status.RequestID != store.input.RequestID || status.State != ascpbearer.SignRequested || status.Replayed {
		t.Fatalf("status=%+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte("canonicalPayload"), []byte("evidenceBundle"), []byte("preparedHandle")} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("public projection leaked %q: %s", forbidden, encoded)
		}
	}
	request.CanonicalPayload[0] ^= 0xff
	request.EvidenceBundle[0] ^= 0xff
	if store.input.CanonicalPayload[0] == request.CanonicalPayload[0] || store.input.EvidenceBundle[0] == request.EvidenceBundle[0] {
		t.Fatal("activation service retained caller-owned byte slices")
	}
}

func TestServiceRejectsNonLiveAuthorizationBeforeSignerRequest(t *testing.T) {
	authorization := &authorizationStub{value: ascporchestration.Authorization{
		AuthorizationID: testHash(1), OperationID: testHash(2), State: ascpexecauth.Invalidated,
	}}
	store := &activationStoreStub{}
	binding := testBinding()
	service, _ := New(Config{Authorizations: authorization, Bindings: binding, Store: store, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 32))})
	if _, err := service.Create(context.Background(), ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}, authorization.value.OperationID, testRequest(time.Now().UTC())); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("non-live authorization error=%v", err)
	}
	if store.requests != 0 {
		t.Fatalf("non-live authorization reached activation store: %d", store.requests)
	}
	if binding.reads != 0 {
		t.Fatalf("non-live authorization reached signer bindings: %d", binding.reads)
	}
}

func TestServiceFailsClosedWhenAuthoritativeSignerBindingIsUnavailable(t *testing.T) {
	authorization := &authorizationStub{value: ascporchestration.Authorization{
		AuthorizationID: testHash(1), OperationID: testHash(2), ReservationID: testHash(3),
		State: ascpexecauth.ValidatedAndReserved,
	}}
	store := &activationStoreStub{}
	binding := testBinding()
	binding.err = ascpsignerbinding.ErrNotFound
	service, _ := New(Config{Authorizations: authorization, Bindings: binding, Store: store})
	if _, err := service.Create(context.Background(), ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}, authorization.value.OperationID, testRequest(time.Now().UTC())); !errors.Is(err, ascpsignerbinding.ErrNotFound) {
		t.Fatalf("missing binding error=%v", err)
	}
	if store.requests != 0 || binding.reads != 1 {
		t.Fatalf("missing binding reads=%d store requests=%d", binding.reads, store.requests)
	}
}

func TestServiceReplaysStoredBindingAfterCurrentBindingRotates(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	authorization := &authorizationStub{value: ascporchestration.Authorization{
		AuthorizationID: testHash(1), OperationID: testHash(2), ReservationID: testHash(3),
		State: ascpexecauth.ValidatedAndReserved,
	}}
	existing := ascpbearer.ActivationRequest{ActivationInput: ascpbearer.ActivationInput{
		RequestID: testHash(4), AuthorizationID: testHash(1), OperationID: testHash(2), ReservationID: testHash(3),
		ActionID: "lock-action-1", CanonicalPayload: []byte("canonical-lock-payload"),
		CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash([]byte("canonical-lock-payload")),
		EvidenceBundle:       []byte("immutable-evidence"), EvidenceBundleHash: ascpbearer.EvidenceBundleHash([]byte("immutable-evidence")),
		Digest: testHash(6), Nonce: testHash(7), InstrumentType: ascpbearer.InstrumentLockAuthorization,
		SignerBindingVersion: 1, SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}, InputHash: testHash(8), State: ascpbearer.ActivationAcknowledged, CreatedAt: now}
	store := &activationStoreStub{value: existing, replayed: true}
	binding := testBinding()
	binding.value.Version, binding.value.SignerKeyID, binding.value.KeyEpoch = 2, "signer-key-2", 2
	service, _ := New(Config{Authorizations: authorization, Bindings: binding, Store: store, Random: bytes.NewReader(nil)})
	status, err := service.Create(context.Background(), ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}, authorization.value.OperationID, testRequest(now))
	if err != nil || !status.Replayed || status.SignerBindingVersion != 1 || store.input.SignerKeyID != "signer-key-1" || binding.reads != 0 {
		t.Fatalf("status=%+v input=%+v bindingReads=%d err=%v", status, store.input, binding.reads, err)
	}
}

func TestServiceReadsOnlyThroughScopedAuthorization(t *testing.T) {
	authorization := &authorizationStub{value: ascporchestration.Authorization{
		AuthorizationID: testHash(1), OperationID: testHash(2), ReservationID: testHash(3),
		State: ascpexecauth.ValidatedAndReserved,
	}}
	store := &activationStoreStub{value: ascpbearer.ActivationRequest{ActivationInput: ascpbearer.ActivationInput{
		RequestID: testHash(4), AuthorizationID: testHash(1), OperationID: testHash(2),
	}, InputHash: testHash(5), State: ascpbearer.ActiveMirrored}}
	service, _ := New(Config{Authorizations: authorization, Bindings: testBinding(), Store: store})
	identity := ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}
	status, err := service.Get(context.Background(), identity, authorization.value.OperationID)
	if err != nil || status.RequestID != testHash(4) || store.readID != authorization.value.AuthorizationID || authorization.identity != identity {
		t.Fatalf("status=%+v readID=%s identity=%+v err=%v", status, store.readID, authorization.identity, err)
	}
	authorization.err = ascporchestration.ErrNotFound
	store.reads = 0
	if _, err := service.Get(context.Background(), ascporchestration.Identity{OrganizationID: "org_b", AgentID: "agent_b"}, authorization.value.OperationID); !errors.Is(err, ascporchestration.ErrNotFound) || store.reads != 0 {
		t.Fatalf("cross-tenant read err=%v storeReads=%d", err, store.reads)
	}
}

func TestServiceRejectsAllZeroRandomIdentifier(t *testing.T) {
	authorization := &authorizationStub{value: ascporchestration.Authorization{
		AuthorizationID: testHash(1), OperationID: testHash(2), ReservationID: testHash(3),
		State: ascpexecauth.ValidatedAndReserved,
	}}
	store := &activationStoreStub{}
	service, _ := New(Config{Authorizations: authorization, Bindings: testBinding(), Store: store, Random: bytes.NewReader(make([]byte, 32))})
	if _, err := service.Create(context.Background(), ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}, authorization.value.OperationID, testRequest(time.Now().UTC())); err == nil {
		t.Fatal("all-zero request identifier succeeded")
	}
	if store.requests != 0 {
		t.Fatalf("invalid identifier reached store: %d", store.requests)
	}
}

func testRequest(now time.Time) Request {
	payload := []byte("canonical-lock-payload")
	evidence := []byte("immutable-evidence")
	return Request{
		ActionID: "lock-action-1", CanonicalPayload: payload, CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload),
		EvidenceBundle: evidence, EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence),
		Digest: testHash(6), Nonce: testHash(7), InstrumentType: ascpbearer.InstrumentLockAuthorization,
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}
}

func testHash(value byte) string {
	return "0x" + string(bytes.Repeat([]byte{hexDigit(value)}, 64))
}

func testHashByte(value byte) string {
	return "0x" + string(bytes.Repeat([]byte{hexDigit(value >> 4), hexDigit(value & 0xf)}, 32))
}

func hexDigit(value byte) byte {
	value &= 0xf
	if value < 10 {
		return '0' + value
	}
	return 'a' + value - 10
}
