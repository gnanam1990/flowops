package controlapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

func TestBootstrapAgentCreatesActivePolicyAndDigestOnlyCredentialAtomically(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	input := agentBootstrapFixture(now)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(agentProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT organization_id,principal_id,role,status`).WithArgs(input.OwnerMembershipID).WillReturnRows(
		sqlmock.NewRows([]string{"organization_id", "principal_id", "role", "status"}).AddRow(input.OrganizationID, input.ActorID, RoleOwner, "ACTIVE"))
	mock.ExpectQuery(`SELECT organization_id,id,customer_id,name,purpose,status,updated_at`).WithArgs(input.OrganizationID, input.AgentID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO agents`).WithArgs(input.OrganizationID, input.AgentID, input.CustomerID, input.AgentName, input.Purpose).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT config,active FROM policies`).WithArgs(input.OrganizationID, input.AgentID, input.Policy.Version).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO policies`).WithArgs(input.OrganizationID, input.AgentID, input.Policy.Version, sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT organization_id,principal_id,principal_kind,role,agent_id,token_digest,scopes,expires_at,revoked_at`).WithArgs(input.CredentialID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO credentials`).WithArgs(input.CredentialID, input.OrganizationID, input.CredentialPrincipalID,
		input.AgentID, sqlmock.AnyArg(), sqlmock.AnyArg(), input.CredentialExpiresAt).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO audit_events`).WithArgs(input.AuditID, input.OrganizationID, input.ActorID, input.AgentID,
		jsonWithoutSecret{secret: input.CredentialToken}).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := BootstrapAgent(context.Background(), db, input, now)
	if err != nil || !result.AgentCreated || !result.PolicyCreated || !result.CredentialCreated {
		t.Fatalf("agent bootstrap = %+v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAgentRequiresExactActiveOwner(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	input := agentBootstrapFixture(now)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(agentProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT organization_id,principal_id,role,status`).WithArgs(input.OwnerMembershipID).WillReturnRows(
		sqlmock.NewRows([]string{"organization_id", "principal_id", "role", "status"}).AddRow(input.OrganizationID, "substituted_owner", RoleOwner, "ACTIVE"))
	mock.ExpectRollback()
	if _, err := BootstrapAgent(context.Background(), db, input, now); err != ErrForbidden {
		t.Fatalf("substituted owner error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAgentRefusesToReviveRevokedCredentialByReplay(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	input := agentBootstrapFixture(now)
	policyJSON, _ := json.Marshal(input.Policy)
	tokenDigest := TokenDigest(input.CredentialToken)
	scopesJSON, _ := json.Marshal(agentCredentialScopes)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(agentProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT organization_id,principal_id,role,status`).WithArgs(input.OwnerMembershipID).WillReturnRows(
		sqlmock.NewRows([]string{"organization_id", "principal_id", "role", "status"}).AddRow(input.OrganizationID, input.ActorID, RoleOwner, "ACTIVE"))
	mock.ExpectQuery(`SELECT organization_id,id,customer_id,name,purpose,status,updated_at`).WithArgs(input.OrganizationID, input.AgentID).WillReturnRows(
		sqlmock.NewRows([]string{"organization_id", "id", "customer_id", "name", "purpose", "status", "updated_at"}).AddRow(
			input.OrganizationID, input.AgentID, input.CustomerID, input.AgentName, input.Purpose, AgentActive, now))
	mock.ExpectQuery(`SELECT config,active FROM policies`).WithArgs(input.OrganizationID, input.AgentID, input.Policy.Version).WillReturnRows(
		sqlmock.NewRows([]string{"config", "active"}).AddRow(policyJSON, true))
	mock.ExpectQuery(`SELECT organization_id,principal_id,principal_kind,role,agent_id,token_digest,scopes,expires_at,revoked_at`).WithArgs(input.CredentialID).WillReturnRows(
		sqlmock.NewRows([]string{"organization_id", "principal_id", "principal_kind", "role", "agent_id", "token_digest", "scopes", "expires_at", "revoked_at"}).AddRow(
			input.OrganizationID, input.CredentialPrincipalID, PrincipalAgent, RoleAgent, input.AgentID, tokenDigest[:], scopesJSON, input.CredentialExpiresAt, now))
	mock.ExpectRollback()
	if _, err := BootstrapAgent(context.Background(), db, input, now); !errors.Is(err, ErrProvisioningConflict) {
		t.Fatalf("revoked credential replay error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAgentRejectsDisabledPolicyAndUnsafeCredential(t *testing.T) {
	now := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*AgentBootstrap){
		"disabled policy": func(input *AgentBootstrap) { input.Policy.Enabled = false },
		"multi asset budget": func(input *AgentBootstrap) {
			input.Policy.AllowedAssets = append(input.Policy.AllowedAssets, "0x3333333333333333333333333333333333333333")
		},
		"short token":   func(input *AgentBootstrap) { input.CredentialToken = "short" },
		"expired token": func(input *AgentBootstrap) { input.CredentialExpiresAt = now.Add(-time.Minute) },
		"subsecond expiry": func(input *AgentBootstrap) {
			input.CredentialExpiresAt = input.CredentialExpiresAt.Add(time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, _, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			input := agentBootstrapFixture(now)
			mutate(&input)
			if _, err := BootstrapAgent(context.Background(), db, input, now); err == nil {
				t.Fatal("unsafe agent bootstrap accepted")
			}
		})
	}
}

type jsonWithoutSecret struct{ secret string }

func (match jsonWithoutSecret) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok || !json.Valid(raw) {
		return false
	}
	return !strings.Contains(string(raw), match.secret) && !strings.Contains(string(raw), "tokenDigest")
}

func agentBootstrapFixture(now time.Time) AgentBootstrap {
	asset := "0x1111111111111111111111111111111111111111"
	recipient := "0x2222222222222222222222222222222222222222"
	return AgentBootstrap{
		AuditID: "audit_agent_bootstrap", ActorID: "owner_flowops", OrganizationID: "org_flowops", OwnerMembershipID: "membership_flowops_owner",
		AgentID: "agent_research", CustomerID: "customer_flowops", AgentName: "Research Agent", Purpose: "Acquire verified research evidence",
		Policy: policy.Config{Version: "policy_research_1", Enabled: true, AllowedChainIDs: []uint64{84532}, AllowedRails: []envelope.Rail{envelope.RailEscrow},
			AllowedAssets: []string{asset}, AllowedRecipients: []string{recipient}, ApprovalRequiredRails: []envelope.Rail{envelope.RailEscrow},
			PerActionLimitAtomic: "100", AutoApproveThresholdAtomic: "10", TaskBudgetAtomic: "500", DailyBudgetAtomic: "1000"},
		CredentialID: "cred_agent_research", CredentialPrincipalID: "principal_agent_research",
		CredentialToken: "fo_agent_research_0123456789abcdef0123456789", CredentialExpiresAt: now.Add(24 * time.Hour),
	}
}
