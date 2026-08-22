package controlapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpactivation"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpagent"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
	"github.com/gnanam1990/flowops/internal/directoryreader"
	"github.com/gnanam1990/flowops/internal/mcp"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const crossSurfaceDirectoryAddress = "0x4444444444444444444444444444444444444444"

func TestASCPCreateIsOneOperationAcrossConcurrentRESTMCPResponseLossAndRestart(t *testing.T) {
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	request, evidence := crossSurfaceCreateRequest(t, now)
	store := newCrossSurfaceIntakeStore()
	resolver := &crossSurfaceDirectory{evidence: evidence}
	agent := newCrossSurfaceAgent(t, store, resolver, now)
	rest, _, _, _, journal, _ := setupHandler(t)
	rest.ascpAgent = agent
	defer journal.Close()
	mcpServer, err := mcp.NewServer(mcp.Config{Delegate: rest})
	if err != nil {
		t.Fatal(err)
	}

	const attempts = 32
	type outcome struct {
		operationID string
		err         error
	}
	outcomes := make(chan outcome, attempts)
	var wait sync.WaitGroup
	for index := range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var operationID string
			var err error
			if index%2 == 0 {
				operationID, err = callASCPCreateREST(rest, request, "cross_surface_once")
			} else {
				operationID, err = callASCPCreateMCP(mcpServer, request, "cross_surface_once", index+1)
			}
			// Every seventh response is deliberately discarded after the server
			// completed it. Subsequent retries must not depend on delivery.
			if index%7 == 0 && err == nil {
				operationID = ""
			}
			outcomes <- outcome{operationID: operationID, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)

	var operationID string
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("concurrent cross-surface create: %v", result.err)
		}
		if result.operationID == "" {
			continue
		}
		if operationID == "" {
			operationID = result.operationID
		} else if result.operationID != operationID {
			t.Fatalf("cross-surface operation IDs disagree: %s != %s", result.operationID, operationID)
		}
	}
	created, ids := store.snapshot()
	if operationID == "" || created != 1 || len(ids) != 1 || ids[0] != operationID {
		t.Fatalf("operation=%s created=%d ids=%v", operationID, created, ids)
	}

	// Reconstruct the intake and HTTP/MCP servers around the same durable
	// store. The directory is now unavailable, proving replay is read before
	// current external evidence and survives process replacement.
	restartedResolver := &crossSurfaceDirectory{evidence: evidence, err: errors.New("directory unavailable after restart")}
	restartedAgent := newCrossSurfaceAgent(t, store, restartedResolver, now.Add(24*time.Hour))
	restartedREST, _, _, _, restartedJournal, _ := setupHandler(t)
	restartedREST.ascpAgent = restartedAgent
	defer restartedJournal.Close()
	restartedMCP, err := mcp.NewServer(mcp.Config{Delegate: restartedREST})
	if err != nil {
		t.Fatal(err)
	}
	restReplay, err := callASCPCreateREST(restartedREST, request, "cross_surface_once")
	if err != nil || restReplay != operationID {
		t.Fatalf("REST restart replay=%s err=%v", restReplay, err)
	}
	mcpReplay, err := callASCPCreateMCP(restartedMCP, request, "cross_surface_once", 100)
	if err != nil || mcpReplay != operationID {
		t.Fatalf("MCP restart replay=%s err=%v", mcpReplay, err)
	}
	if restartedResolver.calls.Load() != 0 {
		t.Fatalf("restart replay re-read mutable directory %d times", restartedResolver.calls.Load())
	}

	restartedResolver.err = nil
	status, body, err := callASCPCreateRESTResponse(restartedREST, request, "cross_surface_new_effect")
	if err != nil {
		t.Fatal(err)
	}
	code := nestedErrorCode(body)
	if status != http.StatusConflict || code != "QUOTE_NONCE_CONSUMED" {
		t.Fatalf("new effect reused quote nonce: status=%d code=%s body=%+v", status, code, body)
	}
	created, ids = store.snapshot()
	if created != 1 || len(ids) != 1 || ids[0] != operationID {
		t.Fatalf("restart changed durable effect count=%d ids=%v", created, ids)
	}
}

func TestASCPDecisionReservationAuthorizationAreOneAcrossRESTMCPAndRestart(t *testing.T) {
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	operationID := fmt.Sprintf("0x%064x", 9901)
	flowStore := newCrossSurfaceFlowStore(operationID, now)
	authorizer := &crossSurfaceAuthorizer{store: flowStore}
	service := newCrossSurfaceFlow(t, flowStore, authorizer, now)
	rest, _, _, _, journal, _ := setupHandler(t)
	rest.ascpFlow = service
	defer journal.Close()
	mcpServer, err := mcp.NewServer(mcp.Config{Delegate: rest})
	if err != nil {
		t.Fatal(err)
	}

	decisionIDs := raceFlowSurface(t, rest, mcpServer, operationID, "evaluate", 24)
	if len(decisionIDs) != 1 || flowStore.decisionCreates.Load() != 1 {
		t.Fatalf("decisions=%v creates=%d", decisionIDs, flowStore.decisionCreates.Load())
	}
	authorizationIDs := raceFlowSurface(t, rest, mcpServer, operationID, "authorize", 24)
	if len(authorizationIDs) != 1 || authorizer.reservations.Load() != 1 {
		t.Fatalf("authorizations=%v reservations=%d", authorizationIDs, authorizer.reservations.Load())
	}
	authorization := flowStore.authorizationSnapshot()
	if authorization.AuthorizationID == "" || authorization.ReservationID == "" || authorization.State != ascpexecauth.ValidatedAndReserved {
		t.Fatalf("durable authorization=%+v", authorization)
	}

	// Recreate the application service and both transport boundaries around
	// the same durable stores. Exact replay must perform no second reservation
	// or downstream economic effect.
	restartedService := newCrossSurfaceFlow(t, flowStore, authorizer, now.Add(time.Minute))
	restartedREST, _, _, _, restartedJournal, _ := setupHandler(t)
	restartedREST.ascpFlow = restartedService
	defer restartedJournal.Close()
	restartedMCP, err := mcp.NewServer(mcp.Config{Delegate: restartedREST})
	if err != nil {
		t.Fatal(err)
	}
	replayedDecision, err := callFlowREST(restartedREST, operationID, "evaluate")
	if err != nil || replayedDecision != onlyValue(decisionIDs) {
		t.Fatalf("REST decision restart replay=%s err=%v", replayedDecision, err)
	}
	replayedAuthorization, err := callFlowMCP(restartedMCP, operationID, "authorize", 200)
	if err != nil || replayedAuthorization != onlyValue(authorizationIDs) {
		t.Fatalf("MCP authorization restart replay=%s err=%v", replayedAuthorization, err)
	}
	if flowStore.decisionCreates.Load() != 1 || authorizer.reservations.Load() != 1 {
		t.Fatalf("restart repeated decision/reservation: decisions=%d reservations=%d", flowStore.decisionCreates.Load(), authorizer.reservations.Load())
	}
}

func TestASCPRealPostgresCrossSurfaceDurableUniqueness(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	request, evidence := crossSurfaceCreateRequest(t, now)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id,name) VALUES ('org_a','Cross Surface IT')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agents (organization_id,id,customer_id,name,status)
		VALUES ('org_a','agent_a','customer_cross_surface','Cross Surface Agent','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	policyConfig := orchestrationPolicy("policy_cross_surface_it", false)
	policyConfig.AllowedAssets = []string{testUSDC}
	policyConfig.AllowedRecipients = []string{testRecipient}
	policyJSON, err := json.Marshal(policyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO policies (organization_id,agent_id,version,config,active,activated_at)
		VALUES ('org_a','agent_a',$1,$2,true,$3)`, policyConfig.Version, policyJSON, now); err != nil {
		t.Fatal(err)
	}
	observationDigest := fmt.Sprintf("0x%064x", 9910)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_snapshots
			(observation_digest,chain_id,directory_contract,directory_version,directory_root,
			 finalized_block_number,finalized_block_hash,providers,observed_at)
		VALUES ($1,84532,$2,9,$3,300,$4,'["alpha","bravo"]'::jsonb,$5)`,
		observationDigest, crossSurfaceDirectoryAddress, fmt.Sprintf("0x%064x", 9911), fmt.Sprintf("0x%064x", 9912), now); err != nil {
		t.Fatal(err)
	}
	quote := request.SellerQuote
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_quote_evidence
			(observation_digest,seller_id,resource_id,quote_signing_key,key_epoch,payout_address,
			 ack_authority,amount_base_units,verification_spec_hash,declared_work_time,
			 verification_budget_seconds,active,quote_key_revoked)
		VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,true,false)`,
		observationDigest, quote.SellerID, quote.ResourceID, evidence.QuoteSigningKey, quote.PayTo, quote.AckAuthority,
		quote.AmountBaseUnits, quote.VerificationSpecHash, quote.DeclaredWorkTime, quote.VerificationBudgetSeconds); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_heads
			(chain_id,directory_contract,observation_digest,directory_version,finalized_block_number,updated_at)
		VALUES (84532,$1,$2,9,300,$3)`, crossSurfaceDirectoryAddress, observationDigest, now); err != nil {
		t.Fatal(err)
	}

	intakeStore, err := ascpintake.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	intake, err := ascpintake.New(intakeStore, func() time.Time { return now }, &crossSurfaceRandom{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := directoryreader.NewMaterializedResolver(db, 84532, crossSurfaceDirectoryAddress, time.Minute, 15*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	agentService, err := ascpagent.New(ascpagent.Config{
		Intake: intake, Reader: intakeStore, Directory: resolver, DirectoryContract: crossSurfaceDirectoryAddress,
		ChainID: 84532, Asset: testUSDC, SchemeVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	revalidator, err := ascpexecauth.NewLocalRevalidator(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	executionStore, err := ascpexecauth.NewPostgresStore(db, revalidator, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	flowStore, err := ascporchestration.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	flowService, err := ascporchestration.New(ascporchestration.Config{
		DatabaseStore: flowStore, Authorization: executionStore, EscrowContract: ascpIntegrationModule,
		SettleWindow: time.Hour, Clock: func() time.Time { return now }, Random: &crossSurfaceRandom{},
	})
	if err != nil {
		t.Fatal(err)
	}
	bindingStore, err := ascpsignerbinding.NewStore(db, 84532, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindingStore.Put(ctx, "org_a", "agent_a", "owner_cross_surface", "cross_surface_binding", ascpsignerbinding.PutRequest{
		ExpectedVersion: 0, SignerKeyID: "cross-surface-spend-key", KeyEpoch: 1,
		ModuleAddress: ascpIntegrationModule, SafeAddress: ascpIntegrationSafe,
		KeeperID: "cross-surface-keeper", Reason: "AC-88 production adapter signer route",
	}); err != nil {
		t.Fatal(err)
	}
	activationStore, err := ascpbearer.NewActivationStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	activationService, err := ascpactivation.New(ascpactivation.Config{
		Authorizations: flowStore, Bindings: bindingStore, Store: activationStore, Random: &crossSurfaceRandom{},
	})
	if err != nil {
		t.Fatal(err)
	}
	rest, restStore, _, _, journal, _ := setupHandler(t)
	grantCrossSurfaceActivationScope(restStore)
	rest.ascpAgent, rest.ascpFlow, rest.ascpActivation = agentService, flowService, activationService
	defer journal.Close()
	mcpServer, err := mcp.NewServer(mcp.Config{Delegate: rest})
	if err != nil {
		t.Fatal(err)
	}

	const databaseRaceAttempts = 8
	operationIDs := raceCreateSurface(t, rest, mcpServer, request, databaseRaceAttempts)
	if len(operationIDs) != 1 {
		t.Fatalf("Postgres cross-surface operation IDs=%v", operationIDs)
	}
	operationID := onlyValue(operationIDs)
	decisionIDs := raceFlowSurface(t, rest, mcpServer, operationID, "evaluate", databaseRaceAttempts)
	authorizationIDs := raceFlowSurface(t, rest, mcpServer, operationID, "authorize", databaseRaceAttempts)
	if len(decisionIDs) != 1 || len(authorizationIDs) != 1 {
		t.Fatalf("Postgres decision IDs=%v authorization IDs=%v", decisionIDs, authorizationIDs)
	}
	activationRequest := crossSurfaceActivationRequest(now)
	activationIDs := raceActivationSurface(t, rest, mcpServer, operationID, activationRequest, databaseRaceAttempts)
	if len(activationIDs) != 1 {
		t.Fatalf("Postgres activation IDs=%v", activationIDs)
	}

	assertCrossSurfacePostgresCounts(t, ctx, db, operationID, quote.QuoteNonce)

	// The decision itself is durable policy evidence. Remove current active
	// policy before recreating the process so exact decision replay proves it
	// does not depend on mutable evaluation material.
	if _, err := db.ExecContext(ctx, `UPDATE policies SET active=false WHERE organization_id='org_a' AND agent_id='agent_a'`); err != nil {
		t.Fatal(err)
	}

	// Replace both services while retaining only Postgres state, then replay
	// once through each transport. No second decision, reservation, or
	// authorization row may appear.
	restartedIntake, err := ascpintake.New(intakeStore, func() time.Time { return now.Add(time.Minute) }, &crossSurfaceRandom{})
	if err != nil {
		t.Fatal(err)
	}
	restartedAgent, err := ascpagent.New(ascpagent.Config{
		Intake: restartedIntake, Reader: intakeStore, Directory: resolver, DirectoryContract: crossSurfaceDirectoryAddress,
		ChainID: 84532, Asset: testUSDC, SchemeVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedFlow, err := ascporchestration.New(ascporchestration.Config{
		DatabaseStore: flowStore, Authorization: executionStore, EscrowContract: ascpIntegrationModule,
		SettleWindow: time.Hour, Clock: func() time.Time { return now.Add(time.Minute) }, Random: &crossSurfaceRandom{},
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedActivation, err := ascpactivation.New(ascpactivation.Config{
		Authorizations: flowStore, Bindings: bindingStore, Store: activationStore, Random: &crossSurfaceRandom{},
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedREST, restartedStore, _, _, restartedJournal, _ := setupHandler(t)
	grantCrossSurfaceActivationScope(restartedStore)
	restartedREST.ascpAgent, restartedREST.ascpFlow, restartedREST.ascpActivation = restartedAgent, restartedFlow, restartedActivation
	defer restartedJournal.Close()
	restartedMCP, err := mcp.NewServer(mcp.Config{Delegate: restartedREST})
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := callASCPCreateMCP(restartedMCP, request, "cross_surface_postgres_once", 300); err != nil || replay != operationID {
		t.Fatalf("Postgres MCP restart replay=%s err=%v", replay, err)
	}
	if replay, err := callASCPCreateREST(restartedREST, request, "cross_surface_postgres_once"); err != nil || replay != operationID {
		t.Fatalf("Postgres REST restart replay=%s err=%v", replay, err)
	}
	if replay, err := callFlowMCP(restartedMCP, operationID, "evaluate", 301); err != nil || replay != onlyValue(decisionIDs) {
		t.Fatalf("Postgres MCP decision restart replay=%s err=%v", replay, err)
	}
	if replay, err := callFlowREST(restartedREST, operationID, "evaluate"); err != nil || replay != onlyValue(decisionIDs) {
		t.Fatalf("Postgres REST decision restart replay=%s err=%v", replay, err)
	}
	if replay, err := callFlowREST(restartedREST, operationID, "authorize"); err != nil || replay != onlyValue(authorizationIDs) {
		t.Fatalf("Postgres REST authorization restart replay=%s err=%v", replay, err)
	}
	if replay, err := callFlowMCP(restartedMCP, operationID, "authorize", 302); err != nil || replay != onlyValue(authorizationIDs) {
		t.Fatalf("Postgres MCP authorization restart replay=%s err=%v", replay, err)
	}
	if replay, err := callActivationREST(restartedREST, operationID, activationRequest); err != nil || replay != onlyValue(activationIDs) {
		t.Fatalf("Postgres REST activation restart replay=%s err=%v", replay, err)
	}
	if replay, err := callActivationMCP(restartedMCP, operationID, activationRequest, 303); err != nil || replay != onlyValue(activationIDs) {
		t.Fatalf("Postgres MCP activation restart replay=%s err=%v", replay, err)
	}
	assertCrossSurfacePostgresCounts(t, ctx, db, operationID, quote.QuoteNonce)
}

func assertCrossSurfacePostgresCounts(t *testing.T, ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, operationID, quoteNonce string) {
	t.Helper()
	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{"operation", `SELECT count(*) FROM ascp_intents WHERE operation_id=$1`, []any{operationID}},
		{"idempotency scope", `SELECT count(*) FROM ascp_intents WHERE organization_id=$1 AND actor_id=$2 AND endpoint=$3 AND idempotency_key=$4`, []any{"org_a", "agent_a", "ascp.intent.create", "cross_surface_postgres_once"}},
		{"decision", `SELECT count(*) FROM ascp_policy_decisions WHERE operation_id=$1`, []any{operationID}},
		{"reservation", `SELECT count(*) FROM ascp_budget_reservations WHERE operation_id=$1`, []any{operationID}},
		{"authorization", `SELECT count(*) FROM ascp_execution_authorizations WHERE intent_id=$1`, []any{operationID}},
		{"sign request", `SELECT count(*) FROM ascp_sign_requests WHERE operation_id=$1`, []any{operationID}},
		{"signer outbox effect", `SELECT count(*) FROM ascp_signer_outbox WHERE operation_id=$1 AND kind='SIGN_PREPARE_REQUESTED'`, []any{operationID}},
		{"quote nonce", `SELECT count(*) FROM ascp_intents WHERE quote_nonce=$1 AND organization_id=$2`, []any{quoteNonce, "org_a"}},
	}
	for _, check := range checks {
		var count int
		err := db.QueryRowContext(ctx, check.query, check.args...).Scan(&count)
		if err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", check.name, count, err)
		}
	}
}

func raceCreateSurface(t *testing.T, rest, mcpServer http.Handler, request ascpagent.CreateRequest, attempts int) map[string]struct{} {
	t.Helper()
	type outcome struct {
		id  string
		err error
	}
	outcomes := make(chan outcome, attempts)
	var wait sync.WaitGroup
	for index := range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var id string
			var err error
			if index%2 == 0 {
				id, err = callASCPCreateREST(rest, request, "cross_surface_postgres_once")
			} else {
				id, err = callASCPCreateMCP(mcpServer, request, "cross_surface_postgres_once", 400+index)
			}
			if index%5 == 0 && err == nil {
				id = ""
			}
			outcomes <- outcome{id: id, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	ids := map[string]struct{}{}
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("Postgres cross-surface create: %v", result.err)
		}
		if result.id != "" {
			ids[result.id] = struct{}{}
		}
	}
	return ids
}

func raceFlowSurface(t *testing.T, rest, mcpServer http.Handler, operationID, action string, attempts int) map[string]struct{} {
	t.Helper()
	type outcome struct {
		id  string
		err error
	}
	outcomes := make(chan outcome, attempts)
	var wait sync.WaitGroup
	for index := range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var id string
			var err error
			if index%2 == 0 {
				id, err = callFlowREST(rest, operationID, action)
			} else {
				id, err = callFlowMCP(mcpServer, operationID, action, 1000+index)
			}
			if index%6 == 0 && err == nil {
				id = ""
			}
			outcomes <- outcome{id: id, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	ids := map[string]struct{}{}
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("%s cross-surface call: %v", action, result.err)
		}
		if result.id != "" {
			ids[result.id] = struct{}{}
		}
	}
	return ids
}

func raceActivationSurface(t *testing.T, rest, mcpServer http.Handler, operationID string, request ascpactivation.Request, attempts int) map[string]struct{} {
	t.Helper()
	type outcome struct {
		id  string
		err error
	}
	outcomes := make(chan outcome, attempts)
	var wait sync.WaitGroup
	for index := range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var id string
			var err error
			if index%2 == 0 {
				id, err = callActivationREST(rest, operationID, request)
			} else {
				id, err = callActivationMCP(mcpServer, operationID, request, 2000+index)
			}
			if index%5 == 0 && err == nil {
				id = ""
			}
			outcomes <- outcome{id: id, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	ids := map[string]struct{}{}
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("activation cross-surface call: %v", result.err)
		}
		if result.id != "" {
			ids[result.id] = struct{}{}
		}
	}
	return ids
}

func crossSurfaceActivationRequest(now time.Time) ascpactivation.Request {
	payload := []byte(`{"action":"lock","operation":"cross-surface"}`)
	evidence := []byte(`{"policy":"durable","directoryVersion":9}`)
	return ascpactivation.Request{
		ActionID: "cross-surface-lock", CanonicalPayload: payload,
		CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence), Digest: fmt.Sprintf("0x%064x", 9913),
		Nonce: fmt.Sprintf("0x%064x", 9914), InstrumentType: ascpbearer.InstrumentLockAuthorization,
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}
}

func grantCrossSurfaceActivationScope(store *memoryStore) {
	principal := store.principals[TokenDigest(agentTokenA)]
	principal.Scopes = append(principal.Scopes, "activations:create")
	store.principals[TokenDigest(agentTokenA)] = principal
}

func callActivationREST(handler http.Handler, operationID string, input ascpactivation.Request) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated && recorder.Code != http.StatusOK {
		return "", fmt.Errorf("REST activation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return "", err
	}
	activation, _ := response["activation"].(map[string]any)
	requestID, _ := activation["requestId"].(string)
	if requestID == "" {
		return "", fmt.Errorf("REST activation request ID missing: %+v", response)
	}
	return requestID, nil
}

func callActivationMCP(handler http.Handler, operationID string, input ascpactivation.Request, id int) (string, error) {
	response, err := callMCPWithoutTesting(handler, agentTokenA, id, "ascp.operation.activation.create", map[string]any{
		"operationId": operationID, "request": input,
	})
	if err != nil {
		return "", err
	}
	result, _ := response["result"].(map[string]any)
	if result["isError"] != false {
		return "", fmt.Errorf("MCP activation failed: %+v", response)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	activation, _ := structured["activation"].(map[string]any)
	requestID, _ := activation["requestId"].(string)
	if requestID == "" {
		return "", fmt.Errorf("MCP activation request ID missing: %+v", response)
	}
	return requestID, nil
}

func callFlowREST(handler http.Handler, operationID, action string) (string, error) {
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/"+map[string]string{
		"evaluate": "evaluate", "authorize": "authorization",
	}[action], nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return "", fmt.Errorf("REST %s status=%d body=%s", action, recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return "", err
	}
	field, idField := "decision", "decisionId"
	if action == "authorize" {
		field, idField = "authorization", "authorizationId"
	}
	value, _ := response[field].(map[string]any)
	id, _ := value[idField].(string)
	if id == "" {
		return "", fmt.Errorf("REST %s ID missing: %+v", action, response)
	}
	return id, nil
}

func callFlowMCP(handler http.Handler, operationID, action string, id int) (string, error) {
	tool, field, idField := "ascp.operation.evaluate", "decision", "decisionId"
	if action == "authorize" {
		tool, field, idField = "ascp.operation.authorize", "authorization", "authorizationId"
	}
	response, err := callMCPWithoutTesting(handler, agentTokenA, id, tool, map[string]any{"operationId": operationID})
	if err != nil {
		return "", err
	}
	result, _ := response["result"].(map[string]any)
	if result["isError"] != false {
		return "", fmt.Errorf("MCP %s failed: %+v", action, response)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	value, _ := structured[field].(map[string]any)
	valueID, _ := value[idField].(string)
	if valueID == "" {
		return "", fmt.Errorf("MCP %s ID missing: %+v", action, response)
	}
	return valueID, nil
}

func onlyValue(values map[string]struct{}) string {
	for value := range values {
		return value
	}
	return ""
}

type crossSurfaceFlowStore struct {
	mu              sync.Mutex
	operationID     string
	now             time.Time
	decision        ascporchestration.Decision
	authorization   ascporchestration.Authorization
	decisionCreates atomic.Int32
}

func newCrossSurfaceFlowStore(operationID string, now time.Time) *crossSurfaceFlowStore {
	return &crossSurfaceFlowStore{operationID: operationID, now: now}
}

func (s *crossSurfaceFlowStore) Evaluate(_ context.Context, identity ascporchestration.Identity, operationID string, cfg ascporchestration.EvaluationConfig) (ascporchestration.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != (ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) || operationID != s.operationID {
		return ascporchestration.Decision{}, ascporchestration.ErrNotFound
	}
	if s.decision.DecisionID != "" {
		replayed := s.decision
		replayed.Replayed = true
		return replayed, nil
	}
	s.decision = ascporchestration.Decision{
		DecisionID: cfg.DecisionID, OrganizationID: identity.OrganizationID, AgentID: identity.AgentID,
		OperationID: operationID, Outcome: policy.AutoApprove, Reason: policy.ReasonAllowed,
		PolicyVersion: "policy_cross_surface_v1", EvaluatedAt: cfg.Now.Unix(),
	}
	s.decisionCreates.Add(1)
	return s.decision, nil
}

func (s *crossSurfaceFlowStore) Decision(_ context.Context, identity ascporchestration.Identity, operationID string) (ascporchestration.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != (ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) || operationID != s.operationID || s.decision.DecisionID == "" {
		return ascporchestration.Decision{}, ascporchestration.ErrNotFound
	}
	return s.decision, nil
}

func (s *crossSurfaceFlowStore) Approval(context.Context, string, string) (ascpapproval.Approval, error) {
	return ascpapproval.Approval{}, ascporchestration.ErrNotFound
}

func (s *crossSurfaceFlowStore) DecideApproval(context.Context, string, string, string, bool, string, time.Time) (ascpapproval.Approval, error) {
	return ascpapproval.Approval{}, ascporchestration.ErrNotFound
}

func (s *crossSurfaceFlowStore) AuthorizationInput(_ context.Context, identity ascporchestration.Identity, operationID, authorizationID, reservationID string, _ time.Time) (ascpexecauth.Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != (ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) || operationID != s.operationID || s.decision.DecisionID == "" {
		return ascpexecauth.Input{}, ascporchestration.ErrNotFound
	}
	return ascpexecauth.Input{
		AuthorizationID: authorizationID, AutoDecisionRef: s.decision.DecisionID, IntentID: operationID,
		ExecutionSnapshotHash: fmt.Sprintf("0x%064x", 9902),
		Reservation: ascpreservation.Request{
			ReservationID: reservationID, OperationID: operationID, Amount: "42",
			Dimensions: []ascpreservation.Dimension{{ID: "org_a/agent_a/daily", Limit: "100"}}, ExpiresAt: s.now.Add(time.Hour),
		},
	}, nil
}

func (s *crossSurfaceFlowStore) Authorization(_ context.Context, identity ascporchestration.Identity, operationID string) (ascporchestration.Authorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != (ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) || operationID != s.operationID || s.authorization.AuthorizationID == "" {
		return ascporchestration.Authorization{}, ascporchestration.ErrNotFound
	}
	return s.authorization, nil
}

func (s *crossSurfaceFlowStore) AdaptationRequest(context.Context, ascporchestration.Identity, string) (ascpadaptation.IssueRequest, error) {
	return ascpadaptation.IssueRequest{}, ascpadaptation.ErrReasonIneligible
}

func (s *crossSurfaceFlowStore) storeAuthorization(value ascporchestration.Authorization) {
	s.mu.Lock()
	s.authorization = value
	s.mu.Unlock()
}

func (s *crossSurfaceFlowStore) authorizationSnapshot() ascporchestration.Authorization {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorization
}

type crossSurfaceAuthorizer struct {
	mu           sync.Mutex
	store        *crossSurfaceFlowStore
	created      ascpexecauth.Authorization
	reservations atomic.Int32
}

func (s *crossSurfaceAuthorizer) ValidateAndReserve(_ context.Context, input ascpexecauth.Input) (ascpexecauth.Authorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created.AuthorizationID != "" {
		return s.created, ascpexecauth.ErrAlreadyEvaluated
	}
	s.created = ascpexecauth.Authorization{Input: input, State: ascpexecauth.ValidatedAndReserved}
	s.reservations.Add(1)
	s.store.storeAuthorization(ascporchestration.Authorization{
		AuthorizationID: input.AuthorizationID, OperationID: input.IntentID, DecisionID: input.AutoDecisionRef,
		State: ascpexecauth.ValidatedAndReserved, ExecutionSnapshotHash: input.ExecutionSnapshotHash,
		ReservationID: input.Reservation.ReservationID,
	})
	return s.created, nil
}

func newCrossSurfaceFlow(t *testing.T, store *crossSurfaceFlowStore, authorizer *crossSurfaceAuthorizer, now time.Time) *ascporchestration.Service {
	t.Helper()
	service, err := ascporchestration.New(ascporchestration.Config{
		DatabaseStore: store, Authorization: authorizer, EscrowContract: testRecipient,
		SettleWindow: time.Hour, Clock: func() time.Time { return now }, Random: &crossSurfaceRandom{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type crossSurfaceIntakeStore struct {
	inner *ascpintake.MemoryStore
	mu    sync.Mutex
	ids   map[string]struct{}
}

func newCrossSurfaceIntakeStore() *crossSurfaceIntakeStore {
	return &crossSurfaceIntakeStore{inner: ascpintake.NewMemoryStore(), ids: make(map[string]struct{})}
}

func (s *crossSurfaceIntakeStore) Create(ctx context.Context, input ascpintake.StoreInput) (ascpintake.Operation, bool, error) {
	operation, replayed, err := s.inner.Create(ctx, input)
	if err == nil && !replayed {
		s.mu.Lock()
		s.ids[operation.OperationID] = struct{}{}
		s.mu.Unlock()
	}
	return operation, replayed, err
}

func (s *crossSurfaceIntakeStore) Lookup(ctx context.Context, organizationID, actorID, idempotencyKey string) (ascpintake.Operation, string, bool, error) {
	return s.inner.Lookup(ctx, organizationID, actorID, idempotencyKey)
}

func (s *crossSurfaceIntakeStore) Get(ctx context.Context, organizationID, actorID, operationID string) (ascpintake.Operation, error) {
	return s.inner.Get(ctx, organizationID, actorID, operationID)
}

func (s *crossSurfaceIntakeStore) snapshot() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}
	return len(s.ids), ids
}

type crossSurfaceDirectory struct {
	evidence sellerquote.DirectoryEvidence
	err      error
	calls    atomic.Int32
}

func (r *crossSurfaceDirectory) EvidenceForQuote(context.Context, sellerquote.Quote) (string, sellerquote.DirectoryEvidence, error) {
	r.calls.Add(1)
	return crossSurfaceDirectoryAddress, r.evidence, r.err
}

type crossSurfaceRandom struct {
	mu   sync.Mutex
	next byte
}

func (r *crossSurfaceRandom) Read(target []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	if r.next == 0 {
		r.next = 1
	}
	for index := range target {
		target[index] = r.next
	}
	return len(target), nil
}

func newCrossSurfaceAgent(t *testing.T, store *crossSurfaceIntakeStore, directory *crossSurfaceDirectory, now time.Time) *ascpagent.Service {
	t.Helper()
	intake, err := ascpintake.New(store, func() time.Time { return now }, &crossSurfaceRandom{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ascpagent.New(ascpagent.Config{
		Intake: intake, Reader: store, Directory: directory, DirectoryContract: crossSurfaceDirectoryAddress,
		ChainID: 84532, Asset: testUSDC, SchemeVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func crossSurfaceCreateRequest(t *testing.T, now time.Time) (ascpagent.CreateRequest, sellerquote.DirectoryEvidence) {
	t.Helper()
	body := []byte(`{"query":"cross-surface-proof"}`)
	spec, err := purchasespec.Build(purchasespec.Input{
		OrgID: "org_a", AgentID: "agent_a", TaskID: "task_cross_surface", Method: "POST",
		URL: "https://seller.example/v1/work", Body: body,
		Headers:  []purchasespec.Header{{Name: "content-type", Value: "application/json"}},
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "urn:flowops:cross-surface"},
		Category: "research",
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.HexToECDSA(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	signer := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	quote := sellerquote.Quote{
		PurchaseSpecHash: spec.PurchaseSpecHash, SellerID: fmt.Sprintf("0x%064x", 8801), ResourceID: fmt.Sprintf("0x%064x", 8802),
		DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532", Asset: testUSDC, AmountBaseUnits: "42",
		PayTo: testRecipient, AckAuthority: "0x3333333333333333333333333333333333333333",
		VerificationSpecHash: fmt.Sprintf("0x%064x", 8803), DeclaredWorkTime: 30, VerificationBudgetSeconds: 20,
		QuoteExpiresAt: uint64(now.Add(48 * time.Hour).Unix()), QuoteNonce: fmt.Sprintf("0x%064x", 8804),
	}
	digest, err := quote.Digest(crossSurfaceDirectoryAddress)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	evidence := sellerquote.DirectoryEvidence{
		Verified: true, Version: quote.DirectoryVersion, SellerID: quote.SellerID, ResourceID: quote.ResourceID,
		QuoteSigningKey: signer, KeyEpoch: 1, PayoutAddress: quote.PayTo, AckAuthority: quote.AckAuthority,
		AmountBaseUnits: quote.AmountBaseUnits, VerificationSpecHash: quote.VerificationSpecHash,
		DeclaredWorkTime: quote.DeclaredWorkTime, VerificationBudgetSeconds: quote.VerificationBudgetSeconds, Active: true,
	}
	return ascpagent.CreateRequest{
		TaskID: "task_cross_surface", Method: "POST", URL: "https://seller.example/v1/work",
		RequestBodyBase64: base64.StdEncoding.EncodeToString(body),
		Headers:           []ascpagent.Header{{Name: "content-type", Value: "application/json"}},
		ResponseContract:  purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "urn:flowops:cross-surface"},
		Category:          "research", SellerQuote: quote, SellerQuoteSignature: "0x" + hex.EncodeToString(signature),
	}, evidence
}

func callASCPCreateREST(handler http.Handler, request ascpagent.CreateRequest, idempotencyKey string) (string, error) {
	status, response, err := callASCPCreateRESTResponse(handler, request, idempotencyKey)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return "", fmt.Errorf("REST status=%d body=%+v", status, response)
	}
	operation, ok := response["operation"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("REST operation is missing: %+v", response)
	}
	operationID, _ := operation["operationId"].(string)
	if operationID == "" {
		return "", fmt.Errorf("REST operation ID is missing: %+v", response)
	}
	return operationID, nil
}

func callASCPCreateRESTResponse(handler http.Handler, input ascpagent.CreateRequest, idempotencyKey string) (int, map[string]any, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return recorder.Code, nil, fmt.Errorf("decode REST response: %w", err)
	}
	return recorder.Code, response, nil
}

func callASCPCreateMCP(handler http.Handler, request ascpagent.CreateRequest, idempotencyKey string, id int) (string, error) {
	response, err := callMCPWithoutTesting(handler, agentTokenA, id, "ascp.operation.create", map[string]any{
		"request": request, "idempotencyKey": idempotencyKey,
	})
	if err != nil {
		return "", err
	}
	result, ok := response["result"].(map[string]any)
	if !ok || result["isError"] != false {
		return "", fmt.Errorf("MCP tool failed: %+v", response)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	operation, _ := structured["operation"].(map[string]any)
	operationID, _ := operation["operationId"].(string)
	if operationID == "" {
		return "", fmt.Errorf("MCP operation ID is missing: %+v", response)
	}
	return operationID, nil
}

func callMCPWithoutTesting(handler http.Handler, token string, id int, tool string, arguments any) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments},
	})
	if err != nil {
		return nil, err
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return nil, fmt.Errorf("MCP HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return nil, err
	}
	return response, nil
}

func nestedErrorCode(response map[string]any) string {
	errorValue, _ := response["error"].(map[string]any)
	code, _ := errorValue["code"].(string)
	return code
}
