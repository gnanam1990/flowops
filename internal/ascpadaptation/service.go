package ascpadaptation

import (
	"context"
	"errors"
)

type Service struct {
	issuer *Issuer
	store  Store
}

func NewService(issuer *Issuer, store Store) (*Service, error) {
	if issuer == nil || store == nil {
		return nil, errors.New("adaptation issuer and durable store are required")
	}
	return &Service{issuer: issuer, store: store}, nil
}

func (s *Service) Issue(ctx context.Context, request IssueRequest) (Record, error) {
	requestHash, err := CanonicalIssueRequestHash(request)
	if err != nil {
		return Record{}, err
	}
	existing, err := s.store.GetByOriginalIntent(ctx, request.OrganizationID, request.AgentID, request.OriginalIntentID)
	if err == nil {
		if existing.CanonicalRequestHash != requestHash || existing.ReasonClass != request.ReasonClass {
			return Record{}, ErrIssueConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	if !errors.Is(err, ErrGrantNotFound) {
		return Record{}, err
	}
	artifact, err := s.issuer.Issue(ctx, request)
	if err != nil {
		return Record{}, err
	}
	digest, err := DigestHex(artifact.Grant)
	if err != nil {
		return Record{}, err
	}
	record, replayed, err := s.store.Issue(ctx, Record{Artifact: artifact, ReasonClass: request.ReasonClass, Digest: digest, CanonicalRequestHash: requestHash})
	if err != nil {
		return Record{}, err
	}
	record.Replayed = replayed
	return record, nil
}

func (s *Service) Get(ctx context.Context, organizationID, agentID, grantID string) (Record, error) {
	return s.store.GetGrant(ctx, organizationID, agentID, grantID)
}
