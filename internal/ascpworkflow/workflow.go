// Package ascpworkflow owns dual-control proposal workflows for privileged
// FlowOps governance changes. It deliberately has no chain-writing client.
package ascpworkflow

import (
	"bytes"
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
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

const (
	ProposalTTL                  = 24 * time.Hour
	MaximumStepUpLife            = 5 * time.Minute
	GovernanceWorkflowBoundTopic = "0x71840a8df3cf7e14c302ff72b4fd1c651a2845389dfb0a4fdd884a2ffb104bfe"
	GovernanceExecutionVersion   = "ASCP_GOVERNANCE_EXECUTION_V1"
)

var (
	ErrInvalidWorkflow       = errors.New("proposal workflow is invalid")
	ErrForbiddenRole         = errors.New("role cannot perform this workflow action")
	ErrStepUpRequired        = errors.New("fresh workflow step-up is required")
	ErrSamePrincipal         = errors.New("workflow approver must differ from proposer")
	ErrNotFound              = errors.New("proposal workflow was not found")
	ErrIdempotencyConflict   = errors.New("workflow idempotency key names different input")
	ErrStateConflict         = errors.New("proposal workflow state conflicts with the requested transition")
	ErrCompletionUnavailable = errors.New("workflow completion observer is unavailable")
	ErrGovernanceUnavailable = errors.New("governance action targets are unavailable")
	ErrInvalidReceipt        = errors.New("workflow completion receipt is invalid")
	ErrReceiptOwned          = errors.New("workflow completion receipt is already owned")
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
	Submitted            State = "SUBMITTED"
	Confirmed            State = "CONFIRMED"
	Finalized            State = "FINALIZED"
	Reverted             State = "REVERTED"
	Reorged              State = "REORGED"
	TimedOut             State = "TIMED_OUT"
	RequiresReapproval   State = "REQUIRES_REAPPROVAL"
	Cancelled            State = "CANCELLED"
	Expired              State = "EXPIRED"
)

type TerminalReason string

const (
	ReceiptRejected     TerminalReason = "RECEIPT_REJECTED"
	MinedRevert         TerminalReason = "MINED_REVERT"
	ReorgDetected       TerminalReason = "REORG_DETECTED"
	SubmissionTimeout   TerminalReason = "SUBMISSION_TIMEOUT"
	SafeNonceConflict   TerminalReason = "SAFE_NONCE_CONFLICT"
	PreconditionChanged TerminalReason = "PRECONDITION_CHANGED"
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
	PayloadHash         string          `json:"payloadHash"`
	ChainID             uint64          `json:"chainId,omitempty"`
	ContractAddress     string          `json:"contractAddress,omitempty"`
	FunctionSelector    string          `json:"functionSelector,omitempty"`
	Calldata            string          `json:"calldata,omitempty"`
	GovernanceAction    json.RawMessage `json:"governanceAction,omitempty"`
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
	SubmissionTxHash    string          `json:"submissionTransactionHash,omitempty"`
	SubmittedAt         int64           `json:"submittedAt,omitempty"`
	ConfirmedAt         int64           `json:"confirmedAt,omitempty"`
	CompletionReceipt   json.RawMessage `json:"completionReceipt,omitempty"`
	CompletionDigest    string          `json:"completionDigest,omitempty"`
	FinalizedAt         int64           `json:"finalizedAt,omitempty"`
	TerminalReason      TerminalReason  `json:"terminalReason,omitempty"`
	TerminalAt          int64           `json:"terminalAt,omitempty"`
	Replayed            bool            `json:"replayed"`
}

// GovernanceExecutionCommand is the immutable, versioned command written in
// the same transaction as dual-control approval. It contains only authority
// and exact execution material; a relayer must not reconstruct calldata from
// mutable application state.
type GovernanceExecutionCommand struct {
	Version            string          `json:"version"`
	WorkflowID         string          `json:"workflowId"`
	OrganizationID     string          `json:"organizationId"`
	Kind               Kind            `json:"kind"`
	PayloadHash        string          `json:"payloadHash"`
	ChainID            uint64          `json:"chainId"`
	ContractAddress    string          `json:"contractAddress"`
	FunctionSelector   string          `json:"functionSelector"`
	Calldata           string          `json:"calldata"`
	Value              string          `json:"value"`
	Operation          string          `json:"operation"`
	GovernanceAction   json.RawMessage `json:"governanceAction"`
	ApprovedBy         string          `json:"approvedBy"`
	ApprovedAt         int64           `json:"approvedAt"`
	ExecuteAfter       int64           `json:"executeAfter"`
	ApprovalActionHash string          `json:"approvalActionHash"`
}

// ValidateExecutionCommand rebinds an immutable outbox command to the same
// closed action schema used at proposal approval. Consumers must call this
// instead of treating persisted JSON as trusted merely because it came from a
// database row.
func ValidateExecutionCommand(command GovernanceExecutionCommand) error {
	workflow := Workflow{
		WorkflowID: command.WorkflowID, OrganizationID: command.OrganizationID, Kind: command.Kind,
		PayloadHash: command.PayloadHash, ChainID: command.ChainID, ContractAddress: command.ContractAddress,
		FunctionSelector: command.FunctionSelector, Calldata: command.Calldata,
		GovernanceAction: append(json.RawMessage(nil), command.GovernanceAction...), ApprovedBy: command.ApprovedBy,
		ApprovedAt: command.ApprovedAt, State: ApprovedPendingChain,
	}
	expected, err := buildExecutionCommand(workflow, command.ApprovalActionHash)
	if err != nil || command.Version != expected.Version || command.WorkflowID != expected.WorkflowID ||
		command.OrganizationID != expected.OrganizationID || command.Kind != expected.Kind ||
		command.PayloadHash != expected.PayloadHash || command.ChainID != expected.ChainID ||
		command.ContractAddress != expected.ContractAddress || command.FunctionSelector != expected.FunctionSelector ||
		command.Calldata != expected.Calldata || command.Value != expected.Value || command.Operation != expected.Operation ||
		command.ApprovedBy != expected.ApprovedBy || command.ApprovedAt != expected.ApprovedAt ||
		command.ExecuteAfter != expected.ExecuteAfter || command.ApprovalActionHash != expected.ApprovalActionHash ||
		!jsonEqual(command.GovernanceAction, expected.GovernanceAction) {
		return ErrInvalidWorkflow
	}
	return nil
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(left)), json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

type CreateRequest struct {
	Kind        Kind                       `json:"kind"`
	WorkflowID  string                     `json:"workflowId,omitempty"`
	PayloadHash string                     `json:"payloadHash,omitempty"`
	Action      *governanceworkflow.Action `json:"action,omitempty"`
}

type CompletionReceipt struct {
	WorkflowID           string   `json:"workflowId"`
	PayloadHash          string   `json:"payloadHash"`
	ChainID              uint64   `json:"chainId"`
	TransactionHash      string   `json:"transactionHash"`
	BlockNumber          uint64   `json:"blockNumber"`
	BlockHash            string   `json:"blockHash"`
	BlockTimestamp       uint64   `json:"blockTimestamp"`
	ConfirmedHead        uint64   `json:"confirmedHead"`
	FinalizedHead        uint64   `json:"finalizedHead"`
	LogIndex             uint64   `json:"logIndex"`
	ContractAddress      string   `json:"contractAddress"`
	EventSignature       string   `json:"eventSignature"`
	FunctionSelector     string   `json:"functionSelector"`
	ActionEventSignature string   `json:"actionEventSignature"`
	ActionLogIndexes     []uint64 `json:"actionLogIndexes"`
	Observers            []string `json:"observers"`
	EvidenceDigest       string   `json:"evidenceDigest"`
	Finality             string   `json:"finality"`
}

type SafeRetryProof struct {
	WorkflowID              string   `json:"workflowId"`
	PreviousTransactionHash string   `json:"previousTransactionHash"`
	RetryTransactionHash    string   `json:"retryTransactionHash"`
	Outcome                 string   `json:"outcome"`
	PreviousCanonical       bool     `json:"previousCanonical"`
	SafeAddress             string   `json:"safeAddress"`
	SafeNonce               uint64   `json:"safeNonce"`
	SafeTxHash              string   `json:"safeTxHash"`
	ExecCalldataHash        string   `json:"execCalldataHash"`
	VerifiedPayloadHash     string   `json:"verifiedPayloadHash"`
	Observers               []string `json:"observers"`
	EvidenceDigest          string   `json:"evidenceDigest"`
	ObservedAt              int64    `json:"observedAt"`
}

type CompletionObserver interface {
	ObserveWorkflowCompletion(context.Context, Workflow) (CompletionReceipt, error)
}

// GovernanceActionGate authorizes the exact server-derived chain, contract,
// kind, and selector before a proposal can be stored. Receipt validation is a
// later independent boundary and cannot make an unsafe approval command safe.
type GovernanceActionGate interface {
	ValidateGovernanceAction(governanceworkflow.BoundAction) error
}

type Option func(*Service) error

func WithGovernanceActionGate(gate GovernanceActionGate) Option {
	return func(service *Service) error {
		if gate == nil {
			return errors.New("governance action gate is required")
		}
		if service.actionGate != nil {
			return errors.New("governance action gate is already configured")
		}
		service.actionGate = gate
		return nil
	}
}

type Store interface {
	ReplayCreate(context.Context, Actor, string, string) (Workflow, bool, error)
	Create(context.Context, Workflow, string, string) (Workflow, bool, error)
	Get(context.Context, string, string) (Workflow, error)
	Pending(context.Context, int, string) ([]Workflow, error)
	Approve(context.Context, Actor, string, string, string, time.Time) (Workflow, bool, error)
	Cancel(context.Context, Actor, string, string, string, time.Time) (Workflow, bool, error)
	Expire(context.Context, string, string, time.Time) (Workflow, bool, error)
	Submit(context.Context, string, string, string, time.Time) (Workflow, bool, error)
	RetrySubmission(context.Context, string, string, string, SafeRetryProof, time.Time) (Workflow, bool, error)
	Confirm(context.Context, string, string, string, time.Time) (Workflow, bool, error)
	Complete(context.Context, string, string, CompletionReceipt, string, []byte, time.Time) (Workflow, bool, error)
	FailChain(context.Context, string, string, State, TerminalReason, time.Time) (Workflow, bool, error)
}

type Service struct {
	store      Store
	observer   CompletionObserver
	actionGate GovernanceActionGate
	clock      func() time.Time
	newID      func() (string, error)
}

func New(store Store, observer CompletionObserver, clock func() time.Time, random io.Reader, options ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("durable workflow store is required")
	}
	if clock == nil {
		clock = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	service := &Service{store: store, observer: observer, clock: clock, newID: workflowIDSource(random)}
	if gate, ok := observer.(GovernanceActionGate); ok {
		service.actionGate = gate
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("proposal workflow option is nil")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) Create(ctx context.Context, actor Actor, idempotencyKey string, request CreateRequest) (Workflow, error) {
	observedAt := s.clock().UTC()
	if err := validateActor(actor, observedAt, false); err != nil {
		return Workflow{}, err
	}
	if !canPropose(request.Kind, actor.Role) {
		return Workflow{}, ErrForbiddenRole
	}
	chainBacked := requiresChainReceipt(request.Kind)
	if !idempotency(idempotencyKey) || (chainBacked && (!hash(request.WorkflowID) || request.PayloadHash != "" || request.Action == nil)) ||
		(!chainBacked && (request.WorkflowID != "" || request.Action != nil || !hash(request.PayloadHash))) {
		return Workflow{}, ErrInvalidWorkflow
	}
	var bound governanceworkflow.BoundAction
	if chainBacked {
		var err error
		bound, err = governanceworkflow.BindAction(request.WorkflowID, *request.Action)
		if err != nil || bound.WorkflowKind != string(request.Kind) {
			return Workflow{}, ErrInvalidWorkflow
		}
		request.Action = nil
	}
	createInput := struct {
		Kind        Kind                            `json:"kind"`
		WorkflowID  string                          `json:"workflowId,omitempty"`
		PayloadHash string                          `json:"payloadHash,omitempty"`
		BoundAction *governanceworkflow.BoundAction `json:"boundAction,omitempty"`
	}{Kind: request.Kind, WorkflowID: request.WorkflowID, PayloadHash: request.PayloadHash}
	if chainBacked {
		createInput.BoundAction = &bound
	}
	inputHash := actionHash("CREATE", actor, "", idempotencyKey, createInput)
	if stored, replayed, err := s.store.ReplayCreate(ctx, actor, idempotencyKey, inputHash); err != nil || replayed {
		if replayed {
			stored.Replayed = true
		}
		return stored, err
	}
	if err := validateActor(actor, observedAt, true); err != nil {
		return Workflow{}, err
	}
	if chainBacked {
		if s.actionGate == nil {
			return Workflow{}, ErrGovernanceUnavailable
		}
		if err := s.actionGate.ValidateGovernanceAction(bound); err != nil {
			return Workflow{}, ErrInvalidWorkflow
		}
	}
	id := request.WorkflowID
	if !requiresChainReceipt(request.Kind) {
		var err error
		id, err = s.newID()
		if err != nil {
			return Workflow{}, err
		}
	}
	now := observedAt.Truncate(time.Second)
	workflow := Workflow{
		WorkflowID: id, OrganizationID: actor.OrganizationID, Kind: request.Kind, PayloadHash: request.PayloadHash,
		ProposedBy: actor.PrincipalID, ProposerRole: actor.Role, State: Proposed,
		ProposerStepUpAt: actor.StepUpAt.UTC().Unix(), ProposerStepUpUntil: actor.StepUpUntil.UTC().Unix(), ProposedAt: now.Unix(), ExpiresAt: now.Add(ProposalTTL).Unix(),
	}
	if chainBacked {
		workflow.PayloadHash, workflow.ChainID, workflow.ContractAddress = bound.PayloadHash, bound.ChainID, bound.ContractAddress
		workflow.FunctionSelector, workflow.Calldata = bound.FunctionSelector, bound.Calldata
		workflow.GovernanceAction = append(json.RawMessage(nil), bound.CanonicalAction...)
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
	if err := validateActor(actor, observedAt, false); err != nil {
		return Workflow{}, err
	}
	if !hash(workflowID) || !idempotency(idempotencyKey) {
		return Workflow{}, ErrInvalidWorkflow
	}
	inputHash := actionHash("APPROVE", actor, workflowID, idempotencyKey, struct{}{})
	current, err := s.store.Get(ctx, actor.OrganizationID, workflowID)
	if err != nil {
		return Workflow{}, err
	}
	if current.State != Proposed {
		workflow, replayed, err := s.store.Approve(ctx, actor, workflowID, idempotencyKey, inputHash, observedAt.Truncate(time.Second))
		if err != nil {
			return Workflow{}, err
		}
		workflow.Replayed = replayed
		return workflow, nil
	}
	if err := validateActor(actor, observedAt, true); err != nil {
		return Workflow{}, err
	}
	if current.State == Proposed && requiresChainReceipt(current.Kind) {
		bound, err := boundActionForWorkflow(current)
		if err != nil {
			return Workflow{}, ErrInvalidWorkflow
		}
		if s.actionGate == nil {
			return Workflow{}, ErrGovernanceUnavailable
		}
		if err := s.actionGate.ValidateGovernanceAction(bound); err != nil {
			return Workflow{}, ErrInvalidWorkflow
		}
	}
	workflow, replayed, err := s.store.Approve(ctx, actor, workflowID, idempotencyKey, inputHash, observedAt.Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

func (s *Service) Cancel(ctx context.Context, actor Actor, workflowID, idempotencyKey string) (Workflow, error) {
	observedAt := s.clock().UTC()
	if err := validateActor(actor, observedAt, false); err != nil {
		return Workflow{}, err
	}
	if !hash(workflowID) || !idempotency(idempotencyKey) {
		return Workflow{}, ErrInvalidWorkflow
	}
	inputHash := actionHash("CANCEL", actor, workflowID, idempotencyKey, struct{}{})
	current, err := s.store.Get(ctx, actor.OrganizationID, workflowID)
	if err != nil {
		return Workflow{}, err
	}
	if current.State == Proposed {
		if err := validateActor(actor, observedAt, true); err != nil {
			return Workflow{}, err
		}
	}
	workflow, replayed, err := s.store.Cancel(ctx, actor, workflowID, idempotencyKey, inputHash, observedAt.Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

// RecordSubmission is an internal relayer boundary. It records the exact
// broadcast transaction hash before the process may report submission.
func (s *Service) RecordSubmission(ctx context.Context, organizationID, workflowID, transactionHash string) (Workflow, error) {
	if !identifier(organizationID) || !hash(workflowID) || !hash(transactionHash) {
		return Workflow{}, ErrInvalidWorkflow
	}
	workflow, replayed, err := s.store.Submit(ctx, organizationID, workflowID, transactionHash, s.clock().UTC().Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

// RecordProvenRetry is the sole path from a dropped/reorged side state back to
// SUBMITTED. The proof binds the unchanged Safe transaction and current Safe
// nonce/preconditions; the generic submission boundary remains closed.
func (s *Service) RecordProvenRetry(ctx context.Context, organizationID, workflowID, transactionHash string, proof SafeRetryProof) (Workflow, error) {
	now := s.clock().UTC().Truncate(time.Second)
	if !identifier(organizationID) || !hash(workflowID) || !hash(transactionHash) ||
		!validSafeRetryProof(workflowID, transactionHash, proof, now) {
		return Workflow{}, ErrInvalidWorkflow
	}
	workflow, replayed, err := s.store.RetrySubmission(ctx, organizationID, workflowID, transactionHash, proof, now)
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

// RecordConfirmation is an internal reconciler boundary. Finalization still
// requires the independent canonical receipt observer.
func (s *Service) RecordConfirmation(ctx context.Context, organizationID, workflowID, transactionHash string) (Workflow, error) {
	if !identifier(organizationID) || !hash(workflowID) || !hash(transactionHash) {
		return Workflow{}, ErrInvalidWorkflow
	}
	workflow, replayed, err := s.store.Confirm(ctx, organizationID, workflowID, transactionHash, s.clock().UTC().Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

// ObserveAndComplete is an internal worker boundary, not an owner API. The
// configured observer independently discovers and validates canonical finalized
// chain evidence; no caller supplies a receipt or transaction hash.
func (s *Service) ObserveAndComplete(ctx context.Context, organizationID, workflowID string) (Workflow, error) {
	if s.observer == nil {
		return Workflow{}, ErrCompletionUnavailable
	}
	if !identifier(organizationID) || !hash(workflowID) {
		return Workflow{}, ErrInvalidReceipt
	}
	workflow, err := s.store.Get(ctx, organizationID, workflowID)
	if err != nil {
		return Workflow{}, err
	}
	if workflow.State == Finalized && workflow.CompletionDigest != "" {
		workflow.Replayed = true
		return workflow, nil
	}
	if !completionCandidateState(workflow.State) {
		return Workflow{}, ErrStateConflict
	}
	receipt, err := s.observer.ObserveWorkflowCompletion(ctx, workflow)
	if err != nil {
		return Workflow{}, fmt.Errorf("%w: %w", ErrInvalidReceipt, err)
	}
	if !validReceipt(receipt) || workflow.WorkflowID != receipt.WorkflowID || workflow.PayloadHash != receipt.PayloadHash ||
		workflow.ChainID != receipt.ChainID || workflow.ContractAddress != receipt.ContractAddress || workflow.FunctionSelector != receipt.FunctionSelector ||
		workflow.ApprovedAt <= 0 || receipt.BlockTimestamp <= uint64(workflow.ApprovedAt) {
		return Workflow{}, ErrInvalidReceipt
	}
	bytes, err := json.Marshal(receipt)
	if err != nil {
		return Workflow{}, err
	}
	digest := completionDigest(receipt)
	stored, replayed, err := s.store.Complete(ctx, organizationID, workflowID, receipt, digest, bytes, s.clock().UTC().Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	stored.Replayed = replayed
	return stored, nil
}

// RequireReapproval terminalizes a deterministically rejected chain workflow.
// It is an internal observer boundary: callers cannot supply arbitrary error
// strings or move a finalized workflow back into an approval state.
func (s *Service) RequireReapproval(ctx context.Context, organizationID, workflowID string, reason TerminalReason) (Workflow, error) {
	return s.RecordChainFailure(ctx, organizationID, workflowID, RequiresReapproval, reason)
}

// RecordChainFailure moves an in-flight workflow to one explicit side state.
// Reason/state pairs are closed so raw provider errors never become authority.
func (s *Service) RecordChainFailure(ctx context.Context, organizationID, workflowID string, state State, reason TerminalReason) (Workflow, error) {
	if !identifier(organizationID) || !hash(workflowID) || !validChainFailure(state, reason) {
		return Workflow{}, ErrInvalidWorkflow
	}
	workflow, replayed, err := s.store.FailChain(ctx, organizationID, workflowID, state, reason, s.clock().UTC().Truncate(time.Second))
	if err != nil {
		return Workflow{}, err
	}
	workflow.Replayed = replayed
	return workflow, nil
}

func validChainFailure(state State, reason TerminalReason) bool {
	switch state {
	case Reverted:
		return reason == MinedRevert
	case Reorged:
		return reason == ReorgDetected
	case TimedOut:
		return reason == SubmissionTimeout
	case RequiresReapproval:
		return reason == ReceiptRejected || reason == SafeNonceConflict || reason == PreconditionChanged
	default:
		return false
	}
}

func validSafeRetryProof(workflowID, transactionHash string, proof SafeRetryProof, now time.Time) bool {
	if proof.WorkflowID != workflowID || proof.RetryTransactionHash != transactionHash || !hash(proof.PreviousTransactionHash) ||
		!hash(proof.RetryTransactionHash) || proof.PreviousCanonical ||
		(proof.Outcome != "DROPPED" && proof.Outcome != "REORGED") || !canonicalAddress(proof.SafeAddress) ||
		!hash(proof.SafeTxHash) || !hash(proof.ExecCalldataHash) || !hash(proof.VerifiedPayloadHash) ||
		!hash(proof.EvidenceDigest) || proof.ObservedAt <= 0 || time.Unix(proof.ObservedAt, 0).After(now.Add(time.Minute)) ||
		now.Sub(time.Unix(proof.ObservedAt, 0)) > time.Minute || len(proof.Observers) < 2 || len(proof.Observers) > 5 {
		return false
	}
	seen := make(map[string]struct{}, len(proof.Observers))
	for _, observer := range proof.Observers {
		if !identifier(observer) {
			return false
		}
		if _, duplicate := seen[observer]; duplicate {
			return false
		}
		seen[observer] = struct{}{}
	}
	return true
}

func sameSafeRetryProof(left, right SafeRetryProof) bool {
	if left.WorkflowID != right.WorkflowID || left.PreviousTransactionHash != right.PreviousTransactionHash ||
		left.RetryTransactionHash != right.RetryTransactionHash || left.Outcome != right.Outcome ||
		left.PreviousCanonical != right.PreviousCanonical || left.SafeAddress != right.SafeAddress ||
		left.SafeNonce != right.SafeNonce || left.SafeTxHash != right.SafeTxHash ||
		left.ExecCalldataHash != right.ExecCalldataHash || left.VerifiedPayloadHash != right.VerifiedPayloadHash ||
		left.EvidenceDigest != right.EvidenceDigest || left.ObservedAt != right.ObservedAt ||
		len(left.Observers) != len(right.Observers) {
		return false
	}
	for index := range left.Observers {
		if left.Observers[index] != right.Observers[index] {
			return false
		}
	}
	return true
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

func validRole(role Role) bool {
	return role == OrgAdmin || role == SellerAdmin || role == SignerOperator || role == IncidentResponder
}

func validReceipt(receipt CompletionReceipt) bool {
	if !hash(receipt.WorkflowID) || !hash(receipt.PayloadHash) ||
		(receipt.ChainID != 8453 && receipt.ChainID != 84532) || !hash(receipt.TransactionHash) ||
		!hash(receipt.BlockHash) || receipt.BlockNumber == 0 || !canonicalAddress(receipt.ContractAddress) ||
		receipt.BlockTimestamp == 0 ||
		receipt.EventSignature != GovernanceWorkflowBoundTopic || receipt.Finality != "FINALIZED" {
		return false
	}
	if receipt.ConfirmedHead < receipt.BlockNumber || receipt.FinalizedHead < receipt.BlockNumber || !selector(receipt.FunctionSelector) ||
		!hash(receipt.ActionEventSignature) || !hash(receipt.EvidenceDigest) ||
		len(receipt.ActionLogIndexes) == 0 || len(receipt.ActionLogIndexes) > 100 ||
		len(receipt.Observers) < 2 || len(receipt.Observers) > 5 {
		return false
	}
	seenIndexes := make(map[uint64]struct{}, len(receipt.ActionLogIndexes))
	for _, index := range receipt.ActionLogIndexes {
		if index >= receipt.LogIndex {
			return false
		}
		if _, duplicate := seenIndexes[index]; duplicate {
			return false
		}
		seenIndexes[index] = struct{}{}
	}
	seenObservers := make(map[string]struct{}, len(receipt.Observers))
	for _, observer := range receipt.Observers {
		if !identifier(observer) {
			return false
		}
		if _, duplicate := seenObservers[observer]; duplicate {
			return false
		}
		seenObservers[observer] = struct{}{}
	}
	return true
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

// completionDigest excludes observation-time metadata so two independent
// runtime instances converge on one idempotency identity for the same
// canonical chain event even if their responding-provider subsets or observed
// heads differ.
func completionDigest(receipt CompletionReceipt) string {
	encoded, _ := json.Marshal(struct {
		WorkflowID, PayloadHash, TransactionHash, BlockHash, ContractAddress string
		EventSignature, FunctionSelector, ActionEventSignature, Finality     string
		ChainID, BlockNumber, BlockTimestamp, LogIndex                       uint64
		ActionLogIndexes                                                     []uint64
	}{
		receipt.WorkflowID, receipt.PayloadHash, receipt.TransactionHash, receipt.BlockHash, receipt.ContractAddress,
		receipt.EventSignature, receipt.FunctionSelector, receipt.ActionEventSignature, receipt.Finality,
		receipt.ChainID, receipt.BlockNumber, receipt.BlockTimestamp, receipt.LogIndex, receipt.ActionLogIndexes,
	})
	return "0x" + hex.EncodeToString(sha256Bytes(append([]byte("ASCP_WORKFLOW_COMPLETION_V1\n"), encoded...)))
}
func identifier(value string) bool  { return envelope.ValidIdentifier(value) && len(value) <= 128 }
func idempotency(value string) bool { return identifier(value) }
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

func selector(value string) bool {
	if len(value) != 10 || value == "0x00000000" || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 4
}

func governanceCalldata(value, functionSelector string) bool {
	if len(value) <= 10 || len(value) > 131082 || len(value)%2 != 0 || value != strings.ToLower(value) ||
		!strings.HasPrefix(value, functionSelector) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) > 4
}

func boundActionForWorkflow(workflow Workflow) (governanceworkflow.BoundAction, error) {
	if !hash(workflow.WorkflowID) || len(workflow.GovernanceAction) == 0 || !json.Valid(workflow.GovernanceAction) {
		return governanceworkflow.BoundAction{}, ErrInvalidWorkflow
	}
	bound, err := governanceworkflow.RebindAction(workflow.WorkflowID, workflow.GovernanceAction)
	if err != nil || bound.WorkflowKind != string(workflow.Kind) || bound.PayloadHash != workflow.PayloadHash ||
		bound.ChainID != workflow.ChainID || bound.ContractAddress != workflow.ContractAddress ||
		bound.FunctionSelector != workflow.FunctionSelector || bound.Calldata != workflow.Calldata {
		return governanceworkflow.BoundAction{}, ErrInvalidWorkflow
	}
	return bound, nil
}
