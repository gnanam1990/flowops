package ascpkeeper

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var claimExpiredSelector = crypto.Keccak256([]byte("claimExpired(bytes32)"))[:4]

type ExpiredCall struct {
	OperationID       string
	OrganizationID    string
	ChainID           uint64
	Escrow            string
	CallID            string
	SettleBy          time.Time
	ObservedChainTime time.Time
	ObservedAt        time.Time
	EvidenceDigest    string
	Providers         []string
}

// ExpirySource must derive ObservedChainTime from independent confirmed Base
// headers and return only calls still non-terminal on that canonical view.
type ExpirySource interface {
	Eligible(context.Context, int) ([]ExpiredCall, error)
}

type ExpiryScanner struct {
	store              Store
	source             ExpirySource
	keeperID, gasPayer string
	clock              func() time.Time
}

func NewExpiryScanner(store Store, source ExpirySource, keeperID, gasPayer string, clocks ...func() time.Time) (*ExpiryScanner, error) {
	if store == nil || source == nil || !identifier(keeperID) || !address(gasPayer) || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfig
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &ExpiryScanner{store: store, source: source, keeperID: keeperID, gasPayer: gasPayer, clock: clock}, nil
}

func (s *ExpiryScanner) Scan(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrInvalidConfig
	}
	calls, err := s.source.Eligible(ctx, limit)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, call := range calls {
		if err := validateExpiredCall(call, s.clock().UTC()); err != nil {
			return created, err
		}
		payload := claimExpiredCalldata(call.CallID)
		input := EnqueueInput{JobID: expiredJobID(call), OperationID: call.OperationID, OrganizationID: call.OrganizationID,
			Action: ActionClaimExpired, ChainID: call.ChainID, KeeperID: s.keeperID, GasPayer: s.gasPayer, Target: call.Escrow,
			ValueWei: "0", CanonicalPayload: payload, CanonicalPayloadHash: canonicalPayloadHash(payload), EligibleAfter: call.SettleBy.Add(time.Second),
			EligibilityEvidenceDigest: call.EvidenceDigest, EligibilityObservedAt: call.ObservedAt}
		_, replay, err := s.store.Enqueue(ctx, input)
		if err != nil {
			return created, err
		}
		if !replay {
			created++
		}
	}
	return created, nil
}

func validateExpiredCall(call ExpiredCall, now time.Time) error {
	if !hash(call.OperationID) || !identifier(call.OrganizationID) || (call.ChainID != 8453 && call.ChainID != 84532) ||
		!address(call.Escrow) || !hash(call.CallID) || call.SettleBy.IsZero() || call.ObservedChainTime.IsZero() ||
		!call.ObservedChainTime.After(call.SettleBy) || !hash(call.EvidenceDigest) || call.ObservedAt.IsZero() ||
		call.ObservedAt.After(now.Add(time.Minute)) || now.Sub(call.ObservedAt) > time.Minute {
		return ErrInvalidJob
	}
	seen := map[string]struct{}{}
	if len(call.Providers) < 2 || len(call.Providers) > 5 {
		return ErrInvalidJob
	}
	for _, provider := range call.Providers {
		if !identifier(provider) {
			return ErrInvalidJob
		}
		seen[provider] = struct{}{}
	}
	if len(seen) != len(call.Providers) {
		return ErrInvalidJob
	}
	return nil
}

func claimExpiredCalldata(callID string) []byte {
	result := make([]byte, 4+32)
	copy(result, claimExpiredSelector)
	copy(result[4:], common.HexToHash(callID).Bytes())
	return result
}

func expiredJobID(call ExpiredCall) string {
	buffer := make([]byte, 8)
	binary.BigEndian.PutUint64(buffer, call.ChainID)
	return crypto.Keccak256Hash([]byte("ASCP_KEEPER_CLAIM_EXPIRED_V1"), buffer, common.HexToHash(call.OperationID).Bytes(), common.HexToHash(call.CallID).Bytes(), common.HexToAddress(call.Escrow).Bytes()).Hex()
}

// ClaimExpiredDecoder is the concrete permissionless-call side of the exact
// binding verifier. Signature-bearing selectors use their own ABI decoders.
type ClaimExpiredDecoder struct{}

func (ClaimExpiredDecoder) Decode(_ context.Context, action Action, data []byte) (DecodedCall, error) {
	if action != ActionClaimExpired || len(data) != 36 || !bytes.Equal(data[:4], claimExpiredSelector) || common.BytesToHash(data[4:]) == (common.Hash{}) {
		return DecodedCall{}, errors.Join(ErrInvalidTransaction, errors.New("invalid claimExpired calldata"))
	}
	return DecodedCall{Action: action, CanonicalPayload: append([]byte(nil), data...)}, nil
}
