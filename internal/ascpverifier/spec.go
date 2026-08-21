// Package ascpverifier verifies captured delivery evidence and prepares exact
// ASCP v4 escrow verdict attestations. It has no transaction-broadcast or
// policy-decision capability.
package ascpverifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/crypto"
)

const (
	SpecVersion = "ascp.verification-spec/1"

	ClassStructuredData Class = "structured-data"
	ClassDocument       Class = "document"
	ClassComputation    Class = "computation"
	ClassMedia          Class = "media"

	CheckContentDigest CheckKind = "content-digest"
	CheckHTTPStatus    CheckKind = "http-status"
	CheckNonEmpty      CheckKind = "non-empty"
	CheckContentType   CheckKind = "content-type"
	CheckMinimumBytes  CheckKind = "minimum-bytes"
	CheckMaximumBytes  CheckKind = "maximum-bytes"
	CheckSHA256        CheckKind = "sha256"

	NotesRequireApproval NotesPolicy = "require-approval"

	maxSpecBytes       = 64 << 10
	maxReferenceLength = 2048
	maxArtifactLength  = 256
	maxPredicates      = 64
	maxChecks          = 16
)

var ErrInvalidSpec = errors.New("invalid verification spec")

type Class string
type CheckKind string
type NotesPolicy string

type FormatCheck struct {
	Kind     CheckKind `json:"kind"`
	Expected string    `json:"expected,omitempty"`
}

type Predicate struct {
	ID       string `json:"id"`
	Operator string `json:"operator"`
	Expected string `json:"expected"`
}

// VerificationSpec is intentionally closed and versioned. RequiredChecks and
// SemanticPredicates are canonicalized by bytewise key order before hashing.
type VerificationSpec struct {
	Version                string        `json:"version"`
	Class                  Class         `json:"class"`
	RequiredChecks         []FormatCheck `json:"requiredChecks"`
	ReferenceSource        string        `json:"referenceSource"`
	FreshnessWindowSeconds uint64        `json:"freshnessWindowSeconds"`
	SemanticPredicates     []Predicate   `json:"semanticPredicates"`
	Tolerance              string        `json:"tolerance"`
	TimeoutSeconds         uint64        `json:"timeoutSeconds"`
	EvidenceArtifact       string        `json:"evidenceArtifact"`
	NotesPolicy            NotesPolicy   `json:"notesPolicy"`
}

type CanonicalSpec struct {
	Spec          VerificationSpec
	CanonicalJSON []byte
	Hash          string
}

// ParseSpec rejects unknown fields and trailing JSON, normalizes the two
// set-like arrays, validates the complete v1 schema, and hashes canonical JSON
// with Keccak-256.
func ParseSpec(raw []byte) (CanonicalSpec, error) {
	if len(raw) == 0 || len(raw) > maxSpecBytes {
		return CanonicalSpec{}, fmt.Errorf("%w: document size", ErrInvalidSpec)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec VerificationSpec
	if err := decoder.Decode(&spec); err != nil {
		return CanonicalSpec{}, fmt.Errorf("%w: decode: %v", ErrInvalidSpec, err)
	}
	if err := requireEOF(decoder); err != nil {
		return CanonicalSpec{}, err
	}
	sort.Slice(spec.RequiredChecks, func(i, j int) bool {
		left, right := spec.RequiredChecks[i], spec.RequiredChecks[j]
		if left.Kind == right.Kind {
			return left.Expected < right.Expected
		}
		return left.Kind < right.Kind
	})
	sort.Slice(spec.SemanticPredicates, func(i, j int) bool {
		return spec.SemanticPredicates[i].ID < spec.SemanticPredicates[j].ID
	})
	if err := validateSpec(spec); err != nil {
		return CanonicalSpec{}, err
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		return CanonicalSpec{}, fmt.Errorf("%w: canonical encoding", ErrInvalidSpec)
	}
	hash := crypto.Keccak256Hash(canonical)
	return CanonicalSpec{Spec: spec, CanonicalJSON: canonical, Hash: hash.Hex()}, nil
}

func validateSpec(spec VerificationSpec) error {
	if spec.Version != SpecVersion {
		return fmt.Errorf("%w: unsupported version", ErrInvalidSpec)
	}
	switch spec.Class {
	case ClassStructuredData, ClassDocument, ClassComputation, ClassMedia:
	default:
		return fmt.Errorf("%w: unsupported class", ErrInvalidSpec)
	}
	if !boundedText(spec.ReferenceSource, maxReferenceLength) || !boundedText(spec.EvidenceArtifact, maxArtifactLength) {
		return fmt.Errorf("%w: reference source or evidence artifact", ErrInvalidSpec)
	}
	if spec.FreshnessWindowSeconds == 0 || spec.FreshnessWindowSeconds > 7*24*60*60 || spec.TimeoutSeconds == 0 || spec.TimeoutSeconds > 300 {
		return fmt.Errorf("%w: freshness or timeout", ErrInvalidSpec)
	}
	if spec.NotesPolicy != NotesRequireApproval {
		return fmt.Errorf("%w: PASS_WITH_NOTES requires approval", ErrInvalidSpec)
	}
	if len(spec.RequiredChecks) < 3 || len(spec.RequiredChecks) > maxChecks || len(spec.SemanticPredicates) == 0 || len(spec.SemanticPredicates) > maxPredicates {
		return fmt.Errorf("%w: check or predicate count", ErrInvalidSpec)
	}
	if !canonicalDecimal(spec.Tolerance) {
		return fmt.Errorf("%w: tolerance", ErrInvalidSpec)
	}
	requiredFloor := map[CheckKind]bool{CheckContentDigest: false, CheckHTTPStatus: false, CheckNonEmpty: false}
	seenChecks := make(map[CheckKind]struct{}, len(spec.RequiredChecks))
	for _, check := range spec.RequiredChecks {
		if _, duplicate := seenChecks[check.Kind]; duplicate {
			return fmt.Errorf("%w: duplicate check %q", ErrInvalidSpec, check.Kind)
		}
		seenChecks[check.Kind] = struct{}{}
		if err := validateCheck(check); err != nil {
			return err
		}
		if _, required := requiredFloor[check.Kind]; required {
			requiredFloor[check.Kind] = true
		}
	}
	for kind, present := range requiredFloor {
		if !present {
			return fmt.Errorf("%w: missing floor check %q", ErrInvalidSpec, kind)
		}
	}
	seenPredicates := make(map[string]struct{}, len(spec.SemanticPredicates))
	for _, predicate := range spec.SemanticPredicates {
		if !identifier(predicate.ID) || !identifier(predicate.Operator) || !boundedText(predicate.Expected, 4096) {
			return fmt.Errorf("%w: semantic predicate", ErrInvalidSpec)
		}
		if _, duplicate := seenPredicates[predicate.ID]; duplicate {
			return fmt.Errorf("%w: duplicate predicate %q", ErrInvalidSpec, predicate.ID)
		}
		seenPredicates[predicate.ID] = struct{}{}
	}
	return nil
}

func validateCheck(check FormatCheck) error {
	switch check.Kind {
	case CheckContentDigest, CheckNonEmpty:
		if check.Expected != "" {
			return fmt.Errorf("%w: %s takes no expected value", ErrInvalidSpec, check.Kind)
		}
	case CheckHTTPStatus:
		if check.Expected != "200-299" {
			value, err := strconv.ParseUint(check.Expected, 10, 16)
			if err != nil || value < 100 || value > 599 || strconv.FormatUint(value, 10) != check.Expected {
				return fmt.Errorf("%w: HTTP status expectation", ErrInvalidSpec)
			}
		}
	case CheckContentType:
		if !boundedText(check.Expected, 256) || strings.ToLower(check.Expected) != check.Expected || strings.ContainsAny(check.Expected, " ;\t\r\n") {
			return fmt.Errorf("%w: content type expectation", ErrInvalidSpec)
		}
	case CheckMinimumBytes, CheckMaximumBytes:
		value, err := strconv.ParseUint(check.Expected, 10, 63)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != check.Expected {
			return fmt.Errorf("%w: byte expectation", ErrInvalidSpec)
		}
	case CheckSHA256:
		if !canonicalHash(check.Expected, false) {
			return fmt.Errorf("%w: SHA-256 expectation", ErrInvalidSpec)
		}
	default:
		return fmt.Errorf("%w: unknown check %q", ErrInvalidSpec, check.Kind)
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON", ErrInvalidSpec)
	}
	return nil
}

func canonicalDecimal(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func boundedText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func identifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || index > 0 && (character >= '0' && character <= '9' || character == '-' || character == '_') {
			continue
		}
		return false
	}
	return true
}
