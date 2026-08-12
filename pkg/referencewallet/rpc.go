package referencewallet

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxRPCResponseBytes = 512 * 1024

type rpcKind int

const (
	rpcRemoteBase rpcKind = iota
	rpcLocalWallet
)

type rpcClient struct {
	endpoint string
	client   *http.Client
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
	Error   *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
}

func newRPCClient(rawURL string, kind rpcKind, timeout time.Duration, supplied *http.Client) (*rpcClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("URL must not contain credentials, query, or fragment")
	}
	loopback := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	switch kind {
	case rpcRemoteBase:
		if parsed.Scheme != "https" && !(loopback && parsed.Scheme == "http") {
			return nil, errors.New("remote Base RPC must use HTTPS except on loopback")
		}
	case rpcLocalWallet:
		if parsed.Scheme != "http" || !loopback || (parsed.Path != "" && parsed.Path != "/") {
			return nil, errors.New("wallet RPC must use a loopback HTTP origin without a path")
		}
	}
	client := supplied
	if client == nil {
		client = &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 64 << 10},
		}
	} else if client.Timeout <= 0 || client.Timeout > time.Minute {
		return nil, errors.New("HTTP client timeout must be positive and at most one minute")
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &rpcClient{endpoint: strings.TrimSuffix(parsed.String(), "/"), client: &copyClient}, nil
}

func (c *rpcClient) call(ctx context.Context, method string, params []any, output any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return errors.New("encode JSON-RPC request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("create JSON-RPC request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return errors.New("JSON-RPC request failed")
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.String() != request.URL.String() {
		return errors.New("JSON-RPC request redirected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRPCResponseBytes+1))
	if err != nil || len(raw) > maxRPCResponseBytes {
		return errors.New("JSON-RPC response is invalid")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("JSON-RPC returned HTTP %d", response.StatusCode)
	}
	var decoded rpcResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || decoded.JSONRPC != "2.0" || decoded.ID != 1 {
		return errors.New("JSON-RPC response envelope is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON-RPC response must contain exactly one value")
	}
	if decoded.Error != nil {
		return fmt.Errorf("JSON-RPC method %s failed with code %d", method, decoded.Error.Code)
	}
	if len(decoded.Result) == 0 || string(decoded.Result) == "null" {
		return errors.New("JSON-RPC response result is missing")
	}
	if err := json.Unmarshal(decoded.Result, output); err != nil {
		return errors.New("JSON-RPC result has an invalid shape")
	}
	return nil
}

func decodeCanonicalHex(value string) ([]byte, error) {
	if len(value) < 4 || !strings.HasPrefix(value, "0x") || len(value)%2 != 0 || strings.ToLower(value) != value {
		return nil, errors.New("invalid canonical hex")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || value != "0x"+hex.EncodeToString(decoded) {
		return nil, errors.New("invalid canonical hex")
	}
	return decoded, nil
}
