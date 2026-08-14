// Package rpcadmission validates the split secret and non-secret configuration
// used to admit Base RPC observers. It does not contact providers and cannot
// establish a commercial SLA; it makes the operator assertions explicit and
// fail-closed so public or shared-failure-domain endpoints cannot accidentally
// become a production quorum.
package rpcadmission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

const MaxJSONBytes = 16 * 1024

var identityPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

type ProviderAdmission struct {
	Name               string `json:"name"`
	Operator           string `json:"operator"`
	FailureDomain      string `json:"failureDomain"`
	ServiceTier        string `json:"serviceTier"`
	ProductionEligible bool   `json:"productionEligible"`
}

type ProductionAdmission struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Providers     []ProviderAdmission `json:"providers"`
}

type providerInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// DecodeProviders parses the secret-bearing provider set. The returned URLs
// must stay in process memory and must never be included in status or evidence.
func DecodeProviders(raw string) ([]reconciliation.RPCProvider, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON is required")
	}
	if len(raw) > MaxJSONBytes {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON exceeds 16 KiB")
	}
	if err := RejectDuplicateJSONFields([]byte(raw)); err != nil {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON must not contain duplicate object fields")
	}
	if err := requireExactObjectFields([]byte(raw), "name", "url"); err != nil {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON provider fields must be exactly name and url")
	}
	var inputs []providerInput
	if err := decodeOneStrict(raw, &inputs); err != nil || inputs == nil {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON must be a strict provider array")
	}
	providers := make([]reconciliation.RPCProvider, len(inputs))
	for index, input := range inputs {
		providers[index] = reconciliation.RPCProvider{Name: strings.TrimSpace(input.Name), URL: strings.TrimSpace(input.URL)}
	}
	if _, err := reconciliation.NewObserverSet(84532, providers, nil, nil); err != nil {
		return nil, fmt.Errorf("FLOWOPS_BASE_RPC_PROVIDERS_JSON: %w", err)
	}
	return providers, nil
}

func DecodeProductionAdmission(raw string) (ProductionAdmission, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ProductionAdmission{}, errors.New("FLOWOPS_BASE_RPC_ADMISSION_JSON is required for Base mainnet")
	}
	var admission ProductionAdmission
	if err := json.Unmarshal([]byte(raw), &admission); err != nil {
		return ProductionAdmission{}, errors.New("FLOWOPS_BASE_RPC_ADMISSION_JSON must be strict JSON")
	}
	return admission, nil
}

// UnmarshalJSON keeps admission objects exact even when embedded in another
// strict document, such as the customer reference-signer configuration.
func (a *ProductionAdmission) UnmarshalJSON(raw []byte) error {
	if len(raw) > MaxJSONBytes {
		return errors.New("production RPC admission exceeds 16 KiB")
	}
	if err := RejectDuplicateJSONFields(raw); err != nil {
		return errors.New("production RPC admission contains duplicate object fields")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || !exactFields(top, "schemaVersion", "providers") {
		return errors.New("production RPC admission fields must be exactly schemaVersion and providers")
	}
	var providerObjects []map[string]json.RawMessage
	if err := json.Unmarshal(top["providers"], &providerObjects); err != nil {
		return errors.New("production RPC admission providers must be an array")
	}
	for _, provider := range providerObjects {
		if !exactFields(provider, "name", "operator", "failureDomain", "serviceTier", "productionEligible") {
			return errors.New("production RPC admission provider fields are not canonical")
		}
	}
	type wire ProductionAdmission
	var decoded wire
	if err := decodeOneStrict(string(raw), &decoded); err != nil {
		return errors.New("production RPC admission must be strict JSON")
	}
	*a = ProductionAdmission(decoded)
	return nil
}

// ValidateProduction binds each secret endpoint to reviewed non-secret
// operator metadata. Distinct hostnames remain required by ObserverSet; this
// adds distinct operators and explicitly reviewed failure domains.
func ValidateProduction(providers []reconciliation.RPCProvider, admission ProductionAdmission) error {
	if _, err := reconciliation.NewObserverSet(8453, providers, nil, nil); err != nil {
		return fmt.Errorf("production RPC provider set: %w", err)
	}
	if admission.SchemaVersion != 1 {
		return errors.New("production RPC admission schemaVersion must be 1")
	}
	if len(providers) < 2 || len(providers) > 5 || len(admission.Providers) != len(providers) {
		return errors.New("production RPC admission must bind every one of two to five providers")
	}
	configured := make(map[string]reconciliation.RPCProvider, len(providers))
	for _, provider := range providers {
		configured[provider.Name] = provider
	}
	operators := make(map[string]struct{}, len(providers))
	failureDomains := make(map[string]struct{}, len(providers))
	admitted := make(map[string]struct{}, len(providers))
	for _, item := range admission.Providers {
		if !identityPattern.MatchString(item.Name) || !identityPattern.MatchString(item.Operator) || !identityPattern.MatchString(item.FailureDomain) {
			return errors.New("production RPC names, operators, and failure domains must be canonical identifiers")
		}
		provider, exists := configured[item.Name]
		if !exists {
			return fmt.Errorf("production RPC admission names unknown provider %s", item.Name)
		}
		if _, exists := admitted[item.Name]; exists {
			return errors.New("production RPC admission provider names must be unique")
		}
		admitted[item.Name] = struct{}{}
		if item.ServiceTier != "paid" || !item.ProductionEligible {
			return fmt.Errorf("production RPC provider %s must be reviewed as paid and production eligible", item.Name)
		}
		if _, exists := operators[item.Operator]; exists {
			return errors.New("production RPC operators must be distinct")
		}
		operators[item.Operator] = struct{}{}
		if _, exists := failureDomains[item.FailureDomain]; exists {
			return errors.New("production RPC failure domains must be distinct")
		}
		failureDomains[item.FailureDomain] = struct{}{}
		if isKnownPublicEndpoint(provider.URL) {
			return fmt.Errorf("production RPC provider %s uses a known public endpoint", item.Name)
		}
	}
	if len(admitted) != len(configured) {
		return errors.New("production RPC admission does not bind the complete secret provider set")
	}
	return nil
}

func isKnownPublicEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	switch strings.TrimRight(strings.ToLower(parsed.Hostname()), ".") {
	case "mainnet.base.org", "developer-access-mainnet.base.org", "base-rpc.publicnode.com":
		return true
	default:
		return false
	}
}

func decodeOneStrict(raw string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func requireExactObjectFields(raw []byte, fields ...string) error {
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &objects); err != nil {
		return err
	}
	for _, object := range objects {
		if !exactFields(object, fields...) {
			return errors.New("non-canonical fields")
		}
	}
	return nil
}

func exactFields(object map[string]json.RawMessage, fields ...string) bool {
	if len(object) != len(fields) {
		return false
	}
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			return false
		}
	}
	return true
}

// RejectDuplicateJSONFields recursively rejects duplicate fields and trailing
// JSON values. Security-sensitive decoders can use it before decoding because
// encoding/json otherwise accepts last-value-wins objects.
func RejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate object field")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("invalid object closing token")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("invalid array closing token")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
