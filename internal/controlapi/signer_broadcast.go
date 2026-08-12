package controlapi

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
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
	receipt := signed.Receipt
	publicKey, ok := r.keys.Resolve(receipt.OrganizationID, receipt.CustomerID, signed.KeyID)
	if !ok {
		return reconciliation.Execution{}, ErrBroadcastKeyUnknown
	}
	if err := broadcastreceipt.Verify(signed, publicKey); err != nil {
		return reconciliation.Execution{}, fmt.Errorf("%w: %v", ErrBroadcastSignature, err)
	}
	record, ok := r.lifecycle.GetByAuthorization(receipt.AuthorizationID)
	if !ok || record.Authorization == nil {
		return reconciliation.Execution{}, ErrBroadcastBinding
	}
	authorization := *record.Authorization
	digest, err := authorization.Digest()
	if err != nil {
		return reconciliation.Execution{}, fmt.Errorf("digest issued authorization: %w", err)
	}
	canonicalDigest := "0x" + hex.EncodeToString(digest[:])
	if receipt.OrganizationID != authorization.OrganizationID || receipt.CustomerID != authorization.CustomerID || receipt.AuthorizationDigest != canonicalDigest {
		return reconciliation.Execution{}, ErrBroadcastBinding
	}
	if authorization.Rail != envelope.RailDirect {
		return reconciliation.Execution{}, ErrBroadcastRail
	}
	broadcastAt := time.Unix(receipt.BroadcastAt, 0).UTC()
	if broadcastAt.Before(time.Unix(authorization.IssuedAt, 0)) || !broadcastAt.Before(time.Unix(authorization.ExpiresAt, 0)) || broadcastAt.After(r.clock().UTC().Add(maxBroadcastFutureSkew)) {
		return reconciliation.Execution{}, ErrBroadcastTime
	}
	expected := reconciliation.ExpectedExecution{
		ExecutionID:     executionID(authorization.AuthorizationID),
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

func executionID(authorizationID string) string {
	digest := sha256.Sum256([]byte("flowops:execution:v1\n" + authorizationID))
	return "exec_" + hex.EncodeToString(digest[:])
}
