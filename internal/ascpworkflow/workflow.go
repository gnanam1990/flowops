// Package ascpworkflow owns dual-control proposal workflows for privileged
// FlowOps governance changes. It deliberately has no chain-writing client.
package ascpworkflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	ProposalTTL       = 24 * time.Hour
	MaximumStepUpLife = 5 * time.Minute
)

var (
	ErrInvalidWorkflow       = errors.New("proposal workflow is invalid")
	ErrForbiddenRole         = errors.New("role cannot perform this workflow action")
	ErrStepUpRequired        = errors.New("fresh workflow step-up is required")
	ErrSamePrincipal         = errors.New("workflow approver must differ from proposer")
	ErrNotFound              = errors.New("proposal workflow was not found")
	ErrIdempotencyConflict   = errors.New("workflow idempotency key names different input")
	ErrStateConflict         = errors.New("proposal workflow state conflicts with the requested transition")
	ErrCompletionUnavailable = errors.New("workflow completion verifier is unavailable")
	ErrInvalidReceipt        = errors.New("workflow completion receipt is invalid")
)

type Kind string

const (
	PayoutChange       Kind = "PAYOUT_CHANGE"
	SignerCaps         Kind = "SIGNER_CAPS"
	VerifierGovernance Kind = "VERIFIER_GOVERNANCE"
	ProductionGate     Kind = "PRODUCTION_GATE"
	BreakGlass         Kind = "BREAK_GLASS"
	RoleAdmin          Kind = "ROLE_ADMIN"
	ModuleGovernance   Kind = "MODULE_GOVERNANCE"
	DirectoryCancel    Kind = "DIRECTORY_CANCEL"
)

type State string

const (
	Proposed             State = "PROPOSED"
	ApprovedPendingChain State = "APPROVED_PENDING_CHAIN"
	Approved             State = "APPROVED"
	Cancelled            State = "CANCELLED"
	Expired              State = "EXPIRED"
)

type Role string

const (
	OrgAdmin          Role = "ORG_ADMIN"
	SellerAdmin       Role = "SELLER_ADMIN"
	SignerOperator    Role = "SIGNER_OPERATOR"
	IncidentResponder Role = "INCIDENT_RESPONDER"
)

type Actor struct {
	OrganizationID string
	PrincipalID    string
	Role           Role
	StepUpAt       time.Time
	StepUpUntil    time.Time
}

type Workflow struct {
	WorkflowID          string          `json:"workflowId"`
	OrganizationID      string          `json:"organizationId"`
	Kind                Kind            `json:"kind"`
	ChainAction         ChainAction     `json:"chainAction,omitempty"`
	PayloadHash         string          `json:"payloadHash"`
	ProposedBy          string          `json:"proposedBy"`
	ProposerRole        Role            `json:"proposerRole"`
	State               State           `json:"state"`
	ApprovedBy          string          `json:"approvedBy,omitempty"`
	ApproverRole        Role            `json:"approverRole,omitempty"`
	CancelledBy         string          `json:"cancelledBy,omitempty"`
	ProposerStepUpAt    int64           `json:"proposerStepUpAt"`
	ProposerStepUpUntil int64           `json:"proposerStepUpUntil"`
	ApproverStepUpAt    int64           `json:"approverStepUpAt,omitempty"`
	ApproverStepUpUntil int64           `json:"approverStepUpUntil,omitempty"`
	ProposedAt          int64           `json:"proposedAt"`
	ApprovedAt          int64           `json:"approvedAt,omitempty"`
	CancelledAt         int64           `json:"cancelledAt,omitempty"`
	ExpiredAt           int64           `json:"expiredAt,omitempty"`
	ExpiresAt           int64           `json:"expiresAt"`
	CompletionReceipt   json.RawMessage `json:"completionReceipt,omitempty"`
	CompletionDigest    string          `json:"completionDigest,omitempty"`
	CompletedAt         int64           `json:"completedAt,omitempty"`
	Replayed            bool            `json:"replayed"`
}

type CreateRequest struct {
	Kind        Kind        `json:"kind"`
	ChainAction ChainAction `json:"chainAction,omitempty"`
	PayloadHash string      `json:"payloadHash"`
}

type CompletionReceipt struct {
	WorkflowID      string                 `json:"workflowId"`
	PayloadHash     string                 `json:"payloadHash"`
	ChainAction     ChainAction            `json:"chainAction,omitempty"`
	ChainID         uint64                 `json:"chainId"`
	TransactionHash string                 `json:"transactionHash"`
	BlockNumber     uint64                 `json:"blockNumber"`
	BlockHash       string                 `json:"blockHash"`
	LogIndex        uint64                 `json:"logIndex"`
	ContractAddress string                 `json:"contractAddress"`
	EventSignature  string                 `json:"eventSignature"`
	Finality        string                 `json:"finality"`
	AuthorityProof  []AuthorityObservation `json:"authorityProof,omitempty"`
}

type CompletionVerifier interface {
	VerifyWorkflowCompletion(context.Context, Workflow, CompletionReceipt) error
}

type CreationAuthorityPolicy interface {
	ValidateWorkflow(Kind, ChainAction, string) error
}

type Store interface {
	ReplayCreate(context.Context, Actor, string, string) (Workflow, bool, error)
	Create(context.Context, Workflow, string, string) (Workflow, bool, error)
	Get(context.Context, string, string) (Workflow, error)
	Approve(context.Context, Actor, string, string, string, time.Time) (Workflow, bool, error)
	Cancel(context.Context, Actor, string, string, string, time.Time) (Workflow, bool, error)
	Expire(context.Context, string, string, time.Time) (Workflow, bool, error)
	Complete(context.Context, string, string, CompletionReceipt, string, []byte, time.Time) (Workflow, bool, error)
}

type Service struct {
	store    Store
	verifier CompletionVerifier
	clock    func() time.Time
	newID    func() (string, error)
}

func New(store Store, verifier CompletionVerifier, clock func() time.Time, random io.Reader) (*Service, error) {
	if store == nil {
		return nil, errors.New("durable workflow store is required")
	}
	if clock == nil {
		clock = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &Service{store: store, verifier: verifier, clock: clock, newID: workflowIDSource(random)}, nil
}

func (s *Service) Create(ctx context.Context, actor Actor, idempotencyKey string, request CreateRequest) (Workflow, error) {
	observedAt := s.clock().UTC()
	if err := validateActor(actor, observedAt, true); err != nil {
		return Workflow{}, err
	}
	if !canPropose(request.Kind, actor.Role) {
		return Workflow{}, ErrForbiddenRole
	}
	policy, authorityConfigured := s.verifier.(CreationAuthorityPolicy)
	if request.ChainAction != "" && !authorityConfigured {
		return Workflow{}, ErrCompletionUnavailable
	}
	if authorityConfigured && request.ChainAction != "" {
		if err := policy.ValidateWorkflow(request.Kind, request.ChainAction, request.PayloadHash); err != nil {
			return Workflow{}, err
		}
	}
	if !hash(request.PayloadHash) || !idempotency(idempotencyKey) {
		return Workflow{}, ErrInvalidWorkflow
	}
	inputHash := actionHash("CREATE", actor, "", idempotencyKey, request)
	if stored, replayed, err := s.store.ReplayCreate(ctx, actor, idempotencyKey, inputHash); err != nil || replayed {
		if replayed {
			stored.Replayed = true
		}
		return stored, err
	}
	id, err := s.newID()
	if err != nil {
		return Workflow{}, err
	}
	now := observedAt.Truncate(time.Second)
	workflow := Workflow{
		WorkflowID: id, OrganizationID: actor.OrganizationID, Kind: request.Kind, ChainAction: request.ChainAction, PayloadHash: request.PayloadHash,
		ProposedBy: actor.PrincipalID, ProposerRole: actor.Role, State: Proposed,
		ProposerStepUpAt: actor.StepUpAt.UTC().Unix(), ProposerStepUpUntil: actor.StepUpUntil.UTC().Unix(), ProposedAt: now.Unix(), ExpiresAt: now.Add(ProposalTTL).Unix(),
	}
	stored, replayed, err := s.store.Create(ctx, workflow, idempotencyKey, inputHash)
	if err != nil {
		return Workflow{}, err
	}
	stored.Replayed = replayed
	return stored, nil
}

func (s *Service) Get(ctx context.Context, actor Actor, workflowID string) (Workflow, error) {
	now := s.clock().UTC().Truncate(time.Second)
	if err := validateActor(actor, now, false); err != nil || !hash(workflowID) {
		return Workflow{}, ErrInvalidWorkflow
	}
	workflow, _, err := s.store.Expire(ctx, actor.OrganizationID, workflowID, now)
	return workflow, err
}

func (s *Service) Approve(ctx context.Context, actor Actor, workflowID, idempotencyKey string) (Workflow, error) {
	observedAt := s.clock().UTC()
	if err := validateActor(actor, observedAt, true); err != nil {
		return Workflow{}, err
	}
	now := observedAt.Truncate(time.Second)
	if !hash(workflowID) || !idempotency(idempotencyKey) {
		return Workflow{}, ErrInvalidWorkflow
	}
	inputHash := actionHash("APPROVE", actor, workflowID, idempotencyKey, struct{}{})
	workflow, replayed, err := s.store.Approve(ctx, actor, workflowID, idempotencyKey, inputHash, now)
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

func (s *Service) Cancel(ctx context.Context, actor Actor, workflowID, idempotencyKey string) (Workflow, error) {
	observedAt := s.clock().UTC()
	if err := validateActor(actor, observedAt, true); err != nil {
		return Workflow{}, err
	}
	now := observedAt.Truncate(time.Second)
	if !hash(workflowID) || !idempotency(idempotencyKey) {
		return Workflow{}, ErrInvalidWorkflow
	}
	inputHash := actionHash("CANCEL", actor, workflowID, idempotencyKey, struct{}{})
	workflow, replayed, err := s.store.Cancel(ctx, actor, workflowID, idempotencyKey, inputHash, now)
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

// Complete is an internal observer boundary, not an owner API. The configured
// verifier must independently establish canonical finalized chain evidence.
func (s *Service) Complete(ctx context.Context, organizationID, workflowID string, receipt CompletionReceipt) (Workflow, error) {
	if s.verifier == nil {
		return Workflow{}, ErrCompletionUnavailable
	}
	if !identifier(organizationID) || !hash(workflowID) || !validReceipt(receipt) {
		return Workflow{}, ErrInvalidReceipt
	}
	bytes, err := json.Marshal(receipt)
	if err != nil {
		return Workflow{}, err
	}
	digest := "0x" + hex.EncodeToString(sha256Bytes(bytes))
	workflow, err := s.store.Get(ctx, organizationID, workflowID)
	if err != nil {
		return Workflow{}, err
	}
	if workflow.WorkflowID != receipt.WorkflowID || workflow.PayloadHash != receipt.PayloadHash ||
		(workflow.State != ApprovedPendingChain && !(workflow.State == Approved && workflow.CompletionDigest == digest)) {
		return Workflow{}, ErrStateConflict
	}
	if err := s.verifier.VerifyWorkflowCompletion(ctx, workflow, receipt); err != nil {
		return Workflow{}, fmt.Errorf("%w: %v", ErrInvalidReceipt, err)
	}
	stored, replayed, err := s.store.Complete(ctx, organizationID, workflowID, receipt, digest, bytes, s.clock().UTC().Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	stored.Replayed = replayed
	return stored, nil
}

func validateActor(actor Actor, now time.Time, requireStepUp bool) error {
	if !identifier(actor.OrganizationID) || !identifier(actor.PrincipalID) || !validRole(actor.Role) {
		return ErrInvalidWorkflow
	}
	if requireStepUp && (actor.StepUpAt.IsZero() || actor.StepUpAt.After(now) || now.Sub(actor.StepUpAt) > MaximumStepUpLife ||
		actor.StepUpUntil.IsZero() || !actor.StepUpUntil.After(now)) {
		return ErrStepUpRequired
	}
	return nil
}

func canPropose(kind Kind, role Role) bool {
	switch kind {
	case PayoutChange, DirectoryCancel:
		return role == SellerAdmin
	case SignerCaps, VerifierGovernance, ModuleGovernance:
		return role == SignerOperator
	case ProductionGate, BreakGlass, RoleAdmin:
		return role == OrgAdmin
	default:
		return false
	}
}

func canApprove(kind Kind, role Role) bool {
	switch kind {
	case PayoutChange:
		return role == SellerAdmin || role == OrgAdmin
	case SignerCaps, VerifierGovernance, ProductionGate, RoleAdmin, ModuleGovernance, DirectoryCancel:
		return role == OrgAdmin
	case BreakGlass:
		return role == IncidentResponder
	default:
		return false
	}
}

func canCancel(workflow Workflow, actor Actor) bool {
	return actor.PrincipalID == workflow.ProposedBy || actor.Role == OrgAdmin || canApprove(workflow.Kind, actor.Role)
}

func requiresChainReceipt(kind Kind) bool { return kind != ProductionGate && kind != RoleAdmin }

func workflowRequiresChainReceipt(workflow Workflow) bool {
	return workflow.ChainAction != "" || requiresChainReceipt(workflow.Kind)
}

func validRole(role Role) bool {
	return role == OrgAdmin || role == SellerAdmin || role == SignerOperator || role == IncidentResponder
}

func validReceipt(receipt CompletionReceipt) bool {
	return hash(receipt.WorkflowID) && hash(receipt.PayloadHash) && (receipt.ChainID == 8453 || receipt.ChainID == 84532) &&
		hash(receipt.TransactionHash) && hash(receipt.BlockHash) && receipt.BlockNumber > 0 && canonicalAddress(receipt.ContractAddress) &&
		hash(receipt.EventSignature) && receipt.Finality == "FINALIZED"
}

func actionHash(action string, actor Actor, workflowID, idempotencyKey string, input any) string {
	encoded, _ := json.Marshal(struct {
		Version        string `json:"version"`
		Action         string `json:"action"`
		OrganizationID string `json:"organizationId"`
		PrincipalID    string `json:"principalId"`
		WorkflowID     string `json:"workflowId,omitempty"`
		IdempotencyKey string `json:"idempotencyKey"`
		Input          any    `json:"input"`
	}{"ASCP_WORKFLOW_ACTION_V1", action, actor.OrganizationID, actor.PrincipalID, workflowID, idempotencyKey, input})
	return "0x" + hex.EncodeToString(sha256Bytes(encoded))
}

func workflowIDSource(random io.Reader) func() (string, error) {
	return func() (string, error) {
		value := make([]byte, 32)
		if _, err := io.ReadFull(random, value); err != nil {
			return "", fmt.Errorf("generate workflow ID: %w", err)
		}
		if strings.Trim(string(value), "\x00") == "" {
			return "", errors.New("generated zero workflow ID")
		}
		return "0x" + hex.EncodeToString(value), nil
	}
}

func sha256Bytes(value []byte) []byte { sum := sha256.Sum256(value); return sum[:] }
func identifier(value string) bool    { return envelope.ValidIdentifier(value) && len(value) <= 128 }
func idempotency(value string) bool   { return identifier(value) }
func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 32 {
		return false
	}
	for _, item := range decoded {
		if item != 0 {
			return true
		}
	}
	return false
}

func canonicalAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 20 {
		return false
	}
	for _, item := range decoded {
		if item != 0 {
			return true
		}
	}
	return false
}
