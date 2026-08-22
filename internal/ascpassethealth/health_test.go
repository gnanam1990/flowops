package ascpassethealth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testAsset = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testImpl  = "0x1111111111111111111111111111111111111111"
)

func TestPausedBlacklistedTransferFailureAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Observation){
		"paused":           func(value *Observation) { value.Paused = true },
		"buyer blacklist":  func(value *Observation) { value.BuyerBlacklisted = true },
		"escrow blacklist": func(value *Observation) { value.EscrowBlacklisted = true },
		"transfer failure": func(value *Observation) { value.TransferFailure = true },
		"proxy upgrade":    func(value *Observation) { value.ProxyImplementation = "0x2222222222222222222222222222222222222222" },
		"code change":      func(value *Observation) { value.RuntimeCodeHash = hash(2) },
	} {
		t.Run(name, func(t *testing.T) {
			localNow := now
			localStore := NewMemoryStore()
			verifier := &recoveryVerifierStub{}
			localService, _ := New(testConfig(), localStore, verifier, func() time.Time { return localNow })
			observations := healthyObservations(localNow)
			for index := range observations {
				mutate(&observations[index])
			}
			record, err := localService.Observe(context.Background(), observations)
			if err != nil || record.State == Normal || record.State == Recovering {
				t.Fatalf("record=%+v err=%v", record, err)
			}
			localNow = localNow.Add(time.Minute)
			clean, err := localService.Observe(context.Background(), healthyObservations(localNow))
			if err != nil || clean.State != Recovering {
				t.Fatalf("clean=%+v err=%v", clean, err)
			}
			anchorDigest, anchorObservedAt := clean.EvidenceDigest, clean.ObservedAt
			localNow = localNow.Add(10 * time.Second)
			stillRecovering, err := localService.Observe(context.Background(), healthyObservations(localNow))
			if err != nil || stillRecovering.EvidenceDigest != anchorDigest || !stillRecovering.ObservedAt.Equal(anchorObservedAt) {
				t.Fatalf("recovery anchor moved: before=%+v after=%+v err=%v", clean, stillRecovering, err)
			}
			clean = stillRecovering
			verifier.proof = RecoveryProof{ChainID: clean.ChainID, Asset: clean.Asset, HealthEpoch: clean.Epoch}
			if _, err := localService.CompleteRecovery(context.Background()); !errors.Is(err, ErrRecoveryIncomplete) {
				t.Fatalf("partial proof err=%v", err)
			}
			verifier.proof = recoveryProofFor(clean, localNow)
			recovered, err := localService.CompleteRecovery(context.Background())
			if err != nil || recovered.State != Normal {
				t.Fatalf("recovered=%+v err=%v", recovered, err)
			}
		})
	}
}

func recoveryProofFor(record Record, reconciledAt time.Time) RecoveryProof {
	proof := RecoveryProof{ChainID: record.ChainID, Asset: record.Asset, HealthEpoch: record.Epoch,
		CleanEvidenceDigest: record.EvidenceDigest, CleanFinalizedBlock: record.FinalizedBlock, ReconciledAt: reconciledAt}
	proof.EvidenceDigest = recoveryEvidenceDigest(proof, recoveryCounts{})
	return proof
}

func TestQuorumDisagreementFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	observations := healthyObservations(now)
	observations[1].Paused = true
	if _, err := Evaluate(testConfig(), observations, now); !errors.Is(err, ErrObserverDisagreement) {
		t.Fatalf("disagreement err=%v", err)
	}
	observations = healthyObservations(now)
	observations[1].Provider = observations[0].Provider
	if _, err := Evaluate(testConfig(), observations, now); !errors.Is(err, ErrObserverDisagreement) {
		t.Fatalf("duplicate provider err=%v", err)
	}
	if _, err := Evaluate(testConfig(), observations[:1], now); !errors.Is(err, ErrQuorumUnavailable) {
		t.Fatalf("single provider err=%v", err)
	}
}

func testConfig() Config {
	return Config{ChainID: 84532, Asset: testAsset, ProxyImplementation: testImpl, RuntimeCodeHash: hash(1), Quorum: 2, MaxObservationAge: 2 * time.Minute}
}

func healthyObservations(now time.Time) []Observation {
	base := Observation{ChainID: 84532, Asset: testAsset, ProxyImplementation: testImpl, RuntimeCodeHash: hash(1), FinalizedBlock: 100,
		FinalizedBlockHash: hash(8), ObservedAt: now}
	left, right := base, base
	left.Provider, right.Provider = "provider-a", "provider-b"
	right.ObservedAt = now.Add(time.Second)
	return []Observation{left, right}
}

func hash(value byte) string {
	digits := "0123456789abcdef"
	return "0x" + strings.Repeat("0", 62) + string([]byte{digits[value>>4], digits[value&15]})
}

type recoveryVerifierStub struct {
	proof RecoveryProof
	err   error
}

func (s *recoveryVerifierStub) VerifyRecovery(context.Context, Record) (RecoveryProof, error) {
	return s.proof, s.err
}
