package controlapi

import (
	"context"
	"encoding/json"
)

type Store interface {
	Authenticate(ctx context.Context, tokenDigest [32]byte) (Principal, error)
	Agent(ctx context.Context, organizationID, agentID string) (Agent, error)
	ListAgents(ctx context.Context, organizationID string) ([]Agent, error)
	SetAgentStatus(ctx context.Context, organizationID, agentID string, status AgentStatus, actorID, auditID string) (Agent, error)
	BeginCommand(ctx context.Context, command Command) (stored Command, created bool, err error)
	CompleteCommand(ctx context.Context, organizationID, commandID string, state CommandState, result json.RawMessage, errorCode string) (Command, error)
	Command(ctx context.Context, organizationID, commandID string) (Command, error)
}
