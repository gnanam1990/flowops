// Package ascpadaptation owns the signed, single-use adaptation grant
// protocol. Issuance and intake consumption are durable boundaries; this file
// contains the canonical artifact and its fail-closed verification rules.
package ascpadaptation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	Protocol           = "ASCP_GRANT_V1"
	MaximumLifetime    = 30 * time.Minute
	RequiredAttempts   = uint8(1)
	maximumSellerCount = 64
)

var (
	ErrInvalidGrant      = errors.New("adaptation grant is invalid")
	ErrGrantScope        = errors.New("adaptation grant does not authorize this intake")
	ErrGrantConsumed     = errors.New("adaptation grant is already consumed")
	ErrIssueConflict     = errors.New("original rejection names different adaptation grant terms")
	ErrReasonIneligible  = errors.New("rejection reason cannot issue an adaptation grant")
	ErrSignerUnavailable = errors.New("adaptation grant signer is unavailable")
	ErrGrantNotFound     = errors.New("adaptation grant was not found")
)

type ReasonClass string

const (
	ReasonTooExpensive  ReasonClass = "too_expensive"
	ReasonWrongSeller   ReasonClass = "wrong_seller"
	ReasonInappropriate ReasonClass = "inappropriate"
	ReasonNotNeeded     ReasonClass = "not_needed"
)

type Grant struct {
	Protocol          string   `json:"protocol"`
	GrantID           string   `json:"grantId"`
	OriginalIntentID  string   `json:"originalIntentId"`
	OrganizationID    string   `json:"organizationId"`
	AgentID           string   `json:"agentId"`
	TaskID            string   `json:"taskId"`
	AllowedCategory   string   `json:"allowedCategory"`
	MaxAmountAtomic   string   `json:"maxAmountAtomic"`
	AllowedSellerSet  []string `json:"allowedSellerSet"`
	RemainingAttempts uint8    `json:"remainingAttempts"`
	IssuedAt          int64    `json:"issuedAt"`
	ExpiresAt         int64    `json:"expiresAt"`
}

type SignedGrant struct {
	Grant     Grant  `json:"grant"`
	Signature string `json:"signature"`
}

type IssueRequest struct {
	ReasonClass      ReasonClass
	OriginalIntentID string
	OrganizationID   string
	AgentID          string
	TaskID           string
	AllowedCategory  string
	MaxAmountAtomic  string
	AllowedSellerSet []string
	IssuedAt         int64
}

type Use struct {
	OrganizationID string
	AgentID        string
	TaskID         string
	Category       string
	AmountAtomic   string
	SellerID       string
}

type DigestSigner interface {
	SignDigest(context.Context, []byte) ([]byte, error)
}

type Record struct {
	Artifact             SignedGrant
	ReasonClass          ReasonClass
	Digest               string
	CanonicalRequestHash string
	ConsumedOperationID  string
	Replayed             bool
}

type Store interface {
	Issue(context.Context, Record) (Record, bool, error)
	GetGrant(context.Context, string, string, string) (Record, error)
	GetByOriginalIntent(context.Context, string, string, string) (Record, error)
}

type Issuer struct {
	signer DigestSigner
	clock  func() time.Time
}

// Grant IDs are derived from the authoritative issue request so concurrent
// retries ask the HSM to sign one identical digest.
func NewIssuer(signer DigestSigner, clock func() time.Time) (*Issuer, error) {
	if signer == nil {
		return nil, ErrSignerUnavailable
	}
	if clock == nil {
		clock = time.Now
	}
	return &Issuer{signer: signer, clock: clock}, nil
}

func (i *Issuer) Issue(ctx context.Context, request IssueRequest) (SignedGrant, error) {
	request, err := normalizeIssueRequest(request)
	if err != nil {
		return SignedGrant{}, err
	}
	grantID, err := grantIDForRequest(request)
	if err != nil {
		return SignedGrant{}, err
	}
	now := i.clock().UTC().Truncate(time.Second)
	if request.IssuedAt > now.Add(time.Minute).Unix() || request.IssuedAt+int64(MaximumLifetime/time.Second) <= now.Unix() {
		return SignedGrant{}, ErrGrantScope
	}
	grant := Grant{
		Protocol: Protocol, GrantID: grantID, OriginalIntentID: request.OriginalIntentID,
		OrganizationID: request.OrganizationID, AgentID: request.AgentID, TaskID: request.TaskID,
		AllowedCategory: request.AllowedCategory,
		MaxAmountAtomic: request.MaxAmountAtomic, AllowedSellerSet: append([]string(nil), request.AllowedSellerSet...),
		RemainingAttempts: RequiredAttempts, IssuedAt: request.IssuedAt, ExpiresAt: request.IssuedAt + int64(MaximumLifetime/time.Second),
	}
	sort.Strings(grant.AllowedSellerSet)
	if err := Validate(grant); err != nil {
		return SignedGrant{}, err
	}
	digest, err := Digest(grant)
	if err != nil {
		return SignedGrant{}, err
	}
	signature, err := i.signer.SignDigest(ctx, digest)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: %v", ErrSignerUnavailable, err)
	}
	defer clear(signature)
	if len(signature) != crypto.SignatureLength {
		return SignedGrant{}, ErrInvalidGrant
	}
	return SignedGrant{Grant: grant, Signature: "0x" + hex.EncodeToString(signature)}, nil
}

func Verify(signed SignedGrant, expectedSigner string, now time.Time, use Use) error {
	return verify(signed, expectedSigner, now, use, true)
}

// VerifyReplay authenticates immutable grant bytes and intake scope without
// reapplying expiry. It is only for an exact idempotency result already
// committed with this grant digest.
func VerifyReplay(signed SignedGrant, expectedSigner string, use Use) error {
	return verify(signed, expectedSigner, time.Time{}, use, false)
}

func verify(signed SignedGrant, expectedSigner string, now time.Time, use Use, enforceTime bool) error {
	if err := Validate(signed.Grant); err != nil || !canonicalSigner(expectedSigner) {
		return ErrInvalidGrant
	}
	digest, err := Digest(signed.Grant)
	if err != nil {
		return ErrInvalidGrant
	}
	signature, err := decodeSignature(signed.Signature)
	if err != nil {
		return ErrInvalidGrant
	}
	defer clear(signature)
	publicKey, err := crypto.SigToPub(digest, signature)
	if err != nil || strings.ToLower(crypto.PubkeyToAddress(*publicKey).Hex()) != expectedSigner {
		return ErrInvalidGrant
	}
	grant := signed.Grant
	if enforceTime {
		current := now.UTC().Unix()
		if current < grant.IssuedAt-60 || current >= grant.ExpiresAt {
			return ErrGrantScope
		}
	}
	if grant.OrganizationID != use.OrganizationID || grant.AgentID != use.AgentID || grant.TaskID != use.TaskID ||
		grant.AllowedCategory != strings.ToLower(strings.TrimSpace(use.Category)) || !amountAtMost(use.AmountAtomic, grant.MaxAmountAtomic) ||
		!contains(grant.AllowedSellerSet, use.SellerID) {
		return ErrGrantScope
	}
	return nil
}

func DigestHex(grant Grant) (string, error) {
	digest, err := Digest(grant)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(digest), nil
}

func IssueRequestHash(reason ReasonClass, grant Grant) (string, error) {
	if err := Validate(grant); err != nil {
		return "", err
	}
	return CanonicalIssueRequestHash(IssueRequest{
		ReasonClass: reason, OriginalIntentID: grant.OriginalIntentID,
		OrganizationID: grant.OrganizationID, AgentID: grant.AgentID, TaskID: grant.TaskID,
		AllowedCategory: grant.AllowedCategory, MaxAmountAtomic: grant.MaxAmountAtomic,
		AllowedSellerSet: grant.AllowedSellerSet, IssuedAt: grant.IssuedAt,
	})
}

func CanonicalIssueRequestHash(request IssueRequest) (string, error) {
	request, err := normalizeIssueRequest(request)
	if err != nil {
		return "", err
	}
	input := struct {
		Protocol          string      `json:"protocol"`
		ReasonClass       ReasonClass `json:"reasonClass"`
		OriginalIntentID  string      `json:"originalIntentId"`
		OrganizationID    string      `json:"organizationId"`
		AgentID           string      `json:"agentId"`
		TaskID            string      `json:"taskId"`
		AllowedCategory   string      `json:"allowedCategory"`
		MaxAmountAtomic   string      `json:"maxAmountAtomic"`
		AllowedSellerSet  []string    `json:"allowedSellerSet"`
		RemainingAttempts uint8       `json:"remainingAttempts"`
		IssuedAt          int64       `json:"issuedAt"`
	}{Protocol, request.ReasonClass, request.OriginalIntentID, request.OrganizationID, request.AgentID, request.TaskID, request.AllowedCategory, request.MaxAmountAtomic, request.AllowedSellerSet, RequiredAttempts, request.IssuedAt}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(crypto.Keccak256(append([]byte("ASCP_GRANT_ISSUE_V1\n"), encoded...))), nil
}

func normalizeIssueRequest(request IssueRequest) (IssueRequest, error) {
	if request.ReasonClass != ReasonTooExpensive && request.ReasonClass != ReasonWrongSeller {
		return IssueRequest{}, ErrReasonIneligible
	}
	request.AllowedCategory = strings.ToLower(strings.TrimSpace(request.AllowedCategory))
	request.AllowedSellerSet = append([]string(nil), request.AllowedSellerSet...)
	sort.Strings(request.AllowedSellerSet)
	if !hash(request.OriginalIntentID) || !identifier(request.OrganizationID) || !identifier(request.AgentID) || !identifier(request.TaskID) ||
		request.AllowedCategory == "" || !positiveAtomic(request.MaxAmountAtomic) || request.IssuedAt <= 0 || len(request.AllowedSellerSet) == 0 || len(request.AllowedSellerSet) > maximumSellerCount {
		return IssueRequest{}, ErrInvalidGrant
	}
	previous := ""
	for _, sellerID := range request.AllowedSellerSet {
		if !hash(sellerID) || sellerID <= previous {
			return IssueRequest{}, ErrInvalidGrant
		}
		previous = sellerID
	}
	return request, nil
}

func ValidateRecord(record Record) error {
	digest, err := DigestHex(record.Artifact.Grant)
	if err != nil || digest != record.Digest {
		return ErrInvalidGrant
	}
	requestHash, err := IssueRequestHash(record.ReasonClass, record.Artifact.Grant)
	if err != nil || requestHash != record.CanonicalRequestHash {
		return ErrInvalidGrant
	}
	signature, err := decodeSignature(record.Artifact.Signature)
	if err != nil {
		return ErrInvalidGrant
	}
	clear(signature)
	if record.ConsumedOperationID != "" && !hash(record.ConsumedOperationID) {
		return ErrInvalidGrant
	}
	return nil
}

func Validate(grant Grant) error {
	if grant.Protocol != Protocol || !hash(grant.GrantID) || !hash(grant.OriginalIntentID) ||
		!identifier(grant.OrganizationID) || !identifier(grant.AgentID) || !identifier(grant.TaskID) ||
		grant.AllowedCategory == "" || grant.AllowedCategory != strings.ToLower(strings.TrimSpace(grant.AllowedCategory)) ||
		!positiveAtomic(grant.MaxAmountAtomic) || grant.RemainingAttempts != RequiredAttempts ||
		grant.IssuedAt <= 0 || grant.ExpiresAt <= grant.IssuedAt || time.Duration(grant.ExpiresAt-grant.IssuedAt)*time.Second > MaximumLifetime ||
		len(grant.AllowedSellerSet) == 0 || len(grant.AllowedSellerSet) > maximumSellerCount {
		return ErrInvalidGrant
	}
	previous := ""
	for _, sellerID := range grant.AllowedSellerSet {
		if !hash(sellerID) || sellerID <= previous {
			return ErrInvalidGrant
		}
		previous = sellerID
	}
	return nil
}

func Digest(grant Grant) ([]byte, error) {
	if err := Validate(grant); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return nil, err
	}
	digest := crypto.Keccak256(append([]byte(Protocol+"\n"), encoded...))
	return digest, nil
}

func grantIDForRequest(request IssueRequest) (string, error) {
	requestHash, err := CanonicalIssueRequestHash(request)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(crypto.Keccak256([]byte("ASCP_GRANT_ID_V1\n"+requestHash))), nil
}

func decodeSignature(value string) ([]byte, error) {
	if len(value) != 2+crypto.SignatureLength*2 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return nil, ErrInvalidGrant
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != crypto.SignatureLength || decoded[64] > 1 {
		return nil, ErrInvalidGrant
	}
	if !crypto.ValidateSignatureValues(decoded[64], new(big.Int).SetBytes(decoded[:32]), new(big.Int).SetBytes(decoded[32:64]), true) {
		return nil, ErrInvalidGrant
	}
	return decoded, nil
}

func canonicalSigner(value string) bool {
	return len(value) == 42 && value == strings.ToLower(value) && common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func amountAtMost(value, maximum string) bool {
	amount, ok := new(big.Int).SetString(value, 10)
	limit, limitOK := new(big.Int).SetString(maximum, 10)
	return ok && limitOK && amount.Sign() > 0 && value == amount.String() && amount.Cmp(limit) <= 0
}

func positiveAtomic(value string) bool { return amountAtMost(value, value) }

func contains(sorted []string, value string) bool {
	index := sort.SearchStrings(sorted, value)
	return index < len(sorted) && sorted[index] == value
}

func identifier(value string) bool { return envelope.ValidIdentifier(value) && len(value) <= 128 }

func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32 && new(big.Int).SetBytes(decoded).Sign() > 0
}
