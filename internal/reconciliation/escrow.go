package reconciliation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/sha3"
)

const (
	escrowFundedTopic       = "0x7e04c416707d16b45b505415891eadc7f4d4386a1b582c6feac125744baf8838"
	escrowAcknowledgedTopic = "0x504e951f9f39dac47c6df73e831143a1c598c30685d33bd38abc8f46f848ced2"
	escrowDeliveredTopic    = "0x41bba665699291563190f5d3a33f981e14b60c1287e4825493edc005192cfd7e"
	escrowReleasedTopic     = "0x947d4120eecc0620f53a07f44b9512520ecff811db5f82de7082073dd07a40c7"
	escrowRefundedTopic     = "0xd6edf0b889f4ff3b49ee288998e6efa15c9e6fcf822c49066e55723cd9164e8c"
	escrowCallIDDomain      = "0xf940ea33aeb6531575da935c033b99d0eb2092e5dd42c23a1f9ff5bd84961d72"
)

type EscrowAction string

const (
	EscrowFund        EscrowAction = "FUND"
	EscrowAcknowledge EscrowAction = "ACKNOWLEDGE"
	EscrowDeliver     EscrowAction = "DELIVER"
	EscrowRelease     EscrowAction = "RELEASE"
	EscrowRefund      EscrowAction = "REFUND"
)

// EscrowExpectedReceipt is the immutable claim FlowOps expects one transaction
// to prove. Every field is repeated deliberately: a transaction hash alone is
// never enough to recognize escrow funding, delivery, release, or refund.
type EscrowExpectedReceipt struct {
	Action            EscrowAction `json:"action"`
	TransactionHash   string       `json:"transactionHash"`
	ChainID           uint64       `json:"chainId"`
	Contract          string       `json:"contract"`
	Asset             string       `json:"asset"`
	CallID            string       `json:"callId"`
	Buyer             string       `json:"buyer"`
	Provider          string       `json:"provider"`
	AmountAtomic      string       `json:"amountAtomic"`
	TaskDigest        string       `json:"taskDigest"`
	RequestDigest     string       `json:"requestDigest"`
	ResponseDigest    string       `json:"responseDigest,omitempty"`
	EvidenceDigest    string       `json:"evidenceDigest,omitempty"`
	AcknowledgeBy     uint64       `json:"acknowledgeBy"`
	DeliverBy         uint64       `json:"deliverBy"`
	ReleaseWindow     uint64       `json:"releaseWindowSeconds"`
	BuyerAccepted     *bool        `json:"buyerAccepted,omitempty"`
	RefundedFromState uint8        `json:"refundedFromState,omitempty"`
}

type EscrowReceiptEvidence struct {
	Provider        string       `json:"provider"`
	Action          EscrowAction `json:"action"`
	ChainID         uint64       `json:"chainId"`
	TransactionHash string       `json:"transactionHash"`
	BlockNumber     uint64       `json:"blockNumber"`
	BlockHash       string       `json:"blockHash"`
	ConfirmedHead   uint64       `json:"confirmedHead"`
	Success         bool         `json:"success"`
	CallID          string       `json:"callId"`
	DeliveredAt     uint64       `json:"deliveredAt,omitempty"`
	ReleasableAt    uint64       `json:"releasableAt,omitempty"`
}

type EscrowReceiptResult struct {
	Evidence []EscrowReceiptEvidence `json:"evidence"`
	Failures map[string]string       `json:"failures,omitempty"`
}

func (e EscrowExpectedReceipt) Validate() error {
	if e.ChainID != 8453 && e.ChainID != 84532 {
		return errors.New("escrow receipt supports Base mainnet or Base Sepolia only")
	}
	for name, value := range map[string]string{
		"transactionHash": e.TransactionHash, "callId": e.CallID,
		"taskDigest": e.TaskDigest, "requestDigest": e.RequestDigest,
	} {
		if !hashPattern.MatchString(value) || value == zeroHash {
			return fmt.Errorf("%s must be a canonical lowercase 32-byte hash", name)
		}
	}
	for name, value := range map[string]string{
		"contract": e.Contract, "asset": e.Asset, "buyer": e.Buyer, "provider": e.Provider,
	} {
		if !addressPattern.MatchString(value) {
			return fmt.Errorf("%s must be a canonical lowercase EVM address", name)
		}
	}
	if e.Buyer == e.Provider {
		return errors.New("escrow buyer and provider must differ")
	}
	if _, err := positiveInteger(e.AmountAtomic); err != nil {
		return fmt.Errorf("amountAtomic: %w", err)
	}
	if e.AcknowledgeBy == 0 || e.DeliverBy <= e.AcknowledgeBy || e.ReleaseWindow == 0 || e.ReleaseWindow > 30*24*60*60 {
		return errors.New("escrow deadlines or release window are invalid")
	}
	want, err := DeriveEscrowCallID(e.ChainID, e.Contract, e.Buyer, e.TaskDigest, e.RequestDigest)
	if err != nil {
		return err
	}
	if e.CallID != want {
		return errors.New("callId does not bind the expected chain, contract, buyer, task, and request")
	}
	switch e.Action {
	case EscrowFund, EscrowAcknowledge:
		if e.ResponseDigest != "" || e.EvidenceDigest != "" || e.BuyerAccepted != nil || e.RefundedFromState != 0 {
			return errors.New("escrow action contains fields that are not emitted by that transition")
		}
	case EscrowDeliver:
		if !hashPattern.MatchString(e.ResponseDigest) || !hashPattern.MatchString(e.EvidenceDigest) || e.ResponseDigest == zeroHash || e.EvidenceDigest == zeroHash {
			return errors.New("delivery requires canonical non-zero response and evidence digests")
		}
		if e.BuyerAccepted != nil || e.RefundedFromState != 0 {
			return errors.New("delivery contains terminal-only fields")
		}
	case EscrowRelease:
		if e.BuyerAccepted == nil || e.RefundedFromState != 0 || e.ResponseDigest != "" || e.EvidenceDigest != "" {
			return errors.New("release requires buyerAccepted and no refund or delivery-only fields")
		}
	case EscrowRefund:
		if e.RefundedFromState != 1 && e.RefundedFromState != 2 {
			return errors.New("refund must expire from Funded (1) or Acknowledged (2)")
		}
		if e.BuyerAccepted != nil || e.ResponseDigest != "" || e.EvidenceDigest != "" {
			return errors.New("refund contains release or delivery-only fields")
		}
	default:
		return errors.New("escrow action is unsupported")
	}
	return nil
}

const zeroHash = "0x0000000000000000000000000000000000000000000000000000000000000000"

func DeriveEscrowCallID(chainID uint64, contract, buyer, taskDigest, requestDigest string) (string, error) {
	if chainID == 0 || !addressPattern.MatchString(contract) || !addressPattern.MatchString(buyer) || !hashPattern.MatchString(taskDigest) || !hashPattern.MatchString(requestDigest) {
		return "", errors.New("call ID inputs are invalid")
	}
	words := make([]byte, 0, 32*6)
	for _, encoded := range []string{escrowCallIDDomain, uint256Hex(new(big.Int).SetUint64(chainID)), addressWord(contract), addressWord(buyer), taskDigest, requestDigest} {
		raw, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
		if err != nil || len(raw) != 32 {
			return "", errors.New("call ID input could not be ABI encoded")
		}
		words = append(words, raw...)
	}
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(words)
	return "0x" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *ObserverSet) EscrowReceiptQuorum(ctx context.Context, expected EscrowExpectedReceipt) EscrowReceiptResult {
	if err := expected.Validate(); err != nil || expected.ChainID != s.chainID {
		message := "expected escrow receipt is invalid"
		if err != nil {
			message = err.Error()
		} else {
			message = "expected escrow receipt uses the wrong configured chain"
		}
		return EscrowReceiptResult{Failures: map[string]string{"validation": message}}
	}
	type providerResult struct {
		provider string
		evidence EscrowReceiptEvidence
		err      error
	}
	results := make(chan providerResult, len(s.providers))
	var group sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		group.Add(1)
		go func() {
			defer group.Done()
			evidence, err := s.escrowReceipt(ctx, provider, expected)
			results <- providerResult{provider: provider.Name, evidence: evidence, err: err}
		}()
	}
	group.Wait()
	close(results)
	output := EscrowReceiptResult{Failures: make(map[string]string)}
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

func (s *ObserverSet) escrowReceipt(ctx context.Context, provider RPCProvider, expected EscrowExpectedReceipt) (EscrowReceiptEvidence, error) {
	if err := s.verifyChain(ctx, provider); err != nil {
		return EscrowReceiptEvidence{}, err
	}
	var receipt *rpcReceipt
	if err := s.call(ctx, provider, "eth_getTransactionReceipt", []any{expected.TransactionHash}, &receipt); err != nil {
		return EscrowReceiptEvidence{}, err
	}
	if receipt == nil {
		return EscrowReceiptEvidence{}, errors.New("transaction receipt is not available")
	}
	txHash, err := canonicalHash(receipt.TransactionHash)
	if err != nil || txHash != expected.TransactionHash {
		return EscrowReceiptEvidence{}, errors.New("receipt transaction hash does not match expected escrow transition")
	}
	blockNumber, err := parseHexUint64(receipt.BlockNumber)
	if err != nil || blockNumber == 0 {
		return EscrowReceiptEvidence{}, errors.New("receipt block number is invalid")
	}
	blockHash, err := canonicalHash(receipt.BlockHash)
	if err != nil {
		return EscrowReceiptEvidence{}, errors.New("receipt block hash is invalid")
	}
	status, err := parseHexUint64(receipt.Status)
	if err != nil || status > 1 {
		return EscrowReceiptEvidence{}, errors.New("receipt status is invalid")
	}
	latest, err := s.block(ctx, provider, "latest")
	if err != nil {
		return EscrowReceiptEvidence{}, err
	}
	if latest.number < blockNumber {
		return EscrowReceiptEvidence{}, errors.New("provider head precedes the receipt block")
	}
	evidence := EscrowReceiptEvidence{
		Provider: provider.Name, Action: expected.Action, ChainID: s.chainID,
		TransactionHash: txHash, BlockNumber: blockNumber, BlockHash: blockHash,
		ConfirmedHead: latest.number, Success: status == 1, CallID: expected.CallID,
	}
	if status == 0 {
		return evidence, nil
	}
	deliveredAt, releasableAt, err := verifyEscrowLogs(receipt.Logs, expected)
	if err != nil {
		return EscrowReceiptEvidence{}, err
	}
	evidence.DeliveredAt = deliveredAt
	evidence.ReleasableAt = releasableAt
	return evidence, nil
}

func verifyEscrowLogs(logs []rpcLog, expected EscrowExpectedReceipt) (uint64, uint64, error) {
	topic := map[EscrowAction]string{
		EscrowFund: escrowFundedTopic, EscrowAcknowledge: escrowAcknowledgedTopic,
		EscrowDeliver: escrowDeliveredTopic, EscrowRelease: escrowReleasedTopic, EscrowRefund: escrowRefundedTopic,
	}[expected.Action]
	actionIndex := -1
	transferIndex := -1
	actionMatches := 0
	transferMatches := 0
	var deliveredAt, releasableAt uint64
	for index, event := range logs {
		if event.Removed {
			continue
		}
		address := strings.ToLower(event.Address)
		if address == expected.Contract && len(event.Topics) > 0 && strings.ToLower(event.Topics[0]) == topic {
			if err := matchEscrowEvent(event, expected, &deliveredAt, &releasableAt); err != nil {
				return 0, 0, err
			}
			actionMatches++
			actionIndex = index
		}
		from, to := "", ""
		switch expected.Action {
		case EscrowFund:
			from, to = expected.Buyer, expected.Contract
		case EscrowRelease:
			from, to = expected.Contract, expected.Provider
		case EscrowRefund:
			from, to = expected.Contract, expected.Buyer
		}
		if from != "" && matchesTransfer(event, expected.Asset, from, to, expected.AmountAtomic) {
			transferMatches++
			transferIndex = index
		}
	}
	if actionMatches != 1 {
		return 0, 0, errors.New("receipt must contain exactly one expected CallEscrow transition event")
	}
	needsTransfer := expected.Action == EscrowFund || expected.Action == EscrowRelease || expected.Action == EscrowRefund
	if needsTransfer && transferMatches != 1 {
		return 0, 0, errors.New("receipt must contain exactly one expected escrow asset Transfer event")
	}
	if needsTransfer && transferIndex >= actionIndex {
		return 0, 0, errors.New("escrow asset Transfer must precede the terminal or funding transition event")
	}
	return deliveredAt, releasableAt, nil
}

func matchEscrowEvent(event rpcLog, expected EscrowExpectedReceipt, deliveredAt, releasableAt *uint64) error {
	if len(event.Topics) < 2 || strings.ToLower(event.Topics[1]) != expected.CallID {
		return errors.New("escrow event callId does not match")
	}
	words, err := eventWords(event.Data)
	if err != nil {
		return err
	}
	switch expected.Action {
	case EscrowFund:
		if len(event.Topics) != 4 || len(words) != 5 || topicAddress(event.Topics[2]) != expected.Buyer || topicAddress(event.Topics[3]) != expected.Provider ||
			wordInteger(words[0]).String() != expected.AmountAtomic || wordHash(words[1]) != expected.TaskDigest || wordHash(words[2]) != expected.RequestDigest ||
			wordUint64(words[3]) != expected.AcknowledgeBy || wordUint64(words[4]) != expected.DeliverBy {
			return errors.New("CallFunded fields do not match the immutable expected call")
		}
	case EscrowAcknowledge:
		if len(event.Topics) != 3 || len(words) != 0 || topicAddress(event.Topics[2]) != expected.Provider {
			return errors.New("CallAcknowledged fields do not match the expected provider")
		}
	case EscrowDeliver:
		if len(event.Topics) != 3 || len(words) != 4 || topicAddress(event.Topics[2]) != expected.Provider || wordHash(words[0]) != expected.ResponseDigest || wordHash(words[1]) != expected.EvidenceDigest {
			return errors.New("DeliverySubmitted fields do not match the expected delivery")
		}
		*deliveredAt, *releasableAt = wordUint64(words[2]), wordUint64(words[3])
		if *deliveredAt == 0 || *deliveredAt > ^uint64(0)-expected.ReleaseWindow || *releasableAt != *deliveredAt+expected.ReleaseWindow {
			return errors.New("DeliverySubmitted release deadline does not match the pinned window")
		}
	case EscrowRelease:
		accepted := wordBoolAt(words, 1)
		if len(event.Topics) != 3 || len(words) != 2 || topicAddress(event.Topics[2]) != expected.Provider || wordInteger(words[0]).String() != expected.AmountAtomic || accepted == nil || *accepted != *expected.BuyerAccepted {
			return errors.New("Released fields do not match the expected provider, amount, and acceptance path")
		}
	case EscrowRefund:
		if len(event.Topics) != 3 || len(words) != 2 || topicAddress(event.Topics[2]) != expected.Buyer || wordInteger(words[0]).String() != expected.AmountAtomic || wordInteger(words[1]).Uint64() != uint64(expected.RefundedFromState) {
			return errors.New("Refunded fields do not match the expected buyer, amount, and expired state")
		}
	}
	return nil
}

func matchesTransfer(event rpcLog, asset, from, to, amount string) bool {
	if event.Removed || strings.ToLower(event.Address) != asset || len(event.Topics) != 3 || strings.ToLower(event.Topics[0]) != transferTopic {
		return false
	}
	value, err := dataUint256(event.Data)
	return err == nil && topicAddress(event.Topics[1]) == from && topicAddress(event.Topics[2]) == to && value.String() == amount
}

func eventWords(data string) ([][]byte, error) {
	if !strings.HasPrefix(data, "0x") || (len(data)-2)%64 != 0 {
		return nil, errors.New("escrow event data is not canonical ABI words")
	}
	raw, err := hex.DecodeString(data[2:])
	if err != nil {
		return nil, errors.New("escrow event data is not hex")
	}
	words := make([][]byte, 0, len(raw)/32)
	for len(raw) > 0 {
		words = append(words, raw[:32])
		raw = raw[32:]
	}
	return words, nil
}

func topicAddress(topic string) string {
	address, err := addressFromTopic(topic)
	if err != nil {
		return ""
	}
	return address
}

func wordInteger(word []byte) *big.Int { return new(big.Int).SetBytes(word) }
func wordHash(word []byte) string      { return "0x" + hex.EncodeToString(word) }
func wordUint64(word []byte) uint64 {
	value := wordInteger(word)
	if value.BitLen() > 64 {
		return 0
	}
	return value.Uint64()
}

func wordBoolAt(words [][]byte, index int) *bool {
	if index >= len(words) {
		return nil
	}
	value := wordInteger(words[index])
	if value.Cmp(big.NewInt(0)) != 0 && value.Cmp(big.NewInt(1)) != 0 {
		return nil
	}
	result := value.Sign() == 1
	return &result
}

func addressWord(address string) string { return "0x" + strings.Repeat("0", 24) + address[2:] }
func uint256Hex(value *big.Int) string  { return fmt.Sprintf("0x%064x", value) }
