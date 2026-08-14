package reconciliation

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"
)

const (
	testEscrow      = "0x86e145397f58e71c134c0e054320db929483227a"
	testEscrowAsset = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testBuyer       = "0x079bdde909e28e437768a06d7001eb40896668d4"
	testProvider    = "0xc2f0967c4df966636e4ac1dad40abda65536cbb6"
)

func TestDeriveEscrowCallIDMatchesDeployedContractVector(t *testing.T) {
	t.Parallel()
	task := "0x57ebd2f8b793ad6146ee54d968aa1b7afe317acbcaeb33130e83517893c62e31"
	request := "0x2c1632c5be759c51f0389d73c9b92daae7d0e43ba5db495b075d1ce4d07de19e"
	got, err := DeriveEscrowCallID(84532, testEscrow, testBuyer, task, request)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0x79a66633e3992eb5e1f2b5afc4fdae0bcb79130dc7b3a4e197a7e5e3e861e71e"; got != want {
		t.Fatalf("DeriveEscrowCallID() = %s, want %s", got, want)
	}
}

func TestEscrowEventTopicsMatchContractABI(t *testing.T) {
	t.Parallel()
	for signature, want := range map[string]string{
		"CallFunded(bytes32,address,address,uint256,bytes32,bytes32,uint64,uint64)": escrowFundedTopic,
		"CallAcknowledged(bytes32,address)":                                         escrowAcknowledgedTopic,
		"DeliverySubmitted(bytes32,address,bytes32,bytes32,uint64,uint256)":         escrowDeliveredTopic,
		"Released(bytes32,address,uint256,bool)":                                    escrowReleasedTopic,
		"Refunded(bytes32,address,uint256,uint8)":                                   escrowRefundedTopic,
	} {
		hash := sha3.NewLegacyKeccak256()
		_, _ = hash.Write([]byte(signature))
		if got := "0x" + hex.EncodeToString(hash.Sum(nil)); got != want {
			t.Fatalf("topic %s = %s, want %s", signature, got, want)
		}
	}
}

func TestObserverSetDecodesCompleteEscrowReleaseLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	manifest := testEscrowManifest(t, EscrowRelease)
	receipts := make(map[string]*rpcReceipt)
	for index, transition := range manifest.Transitions {
		block := uint64(101 + index)
		receipts[transition.TransactionHash] = escrowReceiptFixture(transition, block, testHash(block))
	}
	fixtures := escrowRPCFixtures(now, receipts, 110)
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := VerifyEscrowLifecycle(context.Background(), observers, manifest)
	if err != nil || !result.Ready || result.FinalAction != EscrowRelease || len(result.Transitions) != 4 {
		t.Fatalf("VerifyEscrowLifecycle() = %+v, %v", result, err)
	}
	delivery := result.Transitions[2].CanonicalReceipt
	if delivery.DeliveredAt != 1_700_000_000 || delivery.ReleasableAt != 1_700_003_600 {
		t.Fatalf("delivery evidence = %+v", delivery)
	}
}

func TestObserverSetDecodesBothEscrowRefundPaths(t *testing.T) {
	t.Parallel()
	for _, terminal := range []struct {
		name  string
		state uint8
	}{
		{name: "missed acknowledgement", state: 1},
		{name: "missed delivery", state: 2},
	} {
		terminal := terminal
		t.Run(terminal.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
			manifest := testEscrowManifest(t, EscrowRefund)
			manifest.Transitions[len(manifest.Transitions)-1].RefundedFromState = terminal.state
			if terminal.state == 1 {
				manifest.Transitions = []EscrowExpectedReceipt{manifest.Transitions[0], manifest.Transitions[len(manifest.Transitions)-1]}
			} else {
				manifest.Transitions = []EscrowExpectedReceipt{manifest.Transitions[0], manifest.Transitions[1], manifest.Transitions[len(manifest.Transitions)-1]}
			}
			receipts := make(map[string]*rpcReceipt)
			for index, transition := range manifest.Transitions {
				block := uint64(201 + index)
				receipts[transition.TransactionHash] = escrowReceiptFixture(transition, block, testHash(block))
			}
			observers, err := NewObserverSet(84532, observerProviders(), rpcClient(escrowRPCFixtures(now, receipts, 210)), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			result, err := VerifyEscrowLifecycle(context.Background(), observers, manifest)
			if err != nil || !result.Ready || result.FinalAction != EscrowRefund {
				t.Fatalf("VerifyEscrowLifecycle() = %+v, %v", result, err)
			}
		})
	}
}

func TestEscrowReceiptRejectsSubstitutionDuplicateAndWrongLogOrder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	base := testEscrowManifest(t, EscrowRelease).Transitions[3]
	valid := escrowReceiptFixture(base, 101, testHash(101))
	tests := []struct {
		name   string
		mutate func(*rpcReceipt)
	}{
		{name: "provider substitution", mutate: func(receipt *rpcReceipt) { receipt.Logs[1].Topics[2] = addressWord(testBuyer) }},
		{name: "duplicate release", mutate: func(receipt *rpcReceipt) { receipt.Logs = append(receipt.Logs, receipt.Logs[1]) }},
		{name: "release before transfer", mutate: func(receipt *rpcReceipt) { receipt.Logs[0], receipt.Logs[1] = receipt.Logs[1], receipt.Logs[0] }},
		{name: "removed transfer", mutate: func(receipt *rpcReceipt) { receipt.Logs[0].Removed = true }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			receipt := cloneRPCReceipt(valid)
			test.mutate(receipt)
			fixtures := escrowRPCFixtures(now, map[string]*rpcReceipt{base.TransactionHash: receipt}, 110)
			observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			result := observers.EscrowReceiptQuorum(context.Background(), base)
			if len(result.Evidence) != 0 || len(result.Failures) != 2 {
				t.Fatalf("EscrowReceiptQuorum() = %+v", result)
			}
		})
	}
}

func TestEscrowLifecycleRejectsProviderDisagreementAndInsufficientConfirmations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	manifest := testEscrowManifest(t, EscrowRefund)
	manifest.Transitions = []EscrowExpectedReceipt{manifest.Transitions[0], manifest.Transitions[3]}
	manifest.Transitions[1].RefundedFromState = 1
	receipts := make(map[string]*rpcReceipt)
	for index, transition := range manifest.Transitions {
		block := uint64(101 + index)
		receipts[transition.TransactionHash] = escrowReceiptFixture(transition, block, testHash(block))
	}
	fixtures := escrowRPCFixtures(now, receipts, 103)
	fixtures["beta.rpc.example"].receipts = cloneReceiptMap(receipts)
	fixtures["beta.rpc.example"].receipts[manifest.Transitions[1].TransactionHash].BlockHash = testHash(999)
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if result, err := VerifyEscrowLifecycle(context.Background(), observers, manifest); err == nil || result.Ready {
		t.Fatalf("provider disagreement succeeded: %+v, %v", result, err)
	}

	fixtures = escrowRPCFixtures(now, receipts, 102)
	observers, err = NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	manifest.MinConfirmations = 2
	if result, err := VerifyEscrowLifecycle(context.Background(), observers, manifest); err == nil || result.Ready {
		t.Fatalf("insufficient confirmations succeeded: %+v, %v", result, err)
	}
}

func TestEscrowLifecycleRejectsInvalidStatePathAndZeroIdentityHash(t *testing.T) {
	t.Parallel()
	manifest := testEscrowManifest(t, EscrowRelease)
	manifest.Transitions[1], manifest.Transitions[2] = manifest.Transitions[2], manifest.Transitions[1]
	if err := manifest.Validate(); err == nil {
		t.Fatal("out-of-order lifecycle succeeded")
	}
	manifest = testEscrowManifest(t, EscrowRelease)
	manifest.Transitions[0].TaskDigest = zeroHash
	if err := manifest.Validate(); err == nil {
		t.Fatal("zero task digest succeeded")
	}
}

func testEscrowManifest(t *testing.T, terminal EscrowAction) EscrowLifecycleManifest {
	t.Helper()
	task := "0x57ebd2f8b793ad6146ee54d968aa1b7afe317acbcaeb33130e83517893c62e31"
	request := "0x2c1632c5be759c51f0389d73c9b92daae7d0e43ba5db495b075d1ce4d07de19e"
	callID, err := DeriveEscrowCallID(84532, testEscrow, testBuyer, task, request)
	if err != nil {
		t.Fatal(err)
	}
	base := EscrowExpectedReceipt{
		ChainID: 84532, Contract: testEscrow, Asset: testEscrowAsset, CallID: callID,
		Buyer: testBuyer, Provider: testProvider, AmountAtomic: "100000", TaskDigest: task, RequestDigest: request,
		AcknowledgeBy: 1_700_000_100, DeliverBy: 1_700_000_300, ReleaseWindow: 3600,
	}
	accepted := true
	fund := base
	fund.Action, fund.TransactionHash = EscrowFund, testHash(501)
	ack := base
	ack.Action, ack.TransactionHash = EscrowAcknowledge, testHash(502)
	deliver := base
	deliver.Action, deliver.TransactionHash = EscrowDeliver, testHash(503)
	deliver.ResponseDigest = testHash(601)
	deliver.EvidenceDigest = testHash(602)
	final := base
	final.Action, final.TransactionHash = terminal, testHash(504)
	if terminal == EscrowRelease {
		final.BuyerAccepted = &accepted
	} else {
		final.RefundedFromState = 1
	}
	return EscrowLifecycleManifest{SchemaVersion: 1, Network: "base-sepolia", MinConfirmations: 2, Transitions: []EscrowExpectedReceipt{fund, ack, deliver, final}}
}

func escrowReceiptFixture(expected EscrowExpectedReceipt, block uint64, blockHash string) *rpcReceipt {
	return &rpcReceipt{
		TransactionHash: expected.TransactionHash, TransactionIndex: "0x0", BlockNumber: fmt.Sprintf("0x%x", block), BlockHash: blockHash, Status: "0x1",
		Logs: escrowLogs(expected),
	}
}

func escrowLogs(expected EscrowExpectedReceipt) []rpcLog {
	topicAddress := func(address string) string { return addressWord(address) }
	word := func(value uint64) string { return strings.TrimPrefix(uint256Hex(newBig(value)), "0x") }
	hashWord := func(value string) string { return strings.TrimPrefix(value, "0x") }
	transfer := func(from, to string) rpcLog {
		return rpcLog{Address: expected.Asset, Topics: []string{transferTopic, topicAddress(from), topicAddress(to)}, Data: uint256Hex(mustBig(expected.AmountAtomic))}
	}
	switch expected.Action {
	case EscrowFund:
		return []rpcLog{
			transfer(expected.Buyer, expected.Contract),
			{Address: expected.Contract, Topics: []string{escrowFundedTopic, expected.CallID, topicAddress(expected.Buyer), topicAddress(expected.Provider)}, Data: "0x" + wordFromDecimal(expected.AmountAtomic) + hashWord(expected.TaskDigest) + hashWord(expected.RequestDigest) + word(expected.AcknowledgeBy) + word(expected.DeliverBy)},
		}
	case EscrowAcknowledge:
		return []rpcLog{{Address: expected.Contract, Topics: []string{escrowAcknowledgedTopic, expected.CallID, topicAddress(expected.Provider)}, Data: "0x"}}
	case EscrowDeliver:
		return []rpcLog{{Address: expected.Contract, Topics: []string{escrowDeliveredTopic, expected.CallID, topicAddress(expected.Provider)}, Data: "0x" + hashWord(expected.ResponseDigest) + hashWord(expected.EvidenceDigest) + word(1_700_000_000) + word(1_700_003_600)}}
	case EscrowRelease:
		accepted := uint64(0)
		if expected.BuyerAccepted != nil && *expected.BuyerAccepted {
			accepted = 1
		}
		return []rpcLog{
			transfer(expected.Contract, expected.Provider),
			{Address: expected.Contract, Topics: []string{escrowReleasedTopic, expected.CallID, topicAddress(expected.Provider)}, Data: "0x" + wordFromDecimal(expected.AmountAtomic) + word(accepted)},
		}
	case EscrowRefund:
		return []rpcLog{
			transfer(expected.Contract, expected.Buyer),
			{Address: expected.Contract, Topics: []string{escrowRefundedTopic, expected.CallID, topicAddress(expected.Buyer)}, Data: "0x" + wordFromDecimal(expected.AmountAtomic) + word(uint64(expected.RefundedFromState))},
		}
	default:
		return nil
	}
}

func escrowRPCFixtures(now time.Time, receipts map[string]*rpcReceipt, head uint64) map[string]*rpcFixture {
	return map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(head, now)}, receipts: cloneReceiptMap(receipts)},
		"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(head+1, now)}, receipts: cloneReceiptMap(receipts)},
	}
}

func cloneReceiptMap(input map[string]*rpcReceipt) map[string]*rpcReceipt {
	output := make(map[string]*rpcReceipt, len(input))
	for key, receipt := range input {
		output[key] = cloneRPCReceipt(receipt)
	}
	return output
}

func cloneRPCReceipt(input *rpcReceipt) *rpcReceipt {
	output := *input
	output.Logs = append([]rpcLog(nil), input.Logs...)
	for index := range output.Logs {
		output.Logs[index].Topics = append([]string(nil), input.Logs[index].Topics...)
	}
	return &output
}

func newBig(value uint64) *big.Int { return new(big.Int).SetUint64(value) }
func mustBig(value string) *big.Int {
	result, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid test integer")
	}
	return result
}
func wordFromDecimal(value string) string {
	return strings.TrimPrefix(uint256Hex(mustBig(value)), "0x")
}
