package controlapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/gnanam1990/flowops/internal/controlplane"
)

type AgentFreezeGate struct {
	Store Store
}

func (g AgentFreezeGate) Check(ctx context.Context, organizationID, _ string, agentID string) error {
	if g.Store == nil {
		return errors.New("agent store is unavailable")
	}
	agent, err := g.Store.Agent(ctx, organizationID, agentID)
	if err != nil {
		return fmt.Errorf("read governed agent: %w", err)
	}
	if agent.Status != AgentActive {
		return fmt.Errorf("%w while status is %s", controlplane.ErrFrozen, agent.Status)
	}
	return nil
}

func (g AgentFreezeGate) WithAuthorizationLock(ctx context.Context, organizationID, _ string, agentID string, issue func() error) error {
	if g.Store == nil {
		return errors.New("agent store is unavailable")
	}
	return g.Store.WithActiveAgentLock(ctx, organizationID, agentID, issue)
}
