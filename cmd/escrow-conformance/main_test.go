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
	"golang.org/x/crypto/sha3"
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

func TestCommittedEvidenceFetchFailureBindsRefusedDelivery(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "docs", "evidence", "call-escrow-evidence-fetch-refund-2026-08-14.failure.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(raw, []byte{'\n'}) || bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatal("committed failure evidence must be one canonical JSON line plus the repository line feed")
	}
	var failure struct {
		SchemaVersion            int    `json:"schemaVersion"`
		ChainID                  uint64 `json:"chainId"`
		Contract                 string `json:"contract"`
		Buyer                    string `json:"buyer"`
		Provider                 string `json:"provider"`
		AmountAtomic             string `json:"amountAtomic"`
		CallID                   string `json:"callId"`
		TaskJSON                 string `json:"taskJson"`
		TaskDigest               string `json:"taskDigest"`
		RequestJSON              string `json:"requestJson"`
		RequestDigest            string `json:"requestDigest"`
		HandlerStatus            int    `json:"handlerStatus"`
		Deliverable              bool   `json:"deliverable"`
		DeliverySubmitted        bool   `json:"deliverySubmitted"`
		OnchainStateAfterFailure string `json:"onchainStateAfterFailure"`
		ResponseDigest           string `json:"responseDigest"`
		EvidenceDigest           string `json:"evidenceDigest"`
		Error                    struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSuffix(raw, []byte{'\n'}), &failure); err != nil {
		t.Fatal(err)
	}
	const zeroDigest = "0x0000000000000000000000000000000000000000000000000000000000000000"
	if failure.SchemaVersion != 1 || failure.AmountAtomic != "100000" || failure.HandlerStatus != 502 || failure.Error.Code != "UPSTREAM_FAILURE" || failure.Error.Message != "upstream returned HTTP 404" {
		t.Fatalf("unexpected failure evidence: %+v", failure)
	}
	if failure.Deliverable || failure.DeliverySubmitted || failure.OnchainStateAfterFailure != "ACKNOWLEDGED" || failure.ResponseDigest != zeroDigest || failure.EvidenceDigest != zeroDigest {
		t.Fatalf("failed fetch was represented as delivery: %+v", failure)
	}
	keccak := func(value string) string {
		hash := sha3.NewLegacyKeccak256()
		_, _ = hash.Write([]byte(value))
		return fmt.Sprintf("0x%x", hash.Sum(nil))
	}
	if got := keccak(failure.TaskJSON); got != failure.TaskDigest {
		t.Fatalf("task digest = %s, want %s", got, failure.TaskDigest)
	}
	if got := keccak(failure.RequestJSON); got != failure.RequestDigest {
		t.Fatalf("request digest = %s, want %s", got, failure.RequestDigest)
	}
	var request struct {
		URL           string `json:"url"`
		RequestDigest string `json:"requestDigest"`
	}
	if err := json.Unmarshal([]byte(failure.RequestJSON), &request); err != nil {
		t.Fatal(err)
	}
	if request.URL != "https://www.rfc-editor.org/rfc/rfc999999.txt" || request.RequestDigest != failure.TaskDigest {
		t.Fatalf("request binding = %+v, task digest %s", request, failure.TaskDigest)
	}
	callID, err := reconciliation.DeriveEscrowCallID(failure.ChainID, failure.Contract, failure.Buyer, failure.TaskDigest, failure.RequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if callID != failure.CallID {
		t.Fatalf("call ID = %s, want %s", callID, failure.CallID)
	}
	manifest, err := readManifest(filepath.Join("..", "..", "docs", "evidence", "call-escrow-evidence-fetch-refund-2026-08-14.lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Transitions) != 3 || manifest.Transitions[0].Action != reconciliation.EscrowFund || manifest.Transitions[1].Action != reconciliation.EscrowAcknowledge || manifest.Transitions[2].Action != reconciliation.EscrowRefund || manifest.Transitions[2].RefundedFromState != 2 {
		t.Fatalf("committed failure lifecycle has unexpected path: %+v", manifest.Transitions)
	}
	for _, transition := range manifest.Transitions {
		if transition.CallID != failure.CallID || transition.TaskDigest != failure.TaskDigest || transition.RequestDigest != failure.RequestDigest || transition.AmountAtomic != failure.AmountAtomic || transition.Provider != failure.Provider {
			t.Fatalf("failure evidence does not bind transition: %+v", transition)
		}
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
