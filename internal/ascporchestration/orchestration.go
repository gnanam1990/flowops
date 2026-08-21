// Package ascporchestration owns the durable application boundary between an
// accepted ASCP operation, policy evaluation, human approval, and atomic
// execution authorization. Callers select an operation; every economic field
// is reconstructed from authenticated scope and durable records.
package ascporchestration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
)

const (
	defaultAcceptWindow  = 5 * time.Minute
	minimumSettleWindow  = 30 * time.Minute
	maximumSettleWindow  = 30 * 24 * time.Hour
	deliverySafetyBuffer = 2 * time.Minute
)

var (
	ErrInvalidScope        = errors.New("ASCP orchestration scope is invalid")
	ErrNotFound            = errors.New("ASCP orchestration record not found")
	ErrPolicyUnavailable   = errors.New("active ASCP policy is unavailable")
	ErrOperationExpired    = errors.New("ASCP seller quote is expired")
	ErrDecisionDenied      = errors.New("ASCP policy decision denied execution")
	ErrApprovalPending     = errors.New("ASCP approval is pending")
	ErrApprovalUnavailable = errors.New("ASCP approval is unavailable")
	ErrStateConflict       = errors.New("ASCP orchestration state conflicts with the request")
)

type Identity struct {
	OrganizationID string
	AgentID        string
}

type Config struct {
	DatabaseStore  Store
	Authorization  AuthorizationStore
	EscrowContract string
	AcceptWindow   time.Duration
	SettleWindow   time.Duration
	Clock          func() time.Time
	Random         io.Reader
}

type Decision struct {
	DecisionID         string                         `json:"decisionId"`
	OrganizationID     string                         `json:"organizationId"`
	AgentID            string                         `json:"agentId"`
	OperationID        string                         `json:"operationId"`
	Outcome            policy.Outcome                 `json:"outcome"`
	Reason             policy.Reason                  `json:"reason"`
	PolicyVersion      string                         `json:"policyVersion"`
	PolicyHash         string                         `json:"policyHash"`
	CommitmentHash     string                         `json:"commitmentHash"`
	Commitment         executioncommitment.Commitment `json:"commitment"`
	Review             ascpapproval.Review            `json:"review"`
	ReviewSnapshotHash string                         `json:"reviewSnapshotHash"`
	Approval           *ascpapproval.Approval         `json:"approval,omitempty"`
	EvaluatedAt        int64                          `json:"evaluatedAt"`
	Replayed           bool                           `json:"replayed,omitempty"`
}

type Authorization struct {
	AuthorizationID       string             `json:"authorizationId"`
	OperationID           string             `json:"operationId"`
	DecisionID            string             `json:"decisionId"`
	ApprovalID            string             `json:"approvalId,omitempty"`
	State                 ascpexecauth.State `json:"state"`
	ExecutionSnapshotHash string             `json:"executionSnapshotHash"`
	ReservationID         string             `json:"reservationId,omitempty"`
	InvalidationReason    string             `json:"invalidationReason,omitempty"`
}

type Store interface {
	Evaluate(context.Context, Identity, string, EvaluationConfig) (Decision, error)
	Decision(context.Context, Identity, string) (Decision, error)
	Approval(context.Context, string, string) (ascpapproval.Approval, error)
	DecideApproval(context.Context, string, string, string, bool, string, time.Time) (ascpapproval.Approval, error)
	AuthorizationInput(context.Context, Identity, string, string, string, time.Time) (ascpexecauth.Input, error)
	Authorization(context.Context, Identity, string) (Authorization, error)
}

type AuthorizationStore interface {
	ValidateAndReserve(context.Context, ascpexecauth.Input) (ascpexecauth.Authorization, error)
}

type EvaluationConfig struct {
	EscrowContract string
	AcceptWindow   time.Duration
	SettleWindow   time.Duration
	Now            time.Time
	DecisionID     string
	ApprovalID     string
}

type Service struct {
	store          Store
	authorization  AuthorizationStore
	escrowContract string
	acceptWindow   time.Duration
	settleWindow   time.Duration
	clock          func() time.Time
	newID          func() (string, error)
}

func New(cfg Config) (*Service, error) {
	if cfg.DatabaseStore == nil || cfg.Authorization == nil || !canonicalAddress(cfg.EscrowContract) {
		return nil, errors.New("durable orchestration store, authorization store, and canonical escrow contract are required")
	}
	if cfg.AcceptWindow == 0 {
		cfg.AcceptWindow = defaultAcceptWindow
	}
	if cfg.AcceptWindow <= 0 || cfg.AcceptWindow > 15*time.Minute {
		return nil, errors.New("acceptance window must be in (0,15m]")
	}
	if cfg.SettleWindow < minimumSettleWindow || cfg.SettleWindow > maximumSettleWindow {
		return nil, errors.New("settlement window must be in [30m,30d]")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{
		store: cfg.DatabaseStore, authorization: cfg.Authorization, escrowContract: cfg.EscrowContract,
		acceptWindow: cfg.AcceptWindow, settleWindow: cfg.SettleWindow, clock: cfg.Clock,
		newID: idSource(cfg.Random),
	}, nil
}

func (s *Service) Evaluate(ctx context.Context, identity Identity, operationID string) (Decision, error) {
	if !validIdentity(identity) || !validHash(operationID) {
		return Decision{}, ErrInvalidScope
	}
	decisionID, err := s.newID()
	if err != nil {
		return Decision{}, err
	}
	approvalID, err := s.newID()
	if err != nil {
		return Decision{}, err
	}
	return s.store.Evaluate(ctx, identity, operationID, EvaluationConfig{
		EscrowContract: s.escrowContract, AcceptWindow: s.acceptWindow, SettleWindow: s.settleWindow,
		Now: s.clock().UTC(), DecisionID: decisionID, ApprovalID: approvalID,
	})
}

func (s *Service) Decision(ctx context.Context, identity Identity, operationID string) (Decision, error) {
	if !validIdentity(identity) || !validHash(operationID) {
		return Decision{}, ErrInvalidScope
	}
	return s.store.Decision(ctx, identity, operationID)
}

func (s *Service) Approval(ctx context.Context, organizationID, approvalID string) (ascpapproval.Approval, error) {
	if !validIdentifier(organizationID) || !validHash(approvalID) {
		return ascpapproval.Approval{}, ErrInvalidScope
	}
	return s.store.Approval(ctx, organizationID, approvalID)
}

func (s *Service) DecideApproval(ctx context.Context, organizationID, approvalID, snapshot string, approved bool, actor string) (ascpapproval.Approval, error) {
	if !validIdentifier(organizationID) || !validIdentifier(actor) || !validHash(approvalID) || !validHash(snapshot) {
		return ascpapproval.Approval{}, ErrInvalidScope
	}
	return s.store.DecideApproval(ctx, organizationID, approvalID, snapshot, approved, actor, s.clock().UTC())
}

func (s *Service) Authorize(ctx context.Context, identity Identity, operationID string) (Authorization, error) {
	if !validIdentity(identity) || !validHash(operationID) {
		return Authorization{}, ErrInvalidScope
	}
	if existing, err := s.store.Authorization(ctx, identity, operationID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Authorization{}, err
	}
	authorizationID, err := s.newID()
	if err != nil {
		return Authorization{}, err
	}
	reservationID, err := s.newID()
	if err != nil {
		return Authorization{}, err
	}
	input, err := s.store.AuthorizationInput(ctx, identity, operationID, authorizationID, reservationID, s.clock().UTC())
	if err != nil {
		return Authorization{}, err
	}
	created, err := s.authorization.ValidateAndReserve(ctx, input)
	if errors.Is(err, ascpexecauth.ErrAlreadyEvaluated) {
		return s.store.Authorization(ctx, identity, operationID)
	}
	if err != nil && created.AuthorizationID == "" {
		return Authorization{}, err
	}
	if created.AuthorizationID != "" {
		stored, readErr := s.store.Authorization(ctx, identity, operationID)
		if readErr == nil {
			return stored, err
		}
		if err == nil {
			return Authorization{}, readErr
		}
	}
	return authorizationProjection(created, input.AutoDecisionRef), err
}

func (s *Service) Authorization(ctx context.Context, identity Identity, operationID string) (Authorization, error) {
	if !validIdentity(identity) || !validHash(operationID) {
		return Authorization{}, ErrInvalidScope
	}
	return s.store.Authorization(ctx, identity, operationID)
}

func authorizationProjection(value ascpexecauth.Authorization, decisionID string) Authorization {
	return Authorization{
		AuthorizationID: value.AuthorizationID, OperationID: value.IntentID, DecisionID: decisionID,
		ApprovalID: value.ApprovalID, State: value.State, ExecutionSnapshotHash: value.ExecutionSnapshotHash,
		ReservationID: value.Reservation.ReservationID, InvalidationReason: value.InvalidationReason,
	}
}

func OrgDomain(organizationID string) (string, error) {
	if !validIdentifier(organizationID) {
		return "", ErrInvalidScope
	}
	digest := sha256.Sum256([]byte("ASCP_ORG_DOMAIN_V1\n" + organizationID))
	return "0x" + hex.EncodeToString(digest[:]), nil
}

func DeliveryDeadlines(now time.Time, quoteWork, quoteVerification uint64, acceptWindow, settleWindow time.Duration) (uint64, uint64, uint64, error) {
	now = now.UTC()
	nowUnix := now.Unix()
	acceptSeconds, acceptOK := wholePositiveSeconds(acceptWindow)
	settleSeconds, settleOK := wholePositiveSeconds(settleWindow)
	if nowUnix < 0 || quoteWork == 0 || quoteVerification == 0 || !acceptOK || !settleOK ||
		acceptWindow > 15*time.Minute || settleWindow < minimumSettleWindow || settleWindow > maximumSettleWindow {
		return 0, 0, 0, ErrStateConflict
	}
	verification := quoteVerification
	if verification < 120 {
		verification = 120
	}
	deliverySeconds, ok := checkedUnixAdd(quoteWork, verification, uint64(deliverySafetyBuffer/time.Second))
	if !ok {
		return 0, 0, 0, ErrStateConflict
	}
	accept, ok := checkedUnixAdd(uint64(nowUnix), acceptSeconds)
	if !ok {
		return 0, 0, 0, ErrStateConflict
	}
	deliver, ok := checkedUnixAdd(uint64(nowUnix), deliverySeconds)
	if !ok {
		return 0, 0, 0, ErrStateConflict
	}
	if deliver <= accept {
		deliver, ok = checkedUnixAdd(accept, 1)
		if !ok {
			return 0, 0, 0, ErrStateConflict
		}
	}
	settle, ok := checkedUnixAdd(deliver, settleSeconds)
	if !ok || accept == 0 || deliver <= accept || settle <= deliver {
		return 0, 0, 0, ErrStateConflict
	}
	return accept, deliver, settle, nil
}

func wholePositiveSeconds(value time.Duration) (uint64, bool) {
	return uint64(value / time.Second), value > 0 && value%time.Second == 0
}

func checkedUnixAdd(values ...uint64) (uint64, bool) {
	const maximumUnixSecond = uint64(1<<63 - 1)
	var total uint64
	for _, value := range values {
		if value > maximumUnixSecond-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func idSource(random io.Reader) func() (string, error) {
	return func() (string, error) {
		value := make([]byte, 32)
		if _, err := io.ReadFull(random, value); err != nil {
			return "", fmt.Errorf("generate ASCP orchestration ID: %w", err)
		}
		allZero := true
		for _, part := range value {
			allZero = allZero && part == 0
		}
		if allZero {
			return "", errors.New("generated zero ASCP orchestration ID")
		}
		return "0x" + hex.EncodeToString(value), nil
	}
}

func validIdentity(identity Identity) bool {
	return validIdentifier(identity.OrganizationID) && validIdentifier(identity.AgentID)
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && strings.ContainsRune("._:-", character)) {
			return false
		}
	}
	return true
}

func validHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func canonicalAddress(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && strings.ToLower(value) == value && common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}
