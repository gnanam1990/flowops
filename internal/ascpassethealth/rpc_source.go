package ascpassethealth

import (
	"context"
	"errors"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type RPCSource struct {
	observers *reconciliation.ObserverSet
	buyer     string
	escrow    string
	clock     func() time.Time
}

func NewRPCSource(observers *reconciliation.ObserverSet, buyer, escrow string, clock func() time.Time) (*RPCSource, error) {
	if observers == nil || !canonicalAddress(buyer) || !canonicalAddress(escrow) || buyer == escrow {
		return nil, errors.New("asset-health RPC source configuration is invalid")
	}
	if clock == nil {
		clock = time.Now
	}
	return &RPCSource{observers: observers, buyer: buyer, escrow: escrow, clock: clock}, nil
}

func (s *RPCSource) Observe(ctx context.Context, config Config) ([]Observation, map[string]string) {
	result := s.observers.AssetHealthQuorum(ctx, reconciliation.AssetHealthRequest{Asset: config.Asset, Buyer: s.buyer, Escrow: s.escrow})
	now := s.clock().UTC()
	observations := make([]Observation, 0, len(result.Evidence))
	for _, evidence := range result.Evidence {
		observations = append(observations, Observation{
			Provider: evidence.Provider, ChainID: evidence.ChainID, Asset: evidence.Asset,
			ProxyImplementation: evidence.ProxyImplementation, RuntimeCodeHash: evidence.RuntimeCodeHash,
			Paused: evidence.Paused, BuyerBlacklisted: evidence.BuyerBlacklisted,
			EscrowBlacklisted: evidence.EscrowBlacklisted, TransferFailure: evidence.TransferFailure,
			FinalizedBlock: evidence.FinalizedBlock, FinalizedBlockHash: evidence.FinalizedBlockHash, ObservedAt: now,
		})
	}
	return observations, result.Failures
}

type Monitor struct {
	source  *RPCSource
	service *Service
}

func NewMonitor(source *RPCSource, service *Service) (*Monitor, error) {
	if source == nil || service == nil {
		return nil, errors.New("asset-health source and service are required")
	}
	return &Monitor{source: source, service: service}, nil
}

func (m *Monitor) RunOnce(ctx context.Context) (Record, map[string]string, error) {
	observations, failures := m.source.Observe(ctx, m.service.config)
	record, err := m.service.Observe(ctx, observations)
	return record, failures, err
}
