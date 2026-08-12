package controlapi

import (
	"context"
	"errors"
	"fmt"
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
		return fmt.Errorf("agent execution is blocked while status is %s", agent.Status)
	}
	return nil
}
