package controlapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpagent"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/internal/directoryreader"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

func TestASCPAgentIntakeRealPostgresPersistsExactBytesAndReplays(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id, name) VALUES ('org_agent_api_it', 'Agent API Integration')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agents (organization_id, id, customer_id, name, status)
		VALUES ('org_agent_api_it', 'agent_api_it', 'customer_api_it', 'Agent API', 'ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"query":"durable-proof"}`)
	purchase, err := purchasespec.Build(purchasespec.Input{
		OrgID: "org_agent_api_it", AgentID: "agent_api_it", TaskID: "task_api_it", Method: "POST",
		URL: "https://seller.example/v1/work", Body: body,
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "urn:flowops:api-it"}, Category: "research",
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
		PurchaseSpecHash: purchase.PurchaseSpecHash, SellerID: ascpIntegrationHash(1501), ResourceID: ascpIntegrationHash(1502),
		DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532", Asset: ascpIntegrationUSDC, AmountBaseUnits: "10",
		PayTo: ascpIntegrationPayee, AckAuthority: ascpIntegrationAck, VerificationSpecHash: ascpIntegrationHash(1503),
		DeclaredWorkTime: 60, VerificationBudgetSeconds: 30, QuoteExpiresAt: uint64(now.Add(time.Hour).Unix()), QuoteNonce: ascpIntegrationHash(1504),
	}
	digest, err := quote.Digest(ascpIntegrationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	observationDigest := ascpIntegrationHash(1505)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_snapshots
			(observation_digest, chain_id, directory_contract, directory_version, directory_root,
			 finalized_block_number, finalized_block_hash, providers, observed_at)
		VALUES ($1,84532,$2,9,$3,100,$4,'["alpha","bravo"]'::jsonb,$5)`, observationDigest,
		ascpIntegrationDirectory, ascpIntegrationHash(1506), ascpIntegrationHash(1507), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_quote_evidence
			(observation_digest, seller_id, resource_id, quote_signing_key, key_epoch, payout_address,
			 ack_authority, amount_base_units, verification_spec_hash, declared_work_time,
			 verification_budget_seconds, active, quote_key_revoked)
		VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,true,false)`, observationDigest, quote.SellerID,
		quote.ResourceID, signer, quote.PayTo, quote.AckAuthority, quote.AmountBaseUnits,
		quote.VerificationSpecHash, quote.DeclaredWorkTime, quote.VerificationBudgetSeconds); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_heads
			(chain_id, directory_contract, observation_digest, directory_version, finalized_block_number, updated_at)
		VALUES (84532,$1,$2,9,100,$3)`, ascpIntegrationDirectory, observationDigest, now); err != nil {
		t.Fatal(err)
	}
	intakeStore, _ := ascpintake.NewPostgresStore(db)
	intake, _ := ascpintake.New(intakeStore, func() time.Time { return now }, nil)
	resolver, _ := directoryreader.NewMaterializedResolver(db, 84532, ascpIntegrationDirectory, time.Minute, 15*time.Second, func() time.Time { return now })
	service, err := ascpagent.New(ascpagent.Config{Intake: intake, Reader: intakeStore, Directory: resolver, DirectoryContract: ascpIntegrationDirectory, ChainID: 84532, Asset: ascpIntegrationUSDC, SchemeVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := ascpagent.CreateRequest{
		TaskID: "task_api_it", Method: "POST", URL: "https://seller.example/v1/work",
		RequestBodyBase64: base64.StdEncoding.EncodeToString(body),
		ResponseContract:  purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "urn:flowops:api-it"}, Category: "research",
		SellerQuote: quote, SellerQuoteSignature: "0x" + hex.EncodeToString(signature),
	}
	identity := ascpagent.Identity{OrganizationID: "org_agent_api_it", AgentID: "agent_api_it"}
	created, err := service.Create(ctx, identity, "idem_api_it", request)
	if err != nil || created.Replayed {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replayed, err := service.Create(ctx, identity, "idem_api_it", request)
	if err != nil || !replayed.Replayed || replayed.OperationID != created.OperationID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if _, err := service.Create(ctx, identity, "idem_api_it_other", request); !errors.Is(err, ascpintake.ErrQuoteNonceConsumed) {
		t.Fatalf("second nonce owner error=%v", err)
	}
	var storedPurchase, storedBody []byte
	if err := db.QueryRowContext(ctx, `SELECT purchase_spec_bytes, request_body FROM ascp_intents WHERE operation_id=$1`, created.OperationID).Scan(&storedPurchase, &storedBody); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedPurchase, purchase.CanonicalJSON) || !bytes.Equal(storedBody, body) {
		t.Fatalf("stored purchase/body bytes changed")
	}
	// A compromised/misconfigured head writer must not be able to pair this
	// configured directory with a snapshot carrying different head metadata.
	if _, err := db.ExecContext(ctx, `UPDATE ascp_directory_heads SET directory_version=10 WHERE chain_id=84532 AND directory_contract=$1`, ascpIntegrationDirectory); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolver.EvidenceForQuote(ctx, quote); !errors.Is(err, directoryreader.ErrCurrentSnapshotUnavailable) {
		t.Fatalf("mismatched head/snapshot binding error=%v", err)
	}
	replayedAfterHeadMismatch, err := service.Create(ctx, identity, "idem_api_it", request)
	if err != nil || !replayedAfterHeadMismatch.Replayed || replayedAfterHeadMismatch.OperationID != created.OperationID {
		t.Fatalf("replay after head mismatch=%+v err=%v", replayedAfterHeadMismatch, err)
	}
}
