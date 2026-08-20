package sellerquote

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const directoryContract = "0x1111111111111111111111111111111111111111"

func TestSellerQuoteGoldenValues(t *testing.T) {
	quote, evidence, key := testQuote(t)
	domain, err := DomainSeparator(quote.ChainID, directoryContract)
	if err != nil {
		t.Fatal(err)
	}
	structHash, err := quote.StructHash()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := quote.Digest(directoryContract)
	if err != nil {
		t.Fatal(err)
	}
	signature := signQuote(t, key, digest)
	recovered, err := quote.RecoverSigner(directoryContract, signature)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recovered.Hex(), evidence.QuoteSigningKey; !strings.EqualFold(got, want) {
		t.Fatalf("recovered %s, want %s", got, want)
	}
	if got, want := domain.Hex(), "0x38fc76dc6879cd78bd1138e65f61e972f99be7612cd3661abc5d194886acc722"; got != want {
		t.Fatalf("domain separator %s, want %s", got, want)
	}
	if got, want := structHash.Hex(), "0x0152a523033e8ad13633a303e819b0c359174ac12574a86fec9ac22f9301462b"; got != want {
		t.Fatalf("struct hash %s, want %s", got, want)
	}
	if got, want := digest.Hex(), "0x8617e94d747d3126b442bd4911992ef5418109ee0b3f127fc031a49c6fbd141a"; got != want {
		t.Fatalf("digest %s, want %s", got, want)
	}
	if got, want := signature, "0xdbd5bc4c91ee830d56f40129d836df7977a133f17399d3c680633409e4b49e88325a2f03dc9e298b5458393fae9ae1e1c3fa37a4356d3e7e02b0ccba43174a2e01"; got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
}

func TestPublishedVectorAndIntegrityManifest(t *testing.T) {
	root := repositoryRoot(t)
	vectorBytes, err := os.ReadFile(filepath.Join(root, "vectors", "seller-quote-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		TypeString string `json:"typeString"`
		Domain     struct {
			ChainID           string `json:"chainId"`
			VerifyingContract string `json:"verifyingContract"`
			Separator         string `json:"separator"`
		} `json:"domain"`
		Quote           Quote  `json:"quote"`
		StructHash      string `json:"structHash"`
		Digest          string `json:"digest"`
		Signature       string `json:"signature"`
		RecoveredSigner string `json:"recoveredSigner"`
	}
	if err := json.Unmarshal(vectorBytes, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.TypeString != TypeString {
		t.Fatalf("vector type string drifted: %q", vector.TypeString)
	}
	domain, err := DomainSeparator(vector.Domain.ChainID, vector.Domain.VerifyingContract)
	if err != nil || domain.Hex() != vector.Domain.Separator {
		t.Fatalf("vector domain separator=%s err=%v", domain.Hex(), err)
	}
	structHash, err := vector.Quote.StructHash()
	if err != nil || structHash.Hex() != vector.StructHash {
		t.Fatalf("vector struct hash=%s err=%v", structHash.Hex(), err)
	}
	digest, err := vector.Quote.Digest(vector.Domain.VerifyingContract)
	if err != nil || digest.Hex() != vector.Digest {
		t.Fatalf("vector digest=%s err=%v", digest.Hex(), err)
	}
	signer, err := vector.Quote.RecoverSigner(vector.Domain.VerifyingContract, vector.Signature)
	if err != nil || !strings.EqualFold(signer.Hex(), vector.RecoveredSigner) {
		t.Fatalf("vector signer=%s err=%v", signer.Hex(), err)
	}

	manifest, err := os.ReadFile(filepath.Join(root, "artifacts", "seller-quote-v1.manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	expectedPaths := map[string]struct{}{"schemas/seller-quote.schema.json": {}, "vectors/seller-quote-v1.json": {}}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatalf("invalid manifest line %q", line)
		}
		if _, exists := expectedPaths[fields[1]]; !exists {
			t.Fatalf("unexpected manifest path %q", fields[1])
		}
		delete(expectedPaths, fields[1])
		contents, err := os.ReadFile(filepath.Join(root, fields[1]))
		if err != nil {
			t.Fatal(err)
		}
		if ArtifactSHA256(contents) != fields[0] {
			t.Fatalf("manifest hash mismatch for %s", fields[1])
		}
	}
	if len(expectedPaths) != 0 {
		t.Fatalf("manifest does not cover %v", expectedPaths)
	}
}

func TestIntakeAcceptRejectsSubstitutionExpiryAndRevocation(t *testing.T) {
	quote, evidence, key := testQuote(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	intake, err := NewIntake(NewMemoryClaimStore(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	digest, err := quote.Digest(directoryContract)
	if err != nil {
		t.Fatal(err)
	}
	request := IntakeRequest{
		OperationID:       hash(90),
		IdempotencyKey:    "intake-1",
		VerifyingContract: directoryContract,
		Quote:             quote,
		Signature:         signQuote(t, key, digest),
		Expected:          ExpectedTerms{PurchaseSpecHash: quote.PurchaseSpecHash, SchemeVersion: quote.SchemeVersion, ChainID: quote.ChainID, Asset: quote.Asset},
		Evidence:          evidence,
	}
	accepted, err := intake.Accept(request)
	if err != nil || accepted.Replayed || accepted.QuoteHash != digest.Hex() {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}

	changed := request
	changed.OperationID = hash(91)
	changed.IdempotencyKey = "intake-payout-substitution"
	changed.Evidence.PayoutAddress = "0x9999999999999999999999999999999999999999"
	if _, err := intake.Accept(changed); !errors.Is(err, ErrDirectoryEvidence) {
		t.Fatalf("payout substitution error = %v", err)
	}
	changed = request
	changed.OperationID = hash(95)
	changed.IdempotencyKey = "intake-resource-price-substitution"
	changed.Evidence.AmountBaseUnits = "43"
	if _, err := intake.Accept(changed); !errors.Is(err, ErrDirectoryEvidence) {
		t.Fatalf("resource-price substitution error = %v", err)
	}
	changed = request
	changed.OperationID = hash(96)
	changed.IdempotencyKey = "intake-scheme-substitution"
	changed.Expected.SchemeVersion = 2
	if _, err := intake.Accept(changed); !errors.Is(err, ErrDirectoryEvidence) {
		t.Fatalf("scheme substitution error = %v", err)
	}

	changed = request
	changed.OperationID = hash(92)
	changed.IdempotencyKey = "intake-revoked"
	changed.Evidence.QuoteKeyRevoked = true
	if _, err := intake.Accept(changed); !errors.Is(err, ErrDirectoryEvidence) {
		t.Fatalf("revoked key error = %v", err)
	}

	changed = request
	changed.OperationID = hash(93)
	changed.IdempotencyKey = "intake-domain-substitution"
	changed.VerifyingContract = "0x2222222222222222222222222222222222222222"
	if _, err := intake.Accept(changed); !errors.Is(err, ErrDirectoryEvidence) {
		t.Fatalf("domain substitution error = %v", err)
	}

	expired := request
	expired.OperationID = hash(94)
	expired.IdempotencyKey = "intake-expired"
	expired.Quote.QuoteExpiresAt = uint64(now.Unix())
	if _, err := intake.Accept(expired); !errors.Is(err, ErrQuoteExpired) {
		t.Fatalf("expiry error = %v", err)
	}
}

func TestIntakeReplayConflictAndConcurrentNonceWinner(t *testing.T) {
	quote, evidence, key := testQuote(t)
	digest, err := quote.Digest(directoryContract)
	if err != nil {
		t.Fatal(err)
	}
	request := IntakeRequest{
		OperationID: hash(100), IdempotencyKey: "same-key", VerifyingContract: directoryContract, Quote: quote,
		Signature: signQuote(t, key, digest),
		Expected:  ExpectedTerms{PurchaseSpecHash: quote.PurchaseSpecHash, SchemeVersion: quote.SchemeVersion, ChainID: quote.ChainID, Asset: quote.Asset}, Evidence: evidence,
	}
	intake, err := NewIntake(NewMemoryClaimStore(), func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if result, err := intake.Accept(request); err != nil || result.Replayed {
		t.Fatalf("first claim result=%+v err=%v", result, err)
	}
	if result, err := intake.Accept(request); err != nil || !result.Replayed {
		t.Fatalf("replay result=%+v err=%v", result, err)
	}
	conflict := request
	conflict.Quote.AmountBaseUnits = "43"
	conflict.Evidence.AmountBaseUnits = conflict.Quote.AmountBaseUnits
	conflict.Signature = signQuote(t, key, mustDigest(t, conflict.Quote, directoryContract))
	if _, err := intake.Accept(conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}

	concurrent, err := NewIntake(NewMemoryClaimStore(), func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	const callers = 48
	var wg sync.WaitGroup
	errorsOut := make(chan error, callers)
	for n := 0; n < callers; n++ {
		candidate := request
		candidate.OperationID = hash(uint64(200 + n))
		candidate.IdempotencyKey = "race-" + string(rune('a'+n))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := concurrent.Accept(candidate)
			errorsOut <- err
		}()
	}
	wg.Wait()
	close(errorsOut)
	accepted, consumed := 0, 0
	for err := range errorsOut {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrNonceConsumed):
			consumed++
		default:
			t.Fatalf("unexpected concurrent intake error: %v", err)
		}
	}
	if accepted != 1 || consumed != callers-1 {
		t.Fatalf("accepted=%d consumed=%d", accepted, consumed)
	}
}

func TestQuoteRejectsHighSSignatureAndNonCanonicalWireValues(t *testing.T) {
	quote, _, key := testQuote(t)
	digest := mustDigest(t, quote, directoryContract)
	signature := signQuote(t, key, digest)
	bytes, err := hex.DecodeString(signature[2:])
	if err != nil {
		t.Fatal(err)
	}
	s := new(big.Int).SetBytes(bytes[32:64])
	s.Sub(crypto.S256().Params().N, s)
	copy(bytes[32:64], common.LeftPadBytes(s.Bytes(), 32))
	bytes[64] ^= 1
	if _, err := quote.RecoverSigner(directoryContract, "0x"+hex.EncodeToString(bytes)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("high-S error = %v", err)
	}
	quote.Asset = strings.ToUpper(quote.Asset)
	if err := quote.Validate(); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("noncanonical address error = %v", err)
	}
}

func testQuote(t *testing.T) (Quote, DirectoryEvidence, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := crypto.ToECDSA(common.LeftPadBytes(big.NewInt(7).Bytes(), 32))
	if err != nil {
		t.Fatal(err)
	}
	quote := Quote{
		PurchaseSpecHash: hash(1), SellerID: hash(2), ResourceID: hash(3), DirectoryVersion: 9, SchemeVersion: 1,
		ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e", AmountBaseUnits: "42",
		PayTo: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444",
		VerificationSpecHash: hash(4), DeclaredWorkTime: 30, VerificationBudgetSeconds: 10, QuoteExpiresAt: 1_900_000_000, QuoteNonce: hash(5),
	}
	evidence := DirectoryEvidence{
		Verified: true, Version: quote.DirectoryVersion, SellerID: quote.SellerID, ResourceID: quote.ResourceID,
		QuoteSigningKey: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), KeyEpoch: 2, PayoutAddress: quote.PayTo, AckAuthority: quote.AckAuthority,
		AmountBaseUnits: quote.AmountBaseUnits, VerificationSpecHash: quote.VerificationSpecHash, DeclaredWorkTime: quote.DeclaredWorkTime, VerificationBudgetSeconds: quote.VerificationBudgetSeconds, Active: true,
	}
	return quote, evidence, key
}

func signQuote(t *testing.T, key *ecdsa.PrivateKey, digest common.Hash) string {
	t.Helper()
	signature, err := crypto.Sign(digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	return "0x" + hex.EncodeToString(signature)
}

func mustDigest(t *testing.T, quote Quote, contract string) common.Hash {
	t.Helper()
	digest, err := quote.Digest(contract)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func hash(value uint64) string { return fmt.Sprintf("0x%064x", value) }

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
