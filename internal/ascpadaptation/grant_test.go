package ascpadaptation

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestIssuerAndVerifierBindSingleUseGrantScope(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, err := crypto.HexToECDSA(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	signer := testSigner{key: key}
	issuer, err := NewIssuer(signer, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	signed, err := issuer.Issue(context.Background(), IssueRequest{
		ReasonClass: ReasonTooExpensive, OriginalIntentID: testHash(1), OrganizationID: "org_a", AgentID: "agent_a", TaskID: "task_a",
		AllowedCategory: "Research", MaxAmountAtomic: "100", AllowedSellerSet: []string{testHash(3), testHash(2)}, IssuedAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedSigner := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	use := Use{OrganizationID: "org_a", AgentID: "agent_a", TaskID: "task_a", Category: "research", AmountAtomic: "100", SellerID: testHash(2)}
	if err := Verify(signed, expectedSigner, now, use); err != nil {
		t.Fatal(err)
	}
	if signed.Grant.RemainingAttempts != 1 || signed.Grant.ExpiresAt-signed.Grant.IssuedAt != int64(MaximumLifetime/time.Second) || signed.Grant.AllowedSellerSet[0] != testHash(2) {
		t.Fatalf("grant=%+v", signed.Grant)
	}

	mutations := []struct {
		name  string
		edit  func(*SignedGrant, *Use, *time.Time, *string)
		error error
	}{
		{"organization", func(_ *SignedGrant, use *Use, _ *time.Time, _ *string) { use.OrganizationID = "org_b" }, ErrGrantScope},
		{"agent", func(_ *SignedGrant, use *Use, _ *time.Time, _ *string) { use.AgentID = "agent_b" }, ErrGrantScope},
		{"task", func(_ *SignedGrant, use *Use, _ *time.Time, _ *string) { use.TaskID = "task_b" }, ErrGrantScope},
		{"category", func(_ *SignedGrant, use *Use, _ *time.Time, _ *string) { use.Category = "media" }, ErrGrantScope},
		{"amount", func(_ *SignedGrant, use *Use, _ *time.Time, _ *string) { use.AmountAtomic = "101" }, ErrGrantScope},
		{"seller", func(_ *SignedGrant, use *Use, _ *time.Time, _ *string) { use.SellerID = testHash(9) }, ErrGrantScope},
		{"expired", func(_ *SignedGrant, _ *Use, now *time.Time, _ *string) { *now = now.Add(MaximumLifetime) }, ErrGrantScope},
		{"wrong-signer", func(_ *SignedGrant, _ *Use, _ *time.Time, signer *string) {
			*signer = "0x2222222222222222222222222222222222222222"
		}, ErrInvalidGrant},
		{"payload", func(grant *SignedGrant, _ *Use, _ *time.Time, _ *string) { grant.Grant.MaxAmountAtomic = "101" }, ErrInvalidGrant},
		{"signature", func(grant *SignedGrant, _ *Use, _ *time.Time, _ *string) {
			grant.Signature = "0x" + strings.Repeat("0", 130)
		}, ErrInvalidGrant},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changedGrant, changedUse, changedNow, changedSigner := signed, use, now, expectedSigner
			mutation.edit(&changedGrant, &changedUse, &changedNow, &changedSigner)
			if err := Verify(changedGrant, changedSigner, changedNow, changedUse); !errors.Is(err, mutation.error) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestIssuerAllowsOnlyAdaptiveReasonsAndCanonicalGrantShape(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, err := crypto.HexToECDSA(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	issuer, _ := NewIssuer(testSigner{key: key}, func() time.Time { return now })
	base := IssueRequest{OriginalIntentID: testHash(1), OrganizationID: "org_a", AgentID: "agent_a", TaskID: "task_a", AllowedCategory: "research", MaxAmountAtomic: "10", AllowedSellerSet: []string{testHash(2)}, IssuedAt: now.Unix()}
	for _, reason := range []ReasonClass{ReasonTooExpensive, ReasonWrongSeller} {
		request := base
		request.ReasonClass = reason
		if _, err := issuer.Issue(context.Background(), request); err != nil {
			t.Fatalf("reason=%s error=%v", reason, err)
		}
	}
	for _, reason := range []ReasonClass{ReasonInappropriate, ReasonNotNeeded, "unknown"} {
		request := base
		request.ReasonClass = reason
		if _, err := issuer.Issue(context.Background(), request); !errors.Is(err, ErrReasonIneligible) {
			t.Fatalf("reason=%s error=%v", reason, err)
		}
	}
	invalid := base
	invalid.ReasonClass = ReasonTooExpensive
	invalid.AllowedSellerSet = []string{testHash(2), testHash(2)}
	if _, err := issuer.Issue(context.Background(), invalid); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("duplicate seller error=%v", err)
	}
}

func TestIssuerDerivesOneArtifactAcrossConcurrentRetryWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, err := crypto.HexToECDSA(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewIssuer(testSigner{key: key}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := IssueRequest{
		ReasonClass: ReasonTooExpensive, OriginalIntentID: testHash(10), OrganizationID: "org_a", AgentID: "agent_a", TaskID: "task_a",
		AllowedCategory: "research", MaxAmountAtomic: "10", AllowedSellerSet: []string{testHash(11)}, IssuedAt: now.Unix(),
	}
	first, err := issuer.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	second, err := issuer.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := DigestHex(first.Grant)
	secondDigest, _ := DigestHex(second.Grant)
	if first.Grant.GrantID != second.Grant.GrantID || firstDigest != secondDigest || first.Signature != second.Signature {
		t.Fatalf("retry minted distinct artifacts: first=%+v second=%+v", first, second)
	}
}

type testSigner struct{ key *ecdsa.PrivateKey }

func (s testSigner) SignDigest(ctx context.Context, digest []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return crypto.Sign(digest, s.key)
}

func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
