package x402adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	x402types "github.com/x402-foundation/x402/go/v2/types"
)

const maxSupportedResponseBytes = 1 << 20

var evmAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

type FacilitatorClient struct {
	baseURL string
	client  *http.Client
}

type FacilitatorConformance struct {
	V2ExactNetwork bool     `json:"v2ExactNetwork"`
	BuilderCode    bool     `json:"builderCode"`
	Signers        []string `json:"signers,omitempty"`
	Ready          bool     `json:"ready"`
}

func NewFacilitatorClient(baseURL string, client *http.Client) (*FacilitatorClient, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("facilitator URL must be an HTTPS base URL without credentials, query, or fragment")
	}
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &FacilitatorClient{baseURL: u.String(), client: client}, nil
}

func (c *FacilitatorClient) Supported(ctx context.Context) (x402types.SupportedResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/supported", nil)
	if err != nil {
		return x402types.SupportedResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return x402types.SupportedResponse{}, fmt.Errorf("facilitator supported request: %w", err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.String() != request.URL.String() {
		return x402types.SupportedResponse{}, errors.New("facilitator supported endpoint redirected")
	}
	limited := io.LimitReader(response.Body, maxSupportedResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return x402types.SupportedResponse{}, err
	}
	if len(body) > maxSupportedResponseBytes {
		return x402types.SupportedResponse{}, errors.New("facilitator supported response exceeds 1 MiB")
	}
	if response.StatusCode != http.StatusOK {
		return x402types.SupportedResponse{}, fmt.Errorf("facilitator supported returned HTTP %d", response.StatusCode)
	}
	var supported x402types.SupportedResponse
	if err := json.Unmarshal(body, &supported); err != nil {
		return x402types.SupportedResponse{}, fmt.Errorf("decode facilitator supported response: %w", err)
	}
	for _, kind := range supported.Kinds {
		if kind.X402Version <= 0 || kind.Scheme == "" || kind.Network == "" {
			return x402types.SupportedResponse{}, errors.New("facilitator supported response contains an invalid kind")
		}
	}
	return supported, nil
}

func (a *Adapter) CheckFacilitator(supported x402types.SupportedResponse) FacilitatorConformance {
	result := FacilitatorConformance{}
	for _, kind := range supported.Kinds {
		if kind.X402Version == VersionV2 && kind.Scheme == SchemeExact && kind.Network == a.config.Network {
			result.V2ExactNetwork = true
			break
		}
	}
	for _, extension := range supported.Extensions {
		if extension == "builder-code" {
			result.BuilderCode = true
			break
		}
	}
	for family, signers := range supported.Signers {
		if family == "eip155" || family == "eip155:*" || family == a.config.Network {
			for _, signer := range signers {
				if evmAddressPattern.MatchString(signer) {
					result.Signers = append(result.Signers, signer)
				}
			}
		}
	}
	sort.Strings(result.Signers)
	result.Signers = dedupe(result.Signers)
	result.Ready = result.V2ExactNetwork && result.BuilderCode && len(result.Signers) > 0
	return result
}
