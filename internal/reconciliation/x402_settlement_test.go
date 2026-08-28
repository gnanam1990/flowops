package reconciliation

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

func testX402Claim(t *testing.T, expected ExpectedExecution) X402SettlementClaim {
	t.Helper()
	network := "eip155:8453"
	if expected.ChainID == 84532 {
		network = "eip155:84532"
	}
	authorization := envelope.Authorization{
		Version: envelope.Version, AuthorizationID: "auth_x402", OrganizationID: expected.OrganizationID,
		CustomerID: "customer_a", AgentID: expected.AgentID, TaskID: expected.TaskID, ActionID: "action_a",
		Rail: envelope.RailX402, ChainID: expected.ChainID, Recipient: expected.Recipient, Asset: expected.Asset,
		AmountAtomic: expected.AmountAtomic, Resource: "https://service.example/run", PolicyVersion: "policy_v1",
		Nonce: testHash(700), IssuedAt: 1, ExpiresAt: 4_000_000_000,
	}
	digest, err := authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(strings.Repeat("45", ed25519.SeedSize))
	privateKey := ed25519.NewKeyFromSeed(seed)
	signed, err := broadcastreceipt.Sign(broadcastreceipt.Receipt{
		Version: broadcastreceipt.Version, OrganizationID: authorization.OrganizationID, CustomerID: authorization.CustomerID,
		AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: "0x" + hex.EncodeToString(digest[:]),
		TransactionHash: expected.TransactionHash, Sender: expected.Sender, Outcome: broadcastreceipt.OutcomeAmbiguous, BroadcastAt: 2,
	}, "customer_signer_1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return X402SettlementClaim{
		Authorization: authorization, SignedReceipt: signed, PublicKeyB64: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
		Success: true, Payer: expected.Sender, Transaction: expected.TransactionHash,
		Network: network, Amount: expected.AmountAtomic,
	}
}

func TestRegisterX402SettlementIsDurableIdempotentAndChainFailClosed(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "x402.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	expected := testExpected()
	expected.ExecutionID = envelope.ExecutionID("auth_x402")
	claim := testX402Claim(t, expected)
	registered, err := engine.RegisterX402Settlement(context.Background(), expected, claim)
	if err != nil {
		t.Fatal(err)
	}
	if registered.State != ExecutionPendingChainRecovery || registered.X402SettlementClaim == nil {
		t.Fatalf("unhealthy registration = %+v", registered)
	}
	if replay, err := engine.RegisterX402Settlement(context.Background(), expected, claim); err != nil || replay.State != ExecutionPendingChainRecovery {
		t.Fatalf("idempotent replay = %+v, %v", replay, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, ok := restarted.Execution(expected.ExecutionID)
	if !ok || replayed.X402SettlementClaim == nil || replayed.X402SettlementClaim.Transaction != expected.TransactionHash {
		t.Fatalf("replayed x402 claim = %+v", replayed)
	}

	swapped := claim
	swapped.Amount = ""
	if _, err := restarted.RegisterX402Settlement(context.Background(), expected, swapped); err != ErrConflict {
		t.Fatalf("substitution error = %v, want %v", err, ErrConflict)
	}
}

func TestX402SettlementClaimCannotMasqueradeAsAnotherRail(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "x402-invalid.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	expected := testExpected()
	expected.ExecutionID = envelope.ExecutionID("auth_x402")
	claim := testX402Claim(t, expected)
	claim.Authorization.Rail = envelope.RailDirect
	if _, err := engine.RegisterX402Settlement(context.Background(), expected, claim); err == nil {
		t.Fatal("direct authorization entered x402 reconciliation")
	}
}

func TestX402SettlementClaimCannotRebindAuthorizationToAnotherExecution(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "x402-execution-binding.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	expected := testExpected()
	expected.ExecutionID = envelope.ExecutionID("auth_x402")
	claim := testX402Claim(t, expected)
	expected.ExecutionID = "exec_substituted"
	if _, err := engine.RegisterX402Settlement(context.Background(), expected, claim); err == nil {
		t.Fatal("x402 authorization was rebound to another execution identity")
	}
}
