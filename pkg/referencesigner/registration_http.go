package referencesigner

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

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
)

const maxRegistrationResponseBytes = 256 * 1024

type HTTPRegistrationSink struct {
	endpoint string
	client   *http.Client
}

// NewHTTPRegistrationSink creates the customer-side callback transport. It
// refuses redirects so a signed receipt cannot be replayed to another origin.
func NewHTTPRegistrationSink(controlAPIURL string, client *http.Client) (*HTTPRegistrationSink, error) {
	parsed, err := url.Parse(strings.TrimSpace(controlAPIURL))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("FlowOps control API URL is invalid")
	}
	loopback := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !(loopback && parsed.Scheme == "http") {
		return nil, errors.New("FlowOps control API URL must use HTTPS except on loopback")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("FlowOps control API URL must not contain a path")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	} else if client.Timeout <= 0 {
		return nil, errors.New("registration HTTP client must have a positive timeout")
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPRegistrationSink{endpoint: strings.TrimSuffix(parsed.String(), "/") + "/v1/signer/broadcasts", client: &copyClient}, nil
}

func (s *HTTPRegistrationSink) Register(ctx context.Context, receipt broadcastreceipt.SignedReceipt) error {
	body, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("encode signer receipt")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("create signer receipt request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("FlowOps receipt registration request failed")
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.String() != request.URL.String() {
		return errors.New("FlowOps receipt registration redirected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRegistrationResponseBytes+1))
	if err != nil || len(raw) > maxRegistrationResponseBytes {
		return errors.New("FlowOps receipt registration response is invalid")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("FlowOps receipt registration returned HTTP %d", response.StatusCode)
	}
	var acknowledgement struct {
		Execution struct {
			Expected struct {
				TransactionHash string `json:"transactionHash"`
			} `json:"expected"`
			BroadcastAttestation *struct {
				SignedReceipt broadcastreceipt.SignedReceipt `json:"signedReceipt"`
			} `json:"broadcastAttestation"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(raw, &acknowledgement); err != nil {
		return errors.New("FlowOps receipt registration returned invalid JSON")
	}
	if acknowledgement.Execution.Expected.TransactionHash != receipt.Receipt.TransactionHash ||
		acknowledgement.Execution.BroadcastAttestation == nil ||
		acknowledgement.Execution.BroadcastAttestation.SignedReceipt != receipt {
		return errors.New("FlowOps receipt registration acknowledgement is not bound to the submitted receipt")
	}
	return nil
}
