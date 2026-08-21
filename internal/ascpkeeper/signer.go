package ascpkeeper

import (
	"context"
	"errors"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

// ActivatedSignerClient is implemented by the authenticated signer relay
// channel. Returning handle metadata with the bytes lets this side recheck the
// channel response instead of trusting a blob-only RPC.
type ActivatedSignerClient interface {
	Release(context.Context, string, string) (ascpbearer.Handle, []byte, error)
}

type SignerArtifactSource struct {
	client ActivatedSignerClient
	clock  func() time.Time
}

func NewSignerArtifactSource(client ActivatedSignerClient, clocks ...func() time.Time) (*SignerArtifactSource, error) {
	if client == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfig
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &SignerArtifactSource{client: client, clock: clock}, nil
}

func (s *SignerArtifactSource) Release(ctx context.Context, handleID, keeperID string) ([]byte, error) {
	if !opaque(handleID) || !identifier(keeperID) {
		return nil, ErrSignatureUnavailable
	}
	handle, artifact, err := s.client.Release(ctx, handleID, keeperID)
	if err != nil {
		return nil, err
	}
	now := s.clock().UTC()
	if handle.ID != handleID || handle.KeeperID != keeperID || handle.State != ascpbearer.Released ||
		now.Before(handle.ValidAfter) || !now.Before(handle.ValidUntil) || len(artifact) == 0 || len(artifact) > 4096 {
		clear(artifact)
		return nil, errors.Join(ErrSignatureUnavailable, ascpbearer.ErrKeeper)
	}
	return artifact, nil
}
