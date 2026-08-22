// Package basemcp implements the production capability firewall in front of
// the official Base MCP. It deliberately owns no OAuth token, wallet key,
// signer, RPC client, or transaction path. An injected transport can invoke
// only the exact read tools pinned here, and every result remains advisory.
package basemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const (
	OfficialEndpoint = "https://mcp.base.org"
	AdvisoryOnly     = "ADVISORY_SINGLE_PROVIDER"
	maxArguments     = 16 * 1024
	maxResult        = 1024 * 1024
)

var (
	ErrCapabilityDenied = errors.New("Base MCP capability is denied")
	ErrInvalidArguments = errors.New("Base MCP read arguments are invalid")
	ErrInvalidResult    = errors.New("Base MCP read result is invalid")
)

type Invoker interface {
	Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
}

type Adapter struct {
	endpoint string
	invoker  Invoker
}

type Result struct {
	Source      string          `json:"source"`
	Tool        string          `json:"tool"`
	EvidenceUse string          `json:"evidenceUse"`
	Data        json.RawMessage `json:"data"`
}

type argumentKind uint8

const (
	stringArgument argumentKind = iota + 1
	boolArgument
	uintArgument
)

type argumentRule struct {
	kind argumentKind
	min  int64
	max  int64
}

type readProfile struct {
	fields   map[string]argumentRule
	required []string
}

// readProfiles follows the public Base MCP read surfaces. Wallet writes,
// generic RPC, request creation, signing, swaps, x402, and contract calls are
// intentionally absent and therefore fail closed if the upstream catalog
// changes.
var readProfiles = map[string]readProfile{
	"get_wallets": {fields: map[string]argumentRule{}},
	"get_portfolio": {fields: map[string]argumentRule{
		"address": {kind: stringArgument}, "chain": {kind: stringArgument}, "query": {kind: stringArgument},
		"includePnl": {kind: boolArgument}, "limit": {kind: uintArgument, max: 10_000}, "offset": {kind: uintArgument, max: 10_000},
	}},
	"search_tokens": {fields: map[string]argumentRule{
		"query": {kind: stringArgument}, "chain": {kind: stringArgument},
	}, required: []string{"query"}},
	"get_transaction_history": {fields: map[string]argumentRule{
		"address": {kind: stringArgument}, "chain": {kind: stringArgument}, "asset": {kind: stringArgument},
		"limit": {kind: uintArgument, min: 1, max: 200}, "cursor": {kind: stringArgument},
	}, required: []string{"chain"}},
	"get_request_status": {fields: map[string]argumentRule{
		"requestId": {kind: stringArgument},
	}, required: []string{"requestId"}},
}

func New(endpoint string, invoker Invoker) (*Adapter, error) {
	if invoker == nil {
		return nil, errors.New("Base MCP invoker is required")
	}
	canonical, err := canonicalEndpoint(endpoint)
	if err != nil || canonical != OfficialEndpoint {
		return nil, errors.New("Base MCP endpoint must equal the pinned official endpoint")
	}
	return &Adapter{endpoint: canonical, invoker: invoker}, nil
}

func (a *Adapter) Call(ctx context.Context, tool string, arguments json.RawMessage) (Result, error) {
	profile, allowed := readProfiles[tool]
	if !allowed {
		return Result{}, fmt.Errorf("%w: %s", ErrCapabilityDenied, tool)
	}
	canonical, err := validateArguments(arguments, profile)
	if err != nil {
		return Result{}, err
	}
	raw, err := a.invoker.Call(ctx, a.endpoint, tool, canonical)
	if err != nil {
		return Result{}, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxResult || validateResult(trimmed) != nil {
		return Result{}, ErrInvalidResult
	}
	return Result{
		Source: OfficialEndpoint, Tool: tool, EvidenceUse: AdvisoryOnly,
		Data: append(json.RawMessage(nil), trimmed...),
	}, nil
}

func canonicalEndpoint(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host != "mcp.base.org" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid Base MCP endpoint")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func validateArguments(raw json.RawMessage, profile readProfile) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	if len(trimmed) > maxArguments || len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ErrInvalidArguments
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalidArguments
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalidArguments
		}
		key, ok := keyToken.(string)
		rule, allowed := profile.fields[key]
		if !ok || !allowed {
			return nil, ErrInvalidArguments
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrInvalidArguments
		}
		seen[key] = struct{}{}
		var value any
		if err := decoder.Decode(&value); err != nil || !validArgumentValue(value, rule) {
			return nil, ErrInvalidArguments
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, ErrInvalidArguments
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidArguments
	}
	for _, required := range profile.required {
		if _, exists := seen[required]; !exists {
			return nil, ErrInvalidArguments
		}
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func validArgumentValue(value any, rule argumentRule) bool {
	switch rule.kind {
	case stringArgument:
		text, ok := value.(string)
		return ok && len(text) > 0 && len(text) <= 4096
	case boolArgument:
		_, ok := value.(bool)
		return ok
	case uintArgument:
		number, ok := value.(json.Number)
		if !ok || strings.ContainsAny(number.String(), ".eE+-") {
			return false
		}
		parsed, err := number.Int64()
		return err == nil && parsed >= rule.min && parsed <= rule.max
	default:
		return false
	}
}

func validateResult(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkResultJSON(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidResult
	}
	return nil
}

func walkResultJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidResult
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return ErrInvalidResult
	}
	if delimiter != '{' {
		return ErrInvalidResult
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return ErrInvalidResult
		}
		key, ok := keyToken.(string)
		if !ok {
			return ErrInvalidResult
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidResult
		}
		seen[key] = struct{}{}
		if err := walkNestedResultJSON(decoder); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return ErrInvalidResult
	}
	return nil
}

func walkNestedResultJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidResult
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ErrInvalidResult
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidResult
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrInvalidResult
			}
			seen[key] = struct{}{}
			if err := walkNestedResultJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrInvalidResult
		}
	case '[':
		for decoder.More() {
			if err := walkNestedResultJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrInvalidResult
		}
	default:
		return ErrInvalidResult
	}
	return nil
}
