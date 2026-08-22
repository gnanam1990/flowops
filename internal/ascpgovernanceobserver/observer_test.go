package ascpgovernanceobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

type governanceTransport struct {
	mu        sync.Mutex
	head      uint64
	block     uint64
	blockHash string
	txHash    string
	workflow  string
	payload   string
	contract  string
	selector  string
	action    string
	timestamp uint64
	invalid   map[string]bool
}

func (t *governanceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var call struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	log := func(index uint64, topics []string) map[string]any {
		return map[string]any{
			"address": t.contract, "topics": topics, "data": "0x", "removed": false,
			"blockNumber": fmt.Sprintf("0x%x", t.block), "blockHash": t.blockHash,
			"transactionHash": t.txHash, "transactionIndex": "0x0", "logIndex": fmt.Sprintf("0x%x", index),
		}
	}
	actionLog := log(1, []string{t.action})
	bindingLog := log(2, []string{eventTopic(governanceWorkflowBoundSignature), t.workflow, t.payload, t.selector + strings.Repeat("0", 56)})
	var result any
	switch call.Method {
	case "eth_chainId":
		result = "0x14a34"
	case "eth_getLogs":
		result = []any{bindingLog}
	case "eth_getTransactionReceipt":
		status := "0x1"
		if t.invalid[request.URL.Hostname()] {
			status = "0x0"
		}
		result = map[string]any{"transactionHash": t.txHash, "blockNumber": fmt.Sprintf("0x%x", t.block),
			"blockHash": t.blockHash, "status": status, "logs": []any{actionLog, bindingLog}}
	case "eth_getBlockByNumber":
		var params []any
		_ = json.Unmarshal(call.Params, &params)
		number := t.block
		hash := t.blockHash
		if len(params) > 0 && params[0] == "latest" {
			number = t.head
			hash = testHash(t.head)
		}
		result = map[string]any{"number": fmt.Sprintf("0x%x", number), "hash": hash, "timestamp": fmt.Sprintf("0x%x", t.timestamp)}
	default:
		return nil, fmt.Errorf("unexpected RPC method %s", call.Method)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func TestObserverDiscoversFinalizedReceiptWithoutCallerEvidence(t *testing.T) {
	spend := "0x2222222222222222222222222222222222222222"
	action := governanceworkflow.Action{
		Type: governanceworkflow.ActionSpendCaps, ChainID: 84532, ContractAddress: spend,
		SpendCaps: &governanceworkflow.SpendCapsAction{
			Current: governanceworkflow.Caps{PerTransaction: "100", PerDay: "200", AllowanceCeiling: "300"},
			Next:    governanceworkflow.Caps{PerTransaction: "101", PerDay: "201", AllowanceCeiling: "301"},
		},
	}
	bound, err := governanceworkflow.BindAction(testHash(1), action)
	if err != nil {
		t.Fatal(err)
	}
	selector := bound.FunctionSelector
	workflow := ascpworkflow.Workflow{WorkflowID: testHash(1), PayloadHash: bound.PayloadHash, Kind: ascpworkflow.SignerCaps,
		State: ascpworkflow.ApprovedPendingChain, ApprovedAt: 1_800_000_000, ChainID: 84532,
		ContractAddress: spend, FunctionSelector: selector, Calldata: bound.Calldata,
		GovernanceAction: bound.CanonicalAction}
	transport := &governanceTransport{head: 120, block: 100, blockHash: testHash(100), txHash: testHash(3), timestamp: 1_800_000_001,
		workflow: workflow.WorkflowID, payload: workflow.PayloadHash, contract: spend,
		selector: selector,
		action:   eventTopic("CapsScheduled(uint256,uint256,uint256,uint64)"),
	}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	providers := []reconciliation.RPCProvider{{Name: "rpc_a", URL: "https://a.rpc.example"}, {Name: "rpc_b", URL: "https://b.rpc.example"}}
	set, err := reconciliation.NewObserverSet(84532, providers, client, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := New(Config{Observers: set, Quorum: 2, FinalizedConfirmations: 12, FromBlock: 80,
		CallEscrowContract: "0x1111111111111111111111111111111111111111", SpendModuleContract: spend,
		DirectoryContract: "0x3333333333333333333333333333333333333333"})
	if err != nil {
		t.Fatal(err)
	}
	if err != nil || observer.ValidateGovernanceAction(bound) != nil {
		t.Fatalf("configured governance target rejected: bound=%+v err=%v", bound, err)
	}
	wrongTarget := bound
	wrongTarget.ContractAddress = "0x1111111111111111111111111111111111111111"
	if err := observer.ValidateGovernanceAction(wrongTarget); !errors.Is(err, ErrUnsupportedWorkflow) {
		t.Fatalf("unconfigured governance target error=%v", err)
	}
	wrongChain := bound
	wrongChain.ChainID = 8453
	if err := observer.ValidateGovernanceAction(wrongChain); !errors.Is(err, ErrUnsupportedWorkflow) {
		t.Fatalf("wrong governance chain error=%v", err)
	}
	receipt, err := observer.ObserveWorkflowCompletion(t.Context(), workflow)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.WorkflowID != workflow.WorkflowID || receipt.TransactionHash != transport.txHash || receipt.LogIndex != 2 ||
		receipt.FunctionSelector != transport.selector || receipt.BlockTimestamp != transport.timestamp ||
		len(receipt.Observers) != 2 || receipt.EvidenceDigest == "" {
		t.Fatalf("receipt=%+v", receipt)
	}
	late := workflow
	late.State, late.SubmissionTxHash = ascpworkflow.TimedOut, transport.txHash
	if recovered, err := observer.ObserveWorkflowCompletion(t.Context(), late); err != nil || recovered.TransactionHash != transport.txHash {
		t.Fatalf("late finalized receipt=%+v err=%v", recovered, err)
	}

	transport.mu.Lock()
	transport.head = 105
	transport.mu.Unlock()
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), workflow); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("unsafe finality error=%v", err)
	}

	transport.mu.Lock()
	transport.head = 120
	transport.action = testHash(999)
	transport.mu.Unlock()
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), workflow); !errors.Is(err, ErrReceiptRejected) {
		t.Fatalf("deterministically invalid receipt error=%v", err)
	}
	transport.mu.Lock()
	transport.action = eventTopic("CapsScheduled(uint256,uint256,uint256,uint64)")
	transport.timestamp = uint64(workflow.ApprovedAt)
	transport.mu.Unlock()
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), workflow); !errors.Is(err, ErrReceiptRejected) {
		t.Fatalf("pre-approval receipt error=%v", err)
	}
	transport.mu.Lock()
	transport.timestamp = uint64(workflow.ApprovedAt + 1)
	transport.mu.Unlock()
	wrongAction := workflow
	wrongAction.FunctionSelector = functionSelector("setSpendAuthorizer(address,bytes32,bytes32)")
	wrongAction.Calldata = wrongAction.FunctionSelector + strings.Repeat("0", 64)
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), wrongAction); !errors.Is(err, ErrReceiptRejected) {
		t.Fatalf("stored selector substitution error=%v", err)
	}
	wrongContract := workflow
	wrongContract.ContractAddress = "0x1111111111111111111111111111111111111111"
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), wrongContract); !errors.Is(err, ErrReceiptRejected) {
		t.Fatalf("stored contract substitution error=%v", err)
	}
	tamperedAction := action
	tamperedAction.SpendCaps = &governanceworkflow.SpendCapsAction{Current: action.SpendCaps.Current,
		Next: governanceworkflow.Caps{PerTransaction: "102", PerDay: "202", AllowanceCeiling: "302"}}
	wrongBytes, err := json.Marshal(tamperedAction)
	if err != nil {
		t.Fatal(err)
	}
	wrongAction = workflow
	wrongAction.GovernanceAction = wrongBytes
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), wrongAction); !errors.Is(err, ErrReceiptRejected) {
		t.Fatalf("stored action substitution error=%v", err)
	}
}

func TestGovernanceRuleSelectorsAndTopicsMatchContractABI(t *testing.T) {
	if eventTopic(governanceWorkflowBoundSignature) != "0x71840a8df3cf7e14c302ff72b4fd1c651a2845389dfb0a4fdd884a2ffb104bfe" {
		t.Fatal("workflow binding topic drifted from the Solidity ABI")
	}
	tests := []struct{ function, selector, event, topic string }{
		{"addVerifier(address,uint64,bytes32,bytes32)", "0x0627f1cb", "VerifierAdded(address,uint64,uint64)", "0xd688a4337a58a32948cc5c9e6e70ed797ee202fb623df5f7f157e567d3cc2d1a"},
		{"revokeVerifier(address,bytes32,bytes32)", "0x9080ef8c", "VerifierRevoked(address,uint64,uint64)", "0x96562d33c3096add79efe05f7a36dbb95d0f4fc467741418902094addd707aa1"},
		{"setEmergencyPause(bytes32,bytes32)", "0x362b1eb1", "EmergencyPauseSet()", "0x016cfd9d949dae8f901c57daa5a15ca2999514294fb3d33c0ca4bf89d1c3f34d"},
		{"setSpendAuthorizer(address,bytes32,bytes32)", "0x5e12413c", "SpendAuthorizerSet(address,uint64)", "0x73c933d1a00c5a2fd954f1c10e9423e53d94cc4c31c9cd9f77e4d4495c612f55"},
		{"setEscrowAllowlist(address,bytes32,bytes32,bytes32)", "0x7a22532a", "EscrowAllowlistSet(address,bytes32)", "0x02b8c7e709e3f27c20a4ecb3669d2682fcba9309e1902881bf1814c71b9f6eb3"},
		{"scheduleCaps((uint256,uint256,uint256),bytes32,bytes32)", "0x4863f194", "CapsScheduled(uint256,uint256,uint256,uint64)", "0x6040444013009a863522866cc7dc4131940355951a105ea775750a7cdb24f163"},
		{"setEmergencyPause(bool,bytes32,bytes32)", "0x97fd05e8", "EmergencyPauseSet(bool)", "0xc8b290589fc182b8da42313f406cbe272a988911356e5dfcf9d3afccfac6a8f2"},
		{"invalidateNonces(bytes32[],bytes32,bytes32)", "0xdb7e096a", "NonceInvalidated(bytes32)", "0xb024b3933035941c9887c66d2e351d64ce67c4cf4681410bcbddf138f97a97dd"},
		{"approveVersion(uint64,bytes32)", "0x0bf45ed9", "VersionApproved(bytes32,uint64,address,uint64,uint64)", "0x68e3d7e42d2c57374760341c7d800e7bafc59288eb48bc28a7ce23a9349d40c7"},
		{"cancelVersion(uint64,bytes32,bytes32,bytes32)", "0xcc34e4d1", "VersionCancelled(bytes32,uint64,address)", "0x7f3fed08b4017fbaa9bde1ad44c31b29cd63d8876f878607694dcbe875a75a8f"},
	}
	for _, test := range tests {
		if got := functionSelector(test.function); got != test.selector {
			t.Errorf("selector %s = %s want %s", test.function, got, test.selector)
		}
		if got := eventTopic(test.event); got != test.topic {
			t.Errorf("topic %s = %s want %s", test.event, got, test.topic)
		}
	}
}

func TestObserverRuleMapCoversEveryChainWorkflowKind(t *testing.T) {
	observer := &Observer{
		callEscrow:  "0x1111111111111111111111111111111111111111",
		spendModule: "0x2222222222222222222222222222222222222222",
		directory:   "0x3333333333333333333333333333333333333333",
	}
	tests := []struct {
		kind  ascpworkflow.Kind
		count int
	}{
		{ascpworkflow.PayoutChange, 1}, {ascpworkflow.SignerCaps, 1}, {ascpworkflow.VerifierGovernance, 2},
		{ascpworkflow.BreakGlass, 2}, {ascpworkflow.ModuleGovernance, 3}, {ascpworkflow.DirectoryCancel, 1},
	}
	for _, test := range tests {
		rules, err := observer.rules(test.kind)
		if err != nil || len(rules) != test.count {
			t.Fatalf("kind=%s rules=%+v err=%v", test.kind, rules, err)
		}
		for _, rule := range rules {
			if !address(rule.Contract) || rule.FunctionSelector == "0x00000000" || rule.ActionEventSignature == testHash(0) {
				t.Fatalf("kind=%s invalid rule=%+v", test.kind, rule)
			}
		}
	}
	if _, err := observer.rules(ascpworkflow.ProductionGate); !errors.Is(err, ErrUnsupportedWorkflow) {
		t.Fatalf("non-chain workflow mapping error=%v", err)
	}
}

func TestObserverAndWorkerRejectInvalidConfiguration(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("observer config error=%v", err)
	}
	if _, err := NewWorker(nil, nil, WorkerConfig{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("worker config error=%v", err)
	}
}

func TestCanonicalEvidenceQuorumDoesNotRequireUnanimity(t *testing.T) {
	base := reconciliation.GovernanceReceiptEvidence{
		Provider: "rpc_a", ChainID: 84532, WorkflowID: testHash(1), PayloadHash: testHash(2),
		TransactionHash: testHash(3), BlockNumber: 100, BlockHash: testHash(4), BindingLogIndex: 2,
		BlockTimestamp:  1_800_000_001,
		ContractAddress: "0x1111111111111111111111111111111111111111", FunctionSelector: "0x12345678",
		ActionEventSignature: testHash(5), ActionLogIndexes: []uint64{1}, ConfirmedHead: 130, FinalizedHead: 120,
	}
	agreeing := base
	agreeing.Provider = "rpc_b"
	dissenting := base
	dissenting.Provider = "rpc_c"
	dissenting.TransactionHash = testHash(99)
	selected, ok := agreeingQuorum([]reconciliation.GovernanceReceiptEvidence{base, agreeing, dissenting}, 2)
	if !ok || len(selected) != 2 || selected[0].Provider != "rpc_a" || selected[1].Provider != "rpc_b" {
		t.Fatalf("2-of-3 quorum selected=%+v ok=%t", selected, ok)
	}

	secondDissent := dissenting
	secondDissent.Provider = "rpc_d"
	if selected, ok := agreeingQuorum([]reconciliation.GovernanceReceiptEvidence{base, agreeing, dissenting, secondDissent}, 2); ok || selected != nil {
		t.Fatalf("ambiguous 2-vs-2 split selected=%+v ok=%t", selected, ok)
	}
}

func TestObserverDefersValidVersusInvalidQuorumSplit(t *testing.T) {
	spend := "0x2222222222222222222222222222222222222222"
	action := governanceworkflow.Action{
		Type: governanceworkflow.ActionSpendCaps, ChainID: 84532, ContractAddress: spend,
		SpendCaps: &governanceworkflow.SpendCapsAction{
			Current: governanceworkflow.Caps{PerTransaction: "100", PerDay: "200", AllowanceCeiling: "300"},
			Next:    governanceworkflow.Caps{PerTransaction: "101", PerDay: "201", AllowanceCeiling: "301"},
		},
	}
	bound, err := governanceworkflow.BindAction(testHash(71), action)
	if err != nil {
		t.Fatal(err)
	}
	workflow := ascpworkflow.Workflow{
		WorkflowID: testHash(71), PayloadHash: bound.PayloadHash, Kind: ascpworkflow.SignerCaps,
		State: ascpworkflow.ApprovedPendingChain, ApprovedAt: 1_800_000_000, ChainID: 84532,
		ContractAddress: spend, FunctionSelector: bound.FunctionSelector, Calldata: bound.Calldata,
		GovernanceAction: bound.CanonicalAction,
	}
	transport := &governanceTransport{
		head: 120, block: 100, blockHash: testHash(72), txHash: testHash(73), timestamp: 1_800_000_001,
		workflow: workflow.WorkflowID, payload: workflow.PayloadHash, contract: spend,
		selector: bound.FunctionSelector, action: eventTopic("CapsScheduled(uint256,uint256,uint256,uint64)"),
		invalid: map[string]bool{"c.rpc.example": true, "d.rpc.example": true},
	}
	providers := []reconciliation.RPCProvider{
		{Name: "rpc_a", URL: "https://a.rpc.example"}, {Name: "rpc_b", URL: "https://b.rpc.example"},
		{Name: "rpc_c", URL: "https://c.rpc.example"}, {Name: "rpc_d", URL: "https://d.rpc.example"},
	}
	set, err := reconciliation.NewObserverSet(84532, providers, &http.Client{Transport: transport, Timeout: time.Second}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := New(Config{Observers: set, Quorum: 2, FinalizedConfirmations: 12, FromBlock: 80,
		CallEscrowContract: "0x1111111111111111111111111111111111111111", SpendModuleContract: spend,
		DirectoryContract: "0x3333333333333333333333333333333333333333"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.ObserveWorkflowCompletion(t.Context(), workflow); !errors.Is(err, ErrObserverDisagreement) {
		t.Fatalf("valid/invalid quorum split error=%v", err)
	}
}

type pendingStoreStub struct{ workflows []ascpworkflow.Workflow }

func (s pendingStoreStub) Pending(_ context.Context, limit int, afterWorkflowID string) ([]ascpworkflow.Workflow, error) {
	start := 0
	if afterWorkflowID != "" {
		start = -1
		for index, workflow := range s.workflows {
			if workflow.WorkflowID == afterWorkflowID {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, ascpworkflow.ErrInvalidWorkflow
		}
	}
	end := start + limit
	if end > len(s.workflows) {
		end = len(s.workflows)
	}
	return s.workflows[start:end], nil
}

type completerStub struct {
	err           error
	calls         int
	terminalCalls int
}

func (s *completerStub) RequireReapproval(context.Context, string, string, ascpworkflow.TerminalReason) (ascpworkflow.Workflow, error) {
	s.terminalCalls++
	return ascpworkflow.Workflow{State: ascpworkflow.RequiresReapproval}, nil
}

func (s *completerStub) ObserveAndComplete(context.Context, string, string) (ascpworkflow.Workflow, error) {
	s.calls++
	return ascpworkflow.Workflow{}, s.err
}

func TestWorkerDefersUnfinalizedEvidenceAndFailsClosedOnOwnershipConflict(t *testing.T) {
	store := pendingStoreStub{workflows: []ascpworkflow.Workflow{{WorkflowID: testHash(1), OrganizationID: "org_a"}}}
	completer := &completerStub{err: fmt.Errorf("%w: %w", ascpworkflow.ErrInvalidReceipt, ErrReceiptPending)}
	worker, err := NewWorker(store, completer, WorkerConfig{Interval: time.Second, QueryTimeout: 100 * time.Millisecond, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(t.Context())
	if err != nil || cycle.Deferred != 1 || completer.calls != 1 {
		t.Fatalf("cycle=%+v calls=%d err=%v", cycle, completer.calls, err)
	}
	completer.err = fmt.Errorf("%w: %w", ascpworkflow.ErrInvalidReceipt, ErrReceiptRejected)
	cycle, err = worker.RunOnce(t.Context())
	if err != nil || cycle.Rejected != 1 || cycle.Deferred != 0 || completer.terminalCalls != 1 {
		t.Fatalf("rejected cycle=%+v err=%v", cycle, err)
	}
	completer.err = ascpworkflow.ErrReceiptOwned
	if _, err := worker.RunOnce(t.Context()); !errors.Is(err, ascpworkflow.ErrReceiptOwned) {
		t.Fatalf("ownership conflict error=%v", err)
	}
}

type rejectingObserver struct{}

func (rejectingObserver) ValidateGovernanceAction(governanceworkflow.BoundAction) error { return nil }

func (rejectingObserver) ObserveWorkflowCompletion(context.Context, ascpworkflow.Workflow) (ascpworkflow.CompletionReceipt, error) {
	return ascpworkflow.CompletionReceipt{}, ErrReceiptRejected
}

func TestWorkerTerminalizesDeterministicRejectionWithoutStarvingQueue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := ascpworkflow.NewMemoryStore()
	service, err := ascpworkflow.New(store, rejectingObserver{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	proposer := ascpworkflow.Actor{OrganizationID: "org_a", PrincipalID: "signer", Role: ascpworkflow.SignerOperator,
		StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)}
	workflow, err := service.Create(t.Context(), proposer, "create", ascpworkflow.CreateRequest{
		Kind: ascpworkflow.SignerCaps, WorkflowID: testHash(901), Action: &governanceworkflow.Action{
			Type: governanceworkflow.ActionSpendCaps, ChainID: 84532,
			ContractAddress: "0x2222222222222222222222222222222222222222",
			SpendCaps: &governanceworkflow.SpendCapsAction{
				Current: governanceworkflow.Caps{PerTransaction: "100", PerDay: "200", AllowanceCeiling: "300"},
				Next:    governanceworkflow.Caps{PerTransaction: "101", PerDay: "201", AllowanceCeiling: "301"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	approver := ascpworkflow.Actor{OrganizationID: "org_a", PrincipalID: "owner", Role: ascpworkflow.OrgAdmin,
		StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)}
	if _, err := service.Approve(t.Context(), approver, workflow.WorkflowID, "approve"); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(store, service, WorkerConfig{Interval: time.Second, QueryTimeout: 100 * time.Millisecond, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(t.Context())
	if err != nil || cycle.Rejected != 1 {
		t.Fatalf("rejection cycle=%+v err=%v", cycle, err)
	}
	stored, err := service.Get(t.Context(), approver, workflow.WorkflowID)
	if err != nil || stored.State != ascpworkflow.RequiresReapproval || stored.TerminalReason != ascpworkflow.ReceiptRejected {
		t.Fatalf("terminal workflow=%+v err=%v", stored, err)
	}
	next, err := worker.RunOnce(t.Context())
	if err != nil || next.Pending != 0 {
		t.Fatalf("terminal workflow remained queued: cycle=%+v err=%v", next, err)
	}
}

func TestWorkerRunsInitialCycleStopsOnCancellationAndProcessesBoundedBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	worker, err := NewWorker(pendingStoreStub{}, &completerStub{}, WorkerConfig{
		Interval: 20 * time.Millisecond, QueryTimeout: time.Millisecond, BatchSize: 10,
		OnCycle: func(WorkerCycle) { cancel() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("cancelled worker error=%v", err)
	}

	workflows := make([]ascpworkflow.Workflow, 11)
	for index := range workflows {
		workflows[index] = ascpworkflow.Workflow{WorkflowID: testHash(uint64(index + 1)), OrganizationID: "org_a"}
	}
	completer := &completerStub{}
	bounded, err := NewWorker(pendingStoreStub{workflows: workflows}, completer, WorkerConfig{
		Interval: time.Second, QueryTimeout: time.Millisecond, BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := bounded.RunOnce(t.Context())
	if err != nil || completer.calls != 10 || cycle.Pending != 10 || cycle.Completed != 10 {
		t.Fatalf("bounded cycle=%+v error=%v calls=%d", cycle, err, completer.calls)
	}
	cycle, err = bounded.RunOnce(t.Context())
	if err != nil || completer.calls != 11 || cycle.Pending != 1 || cycle.Completed != 1 {
		t.Fatalf("rotated cycle=%+v error=%v calls=%d", cycle, err, completer.calls)
	}
}

func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
