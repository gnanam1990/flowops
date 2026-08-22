package controlapi

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestASCPAdaptationRealPostgresIssuesOnceAndConsumesAtomically(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO organizations (id, name) VALUES
			('org_adaptation_it', 'Adaptation Integration'),
			('org_adaptation_other', 'Adaptation Other');
		INSERT INTO agents (organization_id, id, customer_id, name, status) VALUES
			('org_adaptation_it', 'agent_adaptation_it', 'customer_adaptation_it', 'Adaptation Agent', 'ACTIVE'),
			('org_adaptation_other', 'agent_adaptation_other', 'customer_adaptation_other', 'Other Agent', 'ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	originalIntentID := ascpIntegrationHash(2601)
	insertAdaptationIntent(t, db, originalIntentID, "org_adaptation_it", "agent_adaptation_it", "idem_original", ascpIntegrationHash(2602), "", "", now)

	key, err := crypto.HexToECDSA(strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ascpadaptation.NewIssuer(adaptationIntegrationSigner{key: key}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	grantStore, err := ascpadaptation.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := ascpadaptation.NewService(issuer, grantStore)
	if err != nil {
		t.Fatal(err)
	}
	request := ascpadaptation.IssueRequest{
		ReasonClass: ascpadaptation.ReasonTooExpensive, OriginalIntentID: originalIntentID,
		OrganizationID: "org_adaptation_it", AgentID: "agent_adaptation_it", TaskID: "task_adaptation_it",
		AllowedCategory: "research", MaxAmountAtomic: "100", AllowedSellerSet: []string{ascpIntegrationHash(2603)}, IssuedAt: now.Unix(),
	}
	record, err := grants.Issue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := grants.Issue(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Artifact.Grant.GrantID != record.Artifact.Grant.GrantID || replayed.Artifact.Signature != record.Artifact.Signature {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	changed := request
	changed.MaxAmountAtomic = "99"
	if _, err := grants.Issue(ctx, changed); !errors.Is(err, ascpadaptation.ErrIssueConflict) {
		t.Fatalf("changed issuance error=%v", err)
	}
	crossTenant := request
	crossTenant.OrganizationID, crossTenant.AgentID = "org_adaptation_other", "agent_adaptation_other"
	crossTenant.OriginalIntentID = originalIntentID
	if _, err := grants.Issue(ctx, crossTenant); err == nil {
		t.Fatal("cross-tenant original intent issued a grant")
	}
	secondOriginalID := ascpIntegrationHash(2604)
	insertAdaptationIntent(t, db, secondOriginalID, "org_adaptation_it", "agent_adaptation_it", "idem_original_2", ascpIntegrationHash(2605), "", "", now)
	secondRequest := request
	secondRequest.OriginalIntentID = secondOriginalID
	secondRecord, err := grants.Issue(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	unboundConsumerID := ascpIntegrationHash(2606)
	insertAdaptationIntent(t, db, unboundConsumerID, "org_adaptation_it", "agent_adaptation_it", "idem_unbound_consumer", ascpIntegrationHash(2607), "", "", now)
	if _, err := db.ExecContext(ctx, `
		UPDATE ascp_adaptation_grants
		SET state='CONSUMED', remaining_attempts=0, consumed_operation_id=$2, consumed_at=statement_timestamp()
		WHERE grant_id=$1`, secondRecord.Artifact.Grant.GrantID, unboundConsumerID); err == nil {
		t.Fatal("database accepted a consumer without the exact grant binding")
	}
	var secondState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM ascp_adaptation_grants WHERE grant_id=$1`, secondRecord.Artifact.Grant.GrantID).Scan(&secondState); err != nil || secondState != "ISSUED" {
		t.Fatalf("second grant state=%s err=%v", secondState, err)
	}

	intakeStore, err := ascpintake.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	const competitors = 24
	inputs := make([]ascpintake.StoreInput, competitors)
	for index := range competitors {
		operationID := ascpIntegrationHash(uint64(2700 + index))
		inputs[index] = adaptationStoreInput(operationID, fmt.Sprintf("idem_adapted_%d", index), ascpIntegrationHash(uint64(2800+index)), record, now)
	}
	start := make(chan struct{})
	results := make(chan struct {
		index int
		err   error
	}, competitors)
	var group sync.WaitGroup
	for index := range competitors {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, _, err := intakeStore.Create(ctx, inputs[index])
			results <- struct {
				index int
				err   error
			}{index, err}
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	winner := -1
	for result := range results {
		if result.err == nil {
			if winner != -1 {
				t.Fatalf("multiple grant consumers: %d and %d", winner, result.index)
			}
			winner = result.index
			continue
		}
		var pgError *pgconn.PgError
		if !errors.Is(result.err, ascpadaptation.ErrGrantConsumed) && !(errors.As(result.err, &pgError) && pgError.Code == "40001") {
			t.Fatalf("consumer %d error=%v", result.index, result.err)
		}
	}
	if winner == -1 {
		t.Fatal("adaptation grant had no consumer")
	}
	stored, replay, err := intakeStore.Create(ctx, inputs[winner])
	if err != nil || !replay || stored.OperationID != inputs[winner].Operation.OperationID {
		t.Fatalf("exact replay=%+v replay=%v err=%v", stored, replay, err)
	}
	var state string
	var remaining, adaptedRows int
	var consumedOperationID string
	if err := db.QueryRowContext(ctx, `SELECT state, remaining_attempts, consumed_operation_id FROM ascp_adaptation_grants WHERE grant_id=$1`, record.Artifact.Grant.GrantID).Scan(&state, &remaining, &consumedOperationID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_intents WHERE adaptation_grant_id=$1`, record.Artifact.Grant.GrantID).Scan(&adaptedRows); err != nil {
		t.Fatal(err)
	}
	if state != "CONSUMED" || remaining != 0 || consumedOperationID != inputs[winner].Operation.OperationID || adaptedRows != 1 {
		t.Fatalf("state=%s remaining=%d consumer=%s rows=%d winner=%s", state, remaining, consumedOperationID, adaptedRows, inputs[winner].Operation.OperationID)
	}
}

func adaptationStoreInput(operationID, idempotencyKey, quoteNonce string, record ascpadaptation.Record, now time.Time) ascpintake.StoreInput {
	operation := ascpintake.Operation{
		OperationID: operationID, OrganizationID: "org_adaptation_it", ActorID: "agent_adaptation_it",
		QuoteHash: ascpIntegrationHash(2901), PurchaseSpecHash: ascpIntegrationHash(2902), QuoteNonce: quoteNonce,
		DirectoryVersion: 1, DirectoryContract: ascpIntegrationDirectory, SellerSigner: ascpIntegrationPayee,
		AdaptationGrantID: record.Artifact.Grant.GrantID, CreatedAt: now.Unix(),
	}
	return ascpintake.StoreInput{
		Operation: operation, IdempotencyKey: idempotencyKey,
		CanonicalInputHash: operationID[2:],
		QuoteJSON:          []byte(`{}`), PurchaseSpecJSON: []byte(`{}`), RequestBody: []byte(`{}`),
		AdaptationGrantID: record.Artifact.Grant.GrantID, AdaptationDigest: record.Digest,
	}
}

func insertAdaptationIntent(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, operationID, organizationID, agentID, idempotencyKey, quoteNonce, adaptationGrantID, adaptationDigest string, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO ascp_intents
			(operation_id, organization_id, actor_id, endpoint, idempotency_key, canonical_input_hash,
			 quote_hash, purchase_spec_hash, quote_nonce, directory_version, directory_contract, seller_signer,
			 quote_json, purchase_spec_json, purchase_spec_bytes, request_body,
			 adaptation_grant_id, adaptation_grant_digest, created_at)
		VALUES ($1,$2,$3,'ascp.intent.create',$4,$5,$6,$7,$8,1,$9,$10,'{}'::jsonb,'{}'::jsonb,'{}'::bytea,'{}'::bytea,NULLIF($11,''),NULLIF($12,''),$13)`,
		operationID, organizationID, agentID, idempotencyKey, operationID[2:],
		ascpIntegrationHash(2991), ascpIntegrationHash(2992), quoteNonce,
		ascpIntegrationDirectory, ascpIntegrationPayee, adaptationGrantID, adaptationDigest, now); err != nil {
		t.Fatal(err)
	}
}

type adaptationIntegrationSigner struct {
	key   *ecdsa.PrivateKey
	calls *atomic.Int32
}

func (s adaptationIntegrationSigner) SignDigest(ctx context.Context, digest []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.calls != nil {
		s.calls.Add(1)
	}
	return crypto.Sign(digest, s.key)
}
