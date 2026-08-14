package referencesigner

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	testUSDC      = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testRecipient = "0x1111111111111111111111111111111111111111"
)

type mutableGate struct {
	mu  sync.RWMutex
	err error
}

func (g *mutableGate) set(err error) { g.mu.Lock(); g.err = err; g.mu.Unlock() }
func (g *mutableGate) CheckChain(context.Context, uint64) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.err
}
func (g *mutableGate) CheckFrozen(context.Context, envelope.Authorization) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.err
}

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := hex.DecodeString(strings.Repeat("02", ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func testAuthorization(now time.Time) envelope.Authorization {
	return envelope.Authorization{
		Version:         envelope.Version,
		AuthorizationID: "auth_01k2flowops",
		OrganizationID:  "org_demo",
		CustomerID:      "cust_acme",
		AgentID:         "agent_research",
		TaskID:          "task_104",
		ActionID:        "action_fetch_1",
		Rail:            envelope.RailX402,
		ChainID:         84532,
		Recipient:       testRecipient,
		Asset:           testUSDC,
		AmountAtomic:    "1000",
		Resource:        "https://evidence.flowops.example/v1/fetch",
		PolicyVersion:   "policy_7",
		Nonce:           "0x" + strings.Repeat("ab", 32),
		IssuedAt:        now.Add(-time.Minute).Unix(),
		ExpiresAt:       now.Add(4 * time.Minute).Unix(),
	}
}

func newTestVerifier(t *testing.T, now time.Time, chainGate, freezeGate *mutableGate) (*Verifier, ed25519.PrivateKey, string) {
	t.Helper()
	publicKey, privateKey := testKeys(t)
	journal := filepath.Join(t.TempDir(), "nonces.log")
	store, err := OpenFileNonceStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	verifier, err := New(Config{
		OrganizationID: "org_demo", CustomerID: "cust_acme",
		TrustKeys:         map[string]ed25519.PublicKey{"flowops_control_1": publicKey},
		AllowedChainIDs:   []uint64{84532},
		AllowedRails:      []envelope.Rail{envelope.RailX402},
		AllowedAssets:     []string{testUSDC},
		AllowedRecipients: []string{testRecipient},
		MaxAmountAtomic:   "100000",
		MaxTTL:            10 * time.Minute,
		MaxFutureSkew:     30 * time.Second,
		Clock:             func() time.Time { return now },
		ChainGate:         chainGate,
		FreezeGate:        freezeGate,
		Nonces:            store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier, privateKey, journal
}

func signTest(t *testing.T, a envelope.Authorization, privateKey ed25519.PrivateKey) envelope.SignedAuthorization {
	t.Helper()
	signed, err := envelope.Sign(a, "flowops_control_1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestAuthorizeClaimsNonceExactlyOnceAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	chainGate, freezeGate := &mutableGate{}, &mutableGate{}
	verifier, privateKey, journal := newTestVerifier(t, now, chainGate, freezeGate)
	signed := signTest(t, testAuthorization(now), privateKey)

	authorized, err := verifier.Authorize(context.Background(), signed)
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Authorization.TaskID != "task_104" || !strings.HasPrefix(authorized.Digest, "0x") {
		t.Fatalf("unexpected authorized result: %+v", authorized)
	}
	if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, RefusalReplay) {
		t.Fatalf("replay error = %v, want %s", err, RefusalReplay)
	}
	if err := verifier.nonces.Close(); err != nil {
		t.Fatal(err)
	}

	publicKey, _ := testKeys(t)
	replayedStore, err := OpenFileNonceStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer replayedStore.Close()
	restarted, err := New(Config{
		OrganizationID: "org_demo", CustomerID: "cust_acme",
		TrustKeys: map[string]ed25519.PublicKey{"flowops_control_1": publicKey}, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailX402}, AllowedAssets: []string{testUSDC}, AllowedRecipients: []string{testRecipient},
		MaxAmountAtomic: "100000", MaxTTL: 10 * time.Minute, MaxFutureSkew: 30 * time.Second, Clock: func() time.Time { return now },
		ChainGate: chainGate, FreezeGate: freezeGate, Nonces: replayedStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Authorize(context.Background(), signed); !RefusalIs(err, RefusalReplay) {
		t.Fatalf("restart replay error = %v, want %s", err, RefusalReplay)
	}
}

func TestConcurrentReplayAdmitsExactlyOne(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	verifier, privateKey, _ := newTestVerifier(t, now, &mutableGate{}, &mutableGate{})
	signed := signTest(t, testAuthorization(now), privateKey)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := verifier.Authorize(context.Background(), signed); err == nil {
				admitted.Add(1)
			} else if !RefusalIs(err, RefusalReplay) {
				t.Errorf("unexpected refusal: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 1 {
		t.Fatalf("admitted %d, want exactly 1", got)
	}
}

func TestSignerRejectsValidFlowOpsEnvelopeForAnotherCustomer(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	verifier, privateKey, _ := newTestVerifier(t, now, &mutableGate{}, &mutableGate{})

	for name, mutate := range map[string]func(*envelope.Authorization){
		"organization": func(a *envelope.Authorization) { a.OrganizationID = "org_other" },
		"customer":     func(a *envelope.Authorization) { a.CustomerID = "cust_other" },
	} {
		t.Run(name, func(t *testing.T) {
			authorization := testAuthorization(now)
			mutate(&authorization)
			signed := signTest(t, authorization, privateKey)
			if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, RefusalIdentity) {
				t.Fatalf("identity substitution error = %v, want %s", err, RefusalIdentity)
			}
		})
	}
}

func TestFailedGatesDoNotBurnNonce(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	chainGate, freezeGate := &mutableGate{}, &mutableGate{}
	verifier, privateKey, _ := newTestVerifier(t, now, chainGate, freezeGate)
	signed := signTest(t, testAuthorization(now), privateKey)

	chainGate.set(errors.New("heads stopped"))
	if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, RefusalChainUnhealthy) {
		t.Fatalf("chain refusal = %v", err)
	}
	chainGate.set(nil)
	freezeGate.set(errors.New("task frozen"))
	if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, RefusalFrozen) {
		t.Fatalf("freeze refusal = %v", err)
	}
	freezeGate.set(nil)
	if _, err := verifier.Authorize(context.Background(), signed); err != nil {
		t.Fatalf("nonce was burned by a failed gate: %v", err)
	}
}

func TestLocalLimitsAndTimeFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*envelope.Authorization)
		code   RefusalCode
	}{
		{"future", func(a *envelope.Authorization) {
			a.IssuedAt = now.Add(time.Minute).Unix()
			a.ExpiresAt = now.Add(2 * time.Minute).Unix()
		}, RefusalNotYetValid},
		{"expired", func(a *envelope.Authorization) {
			a.IssuedAt = now.Add(-2 * time.Minute).Unix()
			a.ExpiresAt = now.Unix()
		}, RefusalExpired},
		{"ttl", func(a *envelope.Authorization) { a.ExpiresAt = a.IssuedAt + int64((11 * time.Minute).Seconds()) }, RefusalTTLTooLong},
		{"chain", func(a *envelope.Authorization) { a.ChainID = 8453 }, RefusalChain},
		{"rail", func(a *envelope.Authorization) {
			a.Rail = envelope.RailEscrow
			taskDigest := "0x" + strings.Repeat("31", 32)
			requestDigest := "0x" + strings.Repeat("42", 32)
			buyer := "0x3333333333333333333333333333333333333333"
			callID, err := envelope.DeriveEscrowCallID(a.ChainID, "0x4444444444444444444444444444444444444444", buyer, taskDigest, requestDigest)
			if err != nil {
				t.Fatal(err)
			}
			a.Escrow = &envelope.EscrowTerms{
				Contract: "0x4444444444444444444444444444444444444444", Buyer: buyer, Provider: a.Recipient,
				CallID: callID, TaskDigest: taskDigest, RequestDigest: requestDigest,
				AcknowledgeBy: uint64(now.Add(time.Hour).Unix()), DeliverBy: uint64(now.Add(2 * time.Hour).Unix()), ReleaseWindow: 3600,
			}
		}, RefusalRail},
		{"asset", func(a *envelope.Authorization) { a.Asset = "0x2222222222222222222222222222222222222222" }, RefusalAsset},
		{"recipient", func(a *envelope.Authorization) { a.Recipient = "0x2222222222222222222222222222222222222222" }, RefusalRecipient},
		{"amount", func(a *envelope.Authorization) { a.AmountAtomic = "100001" }, RefusalAmount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verifier, privateKey, _ := newTestVerifier(t, now, &mutableGate{}, &mutableGate{})
			a := testAuthorization(now)
			tc.mutate(&a)
			signed := signTest(t, a, privateKey)
			if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, tc.code) {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestSignatureAndTrustSubstitutionRefused(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	verifier, privateKey, _ := newTestVerifier(t, now, &mutableGate{}, &mutableGate{})
	signed := signTest(t, testAuthorization(now), privateKey)
	signed.Authorization.Recipient = "0x2222222222222222222222222222222222222222"
	if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, RefusalBadSignature) {
		t.Fatalf("substitution error = %v", err)
	}
	signed = signTest(t, testAuthorization(now), privateKey)
	signed.KeyID = "unknown_control"
	if _, err := verifier.Authorize(context.Background(), signed); !RefusalIs(err, RefusalUnknownTrustKey) {
		t.Fatalf("trust error = %v", err)
	}
}

func TestNonceJournalRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonces.log")
	if err := os.WriteFile(path, []byte("malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileNonceStore(path); err == nil {
		t.Fatal("corrupt journal opened")
	}
}

func TestNonceJournalRefusesConcurrentProcessOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonces.log")
	first, err := OpenFileNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := OpenFileNonceStore(path); err == nil {
		t.Fatal("second signer acquired the same nonce journal")
	}
}

func TestNonceJournalRejectsSymlinkAndUnsafePermissions(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileNonceStore(link); err == nil {
		t.Fatal("symlink nonce journal was accepted")
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileNonceStore(target); err == nil {
		t.Fatal("unsafe nonce journal permissions were accepted")
	}
}
