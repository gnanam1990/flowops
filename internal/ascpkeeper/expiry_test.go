package ascpkeeper

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type expirySourceFixture struct{ calls []ExpiredCall }

func (f *expirySourceFixture) Eligible(context.Context, int) ([]ExpiredCall, error) {
	return f.calls, nil
}

func TestExpiryScannerRequiresConfirmedChainTimeAndEnqueuesExactReplay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	call := ExpiredCall{OperationID: testHash(8), OrganizationID: "org-test", ChainID: 84532,
		Escrow: "0x2222222222222222222222222222222222222222", CallID: testHash(9), SettleBy: now.Add(-time.Minute), ObservedChainTime: now,
		ObservedAt: now, EvidenceDigest: testHash(10), Providers: []string{"base-primary", "base-secondary"}}
	source := &expirySourceFixture{calls: []ExpiredCall{call}}
	scanner, err := NewExpiryScanner(store, source, "keeper-primary", testGasPayer(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	created, err := scanner.Scan(context.Background(), 10)
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	created, err = scanner.Scan(context.Background(), 10)
	if err != nil || created != 0 {
		t.Fatalf("replay created=%d err=%v", created, err)
	}
	source.calls[0].EvidenceDigest = testHash(11)
	source.calls[0].ObservedAt = now.Add(30 * time.Second)
	created, err = scanner.Scan(context.Background(), 10)
	if err != nil || created != 0 {
		t.Fatalf("refreshed evidence replay created=%d err=%v", created, err)
	}
	job := store.jobs[expiredJobID(call)]
	expected := append(append([]byte(nil), claimExpiredSelector...), common.HexToHash(call.CallID).Bytes()...)
	if job.Action != ActionClaimExpired || job.SignerHandle != "" || !bytes.Equal(job.CanonicalPayload, expected) || job.EligibleAfter != call.SettleBy.Add(time.Second) {
		t.Fatalf("job=%+v", job)
	}
	if job.EligibilityEvidenceDigest != call.EvidenceDigest || !job.EligibilityObservedAt.Equal(call.ObservedAt) {
		t.Fatal("first eligibility proof was overwritten")
	}
}

func TestExpiryScannerRejectsWallClockOnlyOrSingleProviderEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	call := ExpiredCall{OperationID: testHash(8), OrganizationID: "org-test", ChainID: 84532, Escrow: "0x2222222222222222222222222222222222222222", CallID: testHash(9), SettleBy: now, ObservedChainTime: now, ObservedAt: now, EvidenceDigest: testHash(10), Providers: []string{"only-one"}}
	scanner, _ := NewExpiryScanner(newMemoryStore(now), &expirySourceFixture{calls: []ExpiredCall{call}}, "keeper-primary", testGasPayer(), func() time.Time { return now })
	if _, err := scanner.Scan(context.Background(), 10); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("error=%v", err)
	}
}

func TestClaimExpiredDecoderRejectsSelectorAndCallSubstitution(t *testing.T) {
	decoder := ClaimExpiredDecoder{}
	data := claimExpiredCalldata(testHash(9))
	decoded, err := decoder.Decode(context.Background(), ActionClaimExpired, data)
	if err != nil || !bytes.Equal(decoded.CanonicalPayload, data) {
		t.Fatal(err)
	}
	changed := append([]byte(nil), data...)
	changed[0] ^= 1
	if _, err := decoder.Decode(context.Background(), ActionClaimExpired, changed); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("selector error=%v", err)
	}
	if _, err := decoder.Decode(context.Background(), ActionClaimExpired, claimExpiredCalldata(testHash(0))); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("zero call error=%v", err)
	}
}
