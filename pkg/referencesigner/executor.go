package referencesigner

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/pilotlimits"
)

type AuthorizationVerifier interface {
	Authorize(context.Context, envelope.SignedAuthorization) (Authorized, error)
	CheckExecution(context.Context, envelope.SignedAuthorization) error
}

// WalletAdapter runs entirely inside the customer boundary. Prepare must
// return one fully signed transaction without submitting it. Broadcast must
// submit exactly the supplied bytes and must not construct a replacement.
type WalletAdapter interface {
	Prepare(context.Context, Authorized) (PreparedTransaction, error)
	Broadcast(context.Context, PreparedTransaction) error
}

// RegistrationSink delivers a signed receipt to FlowOps. It is safe to call
// repeatedly; it must never invoke a wallet or interpret an error as authority
// to rebroadcast.
type RegistrationSink interface {
	Register(context.Context, broadcastreceipt.SignedReceipt) error
}

type ExecutorConfig struct {
	Verifier          AuthorizationVerifier
	Wallet            WalletAdapter
	Registration      RegistrationSink
	Journal           *AttemptJournal
	ReceiptKeyID      string
	ReceiptPrivateKey ed25519.PrivateKey
	Clock             func() time.Time
	PilotLimits       *pilotlimits.Limits
}

type Executor struct {
	mu                sync.Mutex
	verifier          AuthorizationVerifier
	wallet            WalletAdapter
	registration      RegistrationSink
	journal           *AttemptJournal
	receiptKeyID      string
	receiptPrivateKey ed25519.PrivateKey
	receiptPublicKey  ed25519.PublicKey
	clock             func() time.Time
	pilotLimits       *pilotlimits.Limits
}

func NewExecutor(cfg ExecutorConfig) (*Executor, error) {
	if cfg.Verifier == nil || cfg.Wallet == nil || cfg.Registration == nil || cfg.Journal == nil || cfg.PilotLimits == nil {
		return nil, errors.New("verifier, wallet, registration sink, attempt journal, and pilot limits are required")
	}
	if !envelope.ValidIdentifier(cfg.ReceiptKeyID) || len(cfg.ReceiptPrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("receipt attestation identity is invalid")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	privateKey := append(ed25519.PrivateKey(nil), cfg.ReceiptPrivateKey...)
	canonical := ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(privateKey, canonical) != 1 {
		return nil, errors.New("receipt attestation private key is not canonical")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("derive receipt attestation public key")
	}
	return &Executor{
		verifier: cfg.Verifier, wallet: cfg.Wallet, registration: cfg.Registration, journal: cfg.Journal,
		receiptKeyID: cfg.ReceiptKeyID, receiptPrivateKey: privateKey,
		receiptPublicKey: append(ed25519.PublicKey(nil), publicKey...), clock: clock,
		pilotLimits: cfg.PilotLimits,
	}, nil
}

// Execute admits a new authorization or resumes its durable attempt. It is
// intentionally idempotent: once BROADCASTING is durable, no call path can
// invoke WalletAdapter.Broadcast for that authorization again.
func (e *Executor) Execute(ctx context.Context, signed envelope.SignedAuthorization) (Attempt, error) {
	if signed.Authorization.Rail != envelope.RailDirect {
		return Attempt{}, ErrUnsupportedRail
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.journal.Get(signed.Authorization.AuthorizationID); ok {
		if existing.Authorization != signed {
			return Attempt{}, ErrAttemptConflict
		}
		return e.advance(ctx, existing)
	}
	if err := e.pilotLimits.Check(signed.Authorization.AmountAtomic, e.pilotOutstanding()); err != nil {
		return Attempt{}, err
	}
	authorized, err := e.verifier.Authorize(ctx, signed)
	if err != nil {
		return Attempt{}, err
	}
	prepared, err := e.wallet.Prepare(ctx, authorized)
	if err != nil {
		// The nonce is deliberately burned. Prepare is forbidden from network
		// submission, so no ambiguous payment exists to reconcile.
		return Attempt{}, fmt.Errorf("prepare customer transaction: %w", err)
	}
	prepared = clonePrepared(prepared)
	if err := prepared.validate(); err != nil {
		return Attempt{}, fmt.Errorf("%w: %v", ErrPreparedTransaction, err)
	}
	now := unixNow(e.clock)
	attempt := Attempt{
		Authorization: signed, Authorized: authorized, Prepared: prepared,
		State: AttemptPrepared, PreparedAt: now,
	}
	attempt, err = e.journal.Append(ctx, e.clock().UTC(), attempt)
	if err != nil {
		return Attempt{}, err
	}
	return e.advance(ctx, attempt)
}

// pilotOutstanding is deliberately conservative: every durable prepared
// attempt remains reserved until a future independently verified settlement
// release protocol exists. Restart therefore cannot silently reset exposure.
func (e *Executor) pilotOutstanding() string {
	total := new(big.Int)
	for _, attempt := range e.journal.Attempts() {
		amount, ok := new(big.Int).SetString(attempt.Authorized.Authorization.AmountAtomic, 10)
		if ok {
			total.Add(total, amount)
		}
	}
	return total.String()
}

// ResumePending recovers every durable non-terminal attempt in authorization
// order. PREPARED may safely cross into its first broadcast. BROADCASTING is
// converted to AMBIGUOUS without touching the wallet. Later states retry only
// receipt registration.
func (e *Executor) ResumePending(ctx context.Context) ([]Attempt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	attempts := e.journal.Attempts()
	results := make([]Attempt, 0, len(attempts))
	var failures []error
	for _, attempt := range attempts {
		if attempt.State == AttemptRegistered {
			continue
		}
		result, err := e.advance(ctx, attempt)
		results = append(results, result)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return results, errors.Join(failures...)
}

func (e *Executor) advance(ctx context.Context, attempt Attempt) (Attempt, error) {
	switch attempt.State {
	case AttemptPrepared:
		if err := e.verifier.CheckExecution(ctx, attempt.Authorization); err != nil {
			return attempt, err
		}
		attempt.State = AttemptBroadcasting
		attempt.BroadcastAt = unixNow(e.clock)
		persisted, err := e.journal.Append(ctx, e.clock().UTC(), attempt)
		if err != nil {
			return Attempt{}, err
		}
		attempt = persisted
		broadcastErr := e.wallet.Broadcast(ctx, clonePrepared(attempt.Prepared))
		outcome := broadcastreceipt.OutcomeSubmitted
		state := AttemptSubmitted
		if broadcastErr != nil {
			outcome = broadcastreceipt.OutcomeAmbiguous
			state = AttemptAmbiguous
		}
		return e.persistOutcomeAndRegister(ctx, attempt, state, outcome)
	case AttemptBroadcasting:
		// The process may have died at any point after the durable transition.
		// Even if the adapter was never entered, availability is sacrificed to
		// preserve the at-most-once payment invariant.
		return e.persistOutcomeAndRegister(ctx, attempt, AttemptAmbiguous, broadcastreceipt.OutcomeAmbiguous)
	case AttemptSubmitted, AttemptAmbiguous:
		return e.register(ctx, attempt)
	case AttemptRegistered:
		if attempt.Receipt != nil && attempt.Receipt.Receipt.Outcome == broadcastreceipt.OutcomeAmbiguous {
			return attempt, ErrBroadcastAmbiguous
		}
		return attempt, nil
	default:
		return Attempt{}, errors.New("durable signer attempt has an unsupported state")
	}
}

func (e *Executor) persistOutcomeAndRegister(ctx context.Context, attempt Attempt, state AttemptState, outcome broadcastreceipt.Outcome) (Attempt, error) {
	receipt, err := broadcastreceipt.Sign(broadcastreceipt.Receipt{
		Version:             broadcastreceipt.Version,
		OrganizationID:      attempt.Authorized.Authorization.OrganizationID,
		CustomerID:          attempt.Authorized.Authorization.CustomerID,
		AuthorizationID:     attempt.Authorized.Authorization.AuthorizationID,
		AuthorizationDigest: attempt.Authorized.Digest,
		TransactionHash:     attempt.Prepared.TransactionHash,
		Sender:              attempt.Prepared.Sender,
		Outcome:             outcome,
		BroadcastAt:         attempt.BroadcastAt,
	}, e.receiptKeyID, e.receiptPrivateKey)
	if err != nil {
		return attempt, err
	}
	attempt.State = state
	attempt.Receipt = &receipt
	attempt.ReceiptPublicKeyB64 = base64.StdEncoding.EncodeToString(e.receiptPublicKey)
	// Cancellation after the wallet boundary cannot prevent the ambiguous
	// outcome from becoming durable. Registration still honors caller cancel.
	durableContext := context.WithoutCancel(ctx)
	persisted, err := e.journal.Append(durableContext, e.clock().UTC(), attempt)
	if err != nil {
		return Attempt{}, err
	}
	return e.register(ctx, persisted)
}

func (e *Executor) register(ctx context.Context, attempt Attempt) (Attempt, error) {
	if attempt.Receipt == nil {
		return Attempt{}, errors.New("durable attempt has no receipt to register")
	}
	if err := e.registration.Register(ctx, *attempt.Receipt); err != nil {
		return attempt, ErrRegistrationPending
	}
	registered := cloneAttempt(attempt)
	registered.State = AttemptRegistered
	registered.RegisteredAt = unixNow(e.clock)
	persisted, err := e.journal.Append(context.WithoutCancel(ctx), e.clock().UTC(), registered)
	if err != nil {
		// The server may have committed. A future retry is receipt-only and the
		// FlowOps registration boundary is idempotent.
		return attempt, ErrRegistrationPending
	}
	if persisted.Receipt.Receipt.Outcome == broadcastreceipt.OutcomeAmbiguous {
		return persisted, ErrBroadcastAmbiguous
	}
	return persisted, nil
}
