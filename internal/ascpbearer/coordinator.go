package ascpbearer

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// PreparedSigner must make the signature durable in its own anti-replay ledger
// before Prepare returns. AcknowledgeActivation must be idempotent and must not
// release bytes anywhere except its authenticated keeper channel.
type PreparedSigner interface {
	Prepare(context.Context, ActivationInput) (opaqueHandle string, err error)
	AcknowledgeActivation(context.Context, ActivationProof) error
}

// IndependentSigningEngine belongs inside the isolated signer boundary. It
// receives the exact canonical payload and evidence bytes and must rerun all
// signer-side policy, directory, quote, and chain-evidence verification before
// returning a signature. A hash-only signing implementation is not compatible
// with this interface.
type IndependentSigningEngine interface {
	VerifyAndSign(context.Context, ActivationInput) ([]byte, error)
}

// LedgerPreparedSigner is the production-safe adapter between the coordinator
// and the signer ledger. The verified signature is durably encrypted before an
// opaque handle crosses back into the control plane.
type LedgerPreparedSigner struct {
	ledger      *SignerStore
	engine      IndependentSigningEngine
	actionsMu   sync.Mutex
	actionLocks map[string]*actionLock
}

type actionLock struct {
	mu   sync.Mutex
	refs int
}

func NewLedgerPreparedSigner(ledger *SignerStore, engine IndependentSigningEngine) (*LedgerPreparedSigner, error) {
	if ledger == nil || engine == nil {
		return nil, errors.New("signer ledger and independent signing engine are required")
	}
	return &LedgerPreparedSigner{ledger: ledger, engine: engine, actionLocks: map[string]*actionLock{}}, nil
}

func (s *LedgerPreparedSigner) Prepare(ctx context.Context, input ActivationInput) (string, error) {
	input.CanonicalPayload = append([]byte(nil), input.CanonicalPayload...)
	input.EvidenceBundle = append([]byte(nil), input.EvidenceBundle...)
	input.ValidAfter, input.ValidUntil = input.ValidAfter.UTC(), input.ValidUntil.UTC()
	if err := validateActivationInput(input, s.ledger.clock().UTC()); err != nil {
		return "", err
	}
	unlock := s.lockAction(input.OperationID, input.ActionID)
	defer unlock()
	inputHash, err := activationInputHash(input)
	if err != nil {
		return "", err
	}
	if handle, exists, err := s.ledger.PreparedFor(input.OperationID, input.ActionID, inputHash); err != nil {
		return "", err
	} else if exists {
		return handle.ID, nil
	}
	signature, err := s.engine.VerifyAndSign(ctx, input)
	if err != nil {
		return "", fmt.Errorf("independently verify and sign activation payload: %w", err)
	}
	handle, err := s.ledger.Prepare(ctx, PrepareInput{
		RequestID: input.RequestID, AuthorizationID: input.AuthorizationID, ReservationID: input.ReservationID,
		ActionID: input.ActionID, OperationID: input.OperationID,
		SignerRequestHash: inputHash, CanonicalPayloadHash: input.CanonicalPayloadHash,
		Digest: input.Digest, Nonce: input.Nonce,
		SignerKeyID: input.SignerKeyID, KeyEpoch: input.KeyEpoch, KeeperID: input.KeeperID,
		ValidAfter: input.ValidAfter, ValidUntil: input.ValidUntil, Signature: signature,
	})
	clear(signature)
	if err != nil {
		return "", err
	}
	return handle.ID, nil
}

func (s *LedgerPreparedSigner) lockAction(operationID, actionID string) func() {
	s.actionsMu.Lock()
	key := signerActionKey(operationID, actionID)
	lock := s.actionLocks[key]
	if lock == nil {
		lock = &actionLock{}
		s.actionLocks[key] = lock
	}
	lock.refs++
	s.actionsMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.actionsMu.Lock()
		lock.refs--
		if lock.refs == 0 && s.actionLocks[key] == lock {
			delete(s.actionLocks, key)
		}
		s.actionsMu.Unlock()
	}
}

func (s *LedgerPreparedSigner) AcknowledgeActivation(ctx context.Context, proof ActivationProof) error {
	_, err := s.ledger.Activate(ctx, proof.HandleID, proof)
	return err
}

// PrimaryRegistryMirror performs a create-if-absent write. An existing object
// at objectKey is success only when its bytes and digest are exact.
type PrimaryRegistryMirror interface {
	PutPrimary(context.Context, string, []byte, string) error
}

type activationRepository interface {
	Get(context.Context, string) (ActivationRequest, error)
	RecordPrepared(context.Context, string, string) (ActivationRequest, error)
	Activate(context.Context, string) (RegistryEntry, error)
	Registry(context.Context, string) (RegistryEntry, error)
	MarkPrimaryMirrored(context.Context, string, string) (ActivationRequest, error)
	MarkAcknowledged(context.Context, string, string) (ActivationRequest, error)
}

// Coordinator advances exactly one durable/external boundary per call. A
// crash at any return point is recovered by invoking Advance again.
type Coordinator struct {
	store  activationRepository
	signer PreparedSigner
	mirror PrimaryRegistryMirror
}

func NewCoordinator(store activationRepository, signer PreparedSigner, mirror PrimaryRegistryMirror) (*Coordinator, error) {
	if store == nil || signer == nil || mirror == nil {
		return nil, errors.New("activation store, prepared signer, and primary registry mirror are required")
	}
	return &Coordinator{store: store, signer: signer, mirror: mirror}, nil
}

func (c *Coordinator) Advance(ctx context.Context, requestID string) (ActivationRequest, error) {
	request, err := c.store.Get(ctx, requestID)
	if err != nil {
		return ActivationRequest{}, err
	}
	switch request.State {
	case SignRequested:
		handle, err := c.signer.Prepare(ctx, request.ActivationInput)
		if err != nil {
			return request, fmt.Errorf("prepare signer artifact: %w", err)
		}
		return c.store.RecordPrepared(ctx, requestID, handle)
	case HandlePrepared:
		if _, err := c.store.Activate(ctx, requestID); err != nil {
			return request, err
		}
		return c.store.Get(ctx, requestID)
	case ActivePendingMirror:
		entry, err := c.store.Registry(ctx, request.Digest)
		if err != nil {
			return request, err
		}
		payload, err := RegistryMirrorBytes(entry)
		if err != nil {
			return request, err
		}
		digest, err := RegistryMirrorDigest(entry)
		if err != nil {
			return request, err
		}
		if err := c.mirror.PutPrimary(ctx, "bearer-registry/"+entry.Digest+".json", payload, digest); err != nil {
			return request, fmt.Errorf("mirror bearer registry primary: %w", err)
		}
		return c.store.MarkPrimaryMirrored(ctx, requestID, digest)
	case ActiveMirrored:
		proof := ActivationProof{
			RequestID: request.RequestID, HandleID: request.PreparedHandle, OperationID: request.OperationID,
			Digest: request.Digest, Nonce: request.Nonce, PrimaryMirrorDigest: request.PrimaryMirrorDigest,
			ActivationOccurredAt: request.ActivatedAt,
		}
		if err := c.signer.AcknowledgeActivation(ctx, proof); err != nil {
			return request, fmt.Errorf("acknowledge signer activation: %w", err)
		}
		return c.store.MarkAcknowledged(ctx, requestID, request.PreparedHandle)
	case ActivationAcknowledged:
		return request, nil
	default:
		return request, ErrActivationState
	}
}
