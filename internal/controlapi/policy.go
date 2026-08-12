package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/policy"
)

type PostgresPolicyProvider struct {
	db *sql.DB
}

func NewPostgresPolicyProvider(db *sql.DB) (*PostgresPolicyProvider, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresPolicyProvider{db: db}, nil
}

func (p *PostgresPolicyProvider) Evaluate(ctx context.Context, intent controlplane.PaymentIntent, spend policy.SpendSnapshot) (policy.Decision, error) {
	config, err := p.activeConfig(ctx, intent.OrganizationID, intent.AgentID)
	if err != nil {
		return policy.Decision{}, err
	}
	engine, err := policy.Compile(config)
	if err != nil {
		return policy.Decision{}, fmt.Errorf("stored active policy is invalid: %w", err)
	}
	return engine.Evaluate(policy.Intent{
		OrganizationID: intent.OrganizationID, CustomerID: intent.CustomerID, AgentID: intent.AgentID,
		TaskID: intent.TaskID, ActionID: intent.ActionID, Rail: intent.Rail, ChainID: intent.ChainID,
		Recipient: intent.Recipient, Asset: intent.Asset, AmountAtomic: intent.AmountAtomic,
		Resource: intent.Resource, Category: intent.Category,
	}, spend), nil
}

func (p *PostgresPolicyProvider) ActiveVersion(ctx context.Context, organizationID, agentID string) (string, error) {
	var version string
	if err := p.db.QueryRowContext(ctx, `
		SELECT version
		FROM policies
		WHERE organization_id = $1 AND agent_id = $2 AND active = true`, organizationID, agentID).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return "", controlplane.ErrPolicyUnavailable
	} else if err != nil {
		return "", fmt.Errorf("read active policy version: %w", err)
	}
	return version, nil
}

func (p *PostgresPolicyProvider) activeConfig(ctx context.Context, organizationID, agentID string) (policy.Config, error) {
	var version string
	var raw []byte
	err := p.db.QueryRowContext(ctx, `
		SELECT version, config
		FROM policies
		WHERE organization_id = $1 AND agent_id = $2 AND active = true`, organizationID, agentID).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Config{}, controlplane.ErrPolicyUnavailable
	}
	if err != nil {
		return policy.Config{}, fmt.Errorf("read active policy: %w", err)
	}
	var config policy.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return policy.Config{}, fmt.Errorf("decode active policy: %w", err)
	}
	if config.Version != version {
		return policy.Config{}, errors.New("active policy row and document versions differ")
	}
	return config, nil
}

var _ controlplane.PolicyProvider = (*PostgresPolicyProvider)(nil)
