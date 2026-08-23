package controlapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gnanam1990/flowops/internal/policy"
)

const agentProvisioningLock = int64(703247667734)

var agentCredentialScopes = []string{"authorizations:issue", "intents:create", "intents:read"}

// AgentBootstrap creates the minimum real control-plane identity required for
// ASCP intake. It is deliberately an offline database-admin operation: hosted
// identity alone cannot create an agent, activate policy, or mint credentials.
type AgentBootstrap struct {
	AuditID               string        `json:"auditId"`
	ActorID               string        `json:"actorId"`
	OrganizationID        string        `json:"organizationId"`
	OwnerMembershipID     string        `json:"ownerMembershipId"`
	AgentID               string        `json:"agentId"`
	CustomerID            string        `json:"customerId"`
	AgentName             string        `json:"agentName"`
	Purpose               string        `json:"purpose"`
	Policy                policy.Config `json:"policy"`
	CredentialID          string        `json:"credentialId"`
	CredentialPrincipalID string        `json:"credentialPrincipalId"`
	CredentialToken       string        `json:"credentialToken"`
	CredentialExpiresAt   time.Time     `json:"credentialExpiresAt"`
}

type AgentBootstrapResult struct {
	AgentCreated      bool `json:"agentCreated"`
	PolicyCreated     bool `json:"policyCreated"`
	CredentialCreated bool `json:"credentialCreated"`
}

func BootstrapAgent(ctx context.Context, db *sql.DB, input AgentBootstrap, now time.Time) (AgentBootstrapResult, error) {
	if db == nil {
		return AgentBootstrapResult{}, errors.New("database is required")
	}
	if err := input.validate(now.UTC()); err != nil {
		return AgentBootstrapResult{}, err
	}
	policyJSON, err := json.Marshal(input.Policy)
	if err != nil {
		return AgentBootstrapResult{}, fmt.Errorf("encode agent policy: %w", err)
	}
	scopesJSON, _ := json.Marshal(agentCredentialScopes)
	tokenDigest := TokenDigest(input.CredentialToken)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return AgentBootstrapResult{}, fmt.Errorf("begin agent bootstrap: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, agentProvisioningLock); err != nil {
		return AgentBootstrapResult{}, fmt.Errorf("lock agent provisioning: %w", err)
	}
	var ownerOrganization, ownerPrincipal, ownerStatus string
	var ownerRole Role
	err = tx.QueryRowContext(ctx, `
		SELECT organization_id,principal_id,role,status
		FROM sites_memberships WHERE id=$1 FOR UPDATE`, input.OwnerMembershipID).Scan(
		&ownerOrganization, &ownerPrincipal, &ownerRole, &ownerStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentBootstrapResult{}, ErrNotFound
	}
	if err != nil {
		return AgentBootstrapResult{}, fmt.Errorf("authorize agent bootstrap: %w", err)
	}
	if ownerOrganization != input.OrganizationID || ownerPrincipal != input.ActorID || ownerRole != RoleOwner || ownerStatus != "ACTIVE" {
		return AgentBootstrapResult{}, ErrForbidden
	}

	result := AgentBootstrapResult{}
	var stored Agent
	err = tx.QueryRowContext(ctx, `
		SELECT organization_id,id,customer_id,name,purpose,status,updated_at
		FROM agents WHERE organization_id=$1 AND id=$2 FOR UPDATE`, input.OrganizationID, input.AgentID).Scan(
		&stored.OrganizationID, &stored.ID, &stored.CustomerID, &stored.Name, &stored.Purpose, &stored.Status, &stored.UpdatedAt)
	wantedAgent := Agent{OrganizationID: input.OrganizationID, ID: input.AgentID, CustomerID: input.CustomerID, Name: input.AgentName, Purpose: input.Purpose, Status: AgentActive}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agents (organization_id,id,customer_id,name,purpose,status)
			VALUES ($1,$2,$3,$4,$5,'ACTIVE')`, input.OrganizationID, input.AgentID, input.CustomerID, input.AgentName, input.Purpose); err != nil {
			return AgentBootstrapResult{}, fmt.Errorf("create agent: %w", err)
		}
		result.AgentCreated = true
	case err != nil:
		return AgentBootstrapResult{}, fmt.Errorf("read provisioned agent: %w", err)
	case stored.OrganizationID != wantedAgent.OrganizationID || stored.ID != wantedAgent.ID || stored.CustomerID != wantedAgent.CustomerID ||
		stored.Name != wantedAgent.Name || stored.Purpose != wantedAgent.Purpose || stored.Status != wantedAgent.Status:
		return AgentBootstrapResult{}, fmt.Errorf("%w: agent", ErrProvisioningConflict)
	}

	var storedPolicy []byte
	var policyActive bool
	err = tx.QueryRowContext(ctx, `
		SELECT config,active FROM policies
		WHERE organization_id=$1 AND agent_id=$2 AND version=$3 FOR UPDATE`,
		input.OrganizationID, input.AgentID, input.Policy.Version).Scan(&storedPolicy, &policyActive)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO policies (organization_id,agent_id,version,config,active,activated_at)
			VALUES ($1,$2,$3,$4,true,$5)`, input.OrganizationID, input.AgentID, input.Policy.Version, policyJSON, now.UTC()); err != nil {
			return AgentBootstrapResult{}, fmt.Errorf("create active agent policy: %w", err)
		}
		result.PolicyCreated = true
	case err != nil:
		return AgentBootstrapResult{}, fmt.Errorf("read provisioned policy: %w", err)
	case !policyActive:
		return AgentBootstrapResult{}, fmt.Errorf("%w: policy", ErrProvisioningConflict)
	default:
		var storedConfig policy.Config
		storedHash, storedErr := func() (string, error) {
			if err := json.Unmarshal(storedPolicy, &storedConfig); err != nil {
				return "", err
			}
			return policy.ConfigHash(storedConfig)
		}()
		wantedHash, _ := policy.ConfigHash(input.Policy)
		if storedErr != nil || storedHash != wantedHash {
			return AgentBootstrapResult{}, fmt.Errorf("%w: policy", ErrProvisioningConflict)
		}
	}

	var storedOrganization, storedPrincipal, storedAgent string
	var storedKind PrincipalKind
	var storedRole Role
	var storedDigest, storedScopes []byte
	var storedExpiry time.Time
	var storedRevokedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT organization_id,principal_id,principal_kind,role,agent_id,token_digest,scopes,expires_at,revoked_at
		FROM credentials WHERE id=$1 FOR UPDATE`, input.CredentialID).Scan(
		&storedOrganization, &storedPrincipal, &storedKind, &storedRole, &storedAgent, &storedDigest, &storedScopes, &storedExpiry, &storedRevokedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO credentials
				(id,organization_id,principal_id,principal_kind,role,agent_id,token_digest,scopes,expires_at)
			VALUES ($1,$2,$3,'AGENT','AGENT',$4,$5,$6,$7)`, input.CredentialID, input.OrganizationID,
			input.CredentialPrincipalID, input.AgentID, tokenDigest[:], scopesJSON, input.CredentialExpiresAt.UTC()); err != nil {
			return AgentBootstrapResult{}, fmt.Errorf("create agent credential: %w", err)
		}
		result.CredentialCreated = true
	case err != nil:
		return AgentBootstrapResult{}, fmt.Errorf("read provisioned credential: %w", err)
	case storedOrganization != input.OrganizationID || storedPrincipal != input.CredentialPrincipalID || storedKind != PrincipalAgent ||
		storedRole != RoleAgent || storedAgent != input.AgentID || !storedExpiry.UTC().Equal(input.CredentialExpiresAt.UTC()) ||
		storedRevokedAt.Valid || len(storedDigest) != len(tokenDigest) || subtle.ConstantTimeCompare(storedDigest, tokenDigest[:]) != 1 || !sameAgentScopes(storedScopes):
		return AgentBootstrapResult{}, fmt.Errorf("%w: credential", ErrProvisioningConflict)
	}

	if result.AgentCreated || result.PolicyCreated || result.CredentialCreated {
		current, _ := json.Marshal(map[string]any{
			"agentId": input.AgentID, "credentialId": input.CredentialID, "policyVersion": input.Policy.Version,
			"status": AgentActive, "credentialExpiresAt": input.CredentialExpiresAt.UTC(),
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (id,organization_id,actor_id,kind,target_id,current)
			VALUES ($1,$2,$3,'agent.bootstrapped',$4,$5)`, input.AuditID, input.OrganizationID, input.ActorID, input.AgentID, current); err != nil {
			return AgentBootstrapResult{}, fmt.Errorf("audit agent bootstrap: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentBootstrapResult{}, fmt.Errorf("commit agent bootstrap: %w", err)
	}
	return result, nil
}

func (input AgentBootstrap) validate(now time.Time) error {
	agent := Agent{OrganizationID: input.OrganizationID, ID: input.AgentID, CustomerID: input.CustomerID, Name: input.AgentName, Purpose: input.Purpose, Status: AgentActive}
	principal := Principal{ID: input.CredentialPrincipalID, OrganizationID: input.OrganizationID, Kind: PrincipalAgent, Role: RoleAgent, AgentID: input.AgentID}
	if !identifierPattern.MatchString(input.AuditID) || !identifierPattern.MatchString(input.ActorID) ||
		!identifierPattern.MatchString(input.OwnerMembershipID) || !agent.Valid() || !principal.Valid() ||
		strings.TrimSpace(input.AgentName) != input.AgentName || !utf8.ValidString(input.AgentName) || utf8.RuneCountInString(input.AgentName) > 200 ||
		!utf8.ValidString(input.Purpose) || len(input.Purpose) > 1024 || !identifierPattern.MatchString(input.CredentialID) ||
		!validAgentCredentialToken(input.CredentialToken) ||
		input.CredentialExpiresAt.IsZero() || !input.CredentialExpiresAt.After(now.Add(5*time.Minute)) || input.CredentialExpiresAt.After(now.Add(366*24*time.Hour)) {
		return errors.New("agent bootstrap input is invalid")
	}
	if !input.CredentialExpiresAt.Equal(input.CredentialExpiresAt.UTC().Truncate(time.Second)) {
		return errors.New("agent bootstrap credential expiry must use canonical whole-second UTC precision")
	}
	if _, err := policy.Compile(input.Policy); err != nil || !input.Policy.Enabled {
		return errors.New("agent bootstrap policy is invalid or disabled")
	}
	if len(input.Policy.AllowedAssets) != 1 {
		return errors.New("agent bootstrap policy must bind one asset so atomic budgets remain comparable")
	}
	return nil
}

func sameAgentScopes(raw []byte) bool {
	var scopes []string
	if json.Unmarshal(raw, &scopes) != nil || len(scopes) != len(agentCredentialScopes) {
		return false
	}
	for index := range scopes {
		if scopes[index] != agentCredentialScopes[index] {
			return false
		}
	}
	return true
}

func validAgentCredentialToken(value string) bool {
	if len(value) < 24 || len(value) > 512 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}
