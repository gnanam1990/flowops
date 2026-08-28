package controlapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

var (
	ErrBroadcastKeyUnknown = errors.New("customer signer receipt key is unknown")
	ErrBroadcastSignature  = errors.New("customer signer receipt signature is invalid")
	ErrBroadcastBinding    = errors.New("broadcast receipt does not match an issued authorization")
	ErrBroadcastTime       = errors.New("broadcast receipt time is outside the authorization window")
	ErrBroadcastRail       = errors.New("authorization rail is not supported by signer broadcast registration")
)

const maxBroadcastFutureSkew = 30 * time.Second

type BroadcastRegistrar interface {
	Register(context.Context, broadcastreceipt.SignedReceipt) (reconciliation.Execution, error)
}

type BroadcastReconciler interface {
	RegisterAttestedBroadcast(context.Context, reconciliation.ExpectedExecution, reconciliation.BroadcastAttestation) (reconciliation.Execution, error)
}

type EscrowBroadcastRegistrar interface {
	Register(context.Context, broadcastreceipt.SignedReceipt) (reconciliation.EscrowCall, error)
}

type EscrowBroadcastReconciler interface {
	RegisterAttestedEscrowBroadcast(context.Context, reconciliation.EscrowIntent, reconciliation.EscrowTransitionCandidate, reconciliation.EscrowBroadcastAttestation) (reconciliation.EscrowCall, error)
}

// BroadcastKey identifies one customer-controlled receipt attestation key.
// FlowOps stores only this public key; the matching private key remains in the
// customer's signer runtime.
type BroadcastKey struct {
	OrganizationID string
	CustomerID     string
	KeyID          string
	PublicKey      ed25519.PublicKey
}

type StaticBroadcastKeys struct {
	keys map[string]ed25519.PublicKey
}

func NewStaticBroadcastKeys(keys []BroadcastKey) (*StaticBroadcastKeys, error) {
	registry := &StaticBroadcastKeys{keys: make(map[string]ed25519.PublicKey, len(keys))}
	for _, key := range keys {
		if !identifierPattern.MatchString(key.OrganizationID) || !identifierPattern.MatchString(key.CustomerID) || !identifierPattern.MatchString(key.KeyID) {
			return nil, errors.New("broadcast key identity is invalid")
		}
		if len(key.PublicKey) != ed25519.PublicKeySize {
			return nil, errors.New("broadcast public key must be Ed25519")
		}
		scoped := broadcastKeyScope(key.OrganizationID, key.CustomerID, key.KeyID)
		if _, exists := registry.keys[scoped]; exists {
			return nil, errors.New("duplicate broadcast key identity")
		}
		registry.keys[scoped] = append(ed25519.PublicKey(nil), key.PublicKey...)
	}
	return registry, nil
}

func (r *StaticBroadcastKeys) Resolve(organizationID, customerID, keyID string) (ed25519.PublicKey, bool) {
	if r == nil {
		return nil, false
	}
	key, ok := r.keys[broadcastKeyScope(organizationID, customerID, keyID)]
	return append(ed25519.PublicKey(nil), key...), ok
}

func broadcastKeyScope(organizationID, customerID, keyID string) string {
	return organizationID + "\x00" + customerID + "\x00" + keyID
}

type SignerBroadcastRegistrar struct {
	lifecycle  *controlplane.Lifecycle
	keys       *StaticBroadcastKeys
	reconciler BroadcastReconciler
	clock      func() time.Time
}

type SignerEscrowBroadcastRegistrar struct {
	lifecycle  *controlplane.Lifecycle
	keys       *StaticBroadcastKeys
	reconciler EscrowBroadcastReconciler
	clock      func() time.Time
}

func NewSignerEscrowBroadcastRegistrar(lifecycle *controlplane.Lifecycle, keys *StaticBroadcastKeys, reconciler EscrowBroadcastReconciler, clock func() time.Time) (*SignerEscrowBroadcastRegistrar, error) {
	if lifecycle == nil || keys == nil || reconciler == nil {
		return nil, errors.New("lifecycle, broadcast keys, and escrow reconciler are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &SignerEscrowBroadcastRegistrar{lifecycle: lifecycle, keys: keys, reconciler: reconciler, clock: clock}, nil
}

func NewSignerBroadcastRegistrar(lifecycle *controlplane.Lifecycle, keys *StaticBroadcastKeys, reconciler BroadcastReconciler, clock func() time.Time) (*SignerBroadcastRegistrar, error) {
	if lifecycle == nil || keys == nil || reconciler == nil {
		return nil, errors.New("lifecycle, broadcast keys, and reconciler are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &SignerBroadcastRegistrar{lifecycle: lifecycle, keys: keys, reconciler: reconciler, clock: clock}, nil
}

func (r *SignerBroadcastRegistrar) Register(ctx context.Context, signed broadcastreceipt.SignedReceipt) (reconciliation.Execution, error) {
	record, authorization, publicKey, err := validateBroadcastBinding(r.lifecycle, r.keys, r.clock, signed)
	if err != nil {
		return reconciliation.Execution{}, err
	}
	receipt := signed.Receipt
	if authorization.Rail != envelope.RailDirect {
		return reconciliation.Execution{}, ErrBroadcastRail
	}
	expected := reconciliation.ExpectedExecution{
		ExecutionID:     controlplane.ExecutionID(authorization.AuthorizationID),
		OrganizationID:  authorization.OrganizationID,
		AgentID:         authorization.AgentID,
		TaskID:          authorization.TaskID,
		IntentDigest:    record.IntentDigest,
		TransactionHash: receipt.TransactionHash,
		ChainID:         authorization.ChainID,
		Sender:          receipt.Sender,
		Asset:           authorization.Asset,
		Recipient:       authorization.Recipient,
		AmountAtomic:    authorization.AmountAtomic,
	}
	attestation := reconciliation.BroadcastAttestation{SignedReceipt: signed, Authorization: authorization, PublicKeyB64: base64.StdEncoding.EncodeToString(publicKey)}
	return r.reconciler.RegisterAttestedBroadcast(ctx, expected, attestation)
}

func (r *SignerEscrowBroadcastRegistrar) Register(ctx context.Context, signed broadcastreceipt.SignedReceipt) (reconciliation.EscrowCall, error) {
	record, authorization, publicKey, err := validateBroadcastBinding(r.lifecycle, r.keys, r.clock, signed)
	if err != nil {
		return reconciliation.EscrowCall{}, err
	}
	terms := authorization.Escrow
	if authorization.Rail != envelope.RailEscrow || terms == nil || signed.Receipt.Sender != terms.Buyer {
		return reconciliation.EscrowCall{}, ErrBroadcastRail
	}
	intent := reconciliation.EscrowIntent{
		OrganizationID: authorization.OrganizationID, CustomerID: authorization.CustomerID, AgentID: authorization.AgentID,
		TaskID: authorization.TaskID, AuthorizationID: authorization.AuthorizationID, IntentDigest: record.IntentDigest,
		ChainID: authorization.ChainID, Contract: terms.Contract, Asset: authorization.Asset, CallID: terms.CallID,
		Buyer: terms.Buyer, Provider: terms.Provider, AmountAtomic: authorization.AmountAtomic, TaskDigest: terms.TaskDigest,
		RequestDigest: terms.RequestDigest, AcknowledgeBy: terms.AcknowledgeBy, DeliverBy: terms.DeliverBy, ReleaseWindow: terms.ReleaseWindow,
	}
	attestation := reconciliation.EscrowBroadcastAttestation{SignedReceipt: signed, Authorization: authorization, PublicKeyB64: base64.StdEncoding.EncodeToString(publicKey)}
	candidate := reconciliation.EscrowTransitionCandidate{Action: reconciliation.EscrowFund, TransactionHash: signed.Receipt.TransactionHash}
	return r.reconciler.RegisterAttestedEscrowBroadcast(ctx, intent, candidate, attestation)
}

func validateBroadcastBinding(lifecycle *controlplane.Lifecycle, keys *StaticBroadcastKeys, clock func() time.Time, signed broadcastreceipt.SignedReceipt) (controlplane.Record, envelope.Authorization, ed25519.PublicKey, error) {
	receipt := signed.Receipt
	publicKey, ok := keys.Resolve(receipt.OrganizationID, receipt.CustomerID, signed.KeyID)
	if !ok {
		return controlplane.Record{}, envelope.Authorization{}, nil, ErrBroadcastKeyUnknown
	}
	if err := broadcastreceipt.Verify(signed, publicKey); err != nil {
		return controlplane.Record{}, envelope.Authorization{}, nil, fmt.Errorf("%w: %v", ErrBroadcastSignature, err)
	}
	record, ok := lifecycle.GetByAuthorization(receipt.AuthorizationID)
	if !ok || record.Authorization == nil {
		return controlplane.Record{}, envelope.Authorization{}, nil, ErrBroadcastBinding
	}
	authorization := *record.Authorization
	digest, err := authorization.Digest()
	if err != nil || receipt.OrganizationID != authorization.OrganizationID || receipt.CustomerID != authorization.CustomerID || receipt.AuthorizationDigest != "0x"+hex.EncodeToString(digest[:]) {
		return controlplane.Record{}, envelope.Authorization{}, nil, ErrBroadcastBinding
	}
	broadcastAt := time.Unix(receipt.BroadcastAt, 0).UTC()
	if broadcastAt.Before(time.Unix(authorization.IssuedAt, 0)) || !broadcastAt.Before(time.Unix(authorization.ExpiresAt, 0)) || broadcastAt.After(clock().UTC().Add(maxBroadcastFutureSkew)) {
		return controlplane.Record{}, envelope.Authorization{}, nil, ErrBroadcastTime
	}
	return record, authorization, publicKey, nil
}
