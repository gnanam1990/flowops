package controlplane

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/referencesigner"
)

func reconciliationHash(number uint64) string { return fmt.Sprintf("0x%064x", number) }

func reconciliationObservations(now time.Time, anchor uint64) []reconciliation.Observation {
	return []reconciliation.Observation{
		{Provider: "rpc_alpha", ChainID: 84532, HeadNumber: anchor + 1, HeadHash: reconciliationHash(anchor + 1), HeadTime: now.Add(-time.Second), AnchorNumber: anchor, AnchorHash: reconciliationHash(anchor), AnchorTime: now.Add(-2 * time.Second), ObservedAt: now},
		{Provider: "rpc_beta", ChainID: 84532, HeadNumber: anchor + 2, HeadHash: reconciliationHash(anchor + 2), HeadTime: now.Add(-time.Second), AnchorNumber: anchor, AnchorHash: reconciliationHash(anchor), AnchorTime: now.Add(-2 * time.Second), ObservedAt: now},
	}
}

func recoverChain(t *testing.T, engine *reconciliation.Engine, clock *fakeClock, anchor uint64) {
	t.Helper()
	if _, err := engine.Observe(context.Background(), reconciliationObservations(clock.Now(), anchor)); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	status, err := engine.Observe(context.Background(), reconciliationObservations(clock.Now(), anchor+1))
	if err != nil || !status.ReadyForManualResume {
		t.Fatalf("recovery status = %+v, %v", status, err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); err != nil {
		t.Fatal(err)
	}
}

// TestSmokeChainHaltStopsBothAuthorizationBoundaries proves that the same
// durable Base state blocks FlowOps issuance and the customer signer, and that
// a pre-halt envelope cannot cross the recovery epoch.
func TestSmokeChainHaltStopsBothAuthorizationBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 11, 21, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	chain, err := reconciliation.Open(filepath.Join(t.TempDir(), "reconciliation.log"), reconciliation.Config{
		ChainID: 84532, ObserverQuorum: 2, HaltConfirmations: 2, RecoveryObservations: 2,
		MinConfirmations: 2, MaxHeadSkew: 2, StallThreshold: time.Minute,
		ObservationMaxAge: 20 * time.Second, MaxFutureClockSkew: 5 * time.Second, Clock: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Close()
	recoverChain(t, chain, clock, 500)

	freeze, version, ids := &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{}
	lifecycle, journal, controlPublicKey := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), clock, freeze, version, ids)
	defer journal.Close()
	lifecycle.chainGate = chain
	first, err := lifecycle.Submit(context.Background(), controlIntent("intent_before_halt", "100"))
	if err != nil {
		t.Fatal(err)
	}
	oldAuthorization, err := lifecycle.Issue(context.Background(), first.RequestID)
	if err != nil {
		t.Fatal(err)
	}

	nonces, err := referencesigner.OpenFileNonceStore(filepath.Join(t.TempDir(), "nonces.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer nonces.Close()
	signer, err := referencesigner.New(referencesigner.Config{
		OrganizationID: "org_demo", CustomerID: "cust_acme",
		TrustKeys:       map[string]ed25519.PublicKey{"flowops_control_1": controlPublicKey},
		AllowedChainIDs: []uint64{84532}, AllowedRails: []envelope.Rail{envelope.RailX402},
		AllowedAssets: []string{controlTestUSDC}, AllowedRecipients: []string{controlTestRecipient},
		MaxAmountAtomic: "200", MaxTTL: 5 * time.Minute, MaxFutureSkew: 30 * time.Second,
		Clock: clock.Now, ChainGate: chain, FreezeGate: healthySignerGates{}, Nonces: nonces,
	})
	if err != nil {
		t.Fatal(err)
	}

	clock.Add(time.Second)
	if _, err := chain.ForceHalt(context.Background(), "operator_alice", "drill"); err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Submit(context.Background(), controlIntent("intent_during_halt", "100"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Issue(context.Background(), second.RequestID); !errors.Is(err, reconciliation.ErrChainUnavailable) {
		t.Fatalf("control-plane issuance during halt = %v", err)
	}
	if _, err := signer.Authorize(context.Background(), oldAuthorization); !referencesigner.RefusalIs(err, referencesigner.RefusalChainUnhealthy) {
		t.Fatalf("customer signer during halt = %v", err)
	}

	clock.Add(time.Second)
	recoverChain(t, chain, clock, 510)
	if _, err := signer.Authorize(context.Background(), oldAuthorization); !referencesigner.RefusalIs(err, referencesigner.RefusalChainUnhealthy) {
		t.Fatalf("pre-halt authorization crossed recovery epoch: %v", err)
	}
	if _, err := lifecycle.Issue(context.Background(), first.RequestID); !errors.Is(err, reconciliation.ErrChainUnavailable) {
		t.Fatalf("control plane re-delivered a pre-halt authorization: %v", err)
	}
	newAuthorization, err := lifecycle.Issue(context.Background(), second.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Authorize(context.Background(), newAuthorization); err != nil {
		t.Fatalf("post-recovery authorization failed: %v", err)
	}
}
