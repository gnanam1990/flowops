package reconciliation

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRPCResponseBytes = 1 << 20
	transferTopic       = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

type RPCProvider struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type ObserverSet struct {
	chainID   uint64
	providers []RPCProvider
	client    *http.Client
	clock     func() time.Time
}

// ChainID returns the immutable Base domain validated at construction. It is
// used by higher-level authorization gates so a command cannot be approved for
// a different Base network and merely fail during later receipt observation.
func (s *ObserverSet) ChainID() uint64 {
	if s == nil {
		return 0
	}
	return s.chainID
}

type SnapshotResult struct {
	Observations []Observation     `json:"observations"`
	Failures     map[string]string `json:"failures,omitempty"`
}

type ReceiptResult struct {
	Evidence []ReceiptEvidence `json:"evidence"`
	Failures map[string]string `json:"failures,omitempty"`
}

type ReorgResult struct {
	Evidence []ReorgEvidence   `json:"evidence"`
	Failures map[string]string `json:"failures,omitempty"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcBlock struct {
	Number    string `json:"number"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

type rpcReceipt struct {
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex,omitempty"`
	BlockNumber      string   `json:"blockNumber"`
	BlockHash        string   `json:"blockHash"`
	Status           string   `json:"status"`
	Logs             []rpcLog `json:"logs"`
}

type rpcTransaction struct {
	Hash        string `json:"hash"`
	From        string `json:"from"`
	BlockNumber string `json:"blockNumber"`
	BlockHash   string `json:"blockHash"`
}

type rpcLog struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	Removed          bool     `json:"removed"`
	BlockNumber      string   `json:"blockNumber,omitempty"`
	BlockHash        string   `json:"blockHash,omitempty"`
	TransactionHash  string   `json:"transactionHash,omitempty"`
	TransactionIndex string   `json:"transactionIndex,omitempty"`
	LogIndex         string   `json:"logIndex,omitempty"`
}

func NewObserverSet(chainID uint64, providers []RPCProvider, client *http.Client, clock func() time.Time) (*ObserverSet, error) {
	if chainID != 8453 && chainID != 84532 {
		return nil, errors.New("observer set supports Base mainnet or Base Sepolia only")
	}
	if len(providers) < 2 || len(providers) > 5 {
		return nil, errors.New("observer set requires two to five independent providers")
	}
	names := make(map[string]struct{}, len(providers))
	hosts := make(map[string]struct{}, len(providers))
	normalized := make([]RPCProvider, 0, len(providers))
	for _, provider := range providers {
		if !identifierPattern.MatchString(provider.Name) {
			return nil, errors.New("RPC provider name is invalid")
		}
		if _, exists := names[provider.Name]; exists {
			return nil, errors.New("RPC provider names must be unique")
		}
		names[provider.Name] = struct{}{}
		parsed, err := url.Parse(strings.TrimSpace(provider.URL))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("RPC provider %s must use an HTTPS URL without credentials or fragment", provider.Name)
		}
		host := strings.TrimRight(strings.ToLower(parsed.Hostname()), ".")
		if host == "" {
			return nil, fmt.Errorf("RPC provider %s has an invalid hostname", provider.Name)
		}
		if _, exists := hosts[host]; exists {
			return nil, errors.New("RPC provider hosts must be distinct to contribute independent observations")
		}
		hosts[host] = struct{}{}
		provider.URL = parsed.String()
		normalized = append(normalized, provider)
	}
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second,
				MaxResponseHeaderBytes: 64 << 10,
			},
		}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &clientCopy
	if clock == nil {
		clock = time.Now
	}
	return &ObserverSet{chainID: chainID, providers: normalized, client: client, clock: clock}, nil
}

func (s *ObserverSet) Snapshot(ctx context.Context) SnapshotResult {
	type headResult struct {
		provider RPCProvider
		block    parsedBlock
		err      error
	}
	heads := make(chan headResult, len(s.providers))
	var group sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		group.Add(1)
		go func() {
			defer group.Done()
			if err := s.verifyChain(ctx, provider); err != nil {
				heads <- headResult{provider: provider, err: err}
				return
			}
			block, err := s.block(ctx, provider, "latest")
			heads <- headResult{provider: provider, block: block, err: err}
		}()
	}
	group.Wait()
	close(heads)
	failures := make(map[string]string)
	available := make([]headResult, 0, len(s.providers))
	var anchor uint64
	for result := range heads {
		if result.err != nil {
			failures[result.provider.Name] = result.err.Error()
			continue
		}
		if len(available) == 0 || result.block.number < anchor {
			anchor = result.block.number
		}
		available = append(available, result)
	}
	type observationResult struct {
		observation Observation
		provider    string
		err         error
	}
	observations := make(chan observationResult, len(available))
	anchorTag := fmt.Sprintf("0x%x", anchor)
	for _, head := range available {
		head := head
		group.Add(1)
		go func() {
			defer group.Done()
			anchorBlock, err := s.block(ctx, head.provider, anchorTag)
			if err != nil {
				observations <- observationResult{provider: head.provider.Name, err: err}
				return
			}
			observations <- observationResult{provider: head.provider.Name, observation: Observation{
				Provider: head.provider.Name, ChainID: s.chainID,
				HeadNumber: head.block.number, HeadHash: head.block.hash, HeadTime: head.block.timestamp,
				AnchorNumber: anchorBlock.number, AnchorHash: anchorBlock.hash, AnchorTime: anchorBlock.timestamp,
				ObservedAt: s.clock().UTC(),
			}}
		}()
	}
	group.Wait()
	close(observations)
	result := SnapshotResult{Failures: failures}
	for observation := range observations {
		if observation.err != nil {
			result.Failures[observation.provider] = observation.err.Error()
			continue
		}
		result.Observations = append(result.Observations, observation.observation)
	}
	sort.Slice(result.Observations, func(i, j int) bool { return result.Observations[i].Provider < result.Observations[j].Provider })
	if len(result.Failures) == 0 {
		result.Failures = nil
	}
	return result
}

func (s *ObserverSet) ReceiptQuorum(ctx context.Context, expected ExpectedExecution) ReceiptResult {
	type providerResult struct {
		provider string
		evidence ReceiptEvidence
		err      error
	}
	results := make(chan providerResult, len(s.providers))
	var group sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		group.Add(1)
		go func() {
			defer group.Done()
			evidence, err := s.receipt(ctx, provider, expected)
			results <- providerResult{provider: provider.Name, evidence: evidence, err: err}
		}()
	}
	group.Wait()
	close(results)
	output := ReceiptResult{Failures: make(map[string]string)}
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

func (s *ObserverSet) ReorgQuorum(ctx context.Context, execution Execution) ReorgResult {
	result := s.CanonicalBlockQuorum(ctx, execution)
	filtered := ReorgResult{Failures: result.Failures}
	for _, evidence := range result.Evidence {
		if evidence.CanonicalBlockHash == execution.BlockHash {
			if filtered.Failures == nil {
				filtered.Failures = make(map[string]string)
			}
			filtered.Failures[evidence.Provider] = "provider still reports the original settlement block as canonical"
			continue
		}
		filtered.Evidence = append(filtered.Evidence, evidence)
	}
	return filtered
}

func (s *ObserverSet) CanonicalBlockQuorum(ctx context.Context, execution Execution) ReorgResult {
	return s.canonicalBlockQuorum(ctx, execution.Expected.TransactionHash, execution.BlockNumber, execution.BlockHash)
}

// EscrowCanonicalBlockQuorum checks whether the exact block that confirmed an
// escrow transition remains canonical. It is read-only and cannot submit or
// retry the transition.
func (s *ObserverSet) EscrowCanonicalBlockQuorum(ctx context.Context, transition EscrowTransition) ReorgResult {
	return s.canonicalBlockQuorum(ctx, transition.Expected.TransactionHash, transition.BlockNumber, transition.BlockHash)
}

func (s *ObserverSet) canonicalBlockQuorum(ctx context.Context, transactionHash string, blockNumber uint64, blockHash string) ReorgResult {
	type providerResult struct {
		provider string
		evidence ReorgEvidence
		err      error
	}
	results := make(chan providerResult, len(s.providers))
	var group sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		group.Add(1)
		go func() {
			defer group.Done()
			if err := s.verifyChain(ctx, provider); err != nil {
				results <- providerResult{provider: provider.Name, err: err}
				return
			}
			canonical, err := s.block(ctx, provider, fmt.Sprintf("0x%x", blockNumber))
			if err != nil {
				results <- providerResult{provider: provider.Name, err: err}
				return
			}
			latest, err := s.block(ctx, provider, "latest")
			if err != nil {
				results <- providerResult{provider: provider.Name, err: err}
				return
			}
			results <- providerResult{provider: provider.Name, evidence: ReorgEvidence{
				Provider: provider.Name, ChainID: s.chainID, TransactionHash: transactionHash,
				OriginalBlockNumber: blockNumber, OriginalBlockHash: blockHash,
				CanonicalBlockHash: canonical.hash, ObservedHead: latest.number,
			}}
		}()
	}
	group.Wait()
	close(results)
	output := ReorgResult{Failures: make(map[string]string)}
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

type parsedBlock struct {
	number    uint64
	hash      string
	timestamp time.Time
}

func (s *ObserverSet) verifyChain(ctx context.Context, provider RPCProvider) error {
	var chainHex string
	if err := s.call(ctx, provider, "eth_chainId", nil, &chainHex); err != nil {
		return err
	}
	chainID, err := parseHexUint64(chainHex)
	if err != nil || chainID != s.chainID {
		return errors.New("RPC provider returned the wrong chain ID")
	}
	return nil
}

func (s *ObserverSet) block(ctx context.Context, provider RPCProvider, tag string) (parsedBlock, error) {
	var block *rpcBlock
	if err := s.call(ctx, provider, "eth_getBlockByNumber", []any{tag, false}, &block); err != nil {
		return parsedBlock{}, err
	}
	if block == nil {
		return parsedBlock{}, errors.New("RPC block result is null")
	}
	number, err := parseHexUint64(block.Number)
	if err != nil {
		return parsedBlock{}, errors.New("RPC block number is invalid")
	}
	timestamp, err := parseHexUint64(block.Timestamp)
	if err != nil || timestamp > uint64(^uint64(0)>>1) {
		return parsedBlock{}, errors.New("RPC block timestamp is invalid")
	}
	hash, err := canonicalHash(block.Hash)
	if err != nil {
		return parsedBlock{}, errors.New("RPC block hash is invalid")
	}
	return parsedBlock{number: number, hash: hash, timestamp: time.Unix(int64(timestamp), 0).UTC()}, nil
}

func (s *ObserverSet) receipt(ctx context.Context, provider RPCProvider, expected ExpectedExecution) (ReceiptEvidence, error) {
	if err := s.verifyChain(ctx, provider); err != nil {
		return ReceiptEvidence{}, err
	}
	var receipt *rpcReceipt
	if err := s.call(ctx, provider, "eth_getTransactionReceipt", []any{expected.TransactionHash}, &receipt); err != nil {
		return ReceiptEvidence{}, err
	}
	if receipt == nil {
		return ReceiptEvidence{}, errors.New("transaction receipt is not available")
	}
	txHash, err := canonicalHash(receipt.TransactionHash)
	if err != nil || txHash != expected.TransactionHash {
		return ReceiptEvidence{}, errors.New("receipt transaction hash does not match execution")
	}
	blockNumber, err := parseHexUint64(receipt.BlockNumber)
	if err != nil || blockNumber == 0 {
		return ReceiptEvidence{}, errors.New("receipt block number is invalid")
	}
	blockHash, err := canonicalHash(receipt.BlockHash)
	if err != nil {
		return ReceiptEvidence{}, errors.New("receipt block hash is invalid")
	}
	status, err := parseHexUint64(receipt.Status)
	if err != nil || status > 1 {
		return ReceiptEvidence{}, errors.New("receipt status is invalid")
	}
	latest, err := s.block(ctx, provider, "latest")
	if err != nil {
		return ReceiptEvidence{}, err
	}
	if latest.number < blockNumber {
		return ReceiptEvidence{}, errors.New("provider head precedes the receipt block")
	}
	if status == 1 {
		if err := verifyTransferLog(receipt.Logs, expected); err != nil {
			return ReceiptEvidence{}, err
		}
	}
	return ReceiptEvidence{
		Provider: provider.Name, ChainID: s.chainID, TransactionHash: txHash,
		BlockNumber: blockNumber, BlockHash: blockHash, ConfirmedHead: latest.number, Success: status == 1,
		Sender: expected.Sender, Asset: expected.Asset, Recipient: expected.Recipient, AmountAtomic: expected.AmountAtomic,
	}, nil
}

func verifyTransferLog(logs []rpcLog, expected ExpectedExecution) error {
	matches := 0
	for _, event := range logs {
		if event.Removed || strings.ToLower(event.Address) != expected.Asset || len(event.Topics) != 3 || strings.ToLower(event.Topics[0]) != transferTopic {
			continue
		}
		from, err := addressFromTopic(event.Topics[1])
		if err != nil {
			continue
		}
		to, err := addressFromTopic(event.Topics[2])
		if err != nil {
			continue
		}
		amount, err := dataUint256(event.Data)
		if err != nil {
			continue
		}
		if from == expected.Sender && to == expected.Recipient && amount.String() == expected.AmountAtomic {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("receipt must contain exactly one expected native-USDC Transfer event")
	}
	return nil
}

func (s *ObserverSet) call(ctx context.Context, provider RPCProvider, method string, params []any, output any) error {
	if params == nil {
		params = []any{}
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Host = request.URL.Host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("RPC %s request failed: %w", method, err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.String() != request.URL.String() {
		return errors.New("RPC provider redirected the request")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("RPC %s returned HTTP %d", method, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRPCResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxRPCResponseBytes {
		return errors.New("RPC response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope rpcResponse
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode RPC %s response: %w", method, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if envelope.JSONRPC != "2.0" || envelope.ID != 1 {
		return errors.New("RPC response envelope is invalid")
	}
	if envelope.Error != nil {
		return fmt.Errorf("RPC %s error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		return errors.New("RPC response result is missing")
	}
	if err := json.Unmarshal(envelope.Result, output); err != nil {
		return fmt.Errorf("decode RPC %s result: %w", method, err)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("RPC response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func parseHexUint64(value string) (uint64, error) {
	if len(value) < 3 || !strings.HasPrefix(value, "0x") || value[2] == '0' && len(value) > 3 {
		return 0, errors.New("non-canonical hex quantity")
	}
	return strconv.ParseUint(value[2:], 16, 64)
}

func canonicalHash(value string) (string, error) {
	value = strings.ToLower(value)
	if !hashPattern.MatchString(value) {
		return "", errors.New("invalid 32-byte hash")
	}
	return value, nil
}

func addressFromTopic(topic string) (string, error) {
	topic = strings.ToLower(topic)
	if !hashPattern.MatchString(topic) || topic[2:26] != strings.Repeat("0", 24) {
		return "", errors.New("invalid indexed address")
	}
	return "0x" + topic[26:], nil
}

func dataUint256(data string) (*big.Int, error) {
	if len(data) != 66 || !strings.HasPrefix(data, "0x") {
		return nil, errors.New("invalid uint256 event data")
	}
	raw, err := hex.DecodeString(data[2:])
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(raw), nil
}
