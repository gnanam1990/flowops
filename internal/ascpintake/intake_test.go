package ascpintake

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	"github.com/jackc/pgx/v5/pgconn"
)

const directoryContract = "0x1111111111111111111111111111111111111111"

func TestCreateAtomicallyClaimsNonceAndReplaysExactInput(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(1_800_000_000, 0).UTC()
	random := append(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)...)
	random = append(random, bytes.Repeat([]byte{3}, 32)...)
	service, err := New(store, func() time.Time { return now }, bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(t)
	first, err := service.Create(context.Background(), request)
	if err != nil || first.Replayed || first.OperationID != "0x"+strings.Repeat("01", 32) {
		t.Fatalf("first = %+v, %v", first, err)
	}
	replayed, err := service.Create(context.Background(), request)
	if err != nil || !replayed.Replayed || replayed.OperationID != first.OperationID {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	changed := request
	changed.Quote.AmountBaseUnits = "43"
	changed.Evidence.AmountBaseUnits = "43"
	changed.Signature = signQuote(t, changed.Quote)
	if _, err := service.Create(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed idempotent request error = %v", err)
	}
}

func TestCreateHasOneConcurrentNonceOwner(t *testing.T) {
	store := NewMemoryStore()
	var ids atomic.Uint64
	service := &Service{store: store, clock: func() time.Time { return time.Unix(1_800_000_000, 0) }, newID: func() (string, error) {
		return hash(ids.Add(100)), nil
	}}
	request := validRequest(t)
	const callers = 48
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		candidate := request
		candidate.IdempotencyKey = fmt.Sprintf("race_%d", index)
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Create(context.Background(), candidate)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	accepted, consumed := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrQuoteNonceConsumed):
			consumed++
		default:
			t.Fatalf("unexpected concurrent result %v", err)
		}
	}
	if accepted != 1 || consumed != callers-1 {
		t.Fatalf("accepted=%d consumed=%d", accepted, consumed)
	}
}

func TestPermanentTombstoneSurvivesSevenThirtyAnd365DayRestores(t *testing.T) {
	base := time.Unix(1_800_000_000, 0).UTC()
	now := base
	store := NewMemoryStore()
	service, _ := New(store, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{8}, 128)))
	request := validRequest(t)
	first, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{7 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour} {
		now = base.Add(age)
		restored, err := NewMemoryStoreFromTombstones(store.SnapshotTombstones())
		if err != nil {
			t.Fatal(err)
		}
		store = restored
		service, _ = New(store, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{9}, 128)))
		replayed, err := service.Create(context.Background(), request)
		if err != nil || !replayed.Replayed || replayed.OperationID != first.OperationID {
			t.Fatalf("age=%s replay=%+v err=%v", age, replayed, err)
		}
	}
	changed := request
	changed.Quote.AmountBaseUnits = "43"
	changed.Evidence.AmountBaseUnits = "43"
	changed.Signature = signQuote(t, changed.Quote)
	if _, err := service.Create(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request err=%v", err)
	}
	secondKey := request
	secondKey.IdempotencyKey = "intake_second_key"
	if _, err := service.Create(context.Background(), secondKey); !errors.Is(err, ErrQuoteNonceConsumed) {
		t.Fatalf("reused quote nonce err=%v", err)
	}
}

func TestAdaptationGrantIssuesIdempotentlyAndConsumesWithIntentAtomically(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := NewMemoryStore()
	key := quoteKey(t)
	var signerCalls atomic.Int32
	issuer, err := ascpadaptation.NewIssuer(adaptationSigner{key: key, calls: &signerCalls}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	grants, err := ascpadaptation.NewService(issuer, store)
	if err != nil {
		t.Fatal(err)
	}
	issue := ascpadaptation.IssueRequest{
		ReasonClass: ascpadaptation.ReasonTooExpensive, OriginalIntentID: hash(80), OrganizationID: "org_1", AgentID: "agent_1", TaskID: "task_1",
		AllowedCategory: "research", MaxAmountAtomic: "42", AllowedSellerSet: []string{hash(2)}, IssuedAt: now.Unix(),
	}
	firstGrant, err := grants.Issue(context.Background(), issue)
	if err != nil || firstGrant.Replayed {
		t.Fatalf("first grant=%+v err=%v", firstGrant, err)
	}
	replayedGrant, err := grants.Issue(context.Background(), issue)
	if err != nil || !replayedGrant.Replayed || replayedGrant.Artifact.Grant.GrantID != firstGrant.Artifact.Grant.GrantID {
		t.Fatalf("grant replay=%+v err=%v", replayedGrant, err)
	}
	changedIssue := issue
	changedIssue.MaxAmountAtomic = "41"
	if _, err := grants.Issue(context.Background(), changedIssue); !errors.Is(err, ascpadaptation.ErrIssueConflict) {
		t.Fatalf("changed issue error=%v", err)
	}
	if signerCalls.Load() != 1 {
		t.Fatalf("exact retry or conflict reached signer calls=%d", signerCalls.Load())
	}

	random := bytes.NewReader(bytes.Repeat([]byte{4}, 96))
	service, err := New(store, func() time.Time { return now }, random)
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(t)
	request.Adaptation = &firstGrant.Artifact
	request.AdaptationSigner = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	created, err := service.Create(context.Background(), request)
	if err != nil || created.AdaptationGrantID != firstGrant.Artifact.Grant.GrantID {
		t.Fatalf("adapted create=%+v err=%v", created, err)
	}
	consumed, err := store.GetGrant(context.Background(), "org_1", "agent_1", created.AdaptationGrantID)
	if err != nil || consumed.ConsumedOperationID != created.OperationID {
		t.Fatalf("consumed=%+v err=%v", consumed, err)
	}
	now = now.Add(31 * time.Minute)
	replay, err := service.Create(context.Background(), request)
	if err != nil || !replay.Replayed || replay.OperationID != created.OperationID {
		t.Fatalf("expired exact replay=%+v err=%v", replay, err)
	}
	now = time.Unix(1_800_000_060, 0).UTC()
	second := request
	second.IdempotencyKey = "adapted_second"
	second.Quote.QuoteNonce = hash(81)
	second.Signature = signQuote(t, second.Quote)
	if _, err := service.Create(context.Background(), second); !errors.Is(err, ascpadaptation.ErrGrantConsumed) {
		t.Fatalf("second grant use error=%v", err)
	}
}

func TestAdaptationGrantHasOneConcurrentIntentWinner(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := NewMemoryStore()
	key := quoteKey(t)
	issuer, _ := ascpadaptation.NewIssuer(adaptationSigner{key: key}, func() time.Time { return now })
	grants, _ := ascpadaptation.NewService(issuer, store)
	record, err := grants.Issue(context.Background(), ascpadaptation.IssueRequest{
		ReasonClass: ascpadaptation.ReasonWrongSeller, OriginalIntentID: hash(90), OrganizationID: "org_1", AgentID: "agent_1", TaskID: "task_1",
		AllowedCategory: "research", MaxAmountAtomic: "42", AllowedSellerSet: []string{hash(2)}, IssuedAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids atomic.Uint64
	service := &Service{store: store, clock: func() time.Time { return now }, newID: func() (string, error) { return hash(ids.Add(200)), nil }}
	const callers = 32
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		request := validRequest(t)
		request.IdempotencyKey = fmt.Sprintf("grant_race_%d", index)
		request.Quote.QuoteNonce = hash(uint64(300 + index))
		request.Signature = signQuote(t, request.Quote)
		request.Adaptation = &record.Artifact
		request.AdaptationSigner = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Create(context.Background(), request)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	winners, consumed := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ascpadaptation.ErrGrantConsumed):
			consumed++
		default:
			t.Fatalf("unexpected grant race error=%v", err)
		}
	}
	if winners != 1 || consumed != callers-1 {
		t.Fatalf("winners=%d consumed=%d", winners, consumed)
	}
}

func TestFailedValidationNeverClaimsNonce(t *testing.T) {
	store := NewMemoryStore()
	service := &Service{store: store, clock: func() time.Time { return time.Unix(1_800_000_000, 0) }, newID: func() (string, error) { return hash(77), nil }}
	request := validRequest(t)
	invalid := request
	invalid.Evidence.QuoteKeyRevoked = true
	if _, err := service.Create(context.Background(), invalid); !errors.Is(err, sellerquote.ErrDirectoryEvidence) {
		t.Fatalf("invalid evidence error = %v", err)
	}
	if operation, err := service.Create(context.Background(), request); err != nil || operation.OperationID != hash(77) {
		t.Fatalf("valid request after failed validation = %+v, %v", operation, err)
	}
	changed := request
	changed.RequestBody = []byte(`{"query":"another"}`)
	purchase, err := purchasespec.Build(purchasespec.Input{OrgID: "org_1", AgentID: "agent_1", TaskID: "task_1", Method: "POST", URL: "https://seller.example/v1/query", Body: changed.RequestBody, Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:result-v1"}, Category: "research"})
	if err != nil {
		t.Fatal(err)
	}
	changed.CanonicalPurchaseSpec = purchase.CanonicalJSON
	changed.IdempotencyKey = "intake_changed_purchase"
	if _, err := service.Create(context.Background(), changed); !errors.Is(err, ErrPurchaseSpecBinding) {
		t.Fatalf("purchase binding error = %v", err)
	}
}

func TestPostgresStorePersistsAndReplaysSameOperation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	input := storeInput(now)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO ascp_financial_tombstones`).WithArgs(
		input.Operation.OrganizationID, input.Operation.ActorID, Endpoint, LogicalOperation, input.IdempotencyKey, input.CanonicalInputHash,
		input.Operation.OperationID, input.Operation.QuoteHash, input.Operation.PurchaseSpecHash, input.Operation.QuoteNonce,
		int64(input.Operation.DirectoryVersion), input.Operation.DirectoryContract, input.Operation.SellerSigner, "", input.Operation.CreatedAt,
	).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(now))
	mock.ExpectQuery(`INSERT INTO ascp_intents`).WithArgs(
		input.Operation.OperationID, input.Operation.OrganizationID, input.Operation.ActorID, Endpoint, input.IdempotencyKey, input.CanonicalInputHash,
		input.Operation.QuoteHash, input.Operation.PurchaseSpecHash, input.Operation.QuoteNonce, int64(input.Operation.DirectoryVersion), input.Operation.DirectoryContract, input.Operation.SellerSigner,
		input.QuoteJSON, input.PurchaseSpecJSON, input.PurchaseSpecJSON, input.RequestBody, "", "", input.Operation.CreatedAt,
	).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(now))
	mock.ExpectCommit()
	created, replayed, err := store.Create(context.Background(), input)
	if err != nil || replayed || created.CreatedAt != now.Unix() {
		t.Fatalf("create=%+v replayed=%t err=%v", created, replayed, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO ascp_financial_tombstones`).WillReturnRows(sqlmock.NewRows([]string{"created_at"}))
	mock.ExpectQuery(`SELECT operation_id, organization_id, actor_id, quote_hash`).WithArgs("org_1", "agent_1", Endpoint, LogicalOperation, "intake_1").WillReturnRows(
		sqlmock.NewRows([]string{"operation_id", "organization_id", "actor_id", "quote_hash", "purchase_spec_hash", "quote_nonce", "directory_version", "directory_contract", "seller_signer", "adaptation_grant_id", "created_at", "canonical_input_hash"}).AddRow(
			input.Operation.OperationID, "org_1", "agent_1", input.Operation.QuoteHash, input.Operation.PurchaseSpecHash, input.Operation.QuoteNonce, int64(9), input.Operation.DirectoryContract, input.Operation.SellerSigner, "", now, input.CanonicalInputHash,
		),
	)
	mock.ExpectCommit()
	stored, replayed, err := store.Create(context.Background(), input)
	if err != nil || !replayed || stored.OperationID != input.Operation.OperationID {
		t.Fatalf("replay=%+v replayed=%t err=%v", stored, replayed, err)
	}
	mock.ExpectQuery(`SELECT operation_id, organization_id, actor_id, quote_hash`).
		WithArgs(input.Operation.OperationID, input.Operation.OrganizationID, input.Operation.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"operation_id", "organization_id", "actor_id", "quote_hash", "purchase_spec_hash", "quote_nonce", "directory_version", "directory_contract", "seller_signer", "adaptation_grant_id", "created_at"}).AddRow(
			input.Operation.OperationID, input.Operation.OrganizationID, input.Operation.ActorID, input.Operation.QuoteHash,
			input.Operation.PurchaseSpecHash, input.Operation.QuoteNonce, int64(9), input.Operation.DirectoryContract,
			input.Operation.SellerSigner, "", now,
		))
	read, err := store.Get(context.Background(), input.Operation.OrganizationID, input.Operation.ActorID, input.Operation.OperationID)
	if err != nil || read.OperationID != input.Operation.OperationID || read.CreatedAt != now.Unix() {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	mock.ExpectQuery(`SELECT operation_id, organization_id, actor_id, quote_hash`).
		WithArgs(input.Operation.OperationID, input.Operation.OrganizationID, "agent_other").
		WillReturnRows(sqlmock.NewRows([]string{"operation_id", "organization_id", "actor_id", "quote_hash", "purchase_spec_hash", "quote_nonce", "directory_version", "directory_contract", "seller_signer", "adaptation_grant_id", "created_at"}))
	if _, err := store.Get(context.Background(), input.Operation.OrganizationID, "agent_other", input.Operation.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-actor read error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresConflictClassificationPreservesOperationalFailures(t *testing.T) {
	duplicate := &pgconn.PgError{Code: "23505", ConstraintName: "ascp_intents_quote_nonce_unique"}
	if err := classifyInsertError(duplicate); !errors.Is(err, ErrQuoteNonceConsumed) {
		t.Fatalf("duplicate error = %v", err)
	}
	unavailable := errors.New("database unavailable")
	if err := classifyInsertError(unavailable); errors.Is(err, ErrQuoteNonceConsumed) || !errors.Is(err, unavailable) {
		t.Fatalf("operational error = %v", err)
	}
}

func validRequest(t *testing.T) Request {
	t.Helper()
	body := []byte(`{"query":"status"}`)
	purchase, err := purchasespec.Build(purchasespec.Input{OrgID: "org_1", AgentID: "agent_1", TaskID: "task_1", Method: "POST", URL: "https://seller.example/v1/query", Body: body, Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:result-v1"}, Category: "research"})
	if err != nil {
		t.Fatal(err)
	}
	quote := sellerquote.Quote{
		PurchaseSpecHash: purchase.PurchaseSpecHash, SellerID: hash(2), ResourceID: hash(3), DirectoryVersion: 9, SchemeVersion: 1,
		ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e", AmountBaseUnits: "42",
		PayTo: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444",
		VerificationSpecHash: hash(4), DeclaredWorkTime: 30, VerificationBudgetSeconds: 10, QuoteExpiresAt: 1_900_000_000, QuoteNonce: hash(5),
	}
	key := quoteKey(t)
	return Request{
		OrganizationID: "org_1", ActorID: "agent_1", IdempotencyKey: "intake_1", DirectoryContract: directoryContract,
		Quote: quote, Signature: sign(t, key, quote),
		Expected: sellerquote.ExpectedTerms{PurchaseSpecHash: quote.PurchaseSpecHash, SchemeVersion: quote.SchemeVersion, ChainID: quote.ChainID, Asset: quote.Asset},
		Evidence: sellerquote.DirectoryEvidence{Verified: true, Version: quote.DirectoryVersion, SellerID: quote.SellerID, ResourceID: quote.ResourceID,
			QuoteSigningKey: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), KeyEpoch: 1, PayoutAddress: quote.PayTo, AckAuthority: quote.AckAuthority,
			AmountBaseUnits: quote.AmountBaseUnits, VerificationSpecHash: quote.VerificationSpecHash, DeclaredWorkTime: quote.DeclaredWorkTime, VerificationBudgetSeconds: quote.VerificationBudgetSeconds, Active: true},
		CanonicalPurchaseSpec: purchase.CanonicalJSON, RequestBody: body,
	}
}

func quoteKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.ToECDSA(common.LeftPadBytes(big.NewInt(7).Bytes(), 32))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signQuote(t *testing.T, quote sellerquote.Quote) string { return sign(t, quoteKey(t), quote) }

func sign(t *testing.T, key *ecdsa.PrivateKey, quote sellerquote.Quote) string {
	t.Helper()
	digest, err := quote.Digest(directoryContract)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	return "0x" + hex.EncodeToString(signature)
}

type adaptationSigner struct {
	key   *ecdsa.PrivateKey
	calls *atomic.Int32
}

func (s adaptationSigner) SignDigest(ctx context.Context, digest []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.calls != nil {
		s.calls.Add(1)
	}
	return crypto.Sign(digest, s.key)
}

func hash(value uint64) string { return fmt.Sprintf("0x%064x", value) }

func storeInput(now time.Time) StoreInput {
	return StoreInput{Operation: Operation{
		OperationID: hash(99), OrganizationID: "org_1", ActorID: "agent_1", QuoteHash: hash(1), PurchaseSpecHash: hash(2), QuoteNonce: hash(3),
		DirectoryVersion: 9, DirectoryContract: directoryContract, SellerSigner: "0xd41c057fd1c78805aac12b0a94a405c0461a6fbb", CreatedAt: now.Unix(),
	}, IdempotencyKey: "intake_1", CanonicalInputHash: strings.Repeat("a", 64), QuoteJSON: []byte(`{"purchaseSpecHash":"0x01"}`), PurchaseSpecJSON: []byte(`{"orgId":"org_1"}`), RequestBody: []byte(`body`)}
}
