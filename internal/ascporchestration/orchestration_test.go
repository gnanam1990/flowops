package ascporchestration

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
)

func TestDeliveryDeadlinesSatisfyOnchainMinimums(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	accept, deliver, settle, err := DeliveryDeadlines(now, 30, 20, 5*time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if accept != uint64(now.Add(5*time.Minute).Unix()) || deliver <= accept ||
		deliver-uint64(now.Unix()) < 30+120 || settle-deliver != 3600 {
		t.Fatalf("deadlines accept=%d deliver=%d settle=%d", accept, deliver, settle)
	}
}

func TestDeliveryDeadlinesRejectOverflowAndSubsecondWindows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	for name, test := range map[string]struct {
		work, verification uint64
		accept, settle     time.Duration
	}{
		"work overflow":         {^uint64(0), 1, 5 * time.Minute, time.Hour},
		"verification overflow": {1, ^uint64(0), 5 * time.Minute, time.Hour},
		"subsecond acceptance":  {1, 1, time.Second + time.Nanosecond, time.Hour},
		"subsecond settlement":  {1, 1, 5 * time.Minute, time.Hour + time.Nanosecond},
		"long acceptance":       {1, 1, 15*time.Minute + time.Second, time.Hour},
		"long settlement":       {1, 1, 5 * time.Minute, 30*24*time.Hour + time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := DeliveryDeadlines(now, test.work, test.verification, test.accept, test.settle); !errors.Is(err, ErrStateConflict) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOrgDomainIsStableAndScoped(t *testing.T) {
	first, err := OrgDomain("org_a")
	if err != nil {
		t.Fatal(err)
	}
	replay, _ := OrgDomain("org_a")
	other, _ := OrgDomain("org_b")
	if first != replay || first == other || !validHash(first) {
		t.Fatalf("first=%s replay=%s other=%s", first, replay, other)
	}
}

func TestServiceDerivesIDsAndConcealsAuthorizationReplay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := &serviceStoreStub{authorizationErr: ErrNotFound}
	authorizer := &authorizationStub{}
	random := bytes.NewReader(append(bytes.Repeat([]byte{1}, 32*2), bytes.Repeat([]byte{2}, 32*4)...))
	service, err := New(Config{
		DatabaseStore: store, Authorization: authorizer,
		EscrowContract: "0x1111111111111111111111111111111111111111",
		SettleWindow:   time.Hour, Clock: func() time.Time { return now }, Random: random,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{OrganizationID: "org_a", AgentID: "agent_a"}
	operationID := testHash(9)
	if _, err := service.Evaluate(context.Background(), identity, operationID); err != nil {
		t.Fatal(err)
	}
	if store.evaluateIdentity != identity || store.evaluateOperation != operationID ||
		!validHash(store.evaluateConfig.DecisionID) || !validHash(store.evaluateConfig.ApprovalID) ||
		store.evaluateConfig.Now != now {
		t.Fatalf("identity=%+v operation=%s config=%+v", store.evaluateIdentity, store.evaluateOperation, store.evaluateConfig)
	}
	store.authorizationInput = ascpexecauth.Input{AuthorizationID: testHash(10), IntentID: operationID, AutoDecisionRef: testHash(11)}
	authorizer.output = ascpexecauth.Authorization{Input: store.authorizationInput, State: ascpexecauth.ValidatedAndReserved}
	created, err := service.Authorize(context.Background(), identity, operationID)
	if err != nil || created.DecisionID != store.authorizationInput.AutoDecisionRef || authorizer.calls != 1 {
		t.Fatalf("created=%+v calls=%d err=%v", created, authorizer.calls, err)
	}
	authorizer.err = ascpexecauth.ErrAlreadyEvaluated
	store.authorization = Authorization{AuthorizationID: testHash(12), OperationID: operationID, DecisionID: testHash(11), State: ascpexecauth.ValidatedAndReserved}
	store.authorizationErr = nil
	replayed, err := service.Authorize(context.Background(), identity, operationID)
	if err != nil || replayed.AuthorizationID != store.authorization.AuthorizationID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
}

type serviceStoreStub struct {
	evaluateIdentity   Identity
	evaluateOperation  string
	evaluateConfig     EvaluationConfig
	authorizationInput ascpexecauth.Input
	authorization      Authorization
	authorizationErr   error
}

func (s *serviceStoreStub) Evaluate(_ context.Context, identity Identity, operationID string, cfg EvaluationConfig) (Decision, error) {
	s.evaluateIdentity, s.evaluateOperation, s.evaluateConfig = identity, operationID, cfg
	return Decision{OperationID: operationID}, nil
}
func (s *serviceStoreStub) Decision(context.Context, Identity, string) (Decision, error) {
	return Decision{}, nil
}
func (s *serviceStoreStub) Approval(context.Context, string, string) (ascpapproval.Approval, error) {
	return ascpapproval.Approval{}, nil
}
func (s *serviceStoreStub) DecideApproval(context.Context, string, string, string, bool, string, time.Time) (ascpapproval.Approval, error) {
	return ascpapproval.Approval{}, nil
}
func (s *serviceStoreStub) AuthorizationInput(_ context.Context, _ Identity, _, authorizationID, reservationID string, _ time.Time) (ascpexecauth.Input, error) {
	input := s.authorizationInput
	input.AuthorizationID = authorizationID
	input.Reservation.ReservationID = reservationID
	if s.authorization.AuthorizationID == "" {
		s.authorization = Authorization{AuthorizationID: authorizationID, OperationID: input.IntentID, DecisionID: input.AutoDecisionRef, State: ascpexecauth.ValidatedAndReserved, ReservationID: reservationID}
	}
	s.authorizationErr = nil
	return input, nil
}
func (s *serviceStoreStub) Authorization(context.Context, Identity, string) (Authorization, error) {
	return s.authorization, s.authorizationErr
}

type authorizationStub struct {
	output ascpexecauth.Authorization
	err    error
	calls  int
}

func (s *authorizationStub) ValidateAndReserve(_ context.Context, input ascpexecauth.Input) (ascpexecauth.Authorization, error) {
	s.calls++
	output := s.output
	output.AuthorizationID = input.AuthorizationID
	output.Reservation.ReservationID = input.Reservation.ReservationID
	return output, s.err
}

func testHash(value byte) string {
	if value == 0 {
		panic(errors.New("test hash must be non-zero"))
	}
	return "0x" + string(bytes.Repeat([]byte{hexDigit(value)}, 64))
}

func hexDigit(value byte) byte { return "0123456789abcdef"[value%16] }
