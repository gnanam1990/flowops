package ascpworkflow

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type MemoryStore struct {
	mu        sync.Mutex
	workflows map[string]Workflow
	actions   map[string]memoryAction
}

type memoryAction struct {
	hash       string
	workflowID string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{workflows: make(map[string]Workflow), actions: make(map[string]memoryAction)}
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
			workflow.State = Approved
			if workflowRequiresChainReceipt(workflow) {
				workflow.State = ApprovedPendingChain
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
	if workflow.State == Approved && workflow.CompletionDigest == digest {
		return cloneWorkflow(workflow), true, nil
	}
	if workflow.State != ApprovedPendingChain || workflow.PayloadHash != receipt.PayloadHash {
		return Workflow{}, false, ErrStateConflict
	}
	workflow.State, workflow.CompletionDigest, workflow.CompletionReceipt, workflow.CompletedAt = Approved, digest, append(json.RawMessage(nil), bytes...), now.Unix()
	s.workflows[workflowID] = cloneWorkflow(workflow)
	return cloneWorkflow(workflow), false, nil
}

func actionScope(org, actor, action, key string) string {
	return org + "\x00" + actor + "\x00" + action + "\x00" + key
}

func cloneWorkflow(workflow Workflow) Workflow {
	workflow.CompletionReceipt = append(json.RawMessage(nil), workflow.CompletionReceipt...)
	return workflow
}
