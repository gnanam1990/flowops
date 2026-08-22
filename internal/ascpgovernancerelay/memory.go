package ascpgovernancerelay

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

type memoryCommand struct {
	outboxID string
	command  ascpworkflow.GovernanceExecutionCommand
	created  time.Time
	consumed bool
}

type authorizationRecord struct {
	inputHash  string
	workflowID string
}

type MemoryStore struct {
	mu             sync.Mutex
	clock          func() time.Time
	commands       []memoryCommand
	jobs           map[string]Job
	authorizations map[string]authorizationRecord
}

func NewMemoryStore(clock func() time.Time) *MemoryStore {
	if clock == nil {
		clock = time.Now
	}
	return &MemoryStore{clock: clock, jobs: map[string]Job{}, authorizations: map[string]authorizationRecord{}}
}

func (s *MemoryStore) EnqueueCommand(ctx context.Context, outboxID string, command ascpworkflow.GovernanceExecutionCommand, created time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !canonicalHash(outboxID) || ascpworkflow.ValidateExecutionCommand(command) != nil || created.IsZero() {
		return ErrInvalidCommand
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, queued := range s.commands {
		if queued.outboxID == outboxID || queued.command.WorkflowID == command.WorkflowID {
			return ErrStateConflict
		}
	}
	s.commands = append(s.commands, memoryCommand{outboxID: outboxID, command: cloneCommand(command), created: created.UTC()})
	sort.Slice(s.commands, func(i, j int) bool {
		if s.commands[i].created.Equal(s.commands[j].created) {
			return s.commands[i].outboxID < s.commands[j].outboxID
		}
		return s.commands[i].created.Before(s.commands[j].created)
	})
	return nil
}

func (s *MemoryStore) ConsumeCommand(ctx context.Context) (Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.commands {
		queued := &s.commands[index]
		if queued.consumed {
			continue
		}
		if existing, ok := s.jobs[queued.command.WorkflowID]; ok {
			queued.consumed = true
			return cloneJob(existing), true, nil
		}
		now := s.clock().UTC()
		job := Job{OutboxID: queued.outboxID, Command: cloneCommand(queued.command), State: StateAwaitingSignatures, CreatedAt: now, UpdatedAt: now}
		s.jobs[job.Command.WorkflowID] = job
		queued.consumed = true
		return cloneJob(job), false, nil
	}
	return Job{}, false, ErrNoWork
}

func (s *MemoryStore) Get(ctx context.Context, organizationID, workflowID string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[workflowID]
	if !ok || job.Command.OrganizationID != organizationID {
		return Job{}, ascpworkflow.ErrNotFound
	}
	return cloneJob(job), nil
}

func (s *MemoryStore) ReplayAuthorization(ctx context.Context, organizationID, workflowID, key, inputHash string) (Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.authorizations[authorizationScope(organizationID, key)]
	if !ok {
		return Job{}, false, nil
	}
	if record.workflowID != workflowID || record.inputHash != inputHash {
		return Job{}, false, ErrIdempotencyConflict
	}
	job, ok := s.jobs[workflowID]
	if !ok || job.Command.OrganizationID != organizationID {
		return Job{}, false, ascpworkflow.ErrNotFound
	}
	return cloneJob(job), true, nil
}

func (s *MemoryStore) Authorize(ctx context.Context, organizationID, workflowID, key, inputHash string, prepared Prepared, handle string, now time.Time) (Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := authorizationScope(organizationID, key)
	if record, ok := s.authorizations[scope]; ok {
		if record.workflowID != workflowID || record.inputHash != inputHash {
			return Job{}, false, ErrIdempotencyConflict
		}
		return cloneJob(s.jobs[workflowID]), true, nil
	}
	job, ok := s.jobs[workflowID]
	if !ok || job.Command.OrganizationID != organizationID {
		return Job{}, false, ascpworkflow.ErrNotFound
	}
	if job.State != StateAwaitingSignatures || prepared.WorkflowID != workflowID || prepared.OrganizationID != organizationID ||
		!identifierPattern.MatchString(handle) || !canonicalHash(inputHash) {
		return Job{}, false, ErrStateConflict
	}
	job.Prepared, job.ArtifactHandle = clonePrepared(prepared), handle
	job.AuthorizationKey, job.AuthorizationHash = key, inputHash
	job.State, job.UpdatedAt = StateReady, now.UTC()
	s.jobs[workflowID] = job
	s.authorizations[scope] = authorizationRecord{inputHash: inputHash, workflowID: workflowID}
	return cloneJob(job), false, nil
}

func (s *MemoryStore) ClaimRelay(ctx context.Context, worker string, duration time.Duration) (Lease, error) {
	return s.claim(ctx, worker, duration, true)
}

func (s *MemoryStore) ClaimObservation(ctx context.Context, worker string, duration time.Duration) (Lease, error) {
	return s.claim(ctx, worker, duration, false)
}

func (s *MemoryStore) claim(ctx context.Context, worker string, duration time.Duration, relay bool) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	if !identifierPattern.MatchString(worker) || duration < time.Second || duration > time.Minute {
		return Lease{}, ErrInvalidCommand
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock().UTC()
	ids := make([]string, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		job := s.jobs[id]
		eligible := relay && (job.State == StateReady || job.State == StateRetryable || job.State == StateBroadcasting) ||
			!relay && (job.State == StateSubmitted || job.State == StatePending)
		if !eligible || !job.LeaseExpiresAt.IsZero() && job.LeaseExpiresAt.After(now) {
			continue
		}
		token, err := memoryToken()
		if err != nil {
			return Lease{}, err
		}
		job.LeaseOwner, job.LeaseToken, job.LeaseExpiresAt = worker, token, now.Add(duration)
		job.UpdatedAt = now
		s.jobs[id] = job
		return Lease{Job: cloneJob(job), Token: token}, nil
	}
	return Lease{}, ErrNoWork
}

func (s *MemoryStore) RecordOuterPrepared(ctx context.Context, lease Lease, outer OuterArtifact, now time.Time) (Job, error) {
	return s.withLease(ctx, lease, now, func(job *Job) error {
		if job.State == StateBroadcasting && job.Outer == outer {
			return nil
		}
		if job.State != StateReady && job.State != StateRetryable || !validOuter(outer, job.Prepared, now) {
			return ErrStateConflict
		}
		job.Outer, job.State = outer, StateBroadcasting
		return nil
	})
}

func (s *MemoryStore) RecordSubmitted(ctx context.Context, lease Lease, transactionHash string, now time.Time) (Job, error) {
	return s.withLease(ctx, lease, now, func(job *Job) error {
		if job.State == StateSubmitted && job.Outer.TransactionHash == transactionHash {
			return nil
		}
		if job.State != StateBroadcasting || job.Outer.TransactionHash != transactionHash {
			return ErrStateConflict
		}
		job.State, job.AttemptCount = StateSubmitted, job.AttemptCount+1
		return nil
	})
}

func (s *MemoryStore) ApplyDecision(ctx context.Context, lease Lease, evidence OutcomeEvidence, decision DecisionResult, now time.Time) (Job, error) {
	return s.withLease(ctx, lease, now, func(job *Job) error {
		retryRefresh := job.State == StateRetryable &&
			(decision.Decision == DecisionRetryExact || decision.Decision == DecisionFinalized)
		if job.State != StateSubmitted && job.State != StatePending && !retryRefresh && decision.Decision != DecisionReapprove {
			return ErrStateConflict
		}
		switch decision.Decision {
		case DecisionWait:
			job.State = StatePending
		case DecisionRetryExact:
			job.State = StateRetryable
		case DecisionReapprove:
			job.State = StateReapprovalRequired
		case DecisionFinalized:
			job.State = StateFinalizedObserved
		default:
			return ErrStateConflict
		}
		if evidence.WorkflowID != "" {
			job.LastOutcome = cloneOutcome(evidence)
		}
		return nil
	})
}

func (s *MemoryStore) ReleaseLease(ctx context.Context, lease Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[lease.Job.Command.WorkflowID]
	if !ok || job.LeaseToken != lease.Token {
		return ErrLeaseLost
	}
	job.LeaseOwner, job.LeaseToken, job.LeaseExpiresAt = "", "", time.Time{}
	job.UpdatedAt = s.clock().UTC()
	s.jobs[job.Command.WorkflowID] = job
	return nil
}

func (s *MemoryStore) withLease(ctx context.Context, lease Lease, now time.Time, mutate func(*Job) error) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[lease.Job.Command.WorkflowID]
	if !ok || job.LeaseToken != lease.Token || job.LeaseExpiresAt.Before(now) {
		return Job{}, ErrLeaseLost
	}
	if err := mutate(&job); err != nil {
		return Job{}, err
	}
	job.UpdatedAt = now.UTC()
	s.jobs[job.Command.WorkflowID] = job
	return cloneJob(job), nil
}

func authorizationScope(organizationID, key string) string { return organizationID + "\x00" + key }

func memoryToken() (string, error) {
	value := make([]byte, 16)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func cloneJob(job Job) Job {
	job.Command = cloneCommand(job.Command)
	job.Prepared = clonePrepared(job.Prepared)
	job.LastOutcome = cloneOutcome(job.LastOutcome)
	return job
}

func cloneCommand(command ascpworkflow.GovernanceExecutionCommand) ascpworkflow.GovernanceExecutionCommand {
	command.GovernanceAction = append([]byte(nil), command.GovernanceAction...)
	return command
}

func clonePrepared(prepared Prepared) Prepared {
	prepared.Transaction.Data = append([]byte(nil), prepared.Transaction.Data...)
	prepared.Owners = append([]string(nil), prepared.Owners...)
	prepared.SnapshotObservers = append([]string(nil), prepared.SnapshotObservers...)
	return prepared
}

func cloneOutcome(evidence OutcomeEvidence) OutcomeEvidence {
	evidence.Observers = append([]string(nil), evidence.Observers...)
	return evidence
}

var _ Store = (*MemoryStore)(nil)
