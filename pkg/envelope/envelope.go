// Package envelope defines FlowOps' canonical, signed authorization capability.
// It deliberately contains no policy or wallet logic: it only makes the exact
// action authorized by the control plane portable and independently verifiable.
package envelope

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"

	"golang.org/x/crypto/sha3"
)

const (
	Version      = "flowops.authorization.v1"
	domainPrefix = "flowops:authorization:v1\n"
)

type Rail string

const (
	RailX402   Rail = "x402"
	RailDirect Rail = "direct_usdc"
	RailEscrow Rail = "escrow"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
	addressPattern    = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	noncePattern      = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
)

// ValidIdentifier reports whether value is safe for use as a canonical
// FlowOps identifier. Other packages use this function so authorization
// producers and consumers cannot silently drift to different grammars.
func ValidIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

// Authorization is the complete authority FlowOps grants. Strings representing
// money and addresses use one canonical form so different runtimes cannot sign
// different byte sequences for the same-looking action.
type Authorization struct {
	Version         string       `json:"version"`
	AuthorizationID string       `json:"authorizationId"`
	OrganizationID  string       `json:"organizationId"`
	CustomerID      string       `json:"customerId"`
	AgentID         string       `json:"agentId"`
	TaskID          string       `json:"taskId"`
	ActionID        string       `json:"actionId"`
	Rail            Rail         `json:"rail"`
	ChainID         uint64       `json:"chainId"`
	Recipient       string       `json:"recipient"`
	Asset           string       `json:"asset"`
	AmountAtomic    string       `json:"amountAtomic"`
	Resource        string       `json:"resource"`
	PolicyVersion   string       `json:"policyVersion"`
	Nonce           string       `json:"nonce"`
	IssuedAt        int64        `json:"issuedAt"`
	ExpiresAt       int64        `json:"expiresAt"`
	Escrow          *EscrowTerms `json:"escrow,omitempty"`
}

// EscrowTerms is the exact CallEscrow calldata authority. It is absent on all
// other rails and signed as part of the authorization canonical bytes.
type EscrowTerms struct {
	Contract      string `json:"contract"`
	Buyer         string `json:"buyer"`
	Provider      string `json:"provider"`
	CallID        string `json:"callId"`
	TaskDigest    string `json:"taskDigest"`
	RequestDigest string `json:"requestDigest"`
	AcknowledgeBy uint64 `json:"acknowledgeBy"`
	DeliverBy     uint64 `json:"deliverBy"`
	ReleaseWindow uint64 `json:"releaseWindowSeconds"`
}

type SignedAuthorization struct {
	Authorization Authorization `json:"authorization"`
	KeyID         string        `json:"keyId"`
	Signature     string        `json:"signature"`
}

func (a Authorization) Validate() error {
	if a.Version != Version {
		return fmt.Errorf("version: got %q, want %q", a.Version, Version)
	}
	for name, value := range map[string]string{
		"authorizationId": a.AuthorizationID,
		"organizationId":  a.OrganizationID,
		"customerId":      a.CustomerID,
		"agentId":         a.AgentID,
		"taskId":          a.TaskID,
		"actionId":        a.ActionID,
		"policyVersion":   a.PolicyVersion,
	} {
		if !ValidIdentifier(value) {
			return fmt.Errorf("%s: must match %s", name, identifierPattern)
		}
	}
	if a.Rail != RailX402 && a.Rail != RailDirect && a.Rail != RailEscrow {
		return fmt.Errorf("rail: unsupported value %q", a.Rail)
	}
	if a.Rail == RailEscrow {
		if a.Escrow == nil {
			return errors.New("escrow: exact escrow terms are required")
		}
		if err := a.Escrow.Validate(a.ChainID, a.Recipient); err != nil {
			return fmt.Errorf("escrow: %w", err)
		}
	} else if a.Escrow != nil {
		return errors.New("escrow: terms are forbidden on non-escrow rails")
	}
	if a.ChainID == 0 {
		return errors.New("chainId: must be positive")
	}
	if !addressPattern.MatchString(a.Recipient) {
		return errors.New("recipient: must be a lowercase 20-byte EVM address")
	}
	if !addressPattern.MatchString(a.Asset) {
		return errors.New("asset: must be a lowercase 20-byte EVM address")
	}
	if err := validateAtomicAmount(a.AmountAtomic); err != nil {
		return fmt.Errorf("amountAtomic: %w", err)
	}
	if strings.TrimSpace(a.Resource) == "" || len(a.Resource) > 2048 {
		return errors.New("resource: must contain 1 to 2048 non-whitespace characters")
	}
	if !noncePattern.MatchString(a.Nonce) {
		return errors.New("nonce: must be a lowercase 32-byte hex value")
	}
	if a.IssuedAt <= 0 {
		return errors.New("issuedAt: must be a positive Unix timestamp")
	}
	if a.ExpiresAt <= a.IssuedAt {
		return errors.New("expiresAt: must be after issuedAt")
	}
	if a.Escrow != nil && (a.IssuedAt >= int64(a.Escrow.AcknowledgeBy) || a.ExpiresAt > int64(a.Escrow.AcknowledgeBy)) {
		return errors.New("escrow: authorization must be issued before and expire no later than acknowledgeBy")
	}
	return nil
}

func (t EscrowTerms) Validate(chainID uint64, recipient string) error {
	if chainID != 8453 && chainID != 84532 {
		return errors.New("escrow terms support Base mainnet or Base Sepolia only")
	}
	for name, value := range map[string]string{"contract": t.Contract, "buyer": t.Buyer, "provider": t.Provider} {
		if !addressPattern.MatchString(value) {
			return fmt.Errorf("%s must be a lowercase 20-byte EVM address", name)
		}
	}
	if t.Provider != recipient {
		return errors.New("provider must equal the approved recipient")
	}
	if t.Buyer == t.Provider {
		return errors.New("buyer and provider must differ")
	}
	for name, value := range map[string]string{"callId": t.CallID, "taskDigest": t.TaskDigest, "requestDigest": t.RequestDigest} {
		if !noncePattern.MatchString(value) || value == "0x"+strings.Repeat("0", 64) {
			return fmt.Errorf("%s must be a canonical non-zero 32-byte hash", name)
		}
	}
	if t.AcknowledgeBy == 0 || t.AcknowledgeBy > math.MaxInt64 || t.DeliverBy <= t.AcknowledgeBy || t.DeliverBy > math.MaxInt64 || t.ReleaseWindow == 0 || t.ReleaseWindow > 30*24*60*60 {
		return errors.New("deadlines or release window are invalid")
	}
	want, err := DeriveEscrowCallID(chainID, t.Contract, t.Buyer, t.TaskDigest, t.RequestDigest)
	if err != nil || want != t.CallID {
		return errors.New("callId does not bind the approved chain, contract, buyer, task, and request")
	}
	return nil
}

func DeriveEscrowCallID(chainID uint64, contract, buyer, taskDigest, requestDigest string) (string, error) {
	if chainID == 0 || !addressPattern.MatchString(contract) || !addressPattern.MatchString(buyer) || !noncePattern.MatchString(taskDigest) || !noncePattern.MatchString(requestDigest) {
		return "", errors.New("call ID inputs are invalid")
	}
	domainHash := sha3.NewLegacyKeccak256()
	_, _ = domainHash.Write([]byte("FLOWOPS_CALL_ESCROW_V1"))
	domain := domainHash.Sum(nil)
	words := make([]byte, 0, 32*6)
	words = append(words, domain...)
	chainWord := make([]byte, 32)
	chain := new(big.Int).SetUint64(chainID).Bytes()
	copy(chainWord[32-len(chain):], chain)
	words = append(words, chainWord...)
	for _, address := range []string{contract, buyer} {
		raw, _ := hex.DecodeString(strings.TrimPrefix(address, "0x"))
		word := make([]byte, 32)
		copy(word[12:], raw)
		words = append(words, word...)
	}
	for _, digest := range []string{taskDigest, requestDigest} {
		raw, _ := hex.DecodeString(strings.TrimPrefix(digest, "0x"))
		words = append(words, raw...)
	}
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(words)
	return "0x" + hex.EncodeToString(hash.Sum(nil)), nil
}

func validateAtomicAmount(value string) error {
	if value == "" || value[0] == '0' {
		return errors.New("must be a canonical positive base-10 integer without leading zeroes")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return errors.New("must contain decimal digits only")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() <= 0 {
		return errors.New("must be positive")
	}
	if n.BitLen() > 256 {
		return errors.New("exceeds uint256")
	}
	return nil
}

// CanonicalBytes returns the only JSON representation that may be signed.
// Go's struct-field order is stable; maps are intentionally absent.
func (a Authorization) CanonicalBytes() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func (a Authorization) Digest() ([32]byte, error) {
	canonical, err := a.CanonicalBytes()
	if err != nil {
		return [32]byte{}, err
	}
	message := make([]byte, 0, len(domainPrefix)+len(canonical))
	message = append(message, domainPrefix...)
	message = append(message, canonical...)
	return sha256.Sum256(message), nil
}

func Sign(a Authorization, keyID string, privateKey ed25519.PrivateKey) (SignedAuthorization, error) {
	if !identifierPattern.MatchString(keyID) {
		return SignedAuthorization{}, errors.New("keyId: invalid identifier")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return SignedAuthorization{}, errors.New("private key: invalid Ed25519 length")
	}
	digest, err := a.Digest()
	if err != nil {
		return SignedAuthorization{}, err
	}
	signature := ed25519.Sign(privateKey, digest[:])
	return SignedAuthorization{
		Authorization: a,
		KeyID:         keyID,
		Signature:     "0x" + hex.EncodeToString(signature),
	}, nil
}

func Verify(s SignedAuthorization, publicKey ed25519.PublicKey) error {
	if !identifierPattern.MatchString(s.KeyID) {
		return errors.New("keyId: invalid identifier")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("public key: invalid Ed25519 length")
	}
	if !strings.HasPrefix(s.Signature, "0x") {
		return errors.New("signature: must use 0x-prefixed lowercase hex")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(s.Signature, "0x"))
	if err != nil || len(raw) != ed25519.SignatureSize || s.Signature != "0x"+hex.EncodeToString(raw) {
		return errors.New("signature: invalid canonical Ed25519 encoding")
	}
	digest, err := s.Authorization.Digest()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, digest[:], raw) {
		return errors.New("signature: verification failed")
	}
	return nil
}

// NormalizeAddress converts user-facing EVM input to the canonical envelope form.
// It validates shape only; recipient identity and checksum policy belong upstream.
func NormalizeAddress(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if !addressPattern.MatchString(normalized) {
		return "", errors.New("address must be a 20-byte EVM hex value")
	}
	return normalized, nil
}
