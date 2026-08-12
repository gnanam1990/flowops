package controlplane

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/referencesigner"
)

type healthySignerGates struct{}

func (healthySignerGates) CheckChain(context.Context, uint64) error                  { return nil }
func (healthySignerGates) CheckFrozen(context.Context, envelope.Authorization) error { return nil }

// TestSmokeApprovalToCustomerSigner exercises the first complete FlowOps vertical
// slice: policy -> reservation -> human approval -> signed capability -> independent
// customer signer -> durable nonce claim -> replay refusal.
func TestSmokeApprovalToCustomerSigner(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	clock, freeze, version, ids := &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{}
	lifecycle, journal, controlPublicKey := newLifecycleForTest(
		t, filepath.Join(t.TempDir(), "control.log"), clock, freeze, version, ids,
	)
	defer journal.Close()

	record, err := lifecycle.Submit(context.Background(), controlIntent("intent_smoke", "150"))
	if err != nil {
		t.Fatal(err)
	}
	record, err = lifecycle.Decide(
		context.Background(), record.RequestID, record.RequestDigest, Approve, "owner_alice", "smoke approval",
	)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil {
		t.Fatal(err)
	}

	nonces, err := referencesigner.OpenFileNonceStore(filepath.Join(t.TempDir(), "signer-nonces.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer nonces.Close()
	customerSigner, err := referencesigner.New(referencesigner.Config{
		OrganizationID: "org_demo", CustomerID: "cust_acme",
		TrustKeys:       map[string]ed25519.PublicKey{"flowops_control_1": controlPublicKey},
		AllowedChainIDs: []uint64{84532}, AllowedRails: []envelope.Rail{envelope.RailX402},
		AllowedAssets: []string{controlTestUSDC}, AllowedRecipients: []string{controlTestRecipient},
		MaxAmountAtomic: "200", MaxTTL: 5 * time.Minute, MaxFutureSkew: 30 * time.Second,
		Clock: func() time.Time { return now }, ChainGate: healthySignerGates{}, FreezeGate: healthySignerGates{}, Nonces: nonces,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := customerSigner.Authorize(context.Background(), signed)
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Authorization.TaskID != "task_104" || authorized.Authorization.AmountAtomic != "150" {
		t.Fatalf("customer signer authorized altered terms: %+v", authorized)
	}
	if _, err := customerSigner.Authorize(context.Background(), signed); !referencesigner.RefusalIs(err, referencesigner.RefusalReplay) {
		t.Fatalf("second authorization = %v, want replay refusal", err)
	}
}
