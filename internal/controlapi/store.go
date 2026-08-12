package controlapi

import (
	"context"
	"encoding/json"
)

type Store interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
	ExchangeSiteIdentity(ctx context.Context, siteProjectID, siteUserKey, email, exchangeToken string) (SiteMembership, error)
	Organization(ctx context.Context, organizationID string) (Organization, error)
	Agent(ctx context.Context, organizationID, agentID string) (Agent, error)
	ListAgents(ctx context.Context, organizationID string) ([]Agent, error)
	SetAgentStatus(ctx context.Context, organizationID, agentID string, status AgentStatus, actorID, auditID string) (Agent, error)
	WithActiveAgentLock(ctx context.Context, organizationID, agentID string, operation func() error) error
	BeginCommand(ctx context.Context, command Command) (stored Command, created bool, err error)
	CompleteCommand(ctx context.Context, organizationID, commandID string, state CommandState, result json.RawMessage, errorCode string) (Command, error)
	Command(ctx context.Context, organizationID, commandID string) (Command, error)
}
