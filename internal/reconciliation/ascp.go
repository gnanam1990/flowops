package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
)

const (
	ascpCallLockedTopic   = "0xb2df740bde72866ac7528fb249f61f95507deefbc5336b232f9e02dc5fb4be3b"
	ascpCallRefundedTopic = "0x2bb01af821732d10c1151893f7eb5aaa456c50bd3149a8e6f8eaaa7667c74e25"
	ascpCallAckedTopic    = "0x3bdde970a74a9f74c0cad87f73dfeccec06e82192c4e26d467b79c193a61afb8"
	ascpCallReleasedTopic = "0x147a936b9d902018126f66c353a75c8bc1bb3d4f7190f509cceddd54bbf32f02"
)

type ASCPReceiptAction string

const (
	ASCPReceiptLock    ASCPReceiptAction = "LOCK"
	ASCPReceiptRelease ASCPReceiptAction = "RELEASE"
	ASCPReceiptRefund  ASCPReceiptAction = "REFUND"
)

// ASCPExpectedReceipt repeats every economic and identity field needed to
// recognize one ASCPCallEscrow transition. A transaction hash by itself never
// proves a lock, release, or refund.
type ASCPExpectedReceipt struct {
	Action          ASCPReceiptAction `json:"action"`
	TransactionHash string            `json:"transactionHash"`
	ChainID         uint64            `json:"chainId"`
	Contract        string            `json:"contract"`
	Asset           string            `json:"asset"`
	CallID          string            `json:"callId"`
	OperationID     string            `json:"operationId"`
	CommitmentHash  string            `json:"commitmentHash"`
	Buyer           string            `json:"buyer"`
	PayTo           string            `json:"payTo"`
	AmountAtomic    string            `json:"amountAtomic"`
	SettleBy        uint64            `json:"settleBy"`
	DeliveryHash    string            `json:"deliveryHash,omitempty"`
	EvidenceHash    string            `json:"evidenceHash,omitempty"`
}

func (e ASCPExpectedReceipt) Validate() error {
	if e.Action != ASCPReceiptLock && e.Action != ASCPReceiptRelease && e.Action != ASCPReceiptRefund ||
		(e.ChainID != 8453 && e.ChainID != 84532) || !hashPattern.MatchString(e.TransactionHash) ||
		!hashPattern.MatchString(e.CallID) || e.CallID == zeroHash || !hashPattern.MatchString(e.OperationID) ||
		e.OperationID == zeroHash || !hashPattern.MatchString(e.CommitmentHash) || e.CommitmentHash == zeroHash ||
		!addressPattern.MatchString(e.Contract) || !addressPattern.MatchString(e.Asset) ||
		!addressPattern.MatchString(e.Buyer) || !addressPattern.MatchString(e.PayTo) || e.SettleBy == 0 {
		return errors.New("expected ASCP receipt is invalid")
	}
	if _, err := positiveInteger(e.AmountAtomic); err != nil {
		return fmt.Errorf("expected ASCP receipt amount: %w", err)
	}
	if e.Action == ASCPReceiptRelease {
		if !hashPattern.MatchString(e.DeliveryHash) || e.DeliveryHash == zeroHash ||
			!hashPattern.MatchString(e.EvidenceHash) || e.EvidenceHash == zeroHash {
			return errors.New("ASCP release requires delivery and evidence hashes")
		}
	} else if e.DeliveryHash != "" || e.EvidenceHash != "" {
		return errors.New("ASCP non-release receipt contains release-only fields")
	}
	return nil
}

type ASCPReceiptEvidence struct {
	Provider        string            `json:"provider"`
	Action          ASCPReceiptAction `json:"action"`
	ChainID         uint64            `json:"chainId"`
	TransactionHash string            `json:"transactionHash"`
	BlockNumber     uint64            `json:"blockNumber"`
	BlockHash       string            `json:"blockHash"`
	ConfirmedHead   uint64            `json:"confirmedHead"`
	Success         bool              `json:"success"`
	CallID          string            `json:"callId"`
	OperationID     string            `json:"operationId"`
}

type ASCPReceiptResult struct {
	Evidence []ASCPReceiptEvidence `json:"evidence"`
	Failures map[string]string     `json:"failures,omitempty"`
}

// ASCPCanonicalBlockQuorum reuses the independent canonical-block observer for
// an ASCP payment attempt without exposing the observer set's raw providers.
func (s *ObserverSet) ASCPCanonicalBlockQuorum(ctx context.Context, transactionHash string, blockNumber uint64, blockHash string) ReorgResult {
	return s.canonicalBlockQuorum(ctx, transactionHash, blockNumber, blockHash)
}

func (s *ObserverSet) ASCPReceiptQuorum(ctx context.Context, expected ASCPExpectedReceipt) ASCPReceiptResult {
	if err := expected.Validate(); err != nil || expected.ChainID != s.chainID {
		message := "expected ASCP receipt uses the wrong configured chain"
		if err != nil {
			message = err.Error()
		}
		return ASCPReceiptResult{Failures: map[string]string{"validation": message}}
	}
	type providerResult struct {
		provider string
		evidence ASCPReceiptEvidence
		err      error
	}
	results := make(chan providerResult, len(s.providers))
	var group sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		group.Add(1)
		go func() {
			defer group.Done()
			evidence, err := s.ascpReceipt(ctx, provider, expected)
			results <- providerResult{provider.Name, evidence, err}
		}()
	}
	group.Wait()
	close(results)
	output := ASCPReceiptResult{Failures: make(map[string]string)}
	for result := range results {
		if result.err != nil {
			output.Failures[result.provider] = result.err.Error()
			continue
		}
		output.Evidence = append(output.Evidence, result.evidence)
	}
	sort.Slice(output.Evidence, func(i, j int) bool { return output.Evidence[i].Provider < output.Evidence[j].Provider })
	if len(output.Failures) == 0 {
		output.Failures = nil
	}
	return output
}

func (s *ObserverSet) ascpReceipt(ctx context.Context, provider RPCProvider, expected ASCPExpectedReceipt) (ASCPReceiptEvidence, error) {
	if err := s.verifyChain(ctx, provider); err != nil {
		return ASCPReceiptEvidence{}, err
	}
	var receipt *rpcReceipt
	if err := s.call(ctx, provider, "eth_getTransactionReceipt", []any{expected.TransactionHash}, &receipt); err != nil {
		return ASCPReceiptEvidence{}, err
	}
	if receipt == nil {
		return ASCPReceiptEvidence{}, errors.New("transaction receipt is not available")
	}
	txHash, err := canonicalHash(receipt.TransactionHash)
	if err != nil || txHash != expected.TransactionHash {
		return ASCPReceiptEvidence{}, errors.New("receipt transaction hash does not match expected ASCP transition")
	}
	blockNumber, err := parseHexUint64(receipt.BlockNumber)
	if err != nil || blockNumber == 0 {
		return ASCPReceiptEvidence{}, errors.New("receipt block number is invalid")
	}
	blockHash, err := canonicalHash(receipt.BlockHash)
	if err != nil {
		return ASCPReceiptEvidence{}, errors.New("receipt block hash is invalid")
	}
	status, err := parseHexUint64(receipt.Status)
	if err != nil || status > 1 {
		return ASCPReceiptEvidence{}, errors.New("receipt status is invalid")
	}
	latest, err := s.block(ctx, provider, "latest")
	if err != nil {
		return ASCPReceiptEvidence{}, err
	}
	if latest.number < blockNumber {
		return ASCPReceiptEvidence{}, errors.New("provider head precedes the receipt block")
	}
	if status == 1 {
		if err := verifyASCPReceiptLogs(receipt.Logs, expected); err != nil {
			return ASCPReceiptEvidence{}, err
		}
	} else {
		for _, event := range receipt.Logs {
			if !event.Removed {
				return ASCPReceiptEvidence{}, errors.New("reverted ASCP receipt contains non-removed logs")
			}
		}
	}
	return ASCPReceiptEvidence{
		Provider: provider.Name, Action: expected.Action, ChainID: s.chainID,
		TransactionHash: txHash, BlockNumber: blockNumber, BlockHash: blockHash,
		ConfirmedHead: latest.number, Success: status == 1, CallID: expected.CallID, OperationID: expected.OperationID,
	}, nil
}

func verifyASCPReceiptLogs(logs []rpcLog, expected ASCPExpectedReceipt) error {
	topic := map[ASCPReceiptAction]string{
		ASCPReceiptLock: ascpCallLockedTopic, ASCPReceiptRelease: ascpCallReleasedTopic, ASCPReceiptRefund: ascpCallRefundedTopic,
	}[expected.Action]
	actionMatches, transferMatches := 0, 0
	actionIndex, transferIndex := -1, -1
	for index, event := range logs {
		if event.Removed {
			continue
		}
		address := strings.ToLower(event.Address)
		if address == expected.Contract && len(event.Topics) >= 2 && strings.ToLower(event.Topics[1]) == expected.CallID {
			eventTopic := strings.ToLower(event.Topics[0])
			if isASCPLifecycleTopic(eventTopic) && eventTopic != topic {
				return errors.New("receipt contains another ASCPCallEscrow lifecycle event for the expected call")
			}
		}
		if address == expected.Contract && len(event.Topics) > 0 && strings.ToLower(event.Topics[0]) == topic {
			if err := matchASCPEvent(event, expected); err != nil {
				return err
			}
			actionMatches++
			actionIndex = index
		}
		from, to := expected.Contract, expected.PayTo
		if expected.Action == ASCPReceiptLock {
			from, to = expected.Buyer, expected.Contract
		} else if expected.Action == ASCPReceiptRefund {
			to = expected.Buyer
		}
		if matchesTransfer(event, expected.Asset, from, to, expected.AmountAtomic) {
			transferMatches++
			transferIndex = index
		}
	}
	if actionMatches != 1 || transferMatches != 1 {
		return errors.New("receipt must contain exactly one expected ASCP event and asset transfer")
	}
	if transferIndex >= actionIndex {
		return errors.New("ASCP asset transfer must precede the lifecycle event")
	}
	return nil
}

func matchASCPEvent(event rpcLog, expected ASCPExpectedReceipt) error {
	words, err := eventWords(event.Data)
	if err != nil {
		return err
	}
	if len(event.Topics) < 3 || strings.ToLower(event.Topics[1]) != expected.CallID || strings.ToLower(event.Topics[2]) != expected.OperationID {
		return errors.New("ASCP event identity binding does not match")
	}
	switch expected.Action {
	case ASCPReceiptLock:
		if len(event.Topics) != 4 || strings.ToLower(event.Topics[3]) != expected.CommitmentHash || len(words) != 4 ||
			addressFromWord(words[0]) != expected.Buyer || addressFromWord(words[1]) != expected.PayTo ||
			new(big.Int).SetBytes(words[2]).String() != expected.AmountAtomic || wordUint64(words[3]) != expected.SettleBy {
			return errors.New("CallLocked fields do not match the durable operation")
		}
	case ASCPReceiptRelease:
		if len(event.Topics) != 4 || strings.ToLower(event.Topics[3]) != expected.CommitmentHash || len(words) != 4 ||
			wordHash(words[0]) != expected.DeliveryHash || wordHash(words[1]) != expected.EvidenceHash ||
			addressFromWord(words[2]) != expected.PayTo || new(big.Int).SetBytes(words[3]).String() != expected.AmountAtomic {
			return errors.New("CallReleased fields do not match the durable operation")
		}
	case ASCPReceiptRefund:
		if len(event.Topics) != 4 || topicAddress(event.Topics[3]) != expected.Buyer || len(words) != 1 ||
			new(big.Int).SetBytes(words[0]).String() != expected.AmountAtomic {
			return errors.New("CallRefunded fields do not match the durable operation")
		}
	}
	return nil
}

func isASCPLifecycleTopic(topic string) bool {
	switch topic {
	case ascpCallLockedTopic, ascpCallRefundedTopic, ascpCallAckedTopic, ascpCallReleasedTopic:
		return true
	default:
		return false
	}
}

func addressFromWord(word []byte) string {
	if len(word) != 32 {
		return ""
	}
	for _, prefix := range word[:12] {
		if prefix != 0 {
			return ""
		}
	}
	return fmt.Sprintf("0x%x", word[12:])
}
