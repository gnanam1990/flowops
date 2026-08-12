package referencesigner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type SnapshotSource interface {
	Snapshot(context.Context) reconciliation.SnapshotResult
}

type QuorumChainGateConfig struct {
	ChainID        uint64
	Source         SnapshotSource
	Quorum         int
	MaxHeadSkew    uint64
	StallThreshold time.Duration
	MaxFutureSkew  time.Duration
	Clock          func() time.Time
}

// QuorumChainGate refuses execution unless independent Base observers agree on
// a canonical anchor and report a recent head. It is an immediate halt-safe
// invariant, not the P1 automatic halt/recovery state machine.
type QuorumChainGate struct {
	chainID        uint64
	source         SnapshotSource
	quorum         int
	maxHeadSkew    uint64
	stallThreshold time.Duration
	maxFutureSkew  time.Duration
	clock          func() time.Time
}

func NewQuorumChainGate(cfg QuorumChainGateConfig) (*QuorumChainGate, error) {
	if cfg.ChainID != 8453 && cfg.ChainID != 84532 {
		return nil, errors.New("chain gate supports Base mainnet or Base Sepolia only")
	}
	if cfg.Source == nil || cfg.Quorum < 2 || cfg.Quorum > 5 || cfg.MaxHeadSkew > 32 || cfg.StallThreshold <= 0 || cfg.MaxFutureSkew < 0 {
		return nil, errors.New("chain gate configuration is invalid")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &QuorumChainGate{chainID: cfg.ChainID, source: cfg.Source, quorum: cfg.Quorum, maxHeadSkew: cfg.MaxHeadSkew, stallThreshold: cfg.StallThreshold, maxFutureSkew: cfg.MaxFutureSkew, clock: clock}, nil
}

func (g *QuorumChainGate) CheckChain(ctx context.Context, chainID uint64) error {
	if chainID != g.chainID {
		return errors.New("authorization chain does not match the customer chain gate")
	}
	result := g.source.Snapshot(ctx)
	return g.evaluate(result)
}

func (g *QuorumChainGate) evaluate(result reconciliation.SnapshotResult) error {
	if len(result.Observations) < g.quorum {
		return fmt.Errorf("independent Base observer quorum unavailable: got %d, need %d", len(result.Observations), g.quorum)
	}
	now := g.clock().UTC()
	observations := append([]reconciliation.Observation(nil), result.Observations...)
	sort.Slice(observations, func(i, j int) bool { return observations[i].Provider < observations[j].Provider })
	anchorNumber, anchorHash := observations[0].AnchorNumber, observations[0].AnchorHash
	minHead, maxHead := observations[0].HeadNumber, observations[0].HeadNumber
	providers := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		if observation.Provider == "" || observation.ChainID != g.chainID || observation.AnchorNumber != anchorNumber || observation.AnchorHash != anchorHash || observation.AnchorHash == "" {
			return errors.New("independent Base observers disagree on canonical state")
		}
		if _, exists := providers[observation.Provider]; exists {
			return errors.New("independent Base observer identity is duplicated")
		}
		providers[observation.Provider] = struct{}{}
		if observation.HeadNumber < minHead {
			minHead = observation.HeadNumber
		}
		if observation.HeadNumber > maxHead {
			maxHead = observation.HeadNumber
		}
		if observation.HeadTime.After(now.Add(g.maxFutureSkew)) || now.Sub(observation.HeadTime) > g.stallThreshold {
			return errors.New("Base canonical head is stale or future-dated")
		}
	}
	if maxHead-minHead > g.maxHeadSkew {
		return errors.New("independent Base observer heads exceed the allowed skew")
	}
	return nil
}
