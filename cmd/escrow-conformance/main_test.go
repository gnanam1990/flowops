package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

func TestReadManifestRejectsUnknownTrailingAndOversizedInput(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	for name, raw := range map[string]string{
		"unknown":  `{"schemaVersion":1,"unknown":true}`,
		"trailing": `{}` + `{}`,
		"oversize": string(make([]byte, maxManifestBytes+1)),
	} {
		path := filepath.Join(directory, name+".json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readManifest(path); err == nil {
			t.Fatalf("readManifest(%s) accepted invalid input", name)
		}
	}
}

func TestReadManifestAcceptsStrictValidatedLifecycle(t *testing.T) {
	t.Parallel()
	manifest := commandTestManifest(t)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readManifest(path)
	if err != nil || len(got.Transitions) != 2 {
		t.Fatalf("readManifest() = %+v, %v", got, err)
	}
}

func TestCommittedEvidenceFetchLifecycleBindsExactResponse(t *testing.T) {
	t.Parallel()
	evidenceDirectory := filepath.Join("..", "..", "docs", "evidence")
	manifest, err := readManifest(filepath.Join(evidenceDirectory, "call-escrow-evidence-fetch-2026-08-14.lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Transitions) != 4 || manifest.Transitions[2].Action != reconciliation.EscrowDeliver || manifest.Transitions[3].Action != reconciliation.EscrowRelease {
		t.Fatalf("committed lifecycle has unexpected path: %+v", manifest.Transitions)
	}

	raw, err := os.ReadFile(filepath.Join(evidenceDirectory, "call-escrow-evidence-fetch-2026-08-14.response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(raw, []byte{'\n'}) || bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatal("committed response must be one canonical JSON line plus the repository line feed")
	}
	canonical := bytes.TrimSuffix(raw, []byte{'\n'})
	var response struct {
		RequestDigest string `json:"requestDigest"`
		ContentSHA256 string `json:"contentSha256"`
	}
	if err := json.Unmarshal(canonical, &response); err != nil {
		t.Fatal(err)
	}
	delivery := manifest.Transitions[2]
	if response.RequestDigest != delivery.TaskDigest {
		t.Fatalf("response parent digest = %s, want task digest %s", response.RequestDigest, delivery.TaskDigest)
	}
	if response.ContentSHA256 != delivery.ResponseDigest {
		t.Fatalf("response digest = %s, want %s", response.ContentSHA256, delivery.ResponseDigest)
	}
	evidenceHash := sha256.Sum256(canonical)
	if got := fmt.Sprintf("0x%x", evidenceHash); got != delivery.EvidenceDigest {
		t.Fatalf("evidence digest = %s, want %s", got, delivery.EvidenceDigest)
	}
}

func TestParseProvidersRequiresExplicitPairs(t *testing.T) {
	t.Parallel()
	providers, err := parseProviders([]string{"alpha=https://alpha.example", "beta=https://beta.example"})
	if err != nil || len(providers) != 2 {
		t.Fatalf("parseProviders() = %+v, %v", providers, err)
	}
	if _, err := parseProviders([]string{"missing"}); err == nil {
		t.Fatal("invalid provider syntax succeeded")
	}
}

func commandTestManifest(t *testing.T) reconciliation.EscrowLifecycleManifest {
	t.Helper()
	contract := "0x86e145397f58e71c134c0e054320db929483227a"
	buyer := "0x079bdde909e28e437768a06d7001eb40896668d4"
	task := "0x57ebd2f8b793ad6146ee54d968aa1b7afe317acbcaeb33130e83517893c62e31"
	request := "0x2c1632c5be759c51f0389d73c9b92daae7d0e43ba5db495b075d1ce4d07de19e"
	callID, err := reconciliation.DeriveEscrowCallID(84532, contract, buyer, task, request)
	if err != nil {
		t.Fatal(err)
	}
	base := reconciliation.EscrowExpectedReceipt{
		ChainID: 84532, Contract: contract, Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e", CallID: callID,
		Buyer: buyer, Provider: "0xc2f0967c4df966636e4ac1dad40abda65536cbb6", AmountAtomic: "100000",
		TaskDigest: task, RequestDigest: request, AcknowledgeBy: 1_700_000_100, DeliverBy: 1_700_000_300, ReleaseWindow: 3600,
	}
	fund := base
	fund.Action, fund.TransactionHash = reconciliation.EscrowFund, "0x0000000000000000000000000000000000000000000000000000000000000001"
	refund := base
	refund.Action, refund.TransactionHash, refund.RefundedFromState = reconciliation.EscrowRefund, "0x0000000000000000000000000000000000000000000000000000000000000002", 1
	return reconciliation.EscrowLifecycleManifest{SchemaVersion: 1, Network: "base-sepolia", MinConfirmations: 2, Transitions: []reconciliation.EscrowExpectedReceipt{fund, refund}}
}
