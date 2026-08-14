package controlapi

import (
	"context"
	"errors"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

var ErrEscrowBinding = errors.New("escrow registration does not match an issued exact authorization")

type EscrowRegistry interface {
	RegisterEscrowIntent(context.Context, reconciliation.EscrowIntent) (reconciliation.EscrowCall, error)
	RegisterEscrowTransition(context.Context, string, string, reconciliation.EscrowTransitionCandidate) (reconciliation.EscrowCall, error)
	EscrowCall(string, string) (reconciliation.EscrowCall, bool)
}

// EscrowRegistrar derives durable intent identity from the control-plane
// journal. Callers cannot substitute tenant, task, provider, amount, or calldata
// terms while registering a transaction hash.
type EscrowRegistrar struct {
	lifecycle *controlplane.Lifecycle
	registry  EscrowRegistry
	clock     func() time.Time
}

func NewEscrowRegistrar(lifecycle *controlplane.Lifecycle, registry EscrowRegistry, clock func() time.Time) (*EscrowRegistrar, error) {
	if lifecycle == nil || registry == nil {
		return nil, errors.New("escrow registrar requires lifecycle and reconciliation registry")
	}
	if clock == nil {
		clock = time.Now
	}
	return &EscrowRegistrar{lifecycle: lifecycle, registry: registry, clock: clock}, nil
}

func (r *EscrowRegistrar) RegisterIntent(ctx context.Context, organizationID, authorizationID string) (reconciliation.EscrowCall, error) {
	record, ok := r.lifecycle.GetByAuthorization(authorizationID)
	if !ok || record.State != controlplane.StateIssued || record.Authorization == nil || record.Intent.OrganizationID != organizationID {
		return reconciliation.EscrowCall{}, ErrNotFound
	}
	authorization := record.Authorization
	if !r.clock().UTC().Before(time.Unix(authorization.ExpiresAt, 0)) {
		return reconciliation.EscrowCall{}, ErrEscrowBinding
	}
	terms := authorization.Escrow
	if authorization.Rail != envelope.RailEscrow || terms == nil || record.Intent.Escrow == nil || *terms != *record.Intent.Escrow {
		return reconciliation.EscrowCall{}, ErrEscrowBinding
	}
	intent := reconciliation.EscrowIntent{
		OrganizationID: authorization.OrganizationID, CustomerID: authorization.CustomerID,
		AgentID: authorization.AgentID, TaskID: authorization.TaskID, AuthorizationID: authorization.AuthorizationID,
		IntentDigest: record.IntentDigest, ChainID: authorization.ChainID, Contract: terms.Contract,
		Asset: authorization.Asset, CallID: terms.CallID, Buyer: terms.Buyer, Provider: terms.Provider,
		AmountAtomic: authorization.AmountAtomic, TaskDigest: terms.TaskDigest, RequestDigest: terms.RequestDigest,
		AcknowledgeBy: terms.AcknowledgeBy, DeliverBy: terms.DeliverBy, ReleaseWindow: terms.ReleaseWindow,
	}
	if err := intent.Validate(); err != nil {
		return reconciliation.EscrowCall{}, ErrEscrowBinding
	}
	return r.registry.RegisterEscrowIntent(ctx, intent)
}

func (r *EscrowRegistrar) RegisterTransition(ctx context.Context, organizationID, callID string, candidate reconciliation.EscrowTransitionCandidate) (reconciliation.EscrowCall, error) {
	if _, ok := r.registry.EscrowCall(organizationID, callID); !ok {
		return reconciliation.EscrowCall{}, ErrNotFound
	}
	return r.registry.RegisterEscrowTransition(ctx, organizationID, callID, candidate)
}

func (r *EscrowRegistrar) Call(organizationID, callID string) (reconciliation.EscrowCall, bool) {
	return r.registry.EscrowCall(organizationID, callID)
}
