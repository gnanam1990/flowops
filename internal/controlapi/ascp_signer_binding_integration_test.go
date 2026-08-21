package controlapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
)

func TestASCPSignerBindingRealPostgresIdempotencyTenantAndVersionRaces(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, statement := range []string{
		`INSERT INTO organizations (id,name) VALUES ('org_binding_it','Binding Integration'),('org_binding_other','Other Binding Integration')`,
		`INSERT INTO agents (organization_id,id,customer_id,name,status) VALUES
			('org_binding_it','agent_binding_it','customer_binding_it','Binding Agent','ACTIVE'),
			('org_binding_other','agent_binding_it','customer_binding_other','Other Binding Agent','ACTIVE')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	store, err := ascpsignerbinding.NewStore(db, 84532, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := ascpsignerbinding.PutRequest{
		SignerKeyID: "signer-binding-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222",
		KeeperID:      "keeper-binding-primary", Reason: "Initial customer controlled signer",
	}
	type putResult struct {
		result ascpsignerbinding.Result
		err    error
	}
	start := make(chan struct{})
	results := make(chan putResult, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := store.Put(ctx, "org_binding_it", "agent_binding_it", "owner_binding_it", "binding_create_once", request)
			results <- putResult{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	created := 0
	var changeID string
	for current := range results {
		if current.err != nil || current.result.Binding.Version != 1 || current.result.Binding.SignerKeyID != request.SignerKeyID {
			t.Fatalf("concurrent create=%+v err=%v", current.result, current.err)
		}
		if changeID == "" {
			changeID = current.result.ChangeID
		} else if current.result.ChangeID != changeID {
			t.Fatalf("idempotent create change IDs differ: %s != %s", current.result.ChangeID, changeID)
		}
		if !current.result.Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("concurrent signer binding creators=%d", created)
	}
	mainnetStore, err := ascpsignerbinding.NewStore(db, 8453, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mainnetStore.Current(ctx, "org_binding_it", "agent_binding_it"); !errors.Is(err, ascpsignerbinding.ErrNotFound) {
		t.Fatalf("cross-chain current error=%v", err)
	}
	if _, err := mainnetStore.Put(ctx, "org_binding_it", "agent_binding_it", "owner_binding_it", "binding_create_once", request); !errors.Is(err, ascpsignerbinding.ErrIdempotencyConflict) {
		t.Fatalf("cross-chain idempotency error=%v", err)
	}
	var histories, changes, audits int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_agent_signer_binding_history WHERE organization_id='org_binding_it' AND agent_id='agent_binding_it'`).Scan(&histories); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_agent_signer_binding_changes WHERE organization_id='org_binding_it'`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE organization_id='org_binding_it' AND kind='ascp.signer_binding.changed'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if histories != 1 || changes != 1 || audits != 1 {
		t.Fatalf("history=%d changes=%d audits=%d", histories, changes, audits)
	}

	conflict := request
	conflict.KeeperID = "keeper-binding-attacker"
	if _, err := store.Put(ctx, "org_binding_it", "agent_binding_it", "owner_binding_it", "binding_create_once", conflict); !errors.Is(err, ascpsignerbinding.ErrIdempotencyConflict) {
		t.Fatalf("idempotency mismatch error=%v", err)
	}
	if _, err := store.Current(ctx, "org_binding_other", "agent_binding_it"); !errors.Is(err, ascpsignerbinding.ErrNotFound) {
		t.Fatalf("cross-tenant current error=%v", err)
	}
	if result, err := store.Put(ctx, "org_binding_it", "agent_binding_it", "owner_binding_it", "binding_same_route", ascpsignerbinding.PutRequest{
		ExpectedVersion: 1, SignerKeyID: request.SignerKeyID, KeyEpoch: request.KeyEpoch,
		ModuleAddress: request.ModuleAddress, SafeAddress: request.SafeAddress, KeeperID: request.KeeperID,
		Reason: "Confirm unchanged binding",
	}); err != nil || result.Binding.Version != 1 {
		t.Fatalf("same-route update=%+v err=%v", result, err)
	}

	rotation := request
	rotation.ExpectedVersion = 1
	rotation.SignerKeyID = "signer-binding-key-2"
	rotation.KeyEpoch = 2
	rotation.Reason = "Rotate customer controlled signer"
	start = make(chan struct{})
	results = make(chan putResult, 8)
	for index := range 8 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			result, err := store.Put(ctx, "org_binding_it", "agent_binding_it", "owner_binding_it", "binding_rotate_"+string(rune('a'+index)), rotation)
			results <- putResult{result: result, err: err}
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	rotated, conflicted := 0, 0
	for current := range results {
		switch {
		case current.err == nil && current.result.Binding.Version == 2:
			rotated++
		case errors.Is(current.err, ascpsignerbinding.ErrVersionConflict):
			conflicted++
		default:
			t.Fatalf("rotation race result=%+v err=%v", current.result, current.err)
		}
	}
	if rotated != 1 || conflicted != 7 {
		t.Fatalf("rotation race rotated=%d conflicted=%d", rotated, conflicted)
	}
	current, err := store.Current(ctx, "org_binding_it", "agent_binding_it")
	if err != nil || current.Version != 2 || current.SignerKeyID != rotation.SignerKeyID || current.KeyEpoch != 2 {
		t.Fatalf("current rotation=%+v err=%v", current, err)
	}
	reuse := rotation
	reuse.ExpectedVersion = 2
	reuse.KeeperID = "keeper-binding-secondary"
	reuse.Reason = "Attempt routing change without a fresh key epoch"
	if _, err := store.Put(ctx, "org_binding_it", "agent_binding_it", "owner_binding_it", "binding_reuse_epoch", reuse); !errors.Is(err, ascpsignerbinding.ErrKeyEpochReuse) {
		t.Fatalf("key epoch reuse error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_agent_signer_bindings SET keeper_id='tampered-keeper' WHERE organization_id='org_binding_it' AND agent_id='agent_binding_it'`); err == nil {
		t.Fatal("current signer binding accepted state absent from immutable history")
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_agent_signer_binding_history SET reason='tampered' WHERE organization_id='org_binding_it'`); err == nil {
		t.Fatal("append-only signer binding history accepted mutation")
	}
}

func TestASCPSignerBindingRotationCannotRaceOldBindingIntoSignRequested(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	const organizationID = "org_binding_race"
	const agentID = "agent_binding_race"
	operationID, approvalID := ascpIntegrationHash(9501), ascpIntegrationHash(9502)
	reservationID, authorizationID := ascpIntegrationHash(9503), ascpIntegrationHash(9504)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations (id,name) VALUES ($1,'Binding Race')`, []any{organizationID}},
		{`INSERT INTO agents (organization_id,id,customer_id,name,status) VALUES ($1,$2,'customer_binding_race','Binding Race Agent','ACTIVE')`, []any{organizationID, agentID}},
		{`INSERT INTO ascp_intents
			(operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,quote_hash,
			 purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,quote_json,
			 purchase_spec_json,purchase_spec_bytes,request_body,created_at)
			VALUES ($1,$2,$3,'ascp.intent.create','binding_race',$4,$5,$6,$7,1,$8,$9,
			'{}'::jsonb,'{}'::jsonb,'{}'::bytea,''::bytea,$10)`, []any{operationID, organizationID, agentID,
			fmt.Sprintf("%064x", 9505), ascpIntegrationHash(9506), ascpIntegrationHash(9507), ascpIntegrationHash(9508),
			ascpIntegrationDirectory, ascpIntegrationSigner, now}},
		{`INSERT INTO ascp_approvals
			(approval_id,organization_id,intent_id,state,review_snapshot_hash,requested_at,expires_at,decided_at,decided_by)
			VALUES ($1,$2,$3,'APPROVED',$4,$5,$6,$5,'owner_binding_race')`, []any{approvalID, organizationID, operationID, ascpIntegrationHash(9509), now, now.Add(time.Hour)}},
		{`INSERT INTO ascp_budget_reservations
			(reservation_id,operation_id,amount_base_units,state,dimensions,created_at,expires_at)
			VALUES ($1,$2,'10','RESERVED','[]'::jsonb,$3,$4)`, []any{reservationID, operationID, now, now.Add(15 * time.Minute)}},
		{`INSERT INTO ascp_execution_authorizations
			(authorization_id,approval_id,intent_id,state,execution_snapshot_hash,reservation_id,created_at,evaluated_at)
			VALUES ($1,$2,$3,'VALIDATED_AND_RESERVED',$4,$5,$6,$6)`, []any{authorizationID, approvalID, operationID, ascpIntegrationHash(9510), reservationID, now}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	bindingStore, err := ascpsignerbinding.NewStore(db, 84532, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	binding := ascpsignerbinding.PutRequest{
		SignerKeyID: "signer-race-v1", KeyEpoch: 1,
		ModuleAddress: ascpIntegrationModule, SafeAddress: ascpIntegrationSafe,
		KeeperID: "keeper-race-v1", Reason: "Initial race binding",
	}
	if _, err := bindingStore.Put(ctx, organizationID, agentID, "owner_binding_race", "binding_race_v1", binding); err != nil {
		t.Fatal(err)
	}
	activationStore, err := ascpbearer.NewActivationStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	payload, evidence := []byte("binding-race-payload"), []byte("binding-race-evidence")
	input := ascpbearer.ActivationInput{
		RequestID: ascpIntegrationHash(9511), AuthorizationID: authorizationID, OperationID: operationID,
		ReservationID: reservationID, ActionID: "binding-race-action", CanonicalPayload: payload,
		CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence), Digest: ascpIntegrationHash(9512),
		Nonce: ascpIntegrationHash(9513), InstrumentType: ascpbearer.InstrumentLockAuthorization,
		SignerBindingVersion: 1, SignerKeyID: binding.SignerKeyID, KeyEpoch: binding.KeyEpoch,
		ModuleAddress: binding.ModuleAddress, SafeAddress: binding.SafeAddress, KeeperID: binding.KeeperID,
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}

	const advisoryKey = int64(9514)
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION flowops_test_block_binding_rotation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$;
		CREATE TRIGGER aaa_test_block_binding_rotation
		BEFORE UPDATE ON ascp_agent_signer_bindings
		FOR EACH ROW EXECUTE FUNCTION flowops_test_block_binding_rotation()`, advisoryKey)); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryKey); err != nil {
		t.Fatal(err)
	}
	rotation := binding
	rotation.ExpectedVersion, rotation.SignerKeyID, rotation.KeyEpoch = 1, "signer-race-v2", 2
	rotation.KeeperID, rotation.Reason = "keeper-race-v2", "Rotate during activation race"
	rotationResult := make(chan error, 1)
	go func() {
		_, err := bindingStore.Put(ctx, organizationID, agentID, "owner_binding_race", "binding_race_v2", rotation)
		rotationResult <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted`, advisoryKey).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rotation did not reach deterministic binding-update barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	activationResult := make(chan error, 1)
	go func() {
		_, _, err := activationStore.Request(ctx, input)
		activationResult <- err
	}()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, advisoryKey); err != nil {
		t.Fatal(err)
	}
	if err := <-rotationResult; err != nil {
		t.Fatalf("rotation error=%v", err)
	}
	if err := <-activationResult; !errors.Is(err, ascpbearer.ErrActivationBinding) {
		t.Fatalf("old binding activation race error=%v", err)
	}
	var requests int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_sign_requests WHERE operation_id=$1`, operationID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	current, err := bindingStore.Current(ctx, organizationID, agentID)
	if err != nil || requests != 0 || current.Version != 2 || current.SignerKeyID != rotation.SignerKeyID {
		t.Fatalf("requests=%d current=%+v err=%v", requests, current, err)
	}
}
