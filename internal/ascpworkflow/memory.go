package ascpworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu          sync.Mutex
	workflows   map[string]Workflow
	actions     map[string]memoryAction
	receipts    map[string]memoryReceiptOwner
	retryProofs map[string][]SafeRetryProof
}

type memoryAction struct {
	hash       string
	workflowID string
}

type memoryReceiptOwner struct {
	workflowID string
	digest     string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		workflows: make(map[string]Workflow), actions: make(map[string]memoryAction), receipts: make(map[string]memoryReceiptOwner),
		retryProofs: make(map[string][]SafeRetryProof),
	}
}

func (s *MemoryStore) ReplayCreate(ctx context.Context, actor Actor, key, inputHash string) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	action, exists := s.actions[actionScope(actor.OrganizationID, actor.PrincipalID, "CREATE", key)]
	if !exists {
		return Workflow{}, false, nil
	}
	if action.hash != inputHash {
		return Workflow{}, false, ErrIdempotencyConflict
	}
	return cloneWorkflow(s.workflows[action.workflowID]), true, nil
}

func (s *MemoryStore) Create(ctx context.Context, workflow Workflow, key, inputHash string) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := actionScope(workflow.OrganizationID, workflow.ProposedBy, "CREATE", key)
	if action, exists := s.actions[scope]; exists {
		if action.hash != inputHash {
			return Workflow{}, false, ErrIdempotencyConflict
		}
		return cloneWorkflow(s.workflows[action.workflowID]), true, nil
	}
	if _, exists := s.workflows[workflow.WorkflowID]; exists {
		return Workflow{}, false, ErrStateConflict
	}
	s.workflows[workflow.WorkflowID] = cloneWorkflow(workflow)
	s.actions[scope] = memoryAction{hash: inputHash, workflowID: workflow.WorkflowID}
	return cloneWorkflow(workflow), false, nil
}

func (s *MemoryStore) Get(ctx context.Context, organizationID, workflowID string) (Workflow, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, ErrNotFound
	}
	return cloneWorkflow(workflow), nil
}

func (s *MemoryStore) Pending(ctx context.Context, limit int, afterWorkflowID string) ([]Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 || (afterWorkflowID != "" && !hash(afterWorkflowID)) {
		return nil, ErrInvalidWorkflow
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var cursor Workflow
	if afterWorkflowID != "" {
		var exists bool
		cursor, exists = s.workflows[afterWorkflowID]
		if !exists || cursor.ApprovedAt == 0 {
			return nil, ErrInvalidWorkflow
		}
	}
	result := make([]Workflow, 0, limit)
	for _, workflow := range s.workflows {
		if completionCandidate(workflow) && (afterWorkflowID == "" || workflow.ApprovedAt > cursor.ApprovedAt ||
			(workflow.ApprovedAt == cursor.ApprovedAt && workflow.WorkflowID > afterWorkflowID)) {
			result = append(result, cloneWorkflow(workflow))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ApprovedAt != result[j].ApprovedAt {
			return result[i].ApprovedAt < result[j].ApprovedAt
		}
		return result[i].WorkflowID < result[j].WorkflowID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *MemoryStore) Approve(ctx context.Context, actor Actor, workflowID, key, inputHash string, now time.Time) (Workflow, bool, error) {
	return s.transition(ctx, actor, workflowID, "APPROVE", key, inputHash, now)
}

func (s *MemoryStore) Cancel(ctx context.Context, actor Actor, workflowID, key, inputHash string, now time.Time) (Workflow, bool, error) {
	return s.transition(ctx, actor, workflowID, "CANCEL", key, inputHash, now)
}

func (s *MemoryStore) transition(ctx context.Context, actor Actor, workflowID, action, key, inputHash string, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := actionScope(actor.OrganizationID, actor.PrincipalID, action, key)
	if stored, exists := s.actions[scope]; exists {
		if stored.hash != inputHash {
			return Workflow{}, false, ErrIdempotencyConflict
		}
		return cloneWorkflow(s.workflows[stored.workflowID]), true, nil
	}
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != actor.OrganizationID {
		return Workflow{}, false, ErrNotFound
	}
	if actor.PrincipalID == workflow.ProposedBy && action == "APPROVE" {
		return Workflow{}, false, ErrSamePrincipal
	}
	if action == "APPROVE" && !canApprove(workflow.Kind, actor.Role) {
		return Workflow{}, false, ErrForbiddenRole
	}
	if action == "CANCEL" && !canCancel(workflow, actor) {
		return Workflow{}, false, ErrForbiddenRole
	}
	mutated := false
	if workflow.State == Proposed && !now.Before(time.Unix(workflow.ExpiresAt, 0)) {
		workflow.State, workflow.ExpiredAt, mutated = Expired, now.Unix(), true
	} else if workflow.State == Proposed {
		switch action {
		case "APPROVE":
			workflow.State = Finalized
			if requiresChainReceipt(workflow.Kind) {
				workflow.State = ApprovedPendingChain
			} else {
				workflow.FinalizedAt = now.Unix()
			}
			workflow.ApprovedBy, workflow.ApproverRole = actor.PrincipalID, actor.Role
			workflow.ApproverStepUpAt, workflow.ApproverStepUpUntil = actor.StepUpAt.UTC().Unix(), actor.StepUpUntil.UTC().Unix()
			workflow.ApprovedAt, mutated = now.Unix(), true
		case "CANCEL":
			workflow.State, workflow.CancelledBy, workflow.CancelledAt, mutated = Cancelled, actor.PrincipalID, now.Unix(), true
		}
	}
	if mutated {
		s.workflows[workflowID] = cloneWorkflow(workflow)
	}
	s.actions[scope] = memoryAction{hash: inputHash, workflowID: workflowID}
	return cloneWorkflow(workflow), !mutated, nil
}

func (s *MemoryStore) Expire(ctx context.Context, organizationID, workflowID string, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, false, ErrNotFound
	}
	if workflow.State == Proposed && !now.Before(time.Unix(workflow.ExpiresAt, 0)) {
		workflow.State, workflow.ExpiredAt = Expired, now.Unix()
		s.workflows[workflowID] = cloneWorkflow(workflow)
		return cloneWorkflow(workflow), true, nil
	}
	return cloneWorkflow(workflow), false, nil
}

func (s *MemoryStore) Submit(ctx context.Context, organizationID, workflowID, transactionHash string, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, false, ErrNotFound
	}
	if workflow.State == Submitted && workflow.SubmissionTxHash == transactionHash {
		return cloneWorkflow(workflow), true, nil
	}
	// A retry after timeout or reorg needs the relayer's byte-identical Safe
	// transaction and nonce proof. This store boundary does not receive that
	// evidence, so it must not turn an arbitrary replacement transaction hash
	// back into an approved submission.
	if workflow.State != ApprovedPendingChain {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.ApprovedAt {
		return Workflow{}, false, ErrStateConflict
	}
	workflow.State, workflow.SubmissionTxHash, workflow.SubmittedAt = Submitted, transactionHash, now.Unix()
	workflow.ConfirmedAt, workflow.TerminalReason, workflow.TerminalAt = 0, "", 0
	s.workflows[workflowID] = cloneWorkflow(workflow)
	return cloneWorkflow(workflow), false, nil
}

func (s *MemoryStore) RetrySubmission(ctx context.Context, organizationID, workflowID, transactionHash string, proof SafeRetryProof, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, false, ErrNotFound
	}
	if workflow.State == Submitted && workflow.SubmissionTxHash == transactionHash {
		for _, recorded := range s.retryProofs[workflowID] {
			if recorded.RetryTransactionHash == transactionHash && sameSafeRetryProof(recorded, proof) {
				return cloneWorkflow(workflow), true, nil
			}
		}
		return Workflow{}, false, ErrStateConflict
	}
	if (workflow.State != TimedOut && workflow.State != Reorged) || proof.PreviousTransactionHash != workflow.SubmissionTxHash ||
		proof.VerifiedPayloadHash != workflow.PayloadHash || !validSafeRetryProof(workflowID, transactionHash, proof, now) {
		return Workflow{}, false, ErrStateConflict
	}
	workflow.State, workflow.SubmissionTxHash, workflow.SubmittedAt = Submitted, transactionHash, now.Unix()
	workflow.ConfirmedAt, workflow.TerminalReason, workflow.TerminalAt = 0, "", 0
	s.retryProofs[workflowID] = append(s.retryProofs[workflowID], cloneRetryProof(proof))
	s.workflows[workflowID] = cloneWorkflow(workflow)
	return cloneWorkflow(workflow), false, nil
}

func (s *MemoryStore) Confirm(ctx context.Context, organizationID, workflowID, transactionHash string, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, false, ErrNotFound
	}
	if workflow.State == Confirmed && workflow.SubmissionTxHash == transactionHash {
		return cloneWorkflow(workflow), true, nil
	}
	if workflow.State != Submitted || workflow.SubmissionTxHash != transactionHash {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.SubmittedAt {
		return Workflow{}, false, ErrStateConflict
	}
	workflow.State, workflow.ConfirmedAt = Confirmed, now.Unix()
	s.workflows[workflowID] = cloneWorkflow(workflow)
	return cloneWorkflow(workflow), false, nil
}

func (s *MemoryStore) Complete(ctx context.Context, organizationID, workflowID string, receipt CompletionReceipt, digest string, bytes []byte, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, false, ErrNotFound
	}
	if workflow.State == Finalized && workflow.CompletionDigest == digest {
		return cloneWorkflow(workflow), true, nil
	}
	if !completionCandidateState(workflow.State) || workflow.PayloadHash != receipt.PayloadHash ||
		!s.authorizedReceiptAttempt(workflow, receipt.TransactionHash) {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.ApprovedAt || now.Unix() < workflow.SubmittedAt || now.Unix() < workflow.ConfirmedAt {
		return Workflow{}, false, ErrStateConflict
	}
	receiptKey := fmt.Sprintf("%d\x00%s\x00%d", receipt.ChainID, receipt.TransactionHash, receipt.LogIndex)
	if owner, exists := s.receipts[receiptKey]; exists {
		if owner.workflowID != workflowID {
			return Workflow{}, false, ErrReceiptOwned
		}
		if owner.digest != digest {
			return Workflow{}, false, ErrStateConflict
		}
	}
	// The finalized action event identifies the canonical winning attempt. A
	// replacement may be locally SUBMITTED/CONFIRMED while an earlier proven
	// Safe attempt wins, so the workflow's primary transaction hash must
	// converge to the receipt rather than retain the losing replacement.
	workflow.SubmissionTxHash = receipt.TransactionHash
	if workflow.State == ApprovedPendingChain || chainSideState(workflow.State) {
		workflow.State = Submitted
		workflow.SubmittedAt = now.Unix()
		workflow.ConfirmedAt, workflow.TerminalReason, workflow.TerminalAt = 0, "", 0
	}
	if workflow.State == Submitted {
		workflow.State, workflow.ConfirmedAt = Confirmed, now.Unix()
	}
	workflow.State, workflow.CompletionDigest, workflow.CompletionReceipt, workflow.FinalizedAt = Finalized, digest, append(json.RawMessage(nil), bytes...), now.Unix()
	s.workflows[workflowID] = cloneWorkflow(workflow)
	s.receipts[receiptKey] = memoryReceiptOwner{workflowID: workflowID, digest: digest}
	return cloneWorkflow(workflow), false, nil
}

func (s *MemoryStore) FailChain(ctx context.Context, organizationID, workflowID string, state State, reason TerminalReason, now time.Time) (Workflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return Workflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, exists := s.workflows[workflowID]
	if !exists || workflow.OrganizationID != organizationID {
		return Workflow{}, false, ErrNotFound
	}
	if workflow.State == state && workflow.TerminalReason == reason {
		return cloneWorkflow(workflow), true, nil
	}
	if !validFailureTransition(workflow.State, state) {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.ApprovedAt || now.Unix() < workflow.SubmittedAt || now.Unix() < workflow.ConfirmedAt {
		return Workflow{}, false, ErrStateConflict
	}
	workflow.State, workflow.TerminalReason, workflow.TerminalAt = state, reason, now.Unix()
	s.workflows[workflowID] = cloneWorkflow(workflow)
	return cloneWorkflow(workflow), false, nil
}

func actionScope(org, actor, action, key string) string {
	return org + "\x00" + actor + "\x00" + action + "\x00" + key
}

func cloneWorkflow(workflow Workflow) Workflow {
	workflow.CompletionReceipt = append(json.RawMessage(nil), workflow.CompletionReceipt...)
	workflow.GovernanceAction = append(json.RawMessage(nil), workflow.GovernanceAction...)
	return workflow
}

func cloneRetryProof(proof SafeRetryProof) SafeRetryProof {
	proof.Observers = append([]string(nil), proof.Observers...)
	return proof
}

func (s *MemoryStore) authorizedReceiptAttempt(workflow Workflow, transactionHash string) bool {
	if workflow.SubmissionTxHash == "" {
		return workflow.State == ApprovedPendingChain
	}
	if workflow.SubmissionTxHash == transactionHash {
		return true
	}
	for _, proof := range s.retryProofs[workflow.WorkflowID] {
		if proof.PreviousTransactionHash == transactionHash || proof.RetryTransactionHash == transactionHash {
			return true
		}
	}
	return false
}

func activeChainState(state State) bool {
	return state == ApprovedPendingChain || state == Submitted || state == Confirmed
}

func chainSideState(state State) bool {
	return state == Reverted || state == Reorged || state == TimedOut || state == RequiresReapproval
}

func completionCandidateState(state State) bool {
	return activeChainState(state) || chainSideState(state)
}

func completionCandidate(workflow Workflow) bool {
	return activeChainState(workflow.State) || chainSideState(workflow.State) && workflow.SubmissionTxHash != ""
}

func validFailureTransition(from, to State) bool {
	switch from {
	case ApprovedPendingChain:
		return to == TimedOut || to == RequiresReapproval
	case Submitted:
		return to == Reverted || to == Reorged || to == TimedOut || to == RequiresReapproval
	case Confirmed:
		return to == Reorged || to == RequiresReapproval
	case Reverted, Reorged, TimedOut:
		return to == RequiresReapproval
	default:
		return false
	}
}
