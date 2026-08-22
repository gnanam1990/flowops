package ascpgovernancerelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

type State string

const MaxRelayAttempts = 10

const (
	StateAwaitingSignatures State = "AWAITING_SIGNATURES"
	StateReady              State = "READY"
	StateBroadcasting       State = "BROADCASTING"
	StateSubmitted          State = "SUBMITTED"
	StatePending            State = "PENDING"
	StateRetryable          State = "RETRYABLE_EXACT"
	StateReapprovalRequired State = "REAPPROVAL_REQUIRED"
	StateFinalizedObserved  State = "FINALIZED_OBSERVED"
)

var (
	ErrNoWork              = errors.New("no governance relay work available")
	ErrStateConflict       = errors.New("governance relay state conflict")
	ErrIdempotencyConflict = errors.New("governance relay idempotency conflict")
	ErrLeaseLost           = errors.New("governance relay lease lost")
	identifierPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type OuterArtifact struct {
	Handle           string    `json:"handle"`
	TransactionHash  string    `json:"transactionHash"`
	SafeTxHash       string    `json:"safeTxHash"`
	ExecCalldataHash string    `json:"execCalldataHash"`
	PreparedAt       time.Time `json:"preparedAt"`
}

type RelayBinding struct {
	OutboxID          string `json:"outboxId"`
	WorkflowID        string `json:"workflowId"`
	OrganizationID    string `json:"organizationId"`
	ChainID           uint64 `json:"chainId"`
	SafeAddress       string `json:"safeAddress"`
	SafeTxHash        string `json:"safeTxHash"`
	ExecCalldataHash  string `json:"execCalldataHash"`
	PriorAttemptCount int    `json:"priorAttemptCount"`
}

// OutcomeBinding is the complete and deliberately minimal read-side request
// sent to the chain observer. Vault handles, authorization idempotency data,
// signature hashes, leases, and full persisted jobs never cross this boundary.
type OutcomeBinding struct {
	WorkflowID           string `json:"workflowId"`
	ChainID              uint64 `json:"chainId"`
	SafeAddress          string `json:"safeAddress"`
	SafeNonce            uint64 `json:"safeNonce"`
	PayloadHash          string `json:"payloadHash"`
	SafeTxHash           string `json:"safeTxHash"`
	ExecCalldataHash     string `json:"execCalldataHash"`
	OuterTransactionHash string `json:"outerTransactionHash"`
	AttemptCount         int    `json:"attemptCount"`
}

// SnapshotBinding contains only chain-verifiable action material. Approval
// principals, idempotency hashes, and unrelated workflow metadata stay inside
// the control plane.
type SnapshotBinding struct {
	WorkflowID       string          `json:"workflowId"`
	PayloadHash      string          `json:"payloadHash"`
	ChainID          uint64          `json:"chainId"`
	SafeAddress      string          `json:"safeAddress"`
	ContractAddress  string          `json:"contractAddress"`
	FunctionSelector string          `json:"functionSelector"`
	Calldata         string          `json:"calldata"`
	GovernanceAction json.RawMessage `json:"governanceAction"`
	ExecuteAfter     int64           `json:"executeAfter"`
}

type Job struct {
	OutboxID          string                                  `json:"outboxId"`
	Command           ascpworkflow.GovernanceExecutionCommand `json:"command"`
	State             State                                   `json:"state"`
	Prepared          Prepared                                `json:"prepared,omitempty"`
	ArtifactHandle    string                                  `json:"artifactHandle,omitempty"`
	AuthorizationKey  string                                  `json:"authorizationKey,omitempty"`
	AuthorizationHash string                                  `json:"authorizationHash,omitempty"`
	Outer             OuterArtifact                           `json:"outer,omitempty"`
	LastOutcome       OutcomeEvidence                         `json:"lastOutcome,omitempty"`
	AttemptCount      int                                     `json:"attemptCount"`
	LeaseOwner        string                                  `json:"leaseOwner,omitempty"`
	LeaseToken        string                                  `json:"leaseToken,omitempty"`
	LeaseExpiresAt    time.Time                               `json:"leaseExpiresAt,omitempty"`
	CreatedAt         time.Time                               `json:"createdAt"`
	UpdatedAt         time.Time                               `json:"updatedAt"`
}

type Lease struct {
	Job   Job
	Token string
}

type Store interface {
	ConsumeCommand(context.Context) (Job, bool, error)
	Get(context.Context, string, string) (Job, error)
	ReplayAuthorization(context.Context, string, string, string, string) (Job, bool, error)
	Authorize(context.Context, string, string, string, string, Prepared, string, time.Time) (Job, bool, error)
	ClaimRelay(context.Context, string, time.Duration) (Lease, error)
	RecordOuterPrepared(context.Context, Lease, OuterArtifact, time.Time) (Job, error)
	RecordSubmitted(context.Context, Lease, string, time.Time) (Job, error)
	ClaimObservation(context.Context, string, time.Duration) (Lease, error)
	ApplyDecision(context.Context, Lease, OutcomeEvidence, DecisionResult, time.Time) (Job, error)
	ReleaseLease(context.Context, Lease) error
}

type SafeDirectory interface {
	SafeFor(context.Context, string, uint64) (string, error)
}

type SnapshotSource interface {
	Observe(context.Context, ascpworkflow.GovernanceExecutionCommand, string) (Snapshot, error)
}

type ArtifactVault interface {
	Seal(context.Context, []byte, []byte) (string, error)
	Open(context.Context, string, []byte) ([]byte, error)
}

type OuterBroadcaster interface {
	Prepare(context.Context, RelayBinding, []byte) (OuterArtifact, error)
	Broadcast(context.Context, string) (string, error)
}

type OutcomeSource interface {
	Observe(context.Context, OutcomeBinding) (OutcomeEvidence, error)
}

type WorkflowRecorder interface {
	RecordSubmission(context.Context, string, string, string) (ascpworkflow.Workflow, error)
	RecordProvenRetry(context.Context, string, string, string, ascpworkflow.SafeRetryProof) (ascpworkflow.Workflow, error)
	RecordChainFailure(context.Context, string, string, ascpworkflow.State, ascpworkflow.TerminalReason) (ascpworkflow.Workflow, error)
	RequireReapproval(context.Context, string, string, ascpworkflow.TerminalReason) (ascpworkflow.Workflow, error)
}

type Config struct {
	WorkerID      string
	Quorum        int
	LeaseDuration time.Duration
	Clock         func() time.Time
}

type Service struct {
	store       Store
	directory   SafeDirectory
	snapshots   SnapshotSource
	vault       ArtifactVault
	broadcaster OuterBroadcaster
	outcomes    OutcomeSource
	workflows   WorkflowRecorder
	config      Config
}

func NewService(store Store, directory SafeDirectory, snapshots SnapshotSource, vault ArtifactVault,
	broadcaster OuterBroadcaster, outcomes OutcomeSource, workflows WorkflowRecorder, config Config,
) (*Service, error) {
	if store == nil || directory == nil || snapshots == nil || vault == nil || broadcaster == nil || outcomes == nil || workflows == nil ||
		!identifierPattern.MatchString(config.WorkerID) || config.Quorum < 2 || config.Quorum > 5 ||
		config.LeaseDuration < time.Second || config.LeaseDuration > time.Minute {
		return nil, ErrInvalidCommand
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Service{store, directory, snapshots, vault, broadcaster, outcomes, workflows, config}, nil
}

func (s *Service) ConsumeOnce(ctx context.Context) (Job, bool, error) {
	return s.store.ConsumeCommand(ctx)
}

// SigningRequest returns the exact Safe EIP-712 digest and independently
// observed owner snapshot that customer owners must inspect before signing.
// It never accepts signature bytes and has no vault or broadcaster path.
func (s *Service) SigningRequest(ctx context.Context, organizationID, workflowID string) (SigningRequest, error) {
	return InspectSigningRequest(ctx, s.store, s.directory, s.snapshots, organizationID, workflowID, s.config.Quorum, s.config.Clock().UTC())
}

func InspectSigningRequest(ctx context.Context, store Store, directory SafeDirectory, snapshots SnapshotSource,
	organizationID, workflowID string, quorum int, now time.Time,
) (SigningRequest, error) {
	if store == nil || directory == nil || snapshots == nil || !identifierPattern.MatchString(organizationID) || !canonicalHash(workflowID) {
		return SigningRequest{}, ErrInvalidCommand
	}
	job, err := store.Get(ctx, organizationID, workflowID)
	if err != nil {
		return SigningRequest{}, err
	}
	if job.State != StateAwaitingSignatures {
		return SigningRequest{}, ErrStateConflict
	}
	safe, err := directory.SafeFor(ctx, organizationID, job.Command.ChainID)
	if err != nil {
		return SigningRequest{}, fmt.Errorf("resolve customer Safe: %w", err)
	}
	snapshot, err := snapshots.Observe(ctx, job.Command, safe)
	if err != nil {
		return SigningRequest{}, fmt.Errorf("observe Safe signing snapshot: %w", err)
	}
	return BuildSigningRequest(job.Command, safe, snapshot, quorum, now)
}

func (s *Service) Authorize(ctx context.Context, organizationID, workflowID, idempotencyKey string, signatures []byte) (Job, error) {
	return AuthorizeSignatures(ctx, s.store, s.directory, s.snapshots, s.vault, organizationID, workflowID,
		idempotencyKey, signatures, s.config.Quorum, s.config.Clock().UTC())
}

func AuthorizeSignatures(ctx context.Context, store Store, directory SafeDirectory, snapshots SnapshotSource, vault ArtifactVault,
	organizationID, workflowID, idempotencyKey string, signatures []byte, quorum int, now time.Time,
) (Job, error) {
	if store == nil || directory == nil || snapshots == nil || vault == nil {
		return Job{}, ErrInvalidCommand
	}
	if !identifierPattern.MatchString(organizationID) || !canonicalHash(workflowID) ||
		!identifierPattern.MatchString(idempotencyKey) || len(signatures) == 0 || len(signatures) > 50*65 {
		return Job{}, ErrInvalidCommand
	}
	signatureCopy := append([]byte(nil), signatures...)
	defer clear(signatureCopy)
	inputHash := hashBytes(signatureCopy)
	if existing, replayed, err := store.ReplayAuthorization(ctx, organizationID, workflowID, idempotencyKey, inputHash); err != nil || replayed {
		return existing, err
	}
	job, err := store.Get(ctx, organizationID, workflowID)
	if err != nil {
		return Job{}, err
	}
	safe, err := directory.SafeFor(ctx, organizationID, job.Command.ChainID)
	if err != nil {
		return Job{}, fmt.Errorf("resolve customer Safe: %w", err)
	}
	snapshot, err := snapshots.Observe(ctx, job.Command, safe)
	if err != nil {
		return Job{}, fmt.Errorf("observe Safe authorization snapshot: %w", err)
	}
	prepared, execCalldata, err := Prepare(job.Command, safe, snapshot, signatureCopy, quorum, now)
	if err != nil {
		return Job{}, err
	}
	defer clear(execCalldata)
	handle, err := vault.Seal(ctx, execCalldata, artifactAAD(job.OutboxID, prepared))
	if err != nil || !identifierPattern.MatchString(handle) {
		return Job{}, errors.Join(ErrInvalidCommand, err)
	}
	stored, _, err := store.Authorize(ctx, organizationID, workflowID, idempotencyKey, inputHash, prepared, handle, now)
	return stored, err
}

func (s *Service) RelayOnce(ctx context.Context) (Job, error) {
	lease, err := s.store.ClaimRelay(ctx, s.config.WorkerID, s.config.LeaseDuration)
	if err != nil {
		return Job{}, err
	}
	defer s.release(ctx, lease)
	if lease.Job.State == StateBroadcasting {
		return s.recoverBroadcasting(ctx, lease)
	}
	if lease.Job.State == StateRetryable {
		// A retry proof is accepted by the workflow only while its quorum
		// observation is fresh. Re-observe immediately before any new outer
		// transaction is prepared so a queued worker can never broadcast first
		// and discover only afterwards that its proof expired or chain truth
		// changed.
		evidence, err := s.outcomes.Observe(ctx, outcomeBinding(lease.Job))
		if err != nil {
			return Job{}, fmt.Errorf("refresh exact retry evidence: %w", err)
		}
		decision, err := DecideRetry(lease.Job.Prepared, lease.Job.Outer.TransactionHash, evidence,
			s.config.Quorum, s.config.Clock().UTC())
		if err != nil {
			return Job{}, err
		}
		if decision.Decision == DecisionReapprove {
			return s.requireReapproval(ctx, lease, evidence, decision)
		}
		if decision.Decision == DecisionFinalized {
			return s.store.ApplyDecision(ctx, lease, evidence, decision, s.config.Clock().UTC())
		}
		if decision.Decision == DecisionWait {
			// Keep the job retry-claimable: the workflow is already in a side
			// state, and its receipt observer independently scans those states.
			// A later cycle will re-observe rather than inventing a replacement.
			return lease.Job, nil
		}
		if lease.Job.AttemptCount >= MaxRelayAttempts {
			return s.requireReapproval(ctx, lease, evidence, DecisionResult{
				Decision: DecisionReapprove, Reason: ascpworkflow.ReceiptRejected,
			})
		}
		refreshed, err := s.store.ApplyDecision(ctx, lease, evidence, decision, s.config.Clock().UTC())
		if err != nil {
			return Job{}, err
		}
		lease.Job = refreshed
	}
	if lease.Job.AttemptCount >= MaxRelayAttempts {
		return s.requireReapproval(ctx, lease, OutcomeEvidence{}, DecisionResult{
			Decision: DecisionReapprove, Reason: ascpworkflow.ReceiptRejected,
		})
	}
	safe, err := s.directory.SafeFor(ctx, lease.Job.Command.OrganizationID, lease.Job.Command.ChainID)
	if err != nil {
		return Job{}, fmt.Errorf("resolve customer Safe before relay: %w", err)
	}
	if safe != lease.Job.Prepared.Transaction.Safe {
		return s.requireReapproval(ctx, lease, OutcomeEvidence{}, DecisionResult{
			Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged,
		})
	}
	snapshot, err := s.snapshots.Observe(ctx, lease.Job.Command, safe)
	if err != nil {
		return Job{}, err
	}
	decision, err := ValidateRelaySnapshot(lease.Job.Command, lease.Job.Prepared, snapshot, s.config.Quorum, s.config.Clock().UTC())
	if err != nil {
		return Job{}, err
	}
	if decision.Decision == DecisionReapprove {
		return s.requireReapproval(ctx, lease, OutcomeEvidence{}, decision)
	}
	execCalldata, err := s.vault.Open(ctx, lease.Job.ArtifactHandle, artifactAAD(lease.Job.OutboxID, lease.Job.Prepared))
	if err != nil {
		return Job{}, err
	}
	defer clear(execCalldata)
	if err := VerifyPrepared(lease.Job.Command, lease.Job.Prepared, execCalldata); err != nil {
		return Job{}, err
	}
	outer, err := s.broadcaster.Prepare(ctx, relayBinding(lease.Job), execCalldata)
	if err != nil || !validOuter(outer, lease.Job.Prepared, s.config.Clock().UTC()) {
		return Job{}, errors.Join(ErrInvalidOutcome, err)
	}
	if _, err := s.store.RecordOuterPrepared(ctx, lease, outer, s.config.Clock().UTC()); err != nil {
		return Job{}, err
	}
	lease.Job.State, lease.Job.Outer = StateBroadcasting, outer
	return s.broadcastPrepared(ctx, lease)
}

// recoverBroadcasting closes the crash window after an outer transaction was
// durably prepared but before its submission was durably recorded. It first
// reconciles the exact outer hash; it never assumes that a failed/missing write
// response means the transaction was absent.
func (s *Service) recoverBroadcasting(ctx context.Context, lease Lease) (Job, error) {
	evidence, err := s.outcomes.Observe(ctx, outcomeBinding(lease.Job))
	if err != nil {
		return Job{}, fmt.Errorf("reconcile prepared outer transaction: %w", err)
	}
	decision, err := DecideRetry(lease.Job.Prepared, lease.Job.Outer.TransactionHash, evidence,
		s.config.Quorum, s.config.Clock().UTC())
	if err != nil {
		return Job{}, err
	}
	if decision.Decision == DecisionRetryExact {
		if lease.Job.AttemptCount >= MaxRelayAttempts {
			return s.requireReapproval(ctx, lease, evidence, DecisionResult{
				Decision: DecisionReapprove, Reason: ascpworkflow.ReceiptRejected,
			})
		}
		// The previous outer hash is proven absent/non-canonical. Revalidate the
		// live Safe, owner set, threshold, and action preconditions before
		// broadcasting the already prepared byte-identical outer transaction.
		safe, err := s.directory.SafeFor(ctx, lease.Job.Command.OrganizationID, lease.Job.Command.ChainID)
		if err != nil {
			return Job{}, fmt.Errorf("resolve customer Safe during broadcast recovery: %w", err)
		}
		if safe != lease.Job.Prepared.Transaction.Safe {
			return s.requireReapproval(ctx, lease, evidence, DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged})
		}
		snapshot, err := s.snapshots.Observe(ctx, lease.Job.Command, safe)
		if err != nil {
			return Job{}, err
		}
		revalidation, err := ValidateRelaySnapshot(lease.Job.Command, lease.Job.Prepared, snapshot,
			s.config.Quorum, s.config.Clock().UTC())
		if err != nil {
			return Job{}, err
		}
		if revalidation.Decision == DecisionReapprove {
			return s.requireReapproval(ctx, lease, evidence, revalidation)
		}
		return s.broadcastPrepared(ctx, lease)
	}

	// Pending, finalized, mined-revert, or binding-drift evidence proves that
	// this outer hash crossed the external boundary. Persist submission before
	// any later state classification so a crash replays without inventing a new
	// economic attempt.
	if err := s.recordWorkflowSubmission(ctx, lease.Job, lease.Job.Outer.TransactionHash); err != nil {
		return Job{}, err
	}
	job, err := s.store.RecordSubmitted(ctx, lease, lease.Job.Outer.TransactionHash, s.config.Clock().UTC())
	if err != nil {
		return Job{}, err
	}
	lease.Job = job
	if decision.Decision == DecisionReapprove {
		return s.requireReapproval(ctx, lease, evidence, decision)
	}
	if decision.Decision == DecisionFinalized {
		return s.store.ApplyDecision(ctx, lease, evidence, decision, s.config.Clock().UTC())
	}
	return job, nil
}

func (s *Service) broadcastPrepared(ctx context.Context, lease Lease) (Job, error) {
	hash, err := s.broadcaster.Broadcast(ctx, lease.Job.Outer.Handle)
	if err != nil || hash != lease.Job.Outer.TransactionHash {
		return Job{}, errors.Join(ErrInvalidOutcome, err)
	}
	if err := s.recordWorkflowSubmission(ctx, lease.Job, hash); err != nil {
		return Job{}, err
	}
	return s.store.RecordSubmitted(ctx, lease, hash, s.config.Clock().UTC())
}

func (s *Service) recordWorkflowSubmission(ctx context.Context, job Job, transactionHash string) error {
	if job.AttemptCount == 0 {
		_, err := s.workflows.RecordSubmission(ctx, job.Command.OrganizationID, job.Command.WorkflowID, transactionHash)
		return err
	}
	_, err := s.workflows.RecordProvenRetry(ctx, job.Command.OrganizationID, job.Command.WorkflowID,
		transactionHash, safeRetryProof(job, transactionHash))
	return err
}

func (s *Service) ObserveOnce(ctx context.Context) (Job, error) {
	lease, err := s.store.ClaimObservation(ctx, s.config.WorkerID, s.config.LeaseDuration)
	if err != nil {
		return Job{}, err
	}
	defer s.release(ctx, lease)
	evidence, err := s.outcomes.Observe(ctx, outcomeBinding(lease.Job))
	if err != nil {
		return Job{}, err
	}
	decision, err := DecideRetry(lease.Job.Prepared, lease.Job.Outer.TransactionHash, evidence, s.config.Quorum, s.config.Clock().UTC())
	if err != nil {
		return Job{}, err
	}
	if decision.Decision == DecisionReapprove {
		return s.requireReapproval(ctx, lease, evidence, decision)
	}
	if decision.Decision == DecisionRetryExact {
		state, reason := ascpworkflow.TimedOut, ascpworkflow.SubmissionTimeout
		if evidence.Outcome == OutcomeReorged {
			state, reason = ascpworkflow.Reorged, ascpworkflow.ReorgDetected
		}
		if _, err := s.workflows.RecordChainFailure(ctx, lease.Job.Command.OrganizationID, lease.Job.Command.WorkflowID, state, reason); err != nil {
			return Job{}, err
		}
	}
	return s.store.ApplyDecision(ctx, lease, evidence, decision, s.config.Clock().UTC())
}

func (s *Service) requireReapproval(ctx context.Context, lease Lease, evidence OutcomeEvidence, decision DecisionResult) (Job, error) {
	if decision.Reason == ascpworkflow.MinedRevert {
		// Persist mined-revert reapproval as one workflow transition. A former
		// two-step REVERTED -> REQUIRES_REAPPROVAL sequence could crash between
		// transactions and strand the relay job in SUBMITTED forever.
		if _, err := s.workflows.RequireReapproval(ctx, lease.Job.Command.OrganizationID, lease.Job.Command.WorkflowID, ascpworkflow.ReceiptRejected); err != nil {
			return Job{}, err
		}
	} else if _, err := s.workflows.RequireReapproval(ctx, lease.Job.Command.OrganizationID, lease.Job.Command.WorkflowID, decision.Reason); err != nil {
		return Job{}, err
	}
	return s.store.ApplyDecision(ctx, lease, evidence, decision, s.config.Clock().UTC())
}

func (s *Service) release(ctx context.Context, lease Lease) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.store.ReleaseLease(releaseCtx, lease)
}

func validOuter(outer OuterArtifact, prepared Prepared, now time.Time) bool {
	return identifierPattern.MatchString(outer.Handle) && canonicalHash(outer.TransactionHash) &&
		outer.SafeTxHash == prepared.SafeTxHash && outer.ExecCalldataHash == prepared.ExecCalldataHash &&
		!outer.PreparedAt.IsZero() && !outer.PreparedAt.After(now.Add(time.Minute)) && now.Sub(outer.PreparedAt) <= time.Minute
}

func artifactAAD(outboxID string, prepared Prepared) []byte {
	return []byte(strings.Join([]string{"ASCP_GOVERNANCE_SAFE_ARTIFACT_V1", outboxID, prepared.WorkflowID, prepared.SafeTxHash, prepared.ExecCalldataHash}, "\x00"))
}

func safeRetryProof(job Job, retryTransactionHash string) ascpworkflow.SafeRetryProof {
	return ascpworkflow.SafeRetryProof{
		WorkflowID: job.Command.WorkflowID, PreviousTransactionHash: job.LastOutcome.OuterTransactionHash,
		RetryTransactionHash: retryTransactionHash, Outcome: string(job.LastOutcome.Outcome),
		PreviousCanonical: job.LastOutcome.PreviousCanonical, SafeAddress: job.Prepared.Transaction.Safe,
		SafeNonce: job.Prepared.Transaction.Nonce, SafeTxHash: job.Prepared.SafeTxHash,
		ExecCalldataHash: job.Prepared.ExecCalldataHash, VerifiedPayloadHash: job.LastOutcome.VerifiedPayloadHash,
		Observers: append([]string(nil), job.LastOutcome.Observers...), EvidenceDigest: job.LastOutcome.EvidenceDigest,
		ObservedAt: job.LastOutcome.ObservedAt.Unix(),
	}
}

func relayBinding(job Job) RelayBinding {
	return RelayBinding{
		OutboxID: job.OutboxID, WorkflowID: job.Command.WorkflowID, OrganizationID: job.Command.OrganizationID,
		ChainID: job.Command.ChainID, SafeAddress: job.Prepared.Transaction.Safe, SafeTxHash: job.Prepared.SafeTxHash,
		ExecCalldataHash: job.Prepared.ExecCalldataHash, PriorAttemptCount: job.AttemptCount,
	}
}

func outcomeBinding(job Job) OutcomeBinding {
	return OutcomeBinding{
		WorkflowID: job.Command.WorkflowID, ChainID: job.Command.ChainID,
		SafeAddress: job.Prepared.Transaction.Safe, SafeNonce: job.Prepared.Transaction.Nonce,
		PayloadHash: job.Prepared.PayloadHash, SafeTxHash: job.Prepared.SafeTxHash,
		ExecCalldataHash: job.Prepared.ExecCalldataHash, OuterTransactionHash: job.Outer.TransactionHash,
		AttemptCount: job.AttemptCount,
	}
}

func snapshotBinding(command ascpworkflow.GovernanceExecutionCommand, safeAddress string) SnapshotBinding {
	return SnapshotBinding{
		WorkflowID: command.WorkflowID, PayloadHash: command.PayloadHash, ChainID: command.ChainID,
		SafeAddress: safeAddress, ContractAddress: command.ContractAddress, FunctionSelector: command.FunctionSelector,
		Calldata: command.Calldata, GovernanceAction: append(json.RawMessage(nil), command.GovernanceAction...),
		ExecuteAfter: command.ExecuteAfter,
	}
}
