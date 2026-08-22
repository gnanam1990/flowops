package reconciliation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

const (
	governanceWorkflowBoundTopic = "0x71840a8df3cf7e14c302ff72b4fd1c651a2845389dfb0a4fdd884a2ffb104bfe"
	governanceLogBlockSpan       = uint64(10_000)
	governanceMaxLogWindows      = uint64(1_000)
)

var (
	ErrGovernanceReceiptPending = errors.New("governance workflow receipt is not available")
	ErrGovernanceReceiptInvalid = errors.New("governance workflow receipt is invalid")
)

type GovernanceRule struct {
	Contract             string `json:"contract"`
	FunctionSelector     string `json:"functionSelector"`
	ActionEventSignature string `json:"actionEventSignature"`
	MultipleActionEvents bool   `json:"multipleActionEvents"`
	ExpectedActionEvents int    `json:"expectedActionEvents,omitempty"`
}

type GovernanceExpectedReceipt struct {
	WorkflowID  string           `json:"workflowId"`
	PayloadHash string           `json:"payloadHash"`
	ApprovedAt  uint64           `json:"approvedAt"`
	FromBlock   uint64           `json:"fromBlock"`
	Rules       []GovernanceRule `json:"rules"`
}

type GovernanceReceiptEvidence struct {
	Provider             string   `json:"provider"`
	ChainID              uint64   `json:"chainId"`
	WorkflowID           string   `json:"workflowId"`
	PayloadHash          string   `json:"payloadHash"`
	TransactionHash      string   `json:"transactionHash"`
	BlockNumber          uint64   `json:"blockNumber"`
	BlockHash            string   `json:"blockHash"`
	BlockTimestamp       uint64   `json:"blockTimestamp"`
	BindingLogIndex      uint64   `json:"bindingLogIndex"`
	ContractAddress      string   `json:"contractAddress"`
	FunctionSelector     string   `json:"functionSelector"`
	ActionEventSignature string   `json:"actionEventSignature"`
	ActionLogIndexes     []uint64 `json:"actionLogIndexes"`
	ConfirmedHead        uint64   `json:"confirmedHead"`
	FinalizedHead        uint64   `json:"finalizedHead"`
}

type GovernanceReceiptResult struct {
	Evidence         []GovernanceReceiptEvidence `json:"evidence"`
	Failures         map[string]string           `json:"failures,omitempty"`
	PendingProviders []string                    `json:"pendingProviders,omitempty"`
	InvalidProviders []string                    `json:"invalidProviders,omitempty"`
}

func (expected GovernanceExpectedReceipt) Validate() error {
	if !hashPattern.MatchString(expected.WorkflowID) || expected.WorkflowID == zeroHash ||
		!hashPattern.MatchString(expected.PayloadHash) || expected.PayloadHash == zeroHash ||
		expected.ApprovedAt == 0 || expected.FromBlock == 0 || len(expected.Rules) == 0 || len(expected.Rules) > 16 {
		return errors.New("governance receipt expectation is invalid")
	}
	seen := make(map[string]struct{}, len(expected.Rules))
	for _, rule := range expected.Rules {
		key := rule.Contract + "\x00" + rule.FunctionSelector
		if !addressPattern.MatchString(rule.Contract) || rule.Contract == "0x0000000000000000000000000000000000000000" ||
			!validFunctionSelector(rule.FunctionSelector) || rule.FunctionSelector == "0x00000000" ||
			!hashPattern.MatchString(rule.ActionEventSignature) || rule.ActionEventSignature == zeroHash ||
			rule.ExpectedActionEvents < 0 || rule.ExpectedActionEvents > 100 ||
			(!rule.MultipleActionEvents && rule.ExpectedActionEvents > 1) {
			return errors.New("governance receipt rule is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("governance receipt rules contain a duplicate selector")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (s *ObserverSet) GovernanceReceiptQuorum(ctx context.Context, expected GovernanceExpectedReceipt) GovernanceReceiptResult {
	output := GovernanceReceiptResult{Failures: make(map[string]string)}
	if expected.Validate() != nil {
		output.Failures["configuration"] = "invalid governance receipt expectation"
		return output
	}
	type response struct {
		evidence GovernanceReceiptEvidence
		err      error
	}
	responses := make(chan response, len(s.providers))
	var wait sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		wait.Add(1)
		go func() {
			defer wait.Done()
			evidence, err := s.governanceReceipt(ctx, provider, expected)
			responses <- response{evidence: evidence, err: err}
		}()
	}
	wait.Wait()
	close(responses)
	for item := range responses {
		if item.err != nil {
			output.Failures[item.evidence.Provider] = item.err.Error()
			if errors.Is(item.err, ErrGovernanceReceiptPending) {
				output.PendingProviders = append(output.PendingProviders, item.evidence.Provider)
			}
			if errors.Is(item.err, ErrGovernanceReceiptInvalid) {
				output.InvalidProviders = append(output.InvalidProviders, item.evidence.Provider)
			}
			continue
		}
		output.Evidence = append(output.Evidence, item.evidence)
	}
	sort.Slice(output.Evidence, func(i, j int) bool { return output.Evidence[i].Provider < output.Evidence[j].Provider })
	sort.Strings(output.PendingProviders)
	sort.Strings(output.InvalidProviders)
	if len(output.Failures) == 0 {
		output.Failures = nil
	}
	return output
}

func (s *ObserverSet) governanceReceipt(ctx context.Context, provider RPCProvider, expected GovernanceExpectedReceipt) (GovernanceReceiptEvidence, error) {
	failure := GovernanceReceiptEvidence{Provider: provider.Name}
	if err := s.verifyChain(ctx, provider); err != nil {
		return failure, err
	}
	latest, err := s.block(ctx, provider, "latest")
	if err != nil {
		return failure, err
	}
	if latest.number < expected.FromBlock {
		return failure, ErrGovernanceReceiptPending
	}
	addresses := make([]string, 0, len(expected.Rules))
	seenAddresses := make(map[string]struct{}, len(expected.Rules))
	for _, rule := range expected.Rules {
		if _, exists := seenAddresses[rule.Contract]; !exists {
			addresses = append(addresses, rule.Contract)
			seenAddresses[rule.Contract] = struct{}{}
		}
	}
	bindings := make([]rpcLog, 0, 1)
	windows, err := governanceLogWindows(expected.FromBlock, latest.number)
	if err != nil {
		return failure, err
	}
	for _, window := range windows {
		filter := map[string]any{
			"fromBlock": fmt.Sprintf("0x%x", window[0]), "toBlock": fmt.Sprintf("0x%x", window[1]), "address": addresses,
			"topics": []any{governanceWorkflowBoundTopic, expected.WorkflowID, expected.PayloadHash},
		}
		var window []rpcLog
		if err := s.call(ctx, provider, "eth_getLogs", []any{filter}, &window); err != nil {
			return failure, err
		}
		bindings = append(bindings, window...)
		if len(bindings) > 1 {
			return failure, invalidGovernanceReceipt("workflow has multiple binding receipts")
		}
	}
	if len(bindings) == 0 {
		return failure, ErrGovernanceReceiptPending
	}
	if len(bindings) != 1 {
		return failure, invalidGovernanceReceipt("workflow has multiple binding receipts")
	}
	binding := bindings[0]
	if binding.Removed || binding.Data != "0x" || len(binding.Topics) != 4 || strings.ToLower(binding.Topics[0]) != governanceWorkflowBoundTopic ||
		strings.ToLower(binding.Topics[1]) != expected.WorkflowID || strings.ToLower(binding.Topics[2]) != expected.PayloadHash {
		return failure, invalidGovernanceReceipt("binding log is malformed or removed")
	}
	selector, err := selectorFromTopic(binding.Topics[3])
	if err != nil {
		return failure, invalidGovernanceReceipt(err.Error())
	}
	contract := strings.ToLower(binding.Address)
	rule, ok := findGovernanceRule(expected.Rules, contract, selector)
	if !ok {
		return failure, invalidGovernanceReceipt("binding selector is not allowed for the workflow kind")
	}
	txHash, err := canonicalHash(binding.TransactionHash)
	if err != nil {
		return failure, invalidGovernanceReceipt("binding transaction hash is invalid")
	}
	blockNumber, err := parseHexUint64(binding.BlockNumber)
	if err != nil || blockNumber == 0 {
		return failure, invalidGovernanceReceipt("binding block number is invalid")
	}
	blockHash, err := canonicalHash(binding.BlockHash)
	if err != nil {
		return failure, invalidGovernanceReceipt("binding block hash is invalid")
	}
	bindingIndex, err := parseHexUint64(binding.LogIndex)
	if err != nil {
		return failure, invalidGovernanceReceipt("binding log index is invalid")
	}
	var receipt *rpcReceipt
	if err := s.call(ctx, provider, "eth_getTransactionReceipt", []any{txHash}, &receipt); err != nil {
		return failure, err
	}
	if receipt == nil {
		return failure, ErrGovernanceReceiptPending
	}
	status, err := parseHexUint64(receipt.Status)
	if err != nil || status != 1 {
		return failure, invalidGovernanceReceipt("transaction did not succeed")
	}
	receiptHash, hashErr := canonicalHash(receipt.TransactionHash)
	receiptBlock, blockErr := parseHexUint64(receipt.BlockNumber)
	receiptBlockHash, blockHashErr := canonicalHash(receipt.BlockHash)
	if hashErr != nil || blockErr != nil || blockHashErr != nil || receiptHash != txHash ||
		receiptBlock != blockNumber || receiptBlockHash != blockHash {
		return failure, invalidGovernanceReceipt("receipt identity disagrees with the binding log")
	}
	actionIndexes, err := verifyGovernanceReceiptLogs(receipt.Logs, expected, rule, txHash, blockNumber, blockHash, bindingIndex)
	if err != nil {
		return failure, err
	}
	canonical, err := s.block(ctx, provider, fmt.Sprintf("0x%x", blockNumber))
	if err != nil || canonical.number != blockNumber || canonical.hash != blockHash {
		return failure, invalidGovernanceReceipt("receipt block is not canonical")
	}
	if canonical.timestamp.Unix() <= 0 || uint64(canonical.timestamp.Unix()) <= expected.ApprovedAt {
		return failure, invalidGovernanceReceipt("receipt predates or is ambiguous with workflow approval")
	}
	if latest.number < blockNumber {
		return failure, invalidGovernanceReceipt("provider head precedes the receipt block")
	}
	finalized, err := s.block(ctx, provider, "finalized")
	if err != nil {
		return failure, err
	}
	if finalized.number < blockNumber {
		return failure, ErrGovernanceReceiptPending
	}
	return GovernanceReceiptEvidence{
		Provider: provider.Name, ChainID: s.chainID, WorkflowID: expected.WorkflowID, PayloadHash: expected.PayloadHash,
		TransactionHash: txHash, BlockNumber: blockNumber, BlockHash: blockHash, BindingLogIndex: bindingIndex,
		BlockTimestamp:  uint64(canonical.timestamp.Unix()),
		ContractAddress: contract, FunctionSelector: selector, ActionEventSignature: rule.ActionEventSignature,
		ActionLogIndexes: actionIndexes, ConfirmedHead: latest.number, FinalizedHead: finalized.number,
	}, nil
}

func verifyGovernanceReceiptLogs(logs []rpcLog, expected GovernanceExpectedReceipt, rule GovernanceRule, txHash string, blockNumber uint64, blockHash string, bindingIndex uint64) ([]uint64, error) {
	bindingMatches := 0
	actionIndexes := make(map[uint64]struct{}, 1)
	seenLogIndexes := make(map[uint64]struct{}, len(logs))
	for _, event := range logs {
		if event.Removed {
			continue
		}
		index, err := parseHexUint64(event.LogIndex)
		if err != nil {
			return nil, invalidGovernanceReceipt("receipt contains an invalid log index")
		}
		if _, duplicate := seenLogIndexes[index]; duplicate {
			return nil, invalidGovernanceReceipt("receipt contains a duplicate log index")
		}
		seenLogIndexes[index] = struct{}{}
		eventTx, txErr := canonicalHash(event.TransactionHash)
		eventBlock, blockErr := parseHexUint64(event.BlockNumber)
		eventHash, hashErr := canonicalHash(event.BlockHash)
		if txErr != nil || blockErr != nil || hashErr != nil || eventTx != txHash || eventBlock != blockNumber || eventHash != blockHash {
			return nil, invalidGovernanceReceipt("receipt contains a log with conflicting chain identity")
		}
		address := strings.ToLower(event.Address)
		if address != rule.Contract || len(event.Topics) == 0 {
			continue
		}
		topic := strings.ToLower(event.Topics[0])
		if topic == governanceWorkflowBoundTopic {
			if len(event.Topics) == 4 && strings.ToLower(event.Topics[1]) == expected.WorkflowID &&
				strings.ToLower(event.Topics[2]) == expected.PayloadHash && event.Data == "0x" {
				selector, selectorErr := selectorFromTopic(event.Topics[3])
				if selectorErr == nil && selector == rule.FunctionSelector && index == bindingIndex {
					bindingMatches++
				}
			}
			continue
		}
		if topic == rule.ActionEventSignature {
			actionIndexes[index] = struct{}{}
		}
	}
	if bindingMatches != 1 || bindingIndex == 0 {
		return nil, invalidGovernanceReceipt("receipt does not contain the required action and binding event pair")
	}
	if !rule.MultipleActionEvents {
		actionIndex := bindingIndex - 1
		if _, exists := actionIndexes[actionIndex]; !exists {
			return nil, invalidGovernanceReceipt("action event is not adjacent to its workflow binding")
		}
		return []uint64{actionIndex}, nil
	}
	// invalidateNonces emits a contiguous run of action events immediately
	// before its binding. Select only that suffix so another call of the same
	// type in a Safe batch cannot contaminate this workflow's evidence.
	paired := make([]uint64, 0, 1)
	for index := bindingIndex - 1; ; index-- {
		if _, exists := actionIndexes[index]; !exists {
			break
		}
		paired = append(paired, index)
		if len(paired) > 100 || index == 0 {
			break
		}
	}
	if len(paired) == 0 || len(paired) > 100 {
		return nil, invalidGovernanceReceipt("receipt does not contain an adjacent nonce-invalidation event run")
	}
	slices.Reverse(paired)
	if rule.ExpectedActionEvents > 0 && len(paired) != rule.ExpectedActionEvents {
		return nil, invalidGovernanceReceipt("nonce-invalidation event count does not match the approved action")
	}
	return paired, nil
}

func findGovernanceRule(rules []GovernanceRule, contract, selector string) (GovernanceRule, bool) {
	for _, rule := range rules {
		if rule.Contract == contract && rule.FunctionSelector == selector {
			return rule, true
		}
	}
	return GovernanceRule{}, false
}

func selectorFromTopic(topic string) (string, error) {
	value, err := canonicalHash(topic)
	if err != nil || len(value) != 66 || value[10:] != strings.Repeat("0", 56) {
		return "", errors.New("governance function selector topic is malformed")
	}
	return value[:10], nil
}

func validFunctionSelector(value string) bool {
	if len(value) != 10 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 4
}

func invalidGovernanceReceipt(message string) error {
	return fmt.Errorf("%w: %s", ErrGovernanceReceiptInvalid, message)
}

func governanceLogWindows(from, to uint64) ([][2]uint64, error) {
	if from > to {
		return nil, nil
	}
	count := (to-from)/governanceLogBlockSpan + 1
	if count > governanceMaxLogWindows {
		return nil, errors.New("governance log scan range exceeds the bounded observer limit")
	}
	windows := make([][2]uint64, 0, int(count))
	for start := from; ; {
		end := start + governanceLogBlockSpan - 1
		if end < start || end > to {
			end = to
		}
		windows = append(windows, [2]uint64{start, end})
		if end == to {
			return windows, nil
		}
		start = end + 1
	}
}
