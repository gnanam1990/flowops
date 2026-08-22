package ascpbearer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpleadership"
)

var (
	ErrRuntimeBoundary = errors.New("bearer runtime boundary unavailable")
	ErrRuntimeLease    = errors.New("bearer runtime lease lost")
)

type RuntimeLease struct {
	Request   ActivationRequest
	WorkerID  string
	Token     string
	ExpiresAt time.Time
}

type UnactivatedProof struct {
	RequestID   string    `json:"requestId"`
	OperationID string    `json:"operationId"`
	ActionID    string    `json:"actionId"`
	InputHash   string    `json:"inputHash"`
	HandleID    string    `json:"handleId,omitempty"`
	Status      string    `json:"status"`
	ProvenAt    time.Time `json:"provenAt"`
	ProofDigest string    `json:"proofDigest"`
}

type RuntimeSigner interface {
	PreparedSigner
	ProveUnactivated(context.Context, ActivationRequest) (UnactivatedProof, error)
}

type runtimeRepository interface {
	activationRepository
	Claim(context.Context, RuntimeClaim) (RuntimeLease, bool, error)
	ClaimExpired(context.Context, RuntimeClaim) (RuntimeLease, bool, error)
	CompleteLease(context.Context, RuntimeLease) error
	RetryLease(context.Context, RuntimeLease, string, time.Duration) error
	Refuse(context.Context, RuntimeLease, string) (ActivationRequest, error)
	ExpireUnactivated(context.Context, RuntimeLease, UnactivatedProof) (ActivationRequest, error)
}

type RuntimeClaim struct {
	WorkerID       string
	OrganizationID string
	SignerKeyID    string
	KeyEpoch       uint64
	KeeperID       string
	LeaseDuration  time.Duration
}

type RuntimeConfig struct {
	Claim           RuntimeClaim
	RetryDelay      time.Duration
	OrganizationID  string
	LeadershipEpoch uint64
	Leadership      RuntimeLeadershipGate
}

type RuntimeLeadershipGate interface {
	FenceSink(context.Context, string, uint64, ascpleadership.Sink, func(context.Context) error) error
}

type RuntimeStep struct {
	State   ActivationState `json:"state"`
	Expired bool            `json:"expired"`
	Refused bool            `json:"refused"`
	Retried bool            `json:"retried"`
}

type RuntimeService struct {
	store       runtimeRepository
	coordinator *Coordinator
	signer      RuntimeSigner
	config      RuntimeConfig
}

func NewRuntimeService(store runtimeRepository, signer RuntimeSigner, mirror PrimaryRegistryMirror, config RuntimeConfig) (*RuntimeService, error) {
	if store == nil || signer == nil || mirror == nil || !identifier(config.Claim.WorkerID) ||
		!identifier(config.Claim.SignerKeyID) || config.Claim.KeyEpoch == 0 || !identifier(config.Claim.KeeperID) ||
		config.Claim.LeaseDuration < time.Second || config.Claim.LeaseDuration > time.Minute ||
		config.RetryDelay < time.Second || config.RetryDelay > time.Hour {
		return nil, ErrActivationInput
	}
	leadershipConfigured := config.Leadership != nil || config.OrganizationID != "" || config.LeadershipEpoch != 0
	if leadershipConfigured && (config.Leadership == nil || !identifier(config.OrganizationID) || config.LeadershipEpoch == 0) {
		return nil, ErrActivationInput
	}
	if leadershipConfigured && config.Claim.OrganizationID != config.OrganizationID {
		return nil, ErrActivationInput
	}
	coordinator, err := NewCoordinator(store, signer, mirror)
	if err != nil {
		return nil, err
	}
	return &RuntimeService{store: store, coordinator: coordinator, signer: signer, config: config}, nil
}

func (s *RuntimeService) AdvanceOnce(ctx context.Context) (RuntimeStep, bool, error) {
	lease, ok, err := s.store.Claim(ctx, s.config.Claim)
	if err != nil || !ok {
		return RuntimeStep{}, ok, err
	}
	leasedCtx := withRuntimeLease(ctx, lease)
	var request ActivationRequest
	advance := func(fencedContext context.Context) error {
		var advanceErr error
		request, advanceErr = s.coordinator.Advance(fencedContext, lease.Request.RequestID)
		return advanceErr
	}
	if s.config.Leadership != nil {
		sink := ascpleadership.SinkOutboxDispatch
		if lease.Request.State == SignRequested {
			sink = ascpleadership.SinkSignerIssuance
		}
		err = s.config.Leadership.FenceSink(leasedCtx, s.config.OrganizationID, s.config.LeadershipEpoch, sink, advance)
	} else {
		err = advance(leasedCtx)
	}
	if err != nil {
		if permanentSignerRefusal(err) {
			request, refuseErr := s.store.Refuse(leasedCtx, lease, "SIGNER_REFUSED")
			if refuseErr != nil {
				return RuntimeStep{}, true, errors.Join(err, refuseErr)
			}
			return RuntimeStep{State: request.State, Refused: true}, true, nil
		}
		if errors.Is(err, ErrRuntimeBoundary) {
			if retryErr := s.retryLease(ctx, lease, runtimeErrorCode(err)); retryErr != nil {
				return RuntimeStep{}, true, errors.Join(err, retryErr)
			}
			return RuntimeStep{State: lease.Request.State, Retried: true}, true, nil
		}
		return RuntimeStep{}, true, err
	}
	if err := s.store.CompleteLease(ctx, lease); err != nil {
		return RuntimeStep{}, true, err
	}
	return RuntimeStep{State: request.State}, true, nil
}

func (s *RuntimeService) ExpireOnce(ctx context.Context) (RuntimeStep, bool, error) {
	lease, ok, err := s.store.ClaimExpired(ctx, s.config.Claim)
	if err != nil || !ok {
		return RuntimeStep{}, ok, err
	}
	proof, err := s.signer.ProveUnactivated(ctx, lease.Request)
	if err != nil {
		if errors.Is(err, ErrRuntimeBoundary) {
			if retryErr := s.retryLease(ctx, lease, runtimeErrorCode(err)); retryErr != nil {
				return RuntimeStep{}, true, errors.Join(err, retryErr)
			}
			return RuntimeStep{State: lease.Request.State, Retried: true}, true, nil
		}
		return RuntimeStep{}, true, err
	}
	request, err := s.store.ExpireUnactivated(withRuntimeLease(ctx, lease), lease, proof)
	if err != nil {
		return RuntimeStep{}, true, err
	}
	return RuntimeStep{State: request.State, Expired: true}, true, nil
}

func (s *RuntimeService) retryLease(ctx context.Context, lease RuntimeLease, code string) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return s.store.RetryLease(recoveryCtx, lease, code, s.config.RetryDelay)
}

type RuntimeCycle struct {
	Processed    int `json:"processed"`
	Advanced     int `json:"advanced"`
	Prepared     int `json:"prepared"`
	Activated    int `json:"activated"`
	Mirrored     int `json:"mirrored"`
	Acknowledged int `json:"acknowledged"`
	Expired      int `json:"expired"`
	Retried      int `json:"retried"`
	Refused      int `json:"refused"`
}

type RuntimeWorkerConfig struct {
	Interval           time.Duration
	CycleTimeout       time.Duration
	ExpiryPhaseTimeout time.Duration
	ExpiryBatchSize    int
	AdvanceBatchSize   int
	OnCycle            func(RuntimeCycle)
}

type RuntimeWorker struct {
	service *RuntimeService
	config  RuntimeWorkerConfig
}

func NewRuntimeWorker(service *RuntimeService, config RuntimeWorkerConfig) (*RuntimeWorker, error) {
	if service == nil || config.Interval < time.Second || config.Interval > 5*time.Minute ||
		config.CycleTimeout < time.Second || config.CycleTimeout >= config.Interval ||
		config.ExpiryPhaseTimeout < time.Second || config.ExpiryPhaseTimeout >= config.CycleTimeout ||
		config.ExpiryBatchSize < 1 || config.ExpiryBatchSize > 100 ||
		config.AdvanceBatchSize < 1 || config.AdvanceBatchSize > 100 {
		return nil, ErrActivationInput
	}
	return &RuntimeWorker{service: service, config: config}, nil
}

func (w *RuntimeWorker) RunOnce(ctx context.Context) (RuntimeCycle, error) {
	cycle := RuntimeCycle{}
	expiryCtx, cancelExpiry := context.WithTimeout(ctx, w.config.ExpiryPhaseTimeout)
	defer cancelExpiry()
	for index := 0; index < w.config.ExpiryBatchSize; index++ {
		step, ok, err := w.service.ExpireOnce(expiryCtx)
		if err != nil || !ok {
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					break
				}
				return cycle, err
			}
			break
		}
		countRuntimeStep(&cycle, step)
	}
	cancelExpiry()
	for index := 0; index < w.config.AdvanceBatchSize; index++ {
		step, ok, err := w.service.AdvanceOnce(ctx)
		if err != nil || !ok {
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return cycle, nil
				}
				return cycle, err
			}
			break
		}
		countRuntimeStep(&cycle, step)
	}
	return cycle, nil
}

func (w *RuntimeWorker) Run(ctx context.Context) error {
	if err := w.runCycle(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.runCycle(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func (w *RuntimeWorker) runCycle(ctx context.Context) error {
	cycleCtx, cancel := context.WithTimeout(ctx, w.config.CycleTimeout)
	defer cancel()
	cycle, err := w.RunOnce(cycleCtx)
	if err != nil {
		return err
	}
	if w.config.OnCycle != nil {
		w.config.OnCycle(cycle)
	}
	return nil
}

func countRuntimeStep(cycle *RuntimeCycle, step RuntimeStep) {
	cycle.Processed++
	if step.Retried {
		cycle.Retried++
		return
	}
	if step.Expired {
		cycle.Expired++
		return
	}
	if step.Refused {
		cycle.Refused++
		return
	}
	cycle.Advanced++
	switch step.State {
	case HandlePrepared:
		cycle.Prepared++
	case ActivePendingMirror:
		cycle.Activated++
	case ActiveMirrored:
		cycle.Mirrored++
	case ActivationAcknowledged:
		cycle.Acknowledged++
	}
}

func UnactivatedProofDigest(proof UnactivatedProof) (string, error) {
	proof.ProofDigest = ""
	proof.ProvenAt = proof.ProvenAt.UTC()
	encoded, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("ASCP_UNACTIVATED_PROOF_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:]), nil
}

func runtimeErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "BOUNDARY_TIMEOUT"
	}
	return "BOUNDARY_UNAVAILABLE"
}

func permanentSignerRefusal(err error) bool {
	var boundaryError *RuntimeBoundaryError
	return errors.As(err, &boundaryError) && boundaryError.Boundary == "signer" &&
		boundaryError.Code == "SIGNER_REFUSED" && boundaryError.StatusCode == 422
}

func validateUnactivatedProof(request ActivationRequest, proof UnactivatedProof, now time.Time) error {
	digest, err := UnactivatedProofDigest(proof)
	if err != nil || proof.RequestID != request.RequestID || proof.OperationID != request.OperationID || proof.ActionID != request.ActionID ||
		proof.InputHash != request.InputHash || proof.Status != "EXPIRED_UNACTIVATED" || proof.ProofDigest != digest ||
		proof.ProvenAt.Before(request.ValidUntil) || proof.ProvenAt.After(now.Add(time.Minute)) ||
		request.PreparedHandle != "" && proof.HandleID != request.PreparedHandle ||
		proof.HandleID != "" && !opaqueHandle(proof.HandleID) {
		return fmt.Errorf("%w: unactivated proof is not exact", ErrActivationBinding)
	}
	return nil
}
