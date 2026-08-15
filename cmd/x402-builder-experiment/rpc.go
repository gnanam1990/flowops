package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const maxRPCResponseBytes = 2 << 20

type rpcChainClient struct {
	endpoint string
	client   *http.Client
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func newRPCChainClient(endpoint string) (*rpcChainClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("RPC endpoint must be HTTPS without credentials or fragment")
	}
	return &rpcChainClient{
		endpoint: parsed.String(),
		client:   &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (client *rpcChainClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error) {
	result, err := client.call(ctx, "eth_getTransactionByHash", hash.Hex())
	if err != nil {
		return nil, false, err
	}
	var metadata struct {
		BlockNumber *string `json:"blockNumber"`
	}
	if err := json.Unmarshal(result, &metadata); err != nil {
		return nil, false, err
	}
	var transaction types.Transaction
	if err := json.Unmarshal(result, &transaction); err != nil {
		return nil, false, fmt.Errorf("decode transaction: %w", err)
	}
	return &transaction, metadata.BlockNumber == nil, nil
}

func (client *rpcChainClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	result, err := client.call(ctx, "eth_getTransactionReceipt", hash.Hex())
	if err != nil {
		return nil, err
	}
	var receipt types.Receipt
	if err := json.Unmarshal(result, &receipt); err != nil {
		return nil, fmt.Errorf("decode receipt: %w", err)
	}
	return &receipt, nil
}

func (client *rpcChainClient) call(ctx context.Context, method string, parameter interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      int           `json:"id"`
		Method  string        `json:"method"`
		Params  []interface{} `json:"params"`
	}{JSONRPC: "2.0", ID: 1, Method: method, Params: []interface{}{parameter}})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.String() != request.URL.String() {
		return nil, errors.New("RPC endpoint redirected")
	}
	limited := io.LimitReader(response.Body, maxRPCResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(responseBody) > maxRPCResponseBytes {
		return nil, errors.New("RPC response exceeds 2 MiB")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RPC returned HTTP %d", response.StatusCode)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, fmt.Errorf("decode RPC envelope: %w", err)
	}
	if envelope.JSONRPC != "2.0" || envelope.ID != 1 {
		return nil, errors.New("RPC response version or id mismatch")
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return nil, errors.New("RPC result is unavailable")
	}
	return envelope.Result, nil
}
