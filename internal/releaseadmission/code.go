package releaseadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

const maxRPCResponseBytes = 4 << 20

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
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func VerifyCodeQuorum(ctx context.Context, providers []reconciliation.RPCProvider, manifest Manifest, client *http.Client) error {
	if len(providers) < 2 || len(providers) > 5 {
		return errors.New("release bytecode verification requires two to five admitted providers")
	}
	bindings := ContractCodeBindings(manifest)
	if len(bindings) != 5 {
		return errors.New("release bytecode verification requires the complete contract and asset tuple")
	}
	httpClient := hardenedClient(client)
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(providers))
	var wait sync.WaitGroup
	for _, provider := range providers {
		provider := provider
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- result{name: provider.Name, err: verifyProviderCode(ctx, httpClient, provider, bindings)}
		}()
	}
	wait.Wait()
	close(results)
	for item := range results {
		if item.err != nil {
			return fmt.Errorf("release bytecode verification failed for provider %s: %w", item.name, item.err)
		}
	}
	return nil
}

func verifyProviderCode(ctx context.Context, client *http.Client, provider reconciliation.RPCProvider, bindings []ContractBinding) error {
	var chainID string
	if err := callRPC(ctx, client, provider.URL, 1, "eth_chainId", nil, &chainID); err != nil {
		return errors.New("chain identity unavailable")
	}
	if chainID != "0x2105" {
		return errors.New("provider returned the wrong chain")
	}
	for index, binding := range bindings {
		var encodedCode string
		if err := callRPC(ctx, client, provider.URL, index+2, "eth_getCode", []any{binding.Address, "latest"}, &encodedCode); err != nil {
			return fmt.Errorf("%s bytecode unavailable", binding.Name)
		}
		code, err := hexutil.Decode(encodedCode)
		if err != nil || len(code) == 0 {
			return fmt.Errorf("%s has empty or malformed bytecode", binding.Name)
		}
		if strings.ToLower(crypto.Keccak256Hash(code).Hex()) != binding.RuntimeCodeHash {
			return fmt.Errorf("%s runtime code hash mismatch", binding.Name)
		}
	}
	return nil
}

func callRPC(ctx context.Context, client *http.Client, endpoint string, id int, method string, params []any, output any) error {
	if params == nil {
		params = []any{}
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("RPC returned status " + strconv.Itoa(response.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRPCResponseBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxRPCResponseBytes {
		return errors.New("RPC response exceeded the bounded envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope rpcResponse
	if err := decoder.Decode(&envelope); err != nil {
		return errors.New("RPC response was not strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || envelope.JSONRPC != "2.0" || envelope.ID != id || envelope.Error != nil || len(envelope.Result) == 0 {
		return errors.New("RPC response did not match the request")
	}
	if err := json.Unmarshal(envelope.Result, output); err != nil {
		return errors.New("RPC result was malformed")
	}
	return nil
}

func hardenedClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Second}
	}
	copy := *base
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return errors.New("RPC redirects are disabled") }
	if copy.Timeout <= 0 || copy.Timeout > 15*time.Second {
		copy.Timeout = 10 * time.Second
	}
	return &copy
}
