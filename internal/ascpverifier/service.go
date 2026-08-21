package ascpverifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
)

const (
	VerdictPass          Verdict = "PASS"
	VerdictFail          Verdict = "FAIL"
	VerdictPassWithNotes Verdict = "PASS_WITH_NOTES"

	OutcomeRelease Outcome = "RELEASE"
	OutcomeRefund  Outcome = "REFUND"

	maxDeliveryBytes  = 16 << 20
	maxReferenceBytes = 4096
)

var (
	ErrInvalidConfiguration = errors.New("invalid verifier configuration")
	ErrInvalidDelivery      = errors.New("invalid captured delivery")
	ErrSpecHashMismatch     = errors.New("verification spec hash mismatch")
	ErrUnsupportedClass     = errors.New("verification class has no engine")
	ErrInvalidEngineResult  = errors.New("invalid verification engine result")
	ErrApprovalRequired     = errors.New("PASS_WITH_NOTES requires an exact approved decision")
	ErrDecisionConflict     = errors.New("call already has a different verifier decision")
	ErrAttestationExpired   = errors.New("cached verdict attestation expired")
	ErrVerifierInactive     = errors.New("verifier key is not active at the configured epoch")
	ErrSigning              = errors.New("verdict signing failed")
)

type Verdict string
type Outcome string

type Delivery struct {
	Reference     []byte `json:"reference"`
	Content       []byte `json:"content"`
	ContentDigest string `json:"contentDigest"`
	HTTPStatus    uint16 `json:"httpStatus"`
	ContentType   string `json:"contentType"`
	CapturedAt    uint64 `json:"capturedAt"`
}

type Input struct {
	Commitment executioncommitment.Commitment `json:"commitment"`
	SpecJSON   []byte                         `json:"spec"`
	Delivery   Delivery                       `json:"delivery"`
}

type EngineInput struct {
	Spec     VerificationSpec
	Delivery Delivery
}

type EngineResult struct {
	Verdict Verdict  `json:"verdict"`
	Code    string   `json:"code"`
	Notes   []string `json:"notes,omitempty"`
}

type Engine interface {
	Verify(context.Context, EngineInput) (EngineResult, error)
}

type NotesReview struct {
	CallID               string
	CommitmentHash       string
	VerificationSpecHash string
	DeliveryHash         string
	EvidenceHash         string
	Result               EngineResult
}

type NotesDecision struct {
	DecisionID string
	Outcome    Outcome
}

// NotesAuthorizer is implemented by the step-up approval boundary. The
// verifier cannot manufacture or self-approve this decision.
type NotesAuthorizer interface {
	Decide(context.Context, NotesReview) (NotesDecision, error)
}

type DigestSigner interface {
	Address() common.Address
	SignDigest(context.Context, common.Hash) ([]byte, error)
}

type NonceSource interface {
	Next(context.Context) (*big.Int, error)
}

// VerifierKeyGate reads finalized chain governance state. It is checked both
// before evaluation and immediately before signing or returning a cached
// bearer, so revocation stops publication without relying only on execution-
// time contract checks.
type VerifierKeyGate interface {
	CheckActive(context.Context, string, string, common.Address, uint64) error
}

type Config struct {
	VerifierEpoch        uint64
	VerifierSoftwareHash string
	AttestationTTL       time.Duration
	Clock                func() time.Time
	Engines              map[Class]Engine
	NotesAuthorizer      NotesAuthorizer
	Signer               DigestSigner
	Nonces               NonceSource
	VerifierKeyGate      VerifierKeyGate
}

type SignedDecision struct {
	Verdict          Verdict     `json:"verdict"`
	Outcome          Outcome     `json:"outcome"`
	Code             string      `json:"code"`
	Notes            []string    `json:"notes,omitempty"`
	DecisionID       string      `json:"decisionId,omitempty"`
	SpecHash         string      `json:"specHash"`
	CommitmentHash   string      `json:"commitmentHash"`
	DeliveryHash     string      `json:"deliveryHash"`
	EvidenceHash     string      `json:"evidenceHash"`
	Attestation      Attestation `json:"attestation"`
	AttestationHash  string      `json:"attestationHash"`
	Signer           string      `json:"signer"`
	Signature        string      `json:"signature"`
	CanonicalSpec    []byte      `json:"canonicalSpec"`
	VerificationTime uint64      `json:"verificationTime"`
}

type Service struct {
	config Config
	mu     sync.Mutex
	byCall map[string]cachedDecision
	nonces map[string]struct{}
}

type cachedDecision struct {
	fingerprint string
	decision    SignedDecision
}

func New(config Config) (*Service, error) {
	if config.VerifierEpoch == 0 || !canonicalHash(config.VerifierSoftwareHash, true) || config.Signer == nil ||
		config.Signer.Address() == (common.Address{}) || config.Nonces == nil || config.VerifierKeyGate == nil || len(config.Engines) == 0 ||
		config.AttestationTTL < time.Second || config.AttestationTTL > MaximumAttestationWindow*time.Second || config.AttestationTTL%time.Second != 0 {
		return nil, ErrInvalidConfiguration
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	engines := make(map[Class]Engine, len(config.Engines))
	for class, engine := range config.Engines {
		switch class {
		case ClassStructuredData, ClassDocument, ClassComputation, ClassMedia:
		default:
			return nil, ErrInvalidConfiguration
		}
		if engine == nil {
			return nil, ErrInvalidConfiguration
		}
		engines[class] = engine
	}
	config.Engines = engines
	return &Service{config: config, byCall: make(map[string]cachedDecision), nonces: make(map[string]struct{})}, nil
}

// VerifyAndSign performs all spec and evidence checks before asking the
// isolated signer for an ECDSA signature. It never submits a transaction.
// Exact retries in one process return the same result; a second delivery for
// the same call is rejected to avoid contradictory bearer attestations.
func (s *Service) VerifyAndSign(ctx context.Context, input Input) (SignedDecision, error) {
	canonicalSpec, err := ParseSpec(input.SpecJSON)
	if err != nil {
		return SignedDecision{}, err
	}
	if err := input.Commitment.Validate(); err != nil {
		return SignedDecision{}, err
	}
	if canonicalSpec.Hash != input.Commitment.VerificationSpecHash {
		return SignedDecision{}, ErrSpecHashMismatch
	}
	commitmentHash, err := input.Commitment.Digest(input.Commitment.EscrowContract, input.Commitment.ChainID)
	if err != nil {
		return SignedDecision{}, err
	}
	callID := crypto.Keccak256Hash(commitmentHash[:]).Hex()
	deliveryHash, recomputedDigest, err := validateAndHashDelivery(input.Delivery)
	if err != nil {
		return SignedDecision{}, err
	}
	fingerprint := crypto.Keccak256Hash(
		[]byte("ASCP_VERIFIER_INPUT_V1"), commitmentHash[:], common.HexToHash(canonicalSpec.Hash).Bytes(),
		common.HexToHash(deliveryHash).Bytes(), common.HexToHash(input.Delivery.ContentDigest).Bytes(),
	).Hex()
	if err := s.config.VerifierKeyGate.CheckActive(
		ctx, input.Commitment.ChainID, input.Commitment.EscrowContract, s.config.Signer.Address(), s.config.VerifierEpoch,
	); err != nil {
		return SignedDecision{}, fmt.Errorf("%w: %v", ErrVerifierInactive, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byCall[callID]; ok {
		if existing.fingerprint != fingerprint {
			return SignedDecision{}, ErrDecisionConflict
		}
		if uint64(s.config.Clock().UTC().Unix()) > existing.decision.Attestation.ValidUntil {
			return SignedDecision{}, ErrAttestationExpired
		}
		return existing.decision, nil
	}

	result, err := s.evaluate(ctx, canonicalSpec.Spec, input.Delivery, recomputedDigest, input.Commitment.DeliverBy)
	if err != nil {
		return SignedDecision{}, err
	}
	notes := append([]string(nil), result.Notes...)
	sort.Strings(notes)
	result.Notes = notes
	evidenceHash := hashEvidence(callID, commitmentHash.Hex(), canonicalSpec.Hash, deliveryHash, result, "")
	outcome := OutcomeRelease
	decisionID := ""
	if result.Verdict == VerdictFail {
		outcome = OutcomeRefund
	}
	if result.Verdict == VerdictPassWithNotes {
		if s.config.NotesAuthorizer == nil {
			return SignedDecision{}, ErrApprovalRequired
		}
		decision, decisionErr := s.config.NotesAuthorizer.Decide(ctx, NotesReview{
			CallID: callID, CommitmentHash: commitmentHash.Hex(), VerificationSpecHash: canonicalSpec.Hash,
			DeliveryHash: deliveryHash, EvidenceHash: evidenceHash, Result: result,
		})
		if decisionErr != nil || !identifier(decision.DecisionID) || (decision.Outcome != OutcomeRelease && decision.Outcome != OutcomeRefund) {
			return SignedDecision{}, ErrApprovalRequired
		}
		decisionID, outcome = decision.DecisionID, decision.Outcome
		evidenceHash = hashEvidence(callID, commitmentHash.Hex(), canonicalSpec.Hash, deliveryHash, result, decisionID+":"+string(outcome))
	}

	now := s.config.Clock().UTC()
	if now.Unix() <= 0 || uint64(now.Unix()) >= input.Commitment.SettleBy {
		return SignedDecision{}, ErrInvalidDelivery
	}
	validUntil := now.Add(s.config.AttestationTTL).Unix()
	if validUntil > int64(input.Commitment.SettleBy) {
		validUntil = int64(input.Commitment.SettleBy)
	}
	if validUntil <= now.Unix() {
		return SignedDecision{}, ErrInvalidDelivery
	}
	nonce, err := s.config.Nonces.Next(ctx)
	if err != nil || nonce == nil || nonce.Sign() < 0 || nonce.BitLen() > 256 {
		return SignedDecision{}, fmt.Errorf("%w: nonce source", ErrSigning)
	}
	nonceText := nonce.String()
	if _, duplicate := s.nonces[nonceText]; duplicate {
		return SignedDecision{}, fmt.Errorf("%w: duplicate nonce", ErrSigning)
	}
	contractVerdict := ContractVerdictRelease
	if outcome == OutcomeRefund {
		contractVerdict = ContractVerdictEarlyRefund
	}
	attestation := Attestation{
		CallID: callID, CommitmentHash: commitmentHash.Hex(), EscrowContract: input.Commitment.EscrowContract,
		VerifierEpoch: s.config.VerifierEpoch, VerificationSpecHash: canonicalSpec.Hash,
		VerifierSoftwareHash: s.config.VerifierSoftwareHash, DeliveryHash: deliveryHash,
		DeliveredAt: input.Delivery.CapturedAt, EvidenceHash: evidenceHash, Verdict: contractVerdict,
		VerdictNonce: nonceText, IssuedAt: uint64(now.Unix()), ValidUntil: uint64(validUntil),
	}
	digest, err := attestation.Digest(input.Commitment.ChainID)
	if err != nil {
		return SignedDecision{}, err
	}
	if err := s.config.VerifierKeyGate.CheckActive(
		ctx, input.Commitment.ChainID, input.Commitment.EscrowContract, s.config.Signer.Address(), s.config.VerifierEpoch,
	); err != nil {
		return SignedDecision{}, fmt.Errorf("%w: %v", ErrVerifierInactive, err)
	}
	signature, err := s.config.Signer.SignDigest(ctx, digest)
	if err != nil {
		return SignedDecision{}, fmt.Errorf("%w: %v", ErrSigning, err)
	}
	normalizedSignature, signer, err := normalizeAndRecover(signature, digest)
	if err != nil || signer != s.config.Signer.Address() {
		return SignedDecision{}, fmt.Errorf("%w: signature did not recover configured signer", ErrSigning)
	}
	decision := SignedDecision{
		Verdict: result.Verdict, Outcome: outcome, Code: result.Code, Notes: result.Notes, DecisionID: decisionID,
		SpecHash: canonicalSpec.Hash, CommitmentHash: commitmentHash.Hex(), DeliveryHash: deliveryHash,
		EvidenceHash: evidenceHash, Attestation: attestation, AttestationHash: digest.Hex(),
		Signer: strings.ToLower(signer.Hex()), Signature: "0x" + hex.EncodeToString(normalizedSignature),
		CanonicalSpec: canonicalSpec.CanonicalJSON, VerificationTime: uint64(now.Unix()),
	}
	s.nonces[nonceText] = struct{}{}
	s.byCall[callID] = cachedDecision{fingerprint: fingerprint, decision: decision}
	return decision, nil
}

func (s *Service) evaluate(ctx context.Context, spec VerificationSpec, delivery Delivery, recomputedDigest string, deliverBy uint64) (EngineResult, error) {
	now := s.config.Clock().UTC().Unix()
	if now <= 0 || uint64(now) < delivery.CapturedAt || uint64(now)-delivery.CapturedAt > spec.FreshnessWindowSeconds {
		return EngineResult{}, ErrInvalidDelivery
	}
	if delivery.CapturedAt > deliverBy {
		return EngineResult{Verdict: VerdictFail, Code: "LATE_DELIVERY"}, nil
	}
	if result, failed := runFormatChecks(spec.RequiredChecks, delivery, recomputedDigest); failed {
		return result, nil
	}
	engine, ok := s.config.Engines[spec.Class]
	if !ok {
		return EngineResult{}, ErrUnsupportedClass
	}
	checkContext, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
	defer cancel()
	result, err := engine.Verify(checkContext, EngineInput{Spec: spec, Delivery: delivery})
	if err != nil {
		return EngineResult{}, err
	}
	if err := validateEngineResult(result); err != nil {
		return EngineResult{}, err
	}
	return result, nil
}

func runFormatChecks(checks []FormatCheck, delivery Delivery, recomputedDigest string) (EngineResult, bool) {
	for _, check := range checks {
		failed := false
		switch check.Kind {
		case CheckContentDigest:
			failed = delivery.ContentDigest != recomputedDigest
		case CheckHTTPStatus:
			if check.Expected == "200-299" {
				failed = delivery.HTTPStatus < 200 || delivery.HTTPStatus > 299
			} else {
				expected, _ := strconv.ParseUint(check.Expected, 10, 16)
				failed = uint64(delivery.HTTPStatus) != expected
			}
		case CheckNonEmpty:
			failed = len(delivery.Content) == 0
		case CheckContentType:
			failed = delivery.ContentType != check.Expected
		case CheckMinimumBytes:
			expected, _ := strconv.ParseUint(check.Expected, 10, 63)
			failed = uint64(len(delivery.Content)) < expected
		case CheckMaximumBytes:
			expected, _ := strconv.ParseUint(check.Expected, 10, 63)
			failed = uint64(len(delivery.Content)) > expected
		case CheckSHA256:
			failed = recomputedDigest != check.Expected
		}
		if failed {
			return EngineResult{Verdict: VerdictFail, Code: "FORMAT_" + strings.ToUpper(strings.ReplaceAll(string(check.Kind), "-", "_"))}, true
		}
	}
	return EngineResult{}, false
}

func validateAndHashDelivery(delivery Delivery) (string, string, error) {
	if len(delivery.Reference) == 0 || len(delivery.Reference) > maxReferenceBytes || len(delivery.Content) > maxDeliveryBytes ||
		!canonicalHash(delivery.ContentDigest, false) || delivery.HTTPStatus < 100 || delivery.HTTPStatus > 599 ||
		delivery.CapturedAt == 0 || !boundedText(delivery.ContentType, 256) || strings.ToLower(delivery.ContentType) != delivery.ContentType {
		return "", "", ErrInvalidDelivery
	}
	contentDigest := sha256.Sum256(delivery.Content)
	recomputed := "0x" + hex.EncodeToString(contentDigest[:])
	if delivery.ContentDigest != recomputed {
		return "", "", ErrInvalidDelivery
	}
	referenceHash := sha256.Sum256(delivery.Reference)
	artifact := struct {
		Domain        string `json:"domain"`
		ReferenceHash string `json:"referenceHash"`
		ContentDigest string `json:"contentDigest"`
		HTTPStatus    uint16 `json:"httpStatus"`
		ContentType   string `json:"contentType"`
		CapturedAt    uint64 `json:"capturedAt"`
	}{"ASCP_CAPTURED_DELIVERY_V1", "0x" + hex.EncodeToString(referenceHash[:]), recomputed, delivery.HTTPStatus, delivery.ContentType, delivery.CapturedAt}
	encoded, _ := json.Marshal(artifact)
	return crypto.Keccak256Hash(encoded).Hex(), recomputed, nil
}

func validateEngineResult(result EngineResult) error {
	if result.Verdict != VerdictPass && result.Verdict != VerdictFail && result.Verdict != VerdictPassWithNotes || !identifier(result.Code) || len(result.Notes) > 64 {
		return ErrInvalidEngineResult
	}
	if result.Verdict == VerdictPassWithNotes && len(result.Notes) == 0 || result.Verdict != VerdictPassWithNotes && len(result.Notes) != 0 {
		return ErrInvalidEngineResult
	}
	seen := make(map[string]struct{}, len(result.Notes))
	for _, note := range result.Notes {
		if !boundedText(note, 2048) {
			return ErrInvalidEngineResult
		}
		if _, duplicate := seen[note]; duplicate {
			return ErrInvalidEngineResult
		}
		seen[note] = struct{}{}
	}
	return nil
}

func hashEvidence(callID, commitmentHash, specHash, deliveryHash string, result EngineResult, decision string) string {
	record := struct {
		Domain         string       `json:"domain"`
		CallID         string       `json:"callId"`
		CommitmentHash string       `json:"commitmentHash"`
		SpecHash       string       `json:"specHash"`
		DeliveryHash   string       `json:"deliveryHash"`
		Result         EngineResult `json:"result"`
		Decision       string       `json:"decision,omitempty"`
	}{"ASCP_VERIFIER_EVIDENCE_V1", callID, commitmentHash, specHash, deliveryHash, result, decision}
	encoded, _ := json.Marshal(record)
	return crypto.Keccak256Hash(encoded).Hex()
}

func normalizeAndRecover(signature []byte, digest common.Hash) ([]byte, common.Address, error) {
	if len(signature) != crypto.SignatureLength {
		return nil, common.Address{}, ErrSigning
	}
	normalized := append([]byte(nil), signature...)
	recovery := normalized[64]
	if recovery == 27 || recovery == 28 {
		recovery -= 27
	} else if recovery != 0 && recovery != 1 {
		return nil, common.Address{}, ErrSigning
	}
	if !crypto.ValidateSignatureValues(recovery, new(big.Int).SetBytes(normalized[:32]), new(big.Int).SetBytes(normalized[32:64]), true) {
		return nil, common.Address{}, ErrSigning
	}
	recoverable := append([]byte(nil), normalized...)
	recoverable[64] = recovery
	publicKey, err := crypto.SigToPub(digest[:], recoverable)
	if err != nil {
		return nil, common.Address{}, ErrSigning
	}
	normalized[64] = recovery + 27
	return normalized, crypto.PubkeyToAddress(*publicKey), nil
}

// MemoryNonceSource is safe for tests and one-process demonstrations only.
// Production must inject a durable, exclusively owned uint256 nonce source.
type MemoryNonceSource struct {
	mu   sync.Mutex
	next *big.Int
}

func NewMemoryNonceSource(first *big.Int) (*MemoryNonceSource, error) {
	if first == nil || first.Sign() < 0 || first.BitLen() > 256 {
		return nil, ErrInvalidConfiguration
	}
	return &MemoryNonceSource{next: new(big.Int).Set(first)}, nil
}

func (s *MemoryNonceSource) Next(context.Context) (*big.Int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := new(big.Int).Set(s.next)
	s.next.Add(s.next, big.NewInt(1))
	if s.next.BitLen() > 256 {
		return nil, ErrSigning
	}
	return value, nil
}
