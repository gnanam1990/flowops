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
	escrow   bool
}

// NewHTTPRegistrationSink creates the customer-side callback transport. It
// refuses redirects so a signed receipt cannot be replayed to another origin.
func NewHTTPRegistrationSink(controlAPIURL string, client *http.Client) (*HTTPRegistrationSink, error) {
	return newHTTPRegistrationSink(controlAPIURL, client, false)
}

func NewHTTPEscrowRegistrationSink(controlAPIURL string, client *http.Client) (*HTTPRegistrationSink, error) {
	return newHTTPRegistrationSink(controlAPIURL, client, true)
}

func newHTTPRegistrationSink(controlAPIURL string, client *http.Client, escrow bool) (*HTTPRegistrationSink, error) {
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
	path := "/v1/signer/broadcasts"
	if escrow {
		path = "/v1/signer/escrow-broadcasts"
	}
	return &HTTPRegistrationSink{endpoint: strings.TrimSuffix(parsed.String(), "/") + path, client: &copyClient, escrow: escrow}, nil
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
	if s.escrow {
		var acknowledgement struct {
			Call struct {
				Pending *struct {
					Expected struct {
						TransactionHash string `json:"transactionHash"`
					} `json:"expected"`
					BroadcastAttestation *struct {
						SignedReceipt broadcastreceipt.SignedReceipt `json:"signedReceipt"`
					} `json:"broadcastAttestation"`
				} `json:"pending"`
				Transitions []struct {
					Expected struct {
						TransactionHash string `json:"transactionHash"`
					} `json:"expected"`
					BroadcastAttestation *struct {
						SignedReceipt broadcastreceipt.SignedReceipt `json:"signedReceipt"`
					} `json:"broadcastAttestation"`
				} `json:"transitions"`
			} `json:"call"`
		}
		if err := json.Unmarshal(raw, &acknowledgement); err != nil {
			return errors.New("FlowOps escrow registration acknowledgement is not bound to the submitted receipt")
		}
		if acknowledgement.Call.Pending != nil && acknowledgement.Call.Pending.Expected.TransactionHash == receipt.Receipt.TransactionHash &&
			acknowledgement.Call.Pending.BroadcastAttestation != nil && acknowledgement.Call.Pending.BroadcastAttestation.SignedReceipt == receipt {
			return nil
		}
		for _, transition := range acknowledgement.Call.Transitions {
			if transition.Expected.TransactionHash == receipt.Receipt.TransactionHash && transition.BroadcastAttestation != nil && transition.BroadcastAttestation.SignedReceipt == receipt {
				return nil
			}
		}
		return errors.New("FlowOps escrow registration acknowledgement is not bound to the submitted receipt")
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
