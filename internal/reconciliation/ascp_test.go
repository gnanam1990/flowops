package reconciliation

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"
)

const (
	testASCPOperation  = "0x57ebd2f8b793ad6146ee54d968aa1b7afe317acbcaeb33130e83517893c62e31"
	testASCPCommitment = "0x2c1632c5be759c51f0389d73c9b92daae7d0e43ba5db495b075d1ce4d07de19e"
	testASCPCall       = "0x79a66633e3992eb5e1f2b5afc4fdae0bcb79130dc7b3a4e197a7e5e3e861e71e"
)

func TestASCPEventTopicsMatchContractABI(t *testing.T) {
	t.Parallel()
	for signature, want := range map[string]string{
		"CallLocked(bytes32,bytes32,bytes32,address,address,uint256,uint64)":    ascpCallLockedTopic,
		"CallRefunded(bytes32,bytes32,address,uint256)":                         ascpCallRefundedTopic,
		"CallAcked(bytes32,bytes32)":                                            ascpCallAckedTopic,
		"CallReleased(bytes32,bytes32,bytes32,bytes32,bytes32,address,uint256)": ascpCallReleasedTopic,
	} {
		hash := sha3.NewLegacyKeccak256()
		_, _ = hash.Write([]byte(signature))
		if got := "0x" + hex.EncodeToString(hash.Sum(nil)); got != want {
			t.Fatalf("topic %s = %s, want %s", signature, got, want)
		}
	}
}

func TestObserverSetDecodesExactASCPPaymentLifecycleReceipts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 5, 0, 0, 0, time.UTC)
	for index, action := range []ASCPReceiptAction{ASCPReceiptLock, ASCPReceiptRelease, ASCPReceiptRefund} {
		expected := testASCPExpected(action)
		block := uint64(101 + index)
		receipt := ascpReceiptFixture(expected, block, testHash(block))
		observers, err := NewObserverSet(84532, observerProviders(), rpcClient(escrowRPCFixtures(now,
			map[string]*rpcReceipt{expected.TransactionHash: receipt}, 110)), func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		result := observers.ASCPReceiptQuorum(context.Background(), expected)
		if len(result.Evidence) != 2 || len(result.Failures) != 0 {
			t.Fatalf("%s ASCPReceiptQuorum() = %+v", action, result)
		}
		for _, evidence := range result.Evidence {
			if !evidence.Success || evidence.Action != action || evidence.OperationID != expected.OperationID ||
				evidence.CallID != expected.CallID || evidence.BlockNumber != block {
				t.Fatalf("%s evidence = %+v", action, evidence)
			}
		}
	}
}

func TestASCPReceiptRejectsSubstitutionDuplicateAndWrongLogOrder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 5, 0, 0, 0, time.UTC)
	base := testASCPExpected(ASCPReceiptLock)
	valid := ascpReceiptFixture(base, 101, testHash(101))
	tests := []struct {
		name   string
		mutate func(*rpcReceipt)
	}{
		{name: "operation substitution", mutate: func(receipt *rpcReceipt) { receipt.Logs[1].Topics[2] = testHash(999) }},
		{name: "commitment substitution", mutate: func(receipt *rpcReceipt) { receipt.Logs[1].Topics[3] = testHash(999) }},
		{name: "amount substitution", mutate: func(receipt *rpcReceipt) { receipt.Logs[0].Data = uint256Hex(newBig(999)) }},
		{name: "duplicate lifecycle", mutate: func(receipt *rpcReceipt) { receipt.Logs = append(receipt.Logs, receipt.Logs[1]) }},
		{name: "another lifecycle for call", mutate: func(receipt *rpcReceipt) {
			receipt.Logs = append(receipt.Logs, rpcLog{Address: base.Contract, Topics: []string{ascpCallAckedTopic, base.CallID, base.OperationID}, Data: "0x"})
		}},
		{name: "event before transfer", mutate: func(receipt *rpcReceipt) { receipt.Logs[0], receipt.Logs[1] = receipt.Logs[1], receipt.Logs[0] }},
		{name: "removed transfer", mutate: func(receipt *rpcReceipt) { receipt.Logs[0].Removed = true }},
		{name: "reverted with logs", mutate: func(receipt *rpcReceipt) { receipt.Status = "0x0" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			receipt := cloneRPCReceipt(valid)
			test.mutate(receipt)
			observers, err := NewObserverSet(84532, observerProviders(), rpcClient(escrowRPCFixtures(now,
				map[string]*rpcReceipt{base.TransactionHash: receipt}, 110)), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			result := observers.ASCPReceiptQuorum(context.Background(), base)
			if len(result.Evidence) != 0 || len(result.Failures) != 2 {
				t.Fatalf("ASCPReceiptQuorum() = %+v", result)
			}
		})
	}
}

func TestASCPRevertedReceiptRequiresNoCanonicalLogs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 5, 0, 0, 0, time.UTC)
	expected := testASCPExpected(ASCPReceiptLock)
	receipt := ascpReceiptFixture(expected, 101, testHash(101))
	receipt.Status, receipt.Logs = "0x0", nil
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(escrowRPCFixtures(now,
		map[string]*rpcReceipt{expected.TransactionHash: receipt}, 110)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := observers.ASCPReceiptQuorum(context.Background(), expected)
	if len(result.Evidence) != 2 || result.Evidence[0].Success || result.Evidence[1].Success {
		t.Fatalf("ASCPReceiptQuorum() = %+v", result)
	}
}

func testASCPExpected(action ASCPReceiptAction) ASCPExpectedReceipt {
	expected := ASCPExpectedReceipt{
		Action: action, TransactionHash: testHash(700 + uint64(len(action))), ChainID: 84532,
		Contract: testEscrow, Asset: testEscrowAsset, CallID: testASCPCall,
		OperationID: testASCPOperation, CommitmentHash: testASCPCommitment,
		Buyer: testBuyer, PayTo: testProvider, AmountAtomic: "100000", SettleBy: 1_800_000_000,
	}
	if action == ASCPReceiptRelease {
		expected.DeliveryHash, expected.EvidenceHash = testHash(801), testHash(802)
	}
	return expected
}

func ascpReceiptFixture(expected ASCPExpectedReceipt, block uint64, blockHash string) *rpcReceipt {
	return &rpcReceipt{
		TransactionHash: expected.TransactionHash, TransactionIndex: "0x0", BlockNumber: fmt.Sprintf("0x%x", block),
		BlockHash: blockHash, Status: "0x1", Logs: ascpLogs(expected),
	}
}

func ascpLogs(expected ASCPExpectedReceipt) []rpcLog {
	transfer := func(from, to string) rpcLog {
		return rpcLog{Address: expected.Asset, Topics: []string{transferTopic, addressWord(from), addressWord(to)}, Data: uint256Hex(mustBig(expected.AmountAtomic))}
	}
	switch expected.Action {
	case ASCPReceiptLock:
		return []rpcLog{
			transfer(expected.Buyer, expected.Contract),
			{Address: expected.Contract, Topics: []string{ascpCallLockedTopic, expected.CallID, expected.OperationID, expected.CommitmentHash},
				Data: "0x" + addressWord(expected.Buyer)[2:] + addressWord(expected.PayTo)[2:] + wordFromDecimal(expected.AmountAtomic) + fmt.Sprintf("%064x", expected.SettleBy)},
		}
	case ASCPReceiptRelease:
		return []rpcLog{
			transfer(expected.Contract, expected.PayTo),
			{Address: expected.Contract, Topics: []string{ascpCallReleasedTopic, expected.CallID, expected.OperationID, expected.CommitmentHash},
				Data: "0x" + expected.DeliveryHash[2:] + expected.EvidenceHash[2:] + addressWord(expected.PayTo)[2:] + wordFromDecimal(expected.AmountAtomic)},
		}
	case ASCPReceiptRefund:
		return []rpcLog{
			transfer(expected.Contract, expected.Buyer),
			{Address: expected.Contract, Topics: []string{ascpCallRefundedTopic, expected.CallID, expected.OperationID, addressWord(expected.Buyer)},
				Data: uint256Hex(mustBig(expected.AmountAtomic))},
		}
	default:
		return nil
	}
}
