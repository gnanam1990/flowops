package ascpagent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const (
	testChainID   = uint64(84532)
	testAsset     = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testDirectory = "0x1111111111111111111111111111111111111111"
)

func TestCreateDerivesTrustedIdentityAndDirectoryEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	request, evidence := validCreateRequest(t, now)
	store := ascpintake.NewMemoryStore()
	intake, err := ascpintake.New(store, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{1}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	resolver := &stubDirectory{evidence: evidence}
	service, err := New(Config{Intake: intake, Reader: store, Directory: resolver, DirectoryContract: testDirectory, ChainID: testChainID, Asset: testAsset, SchemeVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{OrganizationID: "org_a", AgentID: "agent_a"}
	created, err := service.Create(context.Background(), identity, "idem_1", request)
	if err != nil {
		t.Fatal(err)
	}
	if created.OrganizationID != identity.OrganizationID || created.ActorID != identity.AgentID || created.DirectoryContract != testDirectory || created.PurchaseSpecHash != request.SellerQuote.PurchaseSpecHash || resolver.calls != 1 {
		t.Fatalf("operation=%+v resolverCalls=%d", created, resolver.calls)
	}
	replayed, err := service.Create(context.Background(), identity, "idem_1", request)
	if err != nil || !replayed.Replayed || replayed.OperationID != created.OperationID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	read, err := service.Get(context.Background(), identity, created.OperationID)
	if err != nil || read.OperationID != created.OperationID || read.Replayed {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	if _, err := service.Get(context.Background(), Identity{OrganizationID: "org_a", AgentID: "agent_b"}, created.OperationID); !errors.Is(err, ascpintake.ErrNotFound) {
		t.Fatalf("cross-agent read error = %v", err)
	}
	if _, err := service.Create(context.Background(), identity, "idem_2", request); !errors.Is(err, ascpintake.ErrQuoteNonceConsumed) {
		t.Fatalf("second logical operation error = %v", err)
	}
	now = now.Add(2 * time.Hour)
	resolver.err = errors.New("directory head advanced")
	lateReplay, err := service.Create(context.Background(), identity, "idem_1", request)
	if err != nil || !lateReplay.Replayed || lateReplay.OperationID != created.OperationID || resolver.calls != 2 {
		t.Fatalf("late replay=%+v err=%v resolverCalls=%d", lateReplay, err, resolver.calls)
	}
	rotatedResolver := &stubDirectory{err: errors.New("new directory has no head")}
	rotatedService, err := New(Config{Intake: intake, Reader: store, Directory: rotatedResolver, DirectoryContract: "0x9999999999999999999999999999999999999999", ChainID: testChainID, Asset: testAsset, SchemeVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	rotatedReplay, err := rotatedService.Create(context.Background(), identity, "idem_1", request)
	if err != nil || !rotatedReplay.Replayed || rotatedReplay.OperationID != created.OperationID || rotatedResolver.calls != 0 {
		t.Fatalf("rotated replay=%+v err=%v resolverCalls=%d", rotatedReplay, err, rotatedResolver.calls)
	}
}

func TestCreateRejectsUntrustedDeploymentAndBodyBindingsBeforeMutation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	request, evidence := validCreateRequest(t, now)
	store := ascpintake.NewMemoryStore()
	intake, _ := ascpintake.New(store, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{2}, 64)))
	resolver := &stubDirectory{evidence: evidence}
	service, _ := New(Config{Intake: intake, Reader: store, Directory: resolver, DirectoryContract: testDirectory, ChainID: testChainID, Asset: testAsset, SchemeVersion: 1})

	changed := request
	changed.SellerQuote.ChainID = "8453"
	if _, err := service.Create(context.Background(), Identity{OrganizationID: "org_a", AgentID: "agent_a"}, "idem_chain", changed); !errors.Is(err, ErrUnsupportedTerms) || resolver.calls != 0 {
		t.Fatalf("unsupported chain error=%v resolverCalls=%d", err, resolver.calls)
	}
	changed = request
	changed.RequestBodyBase64 = base64.StdEncoding.EncodeToString([]byte(`{"mutated":true}`))
	if _, err := service.Create(context.Background(), Identity{OrganizationID: "org_a", AgentID: "agent_a"}, "idem_body", changed); !errors.Is(err, ErrUnsupportedTerms) || resolver.calls != 0 {
		t.Fatalf("body mutation error=%v resolverCalls=%d", err, resolver.calls)
	}
	changed = request
	changed.RequestBodyBase64 = "not-base64"
	if _, err := service.Create(context.Background(), Identity{OrganizationID: "org_a", AgentID: "agent_a"}, "idem_encoding", changed); !errors.Is(err, ErrInvalidRequest) || resolver.calls != 0 {
		t.Fatalf("encoding error=%v resolverCalls=%d", err, resolver.calls)
	}
}

func TestCreateResolvesAdaptationGrantIDThroughAuthenticatedScope(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	request, evidence := validCreateRequest(t, now)
	store := ascpintake.NewMemoryStore()
	intake, err := ascpintake.New(store, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.HexToECDSA(strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ascpadaptation.NewIssuer(adaptationTestSigner{key: key}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	grants, err := ascpadaptation.NewService(issuer, store)
	if err != nil {
		t.Fatal(err)
	}
	record, err := grants.Issue(t.Context(), ascpadaptation.IssueRequest{
		ReasonClass: ascpadaptation.ReasonTooExpensive, OriginalIntentID: testHash(10),
		OrganizationID: "org_a", AgentID: "agent_a", TaskID: request.TaskID,
		AllowedCategory: request.Category, MaxAmountAtomic: request.SellerQuote.AmountBaseUnits,
		AllowedSellerSet: []string{request.SellerQuote.SellerID}, IssuedAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request.AdaptationGrantID = record.Artifact.Grant.GrantID
	signer := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	service, err := New(Config{
		Intake: intake, Reader: store, Directory: &stubDirectory{evidence: evidence},
		DirectoryContract: testDirectory, ChainID: testChainID, Asset: testAsset, SchemeVersion: 1,
		AdaptationSigner: signer, Adaptations: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), Identity{OrganizationID: "org_a", AgentID: "agent_a"}, "idem_adapted", request)
	if err != nil || created.AdaptationGrantID != request.AdaptationGrantID {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	request.AdaptationGrantID = "bad"
	if _, err := service.Create(t.Context(), Identity{OrganizationID: "org_a", AgentID: "agent_a"}, "idem_bad_grant", request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("malformed adaptation ID error=%v", err)
	}
	request.AdaptationGrantID = record.Artifact.Grant.GrantID
	if _, err := service.Create(t.Context(), Identity{OrganizationID: "org_a", AgentID: "agent_b"}, "idem_cross_agent", request); !errors.Is(err, ErrUnsupportedTerms) {
		t.Fatalf("cross-agent PurchaseSpec binding error=%v", err)
	}
	unconfigured, err := New(Config{Intake: intake, Reader: store, Directory: &stubDirectory{evidence: evidence}, DirectoryContract: testDirectory, ChainID: testChainID, Asset: testAsset, SchemeVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unconfigured.Create(t.Context(), Identity{OrganizationID: "org_a", AgentID: "agent_a"}, "idem_unconfigured", request); !errors.Is(err, ErrAdaptationUnavailable) {
		t.Fatalf("unconfigured adaptation error=%v", err)
	}
}

type adaptationTestSigner struct{ key *ecdsa.PrivateKey }

func (s adaptationTestSigner) SignDigest(_ context.Context, digest []byte) ([]byte, error) {
	return crypto.Sign(digest, s.key)
}

type stubDirectory struct {
	evidence sellerquote.DirectoryEvidence
	err      error
	calls    int
}

func (s *stubDirectory) EvidenceForQuote(context.Context, sellerquote.Quote) (string, sellerquote.DirectoryEvidence, error) {
	s.calls++
	return testDirectory, s.evidence, s.err
}

func validCreateRequest(t *testing.T, now time.Time) (CreateRequest, sellerquote.DirectoryEvidence) {
	t.Helper()
	body := []byte(`{"query":"proof"}`)
	spec, err := purchasespec.Build(purchasespec.Input{
		OrgID: "org_a", AgentID: "agent_a", TaskID: "task_1", Method: "POST", URL: "https://seller.example/v1/work",
		Body: body, Headers: []purchasespec.Header{{Name: "content-type", Value: "application/json"}},
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "urn:flowops:test"}, Category: "research",
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
		PurchaseSpecHash: spec.PurchaseSpecHash, SellerID: testHash(1), ResourceID: testHash(2), DirectoryVersion: 9,
		SchemeVersion: 1, ChainID: fmt.Sprint(testChainID), Asset: testAsset, AmountBaseUnits: "42",
		PayTo: "0x2222222222222222222222222222222222222222", AckAuthority: "0x3333333333333333333333333333333333333333",
		VerificationSpecHash: testHash(3), DeclaredWorkTime: 30, VerificationBudgetSeconds: 20,
		QuoteExpiresAt: uint64(now.Add(time.Hour).Unix()), QuoteNonce: testHash(4),
	}
	digest, err := quote.Digest(testDirectory)
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
	return CreateRequest{
		TaskID: "task_1", Method: "POST", URL: "https://seller.example/v1/work", RequestBodyBase64: base64.StdEncoding.EncodeToString(body),
		Headers:          []Header{{Name: "content-type", Value: "application/json"}},
		ResponseContract: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "urn:flowops:test"}, Category: "research",
		SellerQuote: quote, SellerQuoteSignature: "0x" + hex.EncodeToString(signature),
	}, evidence
}

func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
