package reconciliation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type rpcFixture struct {
	chainID  string
	blocks   map[string]rpcBlock
	receipt  *rpcReceipt
	receipts map[string]*rpcReceipt
	logs     []rpcLog
}

type fixtureTransport struct {
	mu       sync.Mutex
	fixtures map[string]*rpcFixture
}

func (t *fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var call rpcRequest
	if err := json.Unmarshal(body, &call); err != nil {
		return nil, err
	}
	t.mu.Lock()
	fixture := t.fixtures[request.URL.Hostname()]
	t.mu.Unlock()
	if fixture == nil {
		return nil, fmt.Errorf("unknown fixture host %s", request.URL.Hostname())
	}
	var result any
	switch call.Method {
	case "eth_chainId":
		result = fixture.chainID
	case "eth_getBlockByNumber":
		tag, _ := call.Params[0].(string)
		block, ok := fixture.blocks[tag]
		if !ok {
			result = nil
		} else {
			result = block
		}
	case "eth_getTransactionReceipt":
		hash, _ := call.Params[0].(string)
		if fixture.receipts != nil {
			result = fixture.receipts[hash]
		} else {
			result = fixture.receipt
		}
	case "eth_getLogs":
		result = fixture.logs
	default:
		return nil, fmt.Errorf("unexpected method %s", call.Method)
	}
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewReader(envelope)), Request: request,
	}, nil
}

func TestGovernanceReceiptQuorumRequiresCanonicalPairedEvents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	workflowID, payloadHash, txHash := testHash(501), testHash(502), testHash(503)
	contract := "0x1111111111111111111111111111111111111111"
	selector := "0x12345678"
	actionTopic := testHash(504)
	identity := func(index uint64) rpcLog {
		return rpcLog{Address: contract, Data: "0x", BlockNumber: "0x65", BlockHash: testHash(101),
			TransactionHash: txHash, TransactionIndex: "0x0", LogIndex: fmt.Sprintf("0x%x", index)}
	}
	action := identity(1)
	action.Topics = []string{actionTopic}
	binding := identity(2)
	binding.Topics = []string{governanceWorkflowBoundTopic, workflowID, payloadHash, selector + strings.Repeat("0", 56)}
	unrelatedBefore := identity(0)
	unrelatedBefore.Topics = []string{actionTopic}
	unrelatedAfter := identity(3)
	unrelatedAfter.Topics = []string{actionTopic}
	otherBinding := identity(4)
	otherBinding.Topics = []string{governanceWorkflowBoundTopic, testHash(599), testHash(598), selector + strings.Repeat("0", 56)}
	receipt := &rpcReceipt{TransactionHash: txHash, BlockNumber: "0x65", BlockHash: testHash(101), Status: "0x1",
		Logs: []rpcLog{unrelatedBefore, action, binding, unrelatedAfter, otherBinding}}
	fixtures := map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(130, now), "finalized": rpcBlockFixture(120, now), "0x65": rpcBlockFixture(101, now)}, receipt: receipt, logs: []rpcLog{binding}},
		"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(131, now), "finalized": rpcBlockFixture(121, now), "0x65": rpcBlockFixture(101, now)}, receipt: receipt, logs: []rpcLog{binding}},
	}
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	expected := GovernanceExpectedReceipt{WorkflowID: workflowID, PayloadHash: payloadHash, ApprovedAt: uint64(now.Unix() - 1), FromBlock: 90, Rules: []GovernanceRule{{
		Contract: contract, FunctionSelector: selector, ActionEventSignature: actionTopic,
	}}}
	result := observers.GovernanceReceiptQuorum(t.Context(), expected)
	if len(result.Failures) != 0 || len(result.Evidence) != 2 {
		t.Fatalf("GovernanceReceiptQuorum() = %+v", result)
	}
	for _, evidence := range result.Evidence {
		if evidence.TransactionHash != txHash || evidence.BindingLogIndex != 2 || !slices.Equal(evidence.ActionLogIndexes, []uint64{1}) || evidence.ConfirmedHead < 130 {
			t.Fatalf("evidence = %+v", evidence)
		}
	}
	for _, fixture := range fixtures {
		fixture.logs = nil
	}
	pending := observers.GovernanceReceiptQuorum(t.Context(), expected)
	if len(pending.Evidence) != 0 || len(pending.PendingProviders) != 2 || len(pending.Failures) != 2 {
		t.Fatalf("pending receipt classification=%+v", pending)
	}
	for _, fixture := range fixtures {
		fixture.logs = []rpcLog{binding}
	}

	broken := *receipt
	broken.Logs = []rpcLog{binding}
	fixtures["beta.rpc.example"].receipt = &broken
	result = observers.GovernanceReceiptQuorum(t.Context(), expected)
	if len(result.Evidence) != 1 || len(result.Failures) != 1 || !slices.Equal(result.InvalidProviders, []string{"rpc_beta"}) {
		t.Fatalf("missing paired event was accepted: %+v", result)
	}
}

func TestGovernanceReceiptPairsOnlyAdjacentNonceInvalidationRun(t *testing.T) {
	workflowID, payloadHash, txHash, blockHash := testHash(601), testHash(602), testHash(603), testHash(604)
	contract, actionTopic := "0x1111111111111111111111111111111111111111", testHash(605)
	log := func(index uint64, topic string) rpcLog {
		return rpcLog{Address: contract, Topics: []string{topic}, Data: "0x", BlockNumber: "0x64", BlockHash: blockHash,
			TransactionHash: txHash, TransactionIndex: "0x0", LogIndex: fmt.Sprintf("0x%x", index)}
	}
	logs := []rpcLog{log(0, actionTopic), log(2, actionTopic), log(3, actionTopic), log(4, governanceWorkflowBoundTopic)}
	logs[3].Topics = []string{governanceWorkflowBoundTopic, workflowID, payloadHash, "0x12345678" + strings.Repeat("0", 56)}
	paired, err := verifyGovernanceReceiptLogs(logs, GovernanceExpectedReceipt{WorkflowID: workflowID, PayloadHash: payloadHash},
		GovernanceRule{Contract: contract, FunctionSelector: "0x12345678", ActionEventSignature: actionTopic, MultipleActionEvents: true, ExpectedActionEvents: 2},
		txHash, 100, blockHash, 4)
	if err != nil || !slices.Equal(paired, []uint64{2, 3}) {
		t.Fatalf("paired nonce invalidations=%v err=%v", paired, err)
	}
	duplicate := append(slices.Clone(logs), log(3, actionTopic))
	if _, err := verifyGovernanceReceiptLogs(duplicate, GovernanceExpectedReceipt{WorkflowID: workflowID, PayloadHash: payloadHash},
		GovernanceRule{Contract: contract, FunctionSelector: "0x12345678", ActionEventSignature: actionTopic, MultipleActionEvents: true},
		txHash, 100, blockHash, 4); !errors.Is(err, ErrGovernanceReceiptInvalid) {
		t.Fatalf("duplicate receipt log index error=%v", err)
	}
	if _, err := verifyGovernanceReceiptLogs(logs, GovernanceExpectedReceipt{WorkflowID: workflowID, PayloadHash: payloadHash},
		GovernanceRule{Contract: contract, FunctionSelector: "0x12345678", ActionEventSignature: actionTopic, MultipleActionEvents: true, ExpectedActionEvents: 3},
		txHash, 100, blockHash, 4); !errors.Is(err, ErrGovernanceReceiptInvalid) {
		t.Fatalf("wrong nonce-invalidation count error=%v", err)
	}
}

func TestGovernanceLogWindowsAreBoundedAndOverflowSafe(t *testing.T) {
	got, err := governanceLogWindows(1, 25_001)
	if want := [][2]uint64{{1, 10_000}, {10_001, 20_000}, {20_001, 25_001}}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("windows=%v want=%v", got, want)
	}
	maximum := ^uint64(0)
	got, err = governanceLogWindows(maximum-5, maximum)
	if err != nil || !slices.Equal(got, [][2]uint64{{maximum - 5, maximum}}) {
		t.Fatalf("overflow windows=%v", got)
	}
	if _, err := governanceLogWindows(1, maximum); err == nil {
		t.Fatal("untrusted provider head was allowed to allocate an unbounded governance scan")
	}
}

func rpcClient(fixtures map[string]*rpcFixture) *http.Client {
	return &http.Client{Transport: &fixtureTransport{fixtures: fixtures}, Timeout: time.Second}
}

func rpcBlockFixture(number uint64, timestamp time.Time) rpcBlock {
	return rpcBlock{Number: fmt.Sprintf("0x%x", number), Hash: testHash(number), Timestamp: fmt.Sprintf("0x%x", timestamp.Unix())}
}

func transferLog(expected ExpectedExecution) rpcLog {
	from := "0x" + strings.Repeat("0", 24) + expected.Sender[2:]
	to := "0x" + strings.Repeat("0", 24) + expected.Recipient[2:]
	amount := new(bigIntForTest)
	amount.SetString(expected.AmountAtomic)
	return rpcLog{Address: expected.Asset, Topics: []string{transferTopic, from, to}, Data: "0x" + fmt.Sprintf("%064x", amount.value)}
}

type bigIntForTest struct{ value uint64 }

func (b *bigIntForTest) SetString(value string) {
	_, _ = fmt.Sscan(value, &b.value)
}

func observerProviders() []RPCProvider {
	return []RPCProvider{{Name: "rpc_alpha", URL: "https://alpha.rpc.example/v1"}, {Name: "rpc_beta", URL: "https://beta.rpc.example/v1"}}
}

func TestObserverSetBuildsIndependentCanonicalSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 20, 30, 0, 0, time.UTC)
	fixtures := map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(101, now.Add(-time.Second)), "0x65": rpcBlockFixture(101, now.Add(-time.Second))}},
		"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(102, now.Add(-time.Second)), "0x65": rpcBlockFixture(101, now.Add(-time.Second))}},
	}
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := observers.Snapshot(context.Background())
	if len(result.Failures) != 0 || len(result.Observations) != 2 {
		t.Fatalf("Snapshot() = %+v", result)
	}
	for _, observation := range result.Observations {
		if observation.AnchorNumber != 101 || observation.AnchorHash != testHash(101) || observation.ObservedAt != now {
			t.Fatalf("observation = %+v", observation)
		}
	}
}

func TestObserverSetReportsWrongChainWithoutManufacturingQuorum(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 20, 30, 0, 0, time.UTC)
	fixtures := map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(101, now), "0x65": rpcBlockFixture(101, now)}},
		"beta.rpc.example":  {chainID: "0x1", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(101, now)}},
	}
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := observers.Snapshot(context.Background())
	if len(result.Observations) != 1 || !strings.Contains(result.Failures["rpc_beta"], "wrong chain") {
		t.Fatalf("Snapshot() = %+v", result)
	}
}

func TestObserverSetDecodesCanonicalUSDCReceiptEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 20, 30, 0, 0, time.UTC)
	expected := testExpected()
	receipt := &rpcReceipt{
		TransactionHash: expected.TransactionHash, BlockNumber: "0x65", BlockHash: testHash(101), Status: "0x1",
		Logs: []rpcLog{transferLog(expected)},
	}
	fixtures := map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(103, now)}, receipt: receipt},
		"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(104, now)}, receipt: receipt},
	}
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := observers.ReceiptQuorum(context.Background(), expected)
	if len(result.Failures) != 0 || len(result.Evidence) != 2 {
		t.Fatalf("ReceiptQuorum() = %+v", result)
	}
	for _, evidence := range result.Evidence {
		if !evidence.Success || evidence.BlockNumber != 101 || evidence.Sender != expected.Sender || evidence.AmountAtomic != "100" {
			t.Fatalf("evidence = %+v", evidence)
		}
	}
}

func TestObserverSetBuildsCanonicalReorgEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 20, 30, 0, 0, time.UTC)
	execution := Execution{Expected: testExpected(), State: ExecutionSettled, BlockNumber: 101, BlockHash: testHash(101)}
	fixtures := map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"0x65": rpcBlockFixture(101, now), "latest": rpcBlockFixture(113, now)}},
		"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"0x65": rpcBlockFixture(101, now), "latest": rpcBlockFixture(114, now)}},
	}
	for _, fixture := range fixtures {
		canonical := fixture.blocks["0x65"]
		canonical.Hash = testHash(1601)
		fixture.blocks["0x65"] = canonical
	}
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := observers.ReorgQuorum(context.Background(), execution)
	if len(result.Failures) != 0 || len(result.Evidence) != 2 {
		t.Fatalf("ReorgQuorum() = %+v", result)
	}
	for _, evidence := range result.Evidence {
		if evidence.OriginalBlockHash != testHash(101) || evidence.CanonicalBlockHash != testHash(1601) || evidence.ObservedHead < 113 {
			t.Fatalf("reorg evidence = %+v", evidence)
		}
	}
}

func TestObserverSetBuildsPositiveCanonicalBlockEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 20, 30, 0, 0, time.UTC)
	execution := Execution{Expected: testExpected(), State: ExecutionSettled, BlockNumber: 101, BlockHash: testHash(101)}
	fixtures := map[string]*rpcFixture{
		"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"0x65": rpcBlockFixture(101, now), "latest": rpcBlockFixture(113, now)}},
		"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"0x65": rpcBlockFixture(101, now), "latest": rpcBlockFixture(114, now)}},
	}
	observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := observers.CanonicalBlockQuorum(context.Background(), execution)
	if len(result.Failures) != 0 || len(result.Evidence) != 2 {
		t.Fatalf("CanonicalBlockQuorum() = %+v", result)
	}
	for _, evidence := range result.Evidence {
		if evidence.CanonicalBlockHash != execution.BlockHash || evidence.ObservedHead < 113 {
			t.Fatalf("canonical evidence = %+v", evidence)
		}
	}
	legacy := observers.ReorgQuorum(context.Background(), execution)
	if len(legacy.Evidence) != 0 || len(legacy.Failures) != 2 {
		t.Fatalf("ReorgQuorum() = %+v", legacy)
	}
}

func TestObserverSetRejectsMissingDuplicateOrRemovedTransfer(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 20, 30, 0, 0, time.UTC)
	expected := testExpected()
	valid := transferLog(expected)
	tests := []struct {
		name string
		logs []rpcLog
	}{
		{"missing", nil},
		{"duplicate", []rpcLog{valid, valid}},
		{"removed", []rpcLog{{Address: valid.Address, Topics: valid.Topics, Data: valid.Data, Removed: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			receipt := &rpcReceipt{TransactionHash: expected.TransactionHash, BlockNumber: "0x65", BlockHash: testHash(101), Status: "0x1", Logs: test.logs}
			fixtures := map[string]*rpcFixture{
				"alpha.rpc.example": {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(103, now)}, receipt: receipt},
				"beta.rpc.example":  {chainID: "0x14a34", blocks: map[string]rpcBlock{"latest": rpcBlockFixture(103, now)}, receipt: receipt},
			}
			observers, err := NewObserverSet(84532, observerProviders(), rpcClient(fixtures), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			result := observers.ReceiptQuorum(context.Background(), expected)
			if len(result.Evidence) != 0 || len(result.Failures) != 2 {
				t.Fatalf("ReceiptQuorum() = %+v", result)
			}
		})
	}
}

func TestObserverSetRequiresDistinctHTTPSProviderHosts(t *testing.T) {
	t.Parallel()
	client := rpcClient(nil)
	if _, err := NewObserverSet(84532, []RPCProvider{{Name: "one", URL: "http://one.example"}, {Name: "two", URL: "https://two.example"}}, client, nil); err == nil {
		t.Fatal("HTTP provider configuration succeeded")
	}
	if _, err := NewObserverSet(84532, []RPCProvider{{Name: "one", URL: "https://same.example/a"}, {Name: "two", URL: "https://same.example/b"}}, client, nil); err == nil {
		t.Fatal("same-host providers were treated as independent")
	}
	if _, err := NewObserverSet(84532, []RPCProvider{{Name: "one", URL: "https://same.example/a"}, {Name: "two", URL: "https://SAME.EXAMPLE./b"}}, client, nil); err == nil {
		t.Fatal("DNS-equivalent trailing-dot providers were treated as independent")
	}
}

func TestHexQuantityParsingIsCanonical(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{"0x0", "0x1", "0xff"} {
		if _, err := parseHexUint64(valid); err != nil {
			t.Errorf("parseHexUint64(%q) error = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "0x", "0x00", "1", "0X1", "0xgg"} {
		if _, err := parseHexUint64(invalid); err == nil {
			t.Errorf("parseHexUint64(%q) unexpectedly succeeded", invalid)
		}
	}
}
