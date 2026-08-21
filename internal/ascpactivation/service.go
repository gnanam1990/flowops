// Package ascpactivation exposes the authenticated application boundary for
// creating and reading two-phase signer activation requests. Economic scope is
// reconstructed from the durable execution authorization and signer routing is
// reconstructed from the agent's authoritative signer binding.
package ascpactivation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
)

var (
	ErrUnavailable   = errors.New("ASCP signer activation is unavailable")
	ErrStateConflict = errors.New("ASCP signer activation state conflicts with the request")
)

// Request contains the canonical signing input. Request, authorization,
// operation, reservation, and signer-routing identifiers are derived inside
// Service from authenticated durable state.
type Request struct {
	ActionID             string    `json:"actionId"`
	CanonicalPayload     []byte    `json:"canonicalPayload"`
	CanonicalPayloadHash string    `json:"canonicalPayloadHash"`
	EvidenceBundle       []byte    `json:"evidenceBundle"`
	EvidenceBundleHash   string    `json:"evidenceBundleHash"`
	Digest               string    `json:"digest"`
	Nonce                string    `json:"nonce"`
	InstrumentType       string    `json:"instrumentType"`
	ValidAfter           time.Time `json:"validAfter"`
	ValidUntil           time.Time `json:"validUntil"`
}

// Status is the public projection. In particular, the canonical payload,
// evidence bundle, and prepared signer handle never cross this boundary.
type Status struct {
	RequestID            string                     `json:"requestId"`
	AuthorizationID      string                     `json:"authorizationId"`
	OperationID          string                     `json:"operationId"`
	InputHash            string                     `json:"inputHash"`
	Digest               string                     `json:"digest"`
	SignerBindingVersion uint64                     `json:"signerBindingVersion,omitempty"`
	SignerKeyID          string                     `json:"signerKeyId"`
	KeyEpoch             uint64                     `json:"keyEpoch"`
	ModuleAddress        string                     `json:"moduleAddress"`
	SafeAddress          string                     `json:"safeAddress"`
	KeeperID             string                     `json:"keeperId"`
	State                ascpbearer.ActivationState `json:"state"`
	PrimaryMirrorDigest  string                     `json:"primaryMirrorDigest,omitempty"`
	ValidAfter           time.Time                  `json:"validAfter"`
	ValidUntil           time.Time                  `json:"validUntil"`
	CreatedAt            time.Time                  `json:"createdAt"`
	PreparedAt           time.Time                  `json:"preparedAt,omitempty"`
	ActivatedAt          time.Time                  `json:"activatedAt,omitempty"`
	MirroredAt           time.Time                  `json:"mirroredAt,omitempty"`
	AcknowledgedAt       time.Time                  `json:"acknowledgedAt,omitempty"`
	Replayed             bool                       `json:"replayed,omitempty"`
}

type AuthorizationReader interface {
	Authorization(context.Context, ascporchestration.Identity, string) (ascporchestration.Authorization, error)
}

type Store interface {
	Request(context.Context, ascpbearer.ActivationInput) (ascpbearer.ActivationRequest, bool, error)
	ForAuthorization(context.Context, string) (ascpbearer.ActivationRequest, error)
}

type BindingReader interface {
	Current(context.Context, string, string) (ascpsignerbinding.Binding, error)
}

type Config struct {
	Authorizations AuthorizationReader
	Bindings       BindingReader
	Store          Store
	Random         io.Reader
}

type Service struct {
	authorizations AuthorizationReader
	bindings       BindingReader
	store          Store
	random         io.Reader
}

func New(cfg Config) (*Service, error) {
	if cfg.Authorizations == nil || cfg.Bindings == nil || cfg.Store == nil {
		return nil, ErrUnavailable
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{authorizations: cfg.Authorizations, bindings: cfg.Bindings, store: cfg.Store, random: cfg.Random}, nil
}

func (s *Service) Create(ctx context.Context, identity ascporchestration.Identity, operationID string, request Request) (Status, error) {
	authorization, err := s.authorizations.Authorization(ctx, identity, operationID)
	if err != nil {
		return Status{}, err
	}
	if authorization.OperationID != operationID || authorization.State != ascpexecauth.ValidatedAndReserved || authorization.ReservationID == "" {
		return Status{}, ErrStateConflict
	}
	var binding ascpsignerbinding.Binding
	var requestID string
	existing, existingErr := s.store.ForAuthorization(ctx, authorization.AuthorizationID)
	switch {
	case existingErr == nil:
		requestID = existing.RequestID
		binding = ascpsignerbinding.Binding{
			OrganizationID: identity.OrganizationID, AgentID: identity.AgentID,
			Version: existing.SignerBindingVersion, SignerKeyID: existing.SignerKeyID,
			KeyEpoch: existing.KeyEpoch, ModuleAddress: existing.ModuleAddress,
			SafeAddress: existing.SafeAddress, KeeperID: existing.KeeperID,
		}
	case errors.Is(existingErr, ascpbearer.ErrActivationNotFound):
		binding, err = s.bindings.Current(ctx, identity.OrganizationID, identity.AgentID)
		if err != nil {
			return Status{}, err
		}
		requestID, err = s.requestID()
		if err != nil {
			return Status{}, fmt.Errorf("create activation request identifier: %w", err)
		}
	default:
		return Status{}, existingErr
	}
	stored, replayed, err := s.store.Request(ctx, ascpbearer.ActivationInput{
		RequestID: requestID, AuthorizationID: authorization.AuthorizationID,
		OperationID: operationID, ReservationID: authorization.ReservationID,
		ActionID: request.ActionID, CanonicalPayload: append([]byte(nil), request.CanonicalPayload...),
		CanonicalPayloadHash: request.CanonicalPayloadHash,
		EvidenceBundle:       append([]byte(nil), request.EvidenceBundle...), EvidenceBundleHash: request.EvidenceBundleHash,
		Digest: request.Digest, Nonce: request.Nonce, InstrumentType: request.InstrumentType,
		SignerBindingVersion: binding.Version, SignerKeyID: binding.SignerKeyID, KeyEpoch: binding.KeyEpoch, ModuleAddress: binding.ModuleAddress,
		SafeAddress: binding.SafeAddress, KeeperID: binding.KeeperID,
		ValidAfter: request.ValidAfter, ValidUntil: request.ValidUntil,
	})
	if err != nil {
		return Status{}, err
	}
	return project(stored, replayed), nil
}

func (s *Service) Get(ctx context.Context, identity ascporchestration.Identity, operationID string) (Status, error) {
	authorization, err := s.authorizations.Authorization(ctx, identity, operationID)
	if err != nil {
		return Status{}, err
	}
	if authorization.OperationID != operationID || authorization.AuthorizationID == "" {
		return Status{}, ErrStateConflict
	}
	stored, err := s.store.ForAuthorization(ctx, authorization.AuthorizationID)
	if err != nil {
		return Status{}, err
	}
	if stored.OperationID != operationID || stored.AuthorizationID != authorization.AuthorizationID {
		return Status{}, ErrStateConflict
	}
	return project(stored, false), nil
}

func (s *Service) requestID() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", err
	}
	allZero := true
	for _, current := range value {
		allZero = allZero && current == 0
	}
	if allZero {
		return "", errors.New("random source returned an invalid identifier")
	}
	return "0x" + hex.EncodeToString(value), nil
}

func project(request ascpbearer.ActivationRequest, replayed bool) Status {
	return Status{
		RequestID: request.RequestID, AuthorizationID: request.AuthorizationID, OperationID: request.OperationID,
		InputHash: request.InputHash, Digest: request.Digest, SignerBindingVersion: request.SignerBindingVersion, SignerKeyID: request.SignerKeyID,
		KeyEpoch: request.KeyEpoch, ModuleAddress: request.ModuleAddress, SafeAddress: request.SafeAddress,
		KeeperID: request.KeeperID, State: request.State, PrimaryMirrorDigest: request.PrimaryMirrorDigest,
		ValidAfter: request.ValidAfter, ValidUntil: request.ValidUntil, CreatedAt: request.CreatedAt,
		PreparedAt: request.PreparedAt, ActivatedAt: request.ActivatedAt, MirroredAt: request.MirroredAt,
		AcknowledgedAt: request.AcknowledgedAt, Replayed: replayed,
	}
}
