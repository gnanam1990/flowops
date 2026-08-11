// Package referencesigner provides the customer-run enforcement boundary for
// FlowOps authorization envelopes. It verifies but never stores private keys and
// does not implement broadcasting; wallet adapters consume successful results.
package referencesigner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

type RefusalCode string

const (
	RefusalUnknownTrustKey RefusalCode = "UNKNOWN_TRUST_KEY"
	RefusalBadSignature    RefusalCode = "BAD_SIGNATURE"
	RefusalNotYetValid     RefusalCode = "NOT_YET_VALID"
	RefusalExpired         RefusalCode = "EXPIRED"
	RefusalTTLTooLong      RefusalCode = "TTL_TOO_LONG"
	RefusalChain           RefusalCode = "CHAIN_NOT_ALLOWED"
	RefusalRail            RefusalCode = "RAIL_NOT_ALLOWED"
	RefusalAsset           RefusalCode = "ASSET_NOT_ALLOWED"
	RefusalRecipient       RefusalCode = "RECIPIENT_NOT_ALLOWED"
	RefusalAmount          RefusalCode = "AMOUNT_EXCEEDS_LOCAL_CAP"
	RefusalChainUnhealthy  RefusalCode = "CHAIN_UNHEALTHY"
	RefusalFrozen          RefusalCode = "FROZEN"
	RefusalReplay          RefusalCode = "NONCE_ALREADY_CLAIMED"
	RefusalNonceStore      RefusalCode = "NONCE_STORE_FAILURE"
)

type Refusal struct {
	Code RefusalCode
	Err  error
}

func (r *Refusal) Error() string { return string(r.Code) + ": " + r.Err.Error() }
func (r *Refusal) Unwrap() error { return r.Err }

type ChainGate interface {
	CheckChain(ctx context.Context, chainID uint64) error
}

type authorizationChainGate interface {
	CheckAuthorizationChain(ctx context.Context, authorization envelope.Authorization) error
}

type FreezeGate interface {
	CheckFrozen(ctx context.Context, authorization envelope.Authorization) error
}

type Config struct {
	TrustKeys         map[string]ed25519.PublicKey
	AllowedChainIDs   []uint64
	AllowedRails      []envelope.Rail
	AllowedAssets     []string
	AllowedRecipients []string
	MaxAmountAtomic   string
	MaxTTL            time.Duration
	MaxFutureSkew     time.Duration
	Clock             func() time.Time
	ChainGate         ChainGate
	FreezeGate        FreezeGate
	Nonces            NonceStore
}

type Verifier struct {
	trustKeys     map[string]ed25519.PublicKey
	chains        map[uint64]struct{}
	rails         map[envelope.Rail]struct{}
	assets        map[string]struct{}
	recipients    map[string]struct{}
	maxAmount     *big.Int
	maxTTL        time.Duration
	maxFutureSkew time.Duration
	clock         func() time.Time
	chainGate     ChainGate
	freezeGate    FreezeGate
	nonces        NonceStore
}

type Authorized struct {
	Authorization envelope.Authorization `json:"authorization"`
	Digest        string                 `json:"digest"`
	KeyID         string                 `json:"keyId"`
	ClaimedAt     int64                  `json:"claimedAt"`
}

func New(cfg Config) (*Verifier, error) {
	if len(cfg.TrustKeys) == 0 {
		return nil, errors.New("at least one FlowOps trust key is required")
	}
	if cfg.ChainGate == nil || cfg.FreezeGate == nil || cfg.Nonces == nil {
		return nil, errors.New("chain gate, freeze gate, and nonce store are required")
	}
	if cfg.MaxTTL <= 0 {
		return nil, errors.New("max TTL must be positive")
	}
	if cfg.MaxFutureSkew < 0 {
		return nil, errors.New("max future skew cannot be negative")
	}
	maxAmount, err := parsePositive(cfg.MaxAmountAtomic)
	if err != nil {
		return nil, fmt.Errorf("max amount: %w", err)
	}
	v := &Verifier{
		trustKeys:     make(map[string]ed25519.PublicKey, len(cfg.TrustKeys)),
		chains:        make(map[uint64]struct{}, len(cfg.AllowedChainIDs)),
		rails:         make(map[envelope.Rail]struct{}, len(cfg.AllowedRails)),
		assets:        make(map[string]struct{}, len(cfg.AllowedAssets)),
		recipients:    make(map[string]struct{}, len(cfg.AllowedRecipients)),
		maxAmount:     maxAmount,
		maxTTL:        cfg.MaxTTL,
		maxFutureSkew: cfg.MaxFutureSkew,
		clock:         cfg.Clock,
		chainGate:     cfg.ChainGate,
		freezeGate:    cfg.FreezeGate,
		nonces:        cfg.Nonces,
	}
	if v.clock == nil {
		v.clock = time.Now
	}
	for keyID, key := range cfg.TrustKeys {
		if strings.TrimSpace(keyID) == "" || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("trust key %q is invalid", keyID)
		}
		v.trustKeys[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	for _, chainID := range cfg.AllowedChainIDs {
		if chainID == 0 {
			return nil, errors.New("allowed chain ID cannot be zero")
		}
		v.chains[chainID] = struct{}{}
	}
	for _, rail := range cfg.AllowedRails {
		v.rails[rail] = struct{}{}
	}
	for _, asset := range cfg.AllowedAssets {
		if normalized, err := canonicalAddress(asset); err != nil {
			return nil, fmt.Errorf("allowed asset: %w", err)
		} else {
			v.assets[normalized] = struct{}{}
		}
	}
	for _, recipient := range cfg.AllowedRecipients {
		if normalized, err := canonicalAddress(recipient); err != nil {
			return nil, fmt.Errorf("allowed recipient: %w", err)
		} else {
			v.recipients[normalized] = struct{}{}
		}
	}
	if len(v.chains) == 0 || len(v.rails) == 0 || len(v.assets) == 0 || len(v.recipients) == 0 {
		return nil, errors.New("signer must allow at least one chain, rail, asset, and recipient")
	}
	return v, nil
}

func (v *Verifier) Authorize(ctx context.Context, signed envelope.SignedAuthorization) (Authorized, error) {
	publicKey, ok := v.trustKeys[signed.KeyID]
	if !ok {
		return Authorized{}, refuse(RefusalUnknownTrustKey, errors.New("authorization key is not trusted"))
	}
	if err := envelope.Verify(signed, publicKey); err != nil {
		return Authorized{}, refuse(RefusalBadSignature, err)
	}
	a := signed.Authorization
	now := v.clock().UTC()
	issuedAt := time.Unix(a.IssuedAt, 0)
	expiresAt := time.Unix(a.ExpiresAt, 0)
	if issuedAt.After(now.Add(v.maxFutureSkew)) {
		return Authorized{}, refuse(RefusalNotYetValid, errors.New("authorization issued in the future"))
	}
	if !now.Before(expiresAt) {
		return Authorized{}, refuse(RefusalExpired, errors.New("authorization window elapsed"))
	}
	if expiresAt.Sub(issuedAt) > v.maxTTL {
		return Authorized{}, refuse(RefusalTTLTooLong, errors.New("authorization window exceeds local maximum"))
	}
	if _, ok := v.chains[a.ChainID]; !ok {
		return Authorized{}, refuse(RefusalChain, errors.New("chain is not locally allowed"))
	}
	if _, ok := v.rails[a.Rail]; !ok {
		return Authorized{}, refuse(RefusalRail, errors.New("rail is not locally allowed"))
	}
	if _, ok := v.assets[a.Asset]; !ok {
		return Authorized{}, refuse(RefusalAsset, errors.New("asset is not locally allowed"))
	}
	if _, ok := v.recipients[a.Recipient]; !ok {
		return Authorized{}, refuse(RefusalRecipient, errors.New("recipient is not locally allowed"))
	}
	amount, err := parsePositive(a.AmountAtomic)
	if err != nil || amount.Cmp(v.maxAmount) > 0 {
		return Authorized{}, refuse(RefusalAmount, errors.New("amount exceeds local cap"))
	}
	var chainErr error
	if strictGate, ok := v.chainGate.(authorizationChainGate); ok {
		chainErr = strictGate.CheckAuthorizationChain(ctx, a)
	} else {
		chainErr = v.chainGate.CheckChain(ctx, a.ChainID)
	}
	if chainErr != nil {
		return Authorized{}, refuse(RefusalChainUnhealthy, chainErr)
	}
	if err := v.freezeGate.CheckFrozen(ctx, a); err != nil {
		return Authorized{}, refuse(RefusalFrozen, err)
	}
	digest, err := a.Digest()
	if err != nil {
		return Authorized{}, refuse(RefusalBadSignature, err)
	}
	nonceKey := claimKey(signed.KeyID, a)
	if err := v.nonces.Claim(ctx, nonceKey, now); err != nil {
		if errors.Is(err, ErrNonceAlreadyClaimed) {
			return Authorized{}, refuse(RefusalReplay, err)
		}
		return Authorized{}, refuse(RefusalNonceStore, err)
	}
	return Authorized{
		Authorization: a,
		Digest:        "0x" + hex.EncodeToString(digest[:]),
		KeyID:         signed.KeyID,
		ClaimedAt:     now.Unix(),
	}, nil
}

func claimKey(keyID string, a envelope.Authorization) string {
	sum := sha256.Sum256([]byte(keyID + "\x00" + a.OrganizationID + "\x00" + a.CustomerID + "\x00" + a.Nonce))
	return hex.EncodeToString(sum[:])
}

func refuse(code RefusalCode, err error) error { return &Refusal{Code: code, Err: err} }

func parsePositive(value string) (*big.Int, error) {
	if value == "" || value[0] == '0' {
		return nil, errors.New("must be a canonical positive integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, errors.New("must contain decimal digits only")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 256 {
		return nil, errors.New("must fit uint256")
	}
	return n, nil
}

func canonicalAddress(value string) (string, error) {
	normalized, err := envelope.NormalizeAddress(value)
	if err != nil {
		return "", err
	}
	if normalized != value {
		return "", errors.New("address is not canonical lowercase")
	}
	return normalized, nil
}

func RefusalIs(err error, code RefusalCode) bool {
	var refusal *Refusal
	return errors.As(err, &refusal) && refusal.Code == code
}
