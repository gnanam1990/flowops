package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresIntegrationMigrationAuthCommandPauseAndJournal(t *testing.T) {
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	adminDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_it_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer adminDB.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminDB.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
	})
	pgxConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	pgxConfig.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*pgxConfig)
	defer db.Close()
	db.SetMaxOpenConns(10)
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("migrations are not idempotent: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id, name) VALUES ('org_integration', 'Integration')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agents (organization_id, id, customer_id, name, purpose, status)
		VALUES ('org_integration', 'agent_integration', 'customer_integration', 'Integration Agent', 'Persistence proof', 'ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	policyConfig := policy.Config{
		Version: "policy_integration_1", Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailX402}, AllowedAssets: []string{testUSDC},
		AllowedRecipients: []string{testRecipient}, PerActionLimitAtomic: "200",
		AutoApproveThresholdAtomic: "100", TaskBudgetAtomic: "500", DailyBudgetAtomic: "1000",
	}
	policyJSON, _ := json.Marshal(policyConfig)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO policies (organization_id, agent_id, version, config, active, activated_at)
		VALUES ('org_integration', 'agent_integration', $1, $2, true, now())`, policyConfig.Version, string(policyJSON)); err != nil {
		t.Fatal(err)
	}
	token := "fo_sbx_integration_000000000000000000000001"
	digest := TokenDigest(token)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO credentials
			(id, organization_id, principal_id, principal_kind, role, agent_id, token_digest, scopes, expires_at)
		VALUES ('cred_integration', 'org_integration', 'principal_integration', 'AGENT', 'AGENT',
		        'agent_integration', $1, '["intents:create"]'::jsonb, now() + interval '1 hour')`, digest[:]); err != nil {
		t.Fatal(err)
	}
	siteSessions, err := NewSiteSessionCodec([]byte(strings.Repeat("s", 32)), 2*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(db, siteSessions)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.Authenticate(ctx, token)
	if err != nil || principal.OrganizationID != "org_integration" || principal.AgentID != "agent_integration" {
		t.Fatalf("authenticated principal = %+v, %v", principal, err)
	}
	siteProjectID := "appgprj_integration"
	siteUserKey, err := SiteUserKey(siteProjectID, "opaque_integration_user")
	if err != nil {
		t.Fatal(err)
	}
	exchangeToken := "flowops_sites_exchange_integration_0000000001"
	bootstrap, err := BootstrapSiteOwner(ctx, db, SiteOwnerBootstrap{
		AuditID: "audit_sites_bootstrap_integration", ActorID: "owner_integration",
		OrganizationID: "org_integration", OrganizationName: "Integration",
		SiteProjectID: siteProjectID, SiteUserKey: siteUserKey, Email: "owner@example.com",
		PrincipalID: "owner_integration", MembershipID: "membership_integration", ExchangeToken: exchangeToken,
	})
	if err != nil || bootstrap.OrganizationCreated || !bootstrap.ProviderCreated || !bootstrap.MembershipCreated {
		t.Fatalf("Sites owner bootstrap = %+v, %v", bootstrap, err)
	}
	idempotent, err := BootstrapSiteOwner(ctx, db, SiteOwnerBootstrap{
		AuditID: "audit_sites_bootstrap_replay", ActorID: "owner_integration",
		OrganizationID: "org_integration", OrganizationName: "Integration",
		SiteProjectID: siteProjectID, SiteUserKey: siteUserKey, Email: "OWNER@example.com",
		PrincipalID: "owner_integration", MembershipID: "membership_integration", ExchangeToken: exchangeToken,
	})
	if err != nil || idempotent != (SiteOwnerBootstrapResult{}) {
		t.Fatalf("idempotent Sites owner bootstrap = %+v, %v", idempotent, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id, name) VALUES ('org_sites_other', 'Other')`); err != nil {
		t.Fatal(err)
	}
	otherUserKey, err := SiteUserKey(siteProjectID, "opaque_other_user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapSiteOwner(ctx, db, SiteOwnerBootstrap{
		AuditID: "audit_sites_cross_org", ActorID: "owner_sites_other",
		OrganizationID: "org_sites_other", OrganizationName: "Other",
		SiteProjectID: siteProjectID, SiteUserKey: otherUserKey, Email: "other@example.com",
		PrincipalID: "owner_sites_other", MembershipID: "membership_sites_other", ExchangeToken: exchangeToken,
	}); !errors.Is(err, ErrProvisioningConflict) {
		t.Fatalf("cross-organization Sites project reuse error = %v", err)
	}
	rotatedExchangeToken := "flowops_sites_exchange_rotated_0000000000000001"
	rotated, err := RotateSiteExchangeToken(ctx, db, SiteExchangeTokenRotation{
		AuditID: "audit_sites_rotate_integration", ActorID: "owner_integration", OrganizationID: "org_integration",
		SiteProjectID: siteProjectID, MembershipID: "membership_integration", ExchangeToken: rotatedExchangeToken,
	})
	if err != nil || !rotated {
		t.Fatalf("Sites exchange-token rotation = %v, %v", rotated, err)
	}
	if _, err := store.ExchangeSiteIdentity(ctx, siteProjectID, siteUserKey, "owner@example.com", exchangeToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old Sites exchange token authenticated after rotation: %v", err)
	}
	exchangeToken = rotatedExchangeToken
	membership, err := store.ExchangeSiteIdentity(ctx, siteProjectID, siteUserKey, "Owner@Example.com", exchangeToken)
	if err != nil || membership.OrganizationID != "org_integration" || membership.Role != RoleOwner {
		t.Fatalf("site membership = %+v, %v", membership, err)
	}
	siteToken, _, err := siteSessions.Mint(membership)
	if err != nil {
		t.Fatal(err)
	}
	sitePrincipal, err := store.Authenticate(ctx, siteToken)
	if err != nil || sitePrincipal.OrganizationID != "org_integration" || !sitePrincipal.StepUpUntil.IsZero() || !sitePrincipal.ReadOnly || sitePrincipal.Can(PermissionCreateIntent) {
		t.Fatalf("site principal = %+v, %v", sitePrincipal, err)
	}
	disabled, err := DisableSiteIdentityProvider(ctx, db, SiteProviderDisable{
		AuditID: "audit_sites_disable_integration", ActorID: "owner_integration", OrganizationID: "org_integration",
		SiteProjectID: siteProjectID, MembershipID: "membership_integration",
	})
	if err != nil || !disabled {
		t.Fatalf("Sites provider disable = %v, %v", disabled, err)
	}
	if _, err := store.Authenticate(ctx, siteToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled Sites provider accepted an issued session: %v", err)
	}
	if _, err := store.ExchangeSiteIdentity(ctx, siteProjectID, siteUserKey, "owner@example.com", exchangeToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled Sites provider exchanged a new session: %v", err)
	}
	provider, err := NewPostgresPolicyProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := provider.Evaluate(ctx, controlplane.PaymentIntent{
		IntentID: "intent_policy", OrganizationID: "org_integration", CustomerID: "customer_integration", AgentID: "agent_integration",
		TaskID: "task_integration", ActionID: "action_integration", Rail: envelope.RailX402, ChainID: 84532,
		Recipient: testRecipient, Asset: testUSDC, AmountAtomic: "150", Resource: "https://example.test", Category: "research", Purpose: "integration",
	}, policy.SpendSnapshot{TaskSpentAtomic: "0", TaskReservedAtomic: "0", DailySpentAtomic: "0", DailyReservedAtomic: "0"})
	if err != nil || decision.Outcome != policy.RequireApproval || decision.PolicyVersion != policyConfig.Version {
		t.Fatalf("policy decision = %+v, %v", decision, err)
	}

	command := Command{
		ID: "cmd_integration", OrganizationID: "org_integration", ActorID: principal.ID,
		Kind: "intent.create", TargetID: "intent_integration", IdempotencyKey: "intent_integration",
		InputDigest: "0xintegration", State: CommandPending, CreatedAt: now,
	}
	stored, created, err := store.BeginCommand(ctx, command)
	if err != nil || !created || stored.ID != command.ID {
		t.Fatalf("begin command = %+v, %v, %v", stored, created, err)
	}
	result := json.RawMessage(`{"requestId":"req_integration"}`)
	completed, err := store.CompleteCommand(ctx, command.OrganizationID, command.ID, CommandSucceeded, result, "")
	var wantResult, gotResult map[string]any
	_ = json.Unmarshal(result, &wantResult)
	_ = json.Unmarshal(completed.Result, &gotResult)
	if err != nil || gotResult["requestId"] != wantResult["requestId"] || completed.CompletedAt == nil || completed.CompletedAt.Before(completed.CreatedAt) {
		t.Fatalf("complete command = %+v, %v", completed, err)
	}
	replayed, created, err := store.BeginCommand(ctx, command)
	if err != nil || created || replayed.ID != completed.ID || replayed.State != CommandSucceeded {
		t.Fatalf("replay command = %+v, %v, %v", replayed, created, err)
	}

	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- store.WithActiveAgentLock(ctx, "org_integration", "agent_integration", func() error {
			close(lockHeld)
			<-releaseLock
			return nil
		})
	}()
	<-lockHeld
	pauseDone := make(chan struct {
		agent Agent
		err   error
	}, 1)
	go func() {
		agent, err := store.SetAgentStatus(ctx, "org_integration", "agent_integration", AgentPaused, "owner_integration", "audit_integration")
		pauseDone <- struct {
			agent Agent
			err   error
		}{agent, err}
	}()
	select {
	case result := <-pauseDone:
		t.Fatalf("pause crossed an in-flight authorization lock: %+v, %v", result.agent, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseLock)
	if err := <-lockDone; err != nil {
		t.Fatalf("authorization lock: %v", err)
	}
	pauseResult := <-pauseDone
	paused, err := pauseResult.agent, pauseResult.err
	if err != nil || paused.Status != AgentPaused {
		t.Fatalf("pause = %+v, %v", paused, err)
	}
	freeze := AgentFreezeGate{Store: store}
	if err := freeze.Check(ctx, "org_integration", "task_integration", "agent_integration"); err == nil {
		t.Fatal("PostgreSQL pause did not reach the authorization freeze gate")
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET status = 'REVOKED' WHERE organization_id = 'org_integration' AND id = 'agent_integration'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("credential for revoked agent authenticated: %v", err)
	}

	journal, err := controlplane.OpenPostgresJournal(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	event, err := journal.Append(ctx, now, "integration.proof", "req_integration", map[string]any{
		"commandId": command.ID, "nested": map[string]any{"amount": 1, "accepted": true},
	})
	if err != nil || event.Sequence != 1 || event.Hash == "" {
		t.Fatalf("journal event = %+v, %v", event, err)
	}
	reopened, err := controlplane.OpenPostgresJournal(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if events := reopened.Events(); len(events) != 1 || events[0].Hash != event.Hash {
		t.Fatalf("replayed events = %+v", events)
	}
	if _, err := db.ExecContext(ctx, `UPDATE audit_events SET kind = 'tampered' WHERE id = 'audit_integration'`); err == nil {
		t.Fatal("append-only audit event accepted an update")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM control_events WHERE sequence = 1`); err == nil {
		t.Fatal("hash-chained control event accepted a delete")
	}
	if _, err := db.ExecContext(ctx, `UPDATE flowops_schema_migrations SET checksum = 'tampered' WHERE name = '0001_control_plane.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err == nil {
		t.Fatal("modified applied migration checksum was accepted")
	}
}
