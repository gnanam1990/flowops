package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gnanam1990/flowops/internal/controlplane"
)

type PostgresStore struct {
	db           *sql.DB
	siteSessions *SiteSessionCodec
}

func NewPostgresStore(db *sql.DB, siteSessions ...*SiteSessionCodec) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	var codec *SiteSessionCodec
	if len(siteSessions) > 1 {
		return nil, errors.New("at most one site session codec is allowed")
	}
	if len(siteSessions) == 1 {
		codec = siteSessions[0]
	}
	return &PostgresStore{db: db, siteSessions: codec}, nil
}

func (s *PostgresStore) Authenticate(ctx context.Context, token string) (Principal, error) {
	if strings.HasPrefix(token, siteSessionPrefix) && s.siteSessions != nil {
		claims, err := s.siteSessions.Verify(token)
		if err == nil {
			return s.authenticateSiteMembership(ctx, claims)
		}
	}
	tokenDigest := TokenDigest(token)
	var principal Principal
	var kind, role string
	var agentID sql.NullString
	var scopesJSON []byte
	var stepUpUntil sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT c.principal_id, c.organization_id, c.principal_kind, c.role, c.agent_id, c.scopes, c.step_up_until
		FROM credentials c
		LEFT JOIN agents a
		  ON c.organization_id = a.organization_id AND c.agent_id = a.id
		WHERE c.token_digest = $1
		  AND c.revoked_at IS NULL
		  AND c.expires_at > now()
		  AND (c.principal_kind = 'HUMAN' OR a.status NOT IN ('REVOKED', 'ARCHIVED'))`, tokenDigest[:]).Scan(
		&principal.ID, &principal.OrganizationID, &kind, &role, &agentID, &scopesJSON, &stepUpUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate credential: %w", err)
	}
	principal.Kind, principal.Role = PrincipalKind(kind), Role(role)
	if agentID.Valid {
		principal.AgentID = agentID.String
	}
	if stepUpUntil.Valid {
		principal.StepUpUntil = stepUpUntil.Time.UTC()
	}
	if err := json.Unmarshal(scopesJSON, &principal.Scopes); err != nil || !principal.Valid() {
		return Principal{}, errors.New("stored credential claims are invalid")
	}
	return principal, nil
}

func (s *PostgresStore) authenticateSiteMembership(ctx context.Context, claims SiteMembership) (Principal, error) {
	var membership SiteMembership
	var status string
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.site_project_id, m.site_user_key, m.organization_id, m.principal_id, m.role, m.status
		FROM sites_memberships m
		JOIN sites_identity_providers p ON p.site_project_id = m.site_project_id
		WHERE m.id = $1 AND m.site_project_id = $2 AND m.site_user_key = $3
		  AND m.organization_id = $4 AND m.principal_id = $5 AND m.role = $6
		  AND p.enabled = true`,
		claims.ID, claims.SiteProjectID, claims.SiteUserKey, claims.OrganizationID, claims.PrincipalID, claims.Role,
	).Scan(&membership.ID, &membership.SiteProjectID, &membership.SiteUserKey, &membership.OrganizationID, &membership.PrincipalID, &membership.Role, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate site membership: %w", err)
	}
	if status != "ACTIVE" || !membership.Valid() || membership != claims {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{
		ID: membership.PrincipalID, OrganizationID: membership.OrganizationID,
		Kind: PrincipalHuman, Role: membership.Role, ReadOnly: true,
	}, nil
}

func (s *PostgresStore) ExchangeSiteIdentity(ctx context.Context, siteProjectID, siteUserKey, email, exchangeToken string) (SiteMembership, error) {
	if !identifierPattern.MatchString(siteProjectID) || !validDigestHex(siteUserKey) || len(exchangeToken) < 32 || len(exchangeToken) > 512 {
		return SiteMembership{}, ErrUnauthenticated
	}
	emailDigest, err := normalizedEmailDigest(email)
	if err != nil {
		return SiteMembership{}, ErrUnauthenticated
	}
	tokenDigest := TokenDigest(exchangeToken)
	var membership SiteMembership
	err = s.db.QueryRowContext(ctx, `
		SELECT m.id, m.site_project_id, m.site_user_key, m.organization_id, m.principal_id, m.role
		FROM sites_identity_providers p
		JOIN sites_memberships m ON m.site_project_id = p.site_project_id
		WHERE p.site_project_id = $1 AND p.exchange_token_digest = $2 AND p.enabled = true
		  AND m.site_user_key = $3 AND m.email_digest = $4 AND m.status = 'ACTIVE'`,
		siteProjectID, tokenDigest[:], siteUserKey, emailDigest[:],
	).Scan(&membership.ID, &membership.SiteProjectID, &membership.SiteUserKey, &membership.OrganizationID, &membership.PrincipalID, &membership.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return SiteMembership{}, ErrUnauthenticated
	}
	if err != nil {
		return SiteMembership{}, fmt.Errorf("exchange site identity: %w", err)
	}
	if !membership.Valid() {
		return SiteMembership{}, ErrUnauthenticated
	}
	return membership, nil
}

func (s *PostgresStore) Organization(ctx context.Context, organizationID string) (Organization, error) {
	var organization Organization
	err := s.db.QueryRowContext(ctx, `SELECT id, name FROM organizations WHERE id = $1`, organizationID).Scan(&organization.ID, &organization.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, fmt.Errorf("read organization: %w", err)
	}
	if !organization.Valid() {
		return Organization{}, errors.New("stored organization is invalid")
	}
	return organization, nil
}

func (s *PostgresStore) Agent(ctx context.Context, organizationID, agentID string) (Agent, error) {
	var agent Agent
	err := s.db.QueryRowContext(ctx, `
		SELECT organization_id, id, customer_id, name, purpose, status, updated_at
		FROM agents
		WHERE organization_id = $1 AND id = $2`, organizationID, agentID).Scan(
		&agent.OrganizationID, &agent.ID, &agent.CustomerID, &agent.Name, &agent.Purpose, &agent.Status, &agent.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("read agent: %w", err)
	}
	agent.UpdatedAt = agent.UpdatedAt.UTC()
	if !agent.Valid() {
		return Agent{}, errors.New("stored agent is invalid")
	}
	return agent, nil
}

func (s *PostgresStore) ListAgents(ctx context.Context, organizationID string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT organization_id, id, customer_id, name, purpose, status, updated_at
		FROM agents
		WHERE organization_id = $1
		ORDER BY id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	agents := make([]Agent, 0)
	for rows.Next() {
		var agent Agent
		if err := rows.Scan(&agent.OrganizationID, &agent.ID, &agent.CustomerID, &agent.Name, &agent.Purpose, &agent.Status, &agent.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agent.UpdatedAt = agent.UpdatedAt.UTC()
		if !agent.Valid() {
			return nil, errors.New("stored agent is invalid")
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return agents, nil
}

func (s *PostgresStore) SetAgentStatus(ctx context.Context, organizationID, agentID string, status AgentStatus, actorID, auditID string) (Agent, error) {
	if !identifierPattern.MatchString(organizationID) || !identifierPattern.MatchString(agentID) ||
		!identifierPattern.MatchString(actorID) || !identifierPattern.MatchString(auditID) {
		return Agent{}, errors.New("agent status identifiers are invalid")
	}
	if status != AgentPaused {
		return Agent{}, errors.New("this command only supports PAUSED")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Agent{}, fmt.Errorf("begin pause transaction: %w", err)
	}
	defer tx.Rollback()
	var agent Agent
	err = tx.QueryRowContext(ctx, `
		SELECT organization_id, id, customer_id, name, purpose, status, updated_at
		FROM agents
		WHERE organization_id = $1 AND id = $2
		FOR UPDATE`, organizationID, agentID).Scan(
		&agent.OrganizationID, &agent.ID, &agent.CustomerID, &agent.Name, &agent.Purpose, &agent.Status, &agent.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("lock agent: %w", err)
	}
	previous, err := json.Marshal(map[string]AgentStatus{"status": agent.Status})
	if err != nil {
		return Agent{}, err
	}
	if agent.Status != AgentPaused {
		if agent.Status != AgentActive {
			return Agent{}, ErrForbidden
		}
		if err := tx.QueryRowContext(ctx, `
			UPDATE agents
			SET status = $3, updated_at = GREATEST(now(), updated_at)
			WHERE organization_id = $1 AND id = $2
			RETURNING organization_id, id, customer_id, name, purpose, status, updated_at`,
			organizationID, agentID, status,
		).Scan(&agent.OrganizationID, &agent.ID, &agent.CustomerID, &agent.Name, &agent.Purpose, &agent.Status, &agent.UpdatedAt); err != nil {
			return Agent{}, fmt.Errorf("pause agent: %w", err)
		}
	}
	current, err := json.Marshal(map[string]AgentStatus{"status": agent.Status})
	if err != nil {
		return Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (id, organization_id, actor_id, kind, target_id, previous, current)
		VALUES ($1, $2, $3, 'agent.paused', $4, $5, $6)`, auditID, organizationID, actorID, agentID, previous, current); err != nil {
		return Agent{}, fmt.Errorf("record pause audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, fmt.Errorf("commit pause: %w", err)
	}
	agent.UpdatedAt = agent.UpdatedAt.UTC()
	return agent, nil
}

func (s *PostgresStore) WithActiveAgentLock(ctx context.Context, organizationID, agentID string, operation func() error) error {
	if !identifierPattern.MatchString(organizationID) || !identifierPattern.MatchString(agentID) || operation == nil {
		return errors.New("authorization lock inputs are invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin authorization lock: %w", err)
	}
	defer tx.Rollback()
	var status AgentStatus
	err = tx.QueryRowContext(ctx, `
		SELECT status
		FROM agents
		WHERE organization_id = $1 AND id = $2
		FOR UPDATE`, organizationID, agentID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock agent for authorization: %w", err)
	}
	if status != AgentActive {
		return fmt.Errorf("%w while status is %s", controlplane.ErrFrozen, status)
	}
	if err := operation(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit authorization lock: %w", err)
	}
	return nil
}

func (s *PostgresStore) BeginCommand(ctx context.Context, command Command) (Command, bool, error) {
	if err := validateNewCommand(command); err != nil {
		return Command{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Command{}, false, fmt.Errorf("begin command transaction: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO commands
			(id, organization_id, actor_id, kind, target_id, idempotency_key, input_digest, state, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING', $8)
		ON CONFLICT (organization_id, kind, idempotency_key) DO NOTHING`,
		command.ID, command.OrganizationID, command.ActorID, command.Kind, command.TargetID,
		command.IdempotencyKey, command.InputDigest, command.CreatedAt.UTC(),
	)
	if err != nil {
		return Command{}, false, fmt.Errorf("insert command: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Command{}, false, fmt.Errorf("inspect command insert: %w", err)
	}
	stored, err := scanCommand(tx.QueryRowContext(ctx, `
		SELECT id, organization_id, actor_id, kind, target_id, idempotency_key, input_digest,
		       state, result, error_code, created_at, completed_at
		FROM commands
		WHERE organization_id = $1 AND kind = $2 AND idempotency_key = $3`,
		command.OrganizationID, command.Kind, command.IdempotencyKey,
	))
	if err != nil {
		return Command{}, false, fmt.Errorf("read command after insert: %w", err)
	}
	if stored.InputDigest != command.InputDigest {
		return Command{}, false, ErrIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return Command{}, false, fmt.Errorf("commit command: %w", err)
	}
	return stored, inserted == 1, nil
}

func (s *PostgresStore) CompleteCommand(ctx context.Context, organizationID, commandID string, state CommandState, result json.RawMessage, errorCode string) (Command, error) {
	if state != CommandSucceeded && state != CommandFailed {
		return Command{}, errors.New("command completion state is invalid")
	}
	if len(result) > 1024*1024 || len(errorCode) > 128 {
		return Command{}, errors.New("command result is too large")
	}
	if len(result) > 0 && !json.Valid(result) {
		return Command{}, errors.New("command result is invalid JSON")
	}
	var resultValue any
	if len(result) > 0 {
		resultValue = string(result)
	}
	updated, err := scanCommand(s.db.QueryRowContext(ctx, `
		UPDATE commands
		SET state = $3, result = $4, error_code = $5, completed_at = GREATEST(now(), created_at)
		WHERE organization_id = $1 AND id = $2 AND state = 'PENDING'
		RETURNING id, organization_id, actor_id, kind, target_id, idempotency_key, input_digest,
		          state, result, error_code, created_at, completed_at`,
		organizationID, commandID, state, resultValue, errorCode,
	))
	if errors.Is(err, sql.ErrNoRows) {
		existing, readErr := s.Command(ctx, organizationID, commandID)
		if readErr != nil {
			return Command{}, readErr
		}
		return existing, ErrCommandAlreadyClosed
	}
	if err != nil {
		return Command{}, fmt.Errorf("complete command: %w", err)
	}
	return updated, nil
}

func (s *PostgresStore) Command(ctx context.Context, organizationID, commandID string) (Command, error) {
	command, err := scanCommand(s.db.QueryRowContext(ctx, `
		SELECT id, organization_id, actor_id, kind, target_id, idempotency_key, input_digest,
		       state, result, error_code, created_at, completed_at
		FROM commands
		WHERE organization_id = $1 AND id = $2`, organizationID, commandID))
	if errors.Is(err, sql.ErrNoRows) {
		return Command{}, ErrNotFound
	}
	if err != nil {
		return Command{}, fmt.Errorf("read command: %w", err)
	}
	return command, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCommand(row rowScanner) (Command, error) {
	var command Command
	var result []byte
	var completedAt sql.NullTime
	if err := row.Scan(
		&command.ID, &command.OrganizationID, &command.ActorID, &command.Kind, &command.TargetID,
		&command.IdempotencyKey, &command.InputDigest, &command.State, &result, &command.ErrorCode,
		&command.CreatedAt, &completedAt,
	); err != nil {
		return Command{}, err
	}
	if len(result) > 0 {
		command.Result = append(json.RawMessage(nil), result...)
	}
	command.CreatedAt = command.CreatedAt.UTC()
	if completedAt.Valid {
		completed := completedAt.Time.UTC()
		command.CompletedAt = &completed
	}
	return command, nil
}

func validateNewCommand(command Command) error {
	for _, value := range []string{command.ID, command.OrganizationID, command.ActorID, command.Kind, command.IdempotencyKey} {
		if !identifierPattern.MatchString(value) {
			return errors.New("command identifier is invalid")
		}
	}
	if command.TargetID != "" && !identifierPattern.MatchString(command.TargetID) {
		return errors.New("command target is invalid")
	}
	if command.InputDigest == "" || command.CreatedAt.IsZero() || command.State != CommandPending || len(command.Result) != 0 || command.ErrorCode != "" || command.CompletedAt != nil {
		return errors.New("new command fields are invalid")
	}
	return nil
}

var _ Store = (*PostgresStore)(nil)
