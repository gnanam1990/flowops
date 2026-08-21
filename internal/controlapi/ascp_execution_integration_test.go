package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestASCPExecutionAuthorizationRealPostgresBudgetRace(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)

	config := policy.Config{
		Version: "policy_ascp_it_1", Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailEscrow}, AllowedAssets: []string{ascpIntegrationUSDC},
		AllowedRecipients: []string{ascpIntegrationPayee}, ApprovalRequiredRails: []envelope.Rail{envelope.RailEscrow},
		PerActionLimitAtomic: "10", AutoApproveThresholdAtomic: "1", TaskBudgetAtomic: "10", DailyBudgetAtomic: "10",
	}
	configJSON, _ := json.Marshal(config)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id, name) VALUES ('org_ascp_it', 'ASCP Integration')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agents (organization_id, id, customer_id, name, status)
		VALUES ('org_ascp_it', 'agent_ascp_it', 'customer_ascp_it', 'ASCP Agent', 'ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO policies (organization_id, agent_id, version, config, active, activated_at)
		VALUES ('org_ascp_it', 'agent_ascp_it', $1, $2, true, $3)`, config.Version, configJSON, now); err != nil {
		t.Fatal(err)
	}

	observationDigest := ascpIntegrationHash(500)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_snapshots
			(observation_digest, chain_id, directory_contract, directory_version, directory_root,
			 finalized_block_number, finalized_block_hash, providers, observed_at)
		VALUES ($1,84532,$2,9,$3,100,$4,'["alpha","bravo"]'::jsonb,$5)`, observationDigest,
		ascpIntegrationDirectory, ascpIntegrationHash(501), ascpIntegrationHash(502), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_quote_evidence
			(observation_digest, seller_id, resource_id, quote_signing_key, key_epoch, payout_address,
			 ack_authority, amount_base_units, verification_spec_hash, declared_work_time,
			 verification_budget_seconds, active, quote_key_revoked)
		VALUES ($1,$2,$3,$4,1,$5,$6,'10',$7,60,30,true,false)`, observationDigest,
		ascpIntegrationHash(503), ascpIntegrationHash(504), ascpIntegrationSigner,
		ascpIntegrationPayee, ascpIntegrationAck, ascpIntegrationHash(505)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_heads
			(chain_id, directory_contract, observation_digest, directory_version, finalized_block_number, updated_at)
		VALUES (84532,$1,$2,9,100,$3)`, ascpIntegrationDirectory, observationDigest, now); err != nil {
		t.Fatal(err)
	}

	inputs := []ascpexecauth.Input{
		ascpIntegrationInput(t, db, config, observationDigest, now, 1),
		ascpIntegrationInput(t, db, config, observationDigest, now, 2),
	}
	revalidator, err := ascpexecauth.NewLocalRevalidator(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ascpexecauth.NewPostgresStore(db, revalidator, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsByInput := make([]error, len(inputs))
	var group sync.WaitGroup
	for index := range inputs {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, errorsByInput[index] = store.ValidateAndReserve(ctx, inputs[index])
		}()
	}
	close(start)
	group.Wait()
	succeeded, budgetDenied := 0, 0
	for _, err := range errorsByInput {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ascpreservation.ErrBudgetExceeded):
			budgetDenied++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if succeeded != 1 || budgetDenied != 1 {
		t.Fatalf("success=%d budgetDenied=%d errors=%v", succeeded, budgetDenied, errorsByInput)
	}

	var reservations, reservationDimensions, validated, invalidated, approvals int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_budget_reservations`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_budget_reservation_dimensions`).Scan(&reservationDimensions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_execution_authorizations WHERE state='VALIDATED_AND_RESERVED'`).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_execution_authorizations WHERE state='INVALIDATED'`).Scan(&invalidated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_approvals WHERE state='APPROVED'`).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 || reservationDimensions != 5 || validated != 1 || invalidated != 1 || approvals != 2 {
		t.Fatalf("reservations=%d dimensions=%d validated=%d invalidated=%d approvals=%d", reservations, reservationDimensions, validated, invalidated, approvals)
	}
	for index, err := range errorsByInput {
		if err == nil {
			if _, replayErr := store.ValidateAndReserve(ctx, inputs[index]); !errors.Is(replayErr, ascpexecauth.ErrAlreadyEvaluated) {
				t.Fatalf("successful authorization replay error=%v", replayErr)
			}
		}
	}
}

func TestASCPMigrationBackfillsDimensionsWithoutInventingLegacyCanonicalBytes(t *testing.T) {
	db := ascpRawIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE flowops_schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatal(err)
	}
	manifest, err := MigrationManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range manifest {
		if strings.HasPrefix(migration.Name, "0009_") {
			break
		}
		script, err := migrationFiles.ReadFile("migrations/" + migration.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", migration.Name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO flowops_schema_migrations (name, checksum) VALUES ($1,$2)`, migration.Name, migration.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id, name) VALUES ('org_legacy_ascp', 'Legacy ASCP')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_intents
			(operation_id, organization_id, actor_id, endpoint, idempotency_key, canonical_input_hash,
			 quote_hash, purchase_spec_hash, quote_nonce, directory_version, directory_contract,
			 seller_signer, quote_json, purchase_spec_json, request_body, created_at)
		VALUES ($1,'org_legacy_ascp','agent_legacy','ascp.intent.create','legacy_intake',$2,$3,$4,$5,9,$6,$7,'{}'::jsonb,'{}'::jsonb,$8,$9)`,
		ascpIntegrationHash(1001), fmt.Sprintf("%064x", 1002), ascpIntegrationHash(1003), ascpIntegrationHash(1004),
		ascpIntegrationHash(1005), ascpIntegrationDirectory, ascpIntegrationSigner, []byte{}, now); err != nil {
		t.Fatal(err)
	}
	dimensions, _ := json.Marshal([]ascpreservation.Dimension{
		{ID: "legacy:day", Limit: "100", Refundable: true},
		{ID: "legacy:lifetime", Limit: "1000", Refundable: false},
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_budget_reservations
			(reservation_id, operation_id, amount_base_units, state, dimensions, created_at, expires_at)
		VALUES ($1,$2,'10','RESERVED',$3,$4,$5)`, ascpIntegrationHash(1006), ascpIntegrationHash(1001),
		dimensions, now, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("upgraded migrations are not idempotent: %v", err)
	}
	var dimensionCount int
	var canonicalBytesAvailable bool
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_budget_reservation_dimensions WHERE reservation_id=$1`, ascpIntegrationHash(1006)).Scan(&dimensionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT purchase_spec_bytes IS NOT NULL FROM ascp_intents WHERE operation_id=$1`, ascpIntegrationHash(1001)).Scan(&canonicalBytesAvailable); err != nil {
		t.Fatal(err)
	}
	if dimensionCount != 2 || canonicalBytesAvailable {
		t.Fatalf("dimensionCount=%d canonicalBytesAvailable=%t", dimensionCount, canonicalBytesAvailable)
	}
}

const (
	ascpIntegrationUSDC      = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	ascpIntegrationPayee     = "0x3333333333333333333333333333333333333333"
	ascpIntegrationAck       = "0x4444444444444444444444444444444444444444"
	ascpIntegrationSigner    = "0x5555555555555555555555555555555555555555"
	ascpIntegrationDirectory = "0x6666666666666666666666666666666666666666"
)

func ascpIntegrationInput(t *testing.T, db *sql.DB, config policy.Config, observationDigest string, now time.Time, sequence uint64) ascpexecauth.Input {
	t.Helper()
	operationID := ascpIntegrationHash(sequence)
	approvalID := ascpIntegrationHash(100 + sequence)
	purchase, err := purchasespec.Build(purchasespec.Input{
		OrgID: "org_ascp_it", AgentID: "agent_ascp_it", TaskID: "task_ascp_shared",
		Method: "GET", URL: "https://seller.example/v1/report",
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:report-v1"}, Category: "research",
	})
	if err != nil {
		t.Fatal(err)
	}
	quote := sellerquote.Quote{
		PurchaseSpecHash: purchase.PurchaseSpecHash, SellerID: ascpIntegrationHash(503),
		ResourceID: ascpIntegrationHash(504), DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532",
		Asset: ascpIntegrationUSDC, AmountBaseUnits: "10", PayTo: ascpIntegrationPayee,
		AckAuthority: ascpIntegrationAck, VerificationSpecHash: ascpIntegrationHash(505),
		DeclaredWorkTime: 60, VerificationBudgetSeconds: 30, QuoteExpiresAt: uint64(now.Add(time.Hour).Unix()),
		QuoteNonce: ascpIntegrationHash(300 + sequence),
	}
	quoteJSON, _ := json.Marshal(quote)
	if _, err := db.Exec(`
		INSERT INTO ascp_intents
			(operation_id, organization_id, actor_id, endpoint, idempotency_key, canonical_input_hash,
			 quote_hash, purchase_spec_hash, quote_nonce, directory_version, directory_contract,
			 seller_signer, quote_json, purchase_spec_json, purchase_spec_bytes, request_body, created_at)
		VALUES ($1,'org_ascp_it','agent_ascp_it','ascp.intent.create',$2,$3,$4,$5,$6,9,$7,$8,$9,$10,$11,$12,$13)`,
		operationID, fmt.Sprintf("intent_%d", sequence), fmt.Sprintf("%064x", 400+sequence),
		ascpIntegrationHash(600+sequence), quote.PurchaseSpecHash, quote.QuoteNonce, ascpIntegrationDirectory,
		ascpIntegrationSigner, quoteJSON, purchase.CanonicalJSON, purchase.CanonicalJSON, []byte{}, now); err != nil {
		t.Fatal(err)
	}
	policyHash, err := policy.ConfigHash(config)
	if err != nil {
		t.Fatal(err)
	}
	review := ascpapproval.Review{
		CommitmentHash: ascpIntegrationHash(700 + sequence), PolicyVersion: config.Version, PolicyHash: policyHash,
		DirectoryVersion: 9, PayTo: quote.PayTo, AckAuthority: quote.AckAuthority, AmountBaseUnits: quote.AmountBaseUnits,
		VerificationSpecHash: quote.VerificationSpecHash, Protection: "ESCROW", ChainID: quote.ChainID, Asset: quote.Asset,
	}
	reviewHash, err := ascpapproval.ReviewHash(review)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO ascp_approvals
			(approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at, decided_at, decided_by)
		VALUES ($1,'org_ascp_it',$2,'APPROVED',$3,$4,$5,$4,'owner_ascp_it')`, approvalID,
		operationID, reviewHash, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	dimensions, err := ascpexecauth.RequiredBudgetDimensions(config, purchase.Spec, now)
	if err != nil {
		t.Fatal(err)
	}
	input := ascpexecauth.Input{
		AuthorizationID: ascpIntegrationHash(800 + sequence), ApprovalID: approvalID,
		ApprovalSnapshotHash: reviewHash, IntentID: operationID, Review: review,
		Reservation: ascpreservation.Request{
			ReservationID: ascpIntegrationHash(900 + sequence), OperationID: operationID, Amount: "10",
			Dimensions: dimensions,
			ExpiresAt:  now.Add(15 * time.Minute),
		},
	}
	input.ExecutionSnapshotHash, err = ascpexecauth.ExecutionSnapshotHash(input, "org_ascp_it", "agent_ascp_it", observationDigest)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func ascpIntegrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
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
	schema := fmt.Sprintf("flowops_ascp_it_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(10)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		db.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = adminDB.Close()
	})
	return db
}

func ascpRawIntegrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
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
	schema := fmt.Sprintf("flowops_ascp_upgrade_it_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = adminDB.Close()
	})
	return db
}

func ascpIntegrationHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
