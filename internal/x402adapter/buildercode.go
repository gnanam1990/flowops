package x402adapter

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	builderCodeKey        = "builder-code"
	erc8021Marker         = "80218021802180218021802180218021"
	erc8021Schema2        = 0x02
	maxClientServiceCodes = 5
	maxServerServiceCodes = 5
)

var builderCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

type builderCodeData struct {
	A string
	W string
	S []string
}

type AttributionState string

const (
	AttributionVerifiedSuffix        AttributionState = "VERIFIED_SUFFIX"
	AttributionDeclaredExtensionOnly AttributionState = "DECLARED_EXTENSION_ONLY"
	AttributionNotSupported          AttributionState = "NOT_SUPPORTED"
	AttributionUnknown               AttributionState = "UNKNOWN"
)

type AttributionEvidence struct {
	State        AttributionState `json:"state"`
	AppCode      string           `json:"appCode,omitempty"`
	ServiceCodes []string         `json:"serviceCodes,omitempty"`
	WalletCode   string           `json:"walletCode,omitempty"`
	Reason       string           `json:"reason,omitempty"`
}

// PaymentExtensions returns the builder-code data the signer must carry in its
// x402 payment payload. It echoes the server app code when present and adds
// FlowOps service codes client-first. x402 V2 permits a client to attach `s`
// even when the resource did not declare `a`.
func (a *Adapter) PaymentExtensions(quote Quote) (map[string]interface{}, AttributionEvidence, error) {
	if err := a.ValidateQuote(quote); err != nil {
		return nil, AttributionEvidence{State: AttributionUnknown, Reason: err.Error()}, err
	}
	declaration, ok, err := parseBuilderCodeDeclaration(quote.Extensions)
	if err != nil {
		return nil, AttributionEvidence{State: AttributionUnknown, Reason: err.Error()}, err
	}
	if !ok {
		if len(a.config.ServiceCodes) == 0 {
			return nil, AttributionEvidence{State: AttributionNotSupported, Reason: "no participant supplied a builder code"}, nil
		}
		services := append([]string(nil), a.config.ServiceCodes...)
		return map[string]interface{}{
				builderCodeKey: map[string]interface{}{"info": map[string]interface{}{"s": services}},
			}, AttributionEvidence{
				State: AttributionDeclaredExtensionOnly, ServiceCodes: services,
				Reason: "client extension prepared; onchain calldata has not been parsed",
			}, nil
	}
	services := dedupe(append(append([]string(nil), a.config.ServiceCodes...), declaration.S...))
	if len(services) > maxClientServiceCodes+maxServerServiceCodes {
		return nil, AttributionEvidence{}, errors.New("combined client and server service codes exceed their reserved budget")
	}
	info := map[string]interface{}{"a": declaration.A}
	if len(services) > 0 {
		info["s"] = services
	}
	return map[string]interface{}{
			builderCodeKey: map[string]interface{}{"info": info},
		}, AttributionEvidence{
			State: AttributionDeclaredExtensionOnly, AppCode: declaration.A,
			ServiceCodes: services, Reason: "onchain calldata has not been parsed",
		}, nil
}

// ClassifyCalldata makes attribution truth depend on the settlement calldata.
// Expected app and service codes must all be present in the parsed Schema 2
// suffix; a declaration or facilitator response alone never verifies it.
func ClassifyCalldata(calldata, expectedApp string, expectedServices []string, wasDeclared bool) AttributionEvidence {
	if calldata == "" {
		if wasDeclared {
			return AttributionEvidence{State: AttributionDeclaredExtensionOnly, AppCode: expectedApp, ServiceCodes: append([]string(nil), expectedServices...), Reason: "settlement calldata unavailable"}
		}
		return AttributionEvidence{State: AttributionNotSupported, Reason: "resource did not declare builder-code"}
	}
	parsed, ok := parseBuilderCodeSuffix(calldata)
	if !ok {
		return AttributionEvidence{State: AttributionUnknown, Reason: "settlement calldata has no valid terminal ERC-8021 Schema 2 suffix"}
	}
	evidence := AttributionEvidence{State: AttributionVerifiedSuffix, AppCode: parsed.A, ServiceCodes: append([]string(nil), parsed.S...), WalletCode: parsed.W}
	if expectedApp != "" && parsed.A != expectedApp {
		evidence.State = AttributionUnknown
		evidence.Reason = fmt.Sprintf("parsed app code %q does not match expected %q", parsed.A, expectedApp)
		return evidence
	}
	for _, expected := range expectedServices {
		if !slices.Contains(parsed.S, expected) {
			evidence.State = AttributionUnknown
			evidence.Reason = fmt.Sprintf("parsed service codes do not contain expected %q", expected)
			return evidence
		}
	}
	return evidence
}

func parseBuilderCodeDeclaration(extensions map[string]interface{}) (builderCodeData, bool, error) {
	if extensions == nil {
		return builderCodeData{}, false, nil
	}
	raw, ok := extensions[builderCodeKey]
	if !ok {
		return builderCodeData{}, false, nil
	}
	extension, ok := raw.(map[string]interface{})
	if !ok {
		return builderCodeData{}, true, errors.New("builder-code extension must be an object")
	}
	info, ok := extension["info"].(map[string]interface{})
	if !ok {
		return builderCodeData{}, true, errors.New("builder-code info must be an object")
	}
	app, ok := info["a"].(string)
	if !ok || !builderCodePattern.MatchString(app) {
		return builderCodeData{}, true, errors.New("builder-code app code is invalid")
	}
	services, err := stringSlice(info["s"])
	if err != nil {
		return builderCodeData{}, true, err
	}
	if len(services) > maxServerServiceCodes {
		return builderCodeData{}, true, errors.New("server declared too many builder service codes")
	}
	if err := validateServiceCodes(services); err != nil {
		return builderCodeData{}, true, err
	}
	return builderCodeData{A: app, S: services}, true, nil
}

func stringSlice(value interface{}) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	switch values := value.(type) {
	case string:
		return []string{values}, nil
	case []string:
		return append([]string(nil), values...), nil
	case []interface{}:
		output := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, errors.New("builder-code service codes must be strings")
			}
			output = append(output, text)
		}
		return output, nil
	default:
		return nil, errors.New("builder-code service codes must be an array")
	}
}

func validateServiceCodes(codes []string) error {
	if len(codes) > maxClientServiceCodes {
		return fmt.Errorf("too many service codes: %d exceeds %d", len(codes), maxClientServiceCodes)
	}
	for _, code := range codes {
		if !builderCodePattern.MatchString(code) {
			return fmt.Errorf("invalid builder service code %q", code)
		}
	}
	return nil
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}
	return output
}

// parseBuilderCodeSuffix implements the terminal ERC-8021 Schema 2 layout:
// CBOR map, two-byte CBOR length, schema byte, then the 16-byte marker. It only
// accepts the builder-code map keys and primitive shapes defined by x402 V2.
func parseBuilderCodeSuffix(calldata string) (*builderCodeData, bool) {
	hexData := strings.ToLower(strings.TrimPrefix(calldata, "0x"))
	if len(hexData)%2 != 0 || len(hexData) < (2+1+16)*2 || !isHex(hexData) {
		return nil, false
	}
	markerPosition := len(hexData) - len(erc8021Marker)
	if markerPosition < 6 || hexData[markerPosition:] != erc8021Marker {
		return nil, false
	}
	schema, err := strconv.ParseUint(hexData[markerPosition-2:markerPosition], 16, 8)
	if err != nil || schema != erc8021Schema2 {
		return nil, false
	}
	length, err := strconv.ParseUint(hexData[markerPosition-6:markerPosition-2], 16, 16)
	if err != nil {
		return nil, false
	}
	start := markerPosition - 6 - int(length)*2
	if start < 0 {
		return nil, false
	}
	cbor, err := hex.DecodeString(hexData[start : markerPosition-6])
	if err != nil {
		return nil, false
	}
	return parseBuilderCBOR(cbor)
}

func isHex(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			if r < 'a' || r > 'f' {
				return false
			}
		}
	}
	return true
}

func parseBuilderCBOR(data []byte) (*builderCodeData, bool) {
	offset := 0
	mapSize, ok := readCBORLength(data, &offset, 5)
	if !ok || mapSize > 3 {
		return nil, false
	}
	result := &builderCodeData{}
	seen := make(map[string]struct{}, mapSize)
	for i := 0; i < mapSize; i++ {
		key, ok := readCBORString(data, &offset)
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, false
		}
		seen[key] = struct{}{}
		switch key {
		case "a", "w":
			value, ok := readCBORString(data, &offset)
			if !ok || !builderCodePattern.MatchString(value) {
				return nil, false
			}
			if key == "a" {
				result.A = value
			} else {
				result.W = value
			}
		case "s":
			count, ok := readCBORLength(data, &offset, 4)
			if !ok || count > maxClientServiceCodes+maxServerServiceCodes+1 {
				return nil, false
			}
			result.S = make([]string, 0, count)
			for j := 0; j < count; j++ {
				value, ok := readCBORString(data, &offset)
				if !ok || !builderCodePattern.MatchString(value) {
					return nil, false
				}
				result.S = append(result.S, value)
			}
		default:
			return nil, false
		}
	}
	if offset != len(data) || result.A == "" && result.W == "" && len(result.S) == 0 {
		return nil, false
	}
	return result, true
}

func readCBORString(data []byte, offset *int) (string, bool) {
	length, ok := readCBORLength(data, offset, 3)
	if !ok || length > 32 || *offset+length > len(data) {
		return "", false
	}
	value := string(data[*offset : *offset+length])
	*offset += length
	return value, true
}

func readCBORLength(data []byte, offset *int, expectedMajor byte) (int, bool) {
	if *offset >= len(data) || data[*offset]>>5 != expectedMajor {
		return 0, false
	}
	additional := data[*offset] & 0x1f
	*offset++
	switch {
	case additional <= 23:
		return int(additional), true
	case additional == 24 && *offset < len(data):
		value := int(data[*offset])
		*offset++
		return value, value >= 24
	default:
		return 0, false
	}
}
