// Package purchasespec creates the canonical ASCP PurchaseSpec binding for an
// outbound seller request. It has no network or payment capability.
package purchasespec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/net/idna"
)

const DomainTag = "ASCP_PURCHASE_SPEC_V1"

var (
	ErrInvalidSpec = errors.New("invalid purchase specification")
	ErrInvalidURL  = errors.New("invalid canonical URL")
)

var prohibitedHeaders = map[string]struct{}{
	"authorization": {}, "cookie": {}, "host": {}, "forwarded": {},
	"payment-required": {}, "payment-signature": {}, "payment-response": {},
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
}

var transportHeaders = map[string]struct{}{"traceparent": {}, "accept-encoding": {}}

// Input is an agent-originated request description before transport headers
// are generated. Body is retained by the caller; this package binds its exact
// bytes with Keccak-256.
type Input struct {
	OrgID     string
	AgentID   string
	TaskID    string
	Method    string
	URL       string
	Body      []byte
	Headers   []Header
	Response  ResponseContract
	Category  string
	ReasonRef *ReasonRef
}

type Header struct {
	Name  string
	Value string
}

type ResponseContract struct {
	ContentType string `json:"contentType"`
	SchemaRef   string `json:"schemaRef"`
}

type ReasonRef struct {
	BlobRef     string `json:"blobRef"`
	ContentHash string `json:"contentHash"`
}

// Spec is the only JSON object that may be hashed as purchaseSpecHash.
// HeaderBindings are sorted by lowercase name. ReasonRef is absent, rather
// than null, when no reason reference was supplied.
type Spec struct {
	OrgID           string           `json:"orgId"`
	AgentID         string           `json:"agentId"`
	TaskID          string           `json:"taskId"`
	Method          string           `json:"method"`
	CanonicalURL    string           `json:"canonicalURL"`
	RequestBodyHash string           `json:"requestBodyHash"`
	HeaderBindings  []HeaderBinding  `json:"headerBindings"`
	Response        ResponseContract `json:"responseContract"`
	Category        string           `json:"category"`
	ReasonRef       *ReasonRef       `json:"reasonRef,omitempty"`
}

type HeaderBinding struct {
	LowercaseName string `json:"lowercaseName"`
	ValueHash     string `json:"valueHash"`
}

type Result struct {
	Spec                    Spec
	CanonicalJSON           []byte
	PurchaseSpecHash        string
	StrippedTransportHeader []string
}

// Build validates the caller's immutable request fields, strips only the two
// rails-generated transport headers, produces RFC 8785-compatible JSON for
// this no-floating-point schema, and returns a Keccak-256 purchaseSpecHash.
func Build(input Input) (Result, error) {
	for name, value := range map[string]string{"orgId": input.OrgID, "agentId": input.AgentID, "taskId": input.TaskID, "category": input.Category} {
		if strings.TrimSpace(value) == "" || !jcsSafe(value) || len(value) > 1024 {
			return Result{}, fmt.Errorf("%w: %s is empty, invalid UTF-8, or too long", ErrInvalidSpec, name)
		}
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" || len(method) > 16 || !isToken(method) {
		return Result{}, fmt.Errorf("%w: method is invalid", ErrInvalidSpec)
	}
	canonicalURL, err := CanonicalURL(input.URL)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(input.Response.ContentType) == "" || strings.TrimSpace(input.Response.SchemaRef) == "" ||
		!jcsSafe(input.Response.ContentType) || !jcsSafe(input.Response.SchemaRef) ||
		len(input.Response.ContentType) > 256 || len(input.Response.SchemaRef) > 2048 {
		return Result{}, fmt.Errorf("%w: response contract is invalid", ErrInvalidSpec)
	}
	bindings, stripped, err := canonicalHeaders(input.Headers)
	if err != nil {
		return Result{}, err
	}
	reason, err := canonicalReason(input.ReasonRef)
	if err != nil {
		return Result{}, err
	}
	bodyHash := ""
	if method == "GET" {
		if len(input.Body) != 0 {
			return Result{}, fmt.Errorf("%w: GET must not carry an unbound request body", ErrInvalidSpec)
		}
	} else {
		bodyHash = keccakHex(input.Body)
	}
	spec := Spec{
		OrgID: input.OrgID, AgentID: input.AgentID, TaskID: input.TaskID, Method: method, CanonicalURL: canonicalURL,
		RequestBodyHash: bodyHash, HeaderBindings: bindings, Response: ResponseContract{ContentType: strings.TrimSpace(input.Response.ContentType), SchemaRef: strings.TrimSpace(input.Response.SchemaRef)},
		Category: input.Category, ReasonRef: reason,
	}
	canonical, err := canonicalJSON(spec)
	if err != nil {
		return Result{}, err
	}
	return Result{Spec: spec, CanonicalJSON: canonical, PurchaseSpecHash: keccakHex(canonical), StrippedTransportHeader: stripped}, nil
}

// CanonicalURL implements the ASCP URL rules: HTTPS only, lowercase scheme and
// host, IDNA ASCII host, default-port removal, dot-segment removal, normalized
// percent encoding, and bytewise-sorted duplicate-preserving query pairs.
func CanonicalURL(raw string) (string, error) {
	if raw == "" || len(raw) > 8192 || strings.TrimSpace(raw) != raw || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%w: URL is empty, oversized, has whitespace, or has a fragment", ErrInvalidURL)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: URL must be absolute and contain no credentials or fragment", ErrInvalidURL)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("%w: only https is permitted", ErrInvalidURL)
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("%w: host is missing", ErrInvalidURL)
	}
	var host string
	isIPv6 := false
	if ip := net.ParseIP(hostname); ip != nil {
		host = strings.ToLower(ip.String())
		isIPv6 = strings.Contains(host, ":")
	} else {
		host, err = idna.Lookup.ToASCII(strings.ToLower(hostname))
		if err != nil || host == "" {
			return "", fmt.Errorf("%w: host cannot be converted to IDNA ASCII", ErrInvalidURL)
		}
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		if !validPort(port) {
			return "", fmt.Errorf("%w: port is invalid", ErrInvalidURL)
		}
		host = net.JoinHostPort(host, port)
	} else if isIPv6 {
		host = "[" + host + "]"
	}
	path, err := normalizedPath(parsed.EscapedPath())
	if err != nil {
		return "", err
	}
	query, err := normalizedQuery(parsed.RawQuery)
	if err != nil {
		return "", err
	}
	canonical := "https://" + host + path
	if query != "" {
		canonical += "?" + query
	}
	if !jcsSafe(canonical) {
		return "", fmt.Errorf("%w: URL contains a JSON-unsafe Unicode separator", ErrInvalidURL)
	}
	return canonical, nil
}

func canonicalHeaders(headers []Header) ([]HeaderBinding, []string, error) {
	seen := make(map[string]struct{}, len(headers))
	bindings := make([]HeaderBinding, 0, len(headers))
	stripped := make([]string, 0, 2)
	for _, header := range headers {
		name := strings.ToLower(header.Name)
		if !isHeaderName(name) || !jcsSafe(header.Value) || strings.ContainsAny(header.Value, "\r\n\x00") {
			return nil, nil, fmt.Errorf("%w: header name or value is invalid", ErrInvalidSpec)
		}
		value := strings.Trim(header.Value, " \t")
		if strings.HasPrefix(name, "x-forwarded-") {
			return nil, nil, fmt.Errorf("%w: header %q is prohibited", ErrInvalidSpec, name)
		}
		if _, prohibited := prohibitedHeaders[name]; prohibited {
			return nil, nil, fmt.Errorf("%w: header %q is prohibited", ErrInvalidSpec, name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate header %q", ErrInvalidSpec, name)
		}
		seen[name] = struct{}{}
		if _, railsGenerated := transportHeaders[name]; railsGenerated {
			stripped = append(stripped, name)
			continue
		}
		bindings = append(bindings, HeaderBinding{LowercaseName: name, ValueHash: keccakHex([]byte(value))})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].LowercaseName < bindings[j].LowercaseName })
	sort.Strings(stripped)
	return bindings, stripped, nil
}

func canonicalReason(reason *ReasonRef) (*ReasonRef, error) {
	if reason == nil {
		return nil, nil
	}
	if strings.TrimSpace(reason.BlobRef) == "" || len(reason.BlobRef) > 4096 || !jcsSafe(reason.BlobRef) || !isHash(reason.ContentHash) || reason.ContentHash == "0x"+strings.Repeat("0", 64) {
		return nil, fmt.Errorf("%w: reasonRef is invalid", ErrInvalidSpec)
	}
	return &ReasonRef{BlobRef: reason.BlobRef, ContentHash: reason.ContentHash}, nil
}

func canonicalJSON(spec Spec) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(spec); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func normalizedPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	normalized, err := normalizePercent(raw)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidURL)
	}
	return removeDotSegments(normalized), nil
}

func normalizedQuery(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	pairs := strings.Split(raw, "&")
	type pair struct{ key, value string }
	result := make([]pair, 0, len(pairs))
	for _, rawPair := range pairs {
		if rawPair == "" {
			return "", fmt.Errorf("%w: empty query pair", ErrInvalidURL)
		}
		key, value, hasValue := strings.Cut(rawPair, "=")
		if !hasValue {
			value = ""
		}
		key, err := normalizePercent(key)
		if err != nil {
			return "", err
		}
		value, err = normalizePercent(value)
		if err != nil {
			return "", err
		}
		result = append(result, pair{key: key, value: value})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].key == result[j].key {
			return result[i].value < result[j].value
		}
		return result[i].key < result[j].key
	})
	encoded := make([]string, len(result))
	for i, item := range result {
		encoded[i] = item.key + "=" + item.value
	}
	return strings.Join(encoded, "&"), nil
}

func normalizePercent(value string) (string, error) {
	var output strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			if value[i] <= 0x20 || value[i] == 0x7f {
				return "", fmt.Errorf("%w: control character", ErrInvalidURL)
			}
			if value[i] >= 0x80 || !isURLLiteral(value[i]) {
				output.WriteByte('%')
				output.WriteString(strings.ToUpper(hex.EncodeToString([]byte{value[i]})))
			} else {
				output.WriteByte(value[i])
			}
			continue
		}
		if i+2 >= len(value) || !isHex(value[i+1]) || !isHex(value[i+2]) {
			return "", fmt.Errorf("%w: invalid percent encoding", ErrInvalidURL)
		}
		decoded, _ := hex.DecodeString(value[i+1 : i+3])
		if isUnreserved(decoded[0]) {
			output.WriteByte(decoded[0])
		} else {
			output.WriteByte('%')
			output.WriteString(strings.ToUpper(value[i+1 : i+3]))
		}
		i += 2
	}
	return output.String(), nil
}

func removeDotSegments(input string) string {
	output := ""
	for input != "" {
		switch {
		case strings.HasPrefix(input, "../"):
			input = input[3:]
		case strings.HasPrefix(input, "./"):
			input = input[2:]
		case strings.HasPrefix(input, "/./"):
			input = "/" + input[3:]
		case input == "/.":
			input = "/"
		case strings.HasPrefix(input, "/../"):
			input = "/" + input[4:]
			output = removeLastSegment(output)
		case input == "/..":
			input = "/"
			output = removeLastSegment(output)
		case input == "." || input == "..":
			input = ""
		default:
			end := 0
			if input[0] == '/' {
				end = strings.Index(input[1:], "/")
				if end >= 0 {
					end += 1
				}
			} else {
				end = strings.Index(input, "/")
			}
			if end < 0 {
				output += input
				input = ""
			} else {
				output += input[:end]
				input = input[end:]
			}
		}
	}
	if output == "" {
		return "/"
	}
	return output
}

func removeLastSegment(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[:index]
	}
	return ""
}

func isHeaderName(value string) bool {
	return value != "" && len(value) <= 256 && isToken(value) && value == strings.ToLower(value)
}

func isToken(value string) bool {
	for i := 0; i < len(value); i++ {
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'A' && value[i] <= 'Z') || (value[i] >= 'a' && value[i] <= 'z') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(value[i]))) {
			return false
		}
	}
	return true
}

func isHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func validPort(value string) bool {
	if value == "" || len(value) > 5 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	return err == nil && parsed > 0
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}
func isUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}
func isURLLiteral(value byte) bool {
	return isUnreserved(value) || strings.ContainsRune("!$&'()*+,/:;=@?[]", rune(value))
}
func keccakHex(value []byte) string { return crypto.Keccak256Hash(value).Hex() }
func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
func jcsSafe(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\u2028') && !strings.ContainsRune(value, '\u2029')
}
