package ascpring6

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

const Protocol = "ASCP_SIGNER_DEPENDENCY_V1"

var (
	ErrBinding        = errors.New("Ring 6 action binding mismatch")
	ErrRefused        = errors.New("Ring 6 permanently refused signing request")
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type PermanentRefusal struct{ Code string }

func (e *PermanentRefusal) Error() string { return "Ring 6 verifier refused: " + e.Code }
func (e *PermanentRefusal) Unwrap() error { return ErrRefused }

type IndependentVerifier interface {
	Verify(context.Context, ascpbearer.ActivationInput, string) error
}

type HSMRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	KeyID          string `json:"keyId"`
	KeyEpoch       uint64 `json:"keyEpoch"`
	Digest         string `json:"digest"`
}

type HSMResult struct {
	OperationHandle string `json:"operationHandle"`
	Digest          string `json:"digest"`
	Signature       []byte `json:"signature"`
}

type HSM interface {
	Sign(context.Context, HSMRequest) (HSMResult, error)
}

type ActionBinding struct {
	OperationID     string `json:"operationId"`
	ActionID        string `json:"actionId"`
	InputHash       string `json:"inputHash"`
	Digest          string `json:"digest"`
	KeyID           string `json:"keyId"`
	KeyEpoch        uint64 `json:"keyEpoch"`
	IdempotencyKey  string `json:"idempotencyKey"`
	State           string `json:"state"`
	OperationHandle string `json:"operationHandle,omitempty"`
	RefusalCode     string `json:"refusalCode,omitempty"`
}

type BindingStore interface {
	Bind(context.Context, ActionBinding) (ActionBinding, bool, error)
	MarkHSMRequested(context.Context, ActionBinding) (ActionBinding, error)
	MarkSigned(context.Context, ActionBinding, string) (ActionBinding, error)
	MarkRefused(context.Context, ActionBinding, string) (ActionBinding, error)
}

type Config struct {
	Store         BindingStore
	Verifier      IndependentVerifier
	HSM           HSM
	Clock         func() time.Time
	KeyID         string
	KeyEpoch      uint64
	KeeperID      string
	SignerAddress string
}

type Service struct {
	store           BindingStore
	verifier        IndependentVerifier
	hsm             HSM
	clock           func() time.Time
	keyID, keeperID string
	keyEpoch        uint64
	signer          common.Address
	actionsMu       sync.Mutex
	actionLocks     map[string]*serviceActionLock
}

type serviceActionLock struct {
	mu   sync.Mutex
	refs int
}

func New(config Config) (*Service, error) {
	if config.Store == nil || config.Verifier == nil || config.HSM == nil ||
		!identifierPattern.MatchString(config.KeyID) || !identifierPattern.MatchString(config.KeeperID) || config.KeyEpoch == 0 ||
		!common.IsHexAddress(config.SignerAddress) || config.SignerAddress != strings.ToLower(config.SignerAddress) ||
		common.HexToAddress(config.SignerAddress) == (common.Address{}) {
		return nil, errors.New("Ring 6 configuration is invalid")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Service{store: config.Store, verifier: config.Verifier, hsm: config.HSM, clock: config.Clock,
		keyID: config.KeyID, keyEpoch: config.KeyEpoch, keeperID: config.KeeperID, signer: common.HexToAddress(config.SignerAddress),
		actionLocks: make(map[string]*serviceActionLock)}, nil
}

func (s *Service) VerifyAndSign(ctx context.Context, input ascpbearer.ActivationInput) ([]byte, error) {
	input.CanonicalPayload = append([]byte(nil), input.CanonicalPayload...)
	input.EvidenceBundle = append([]byte(nil), input.EvidenceBundle...)
	defer clear(input.CanonicalPayload)
	defer clear(input.EvidenceBundle)
	input.ValidAfter, input.ValidUntil = input.ValidAfter.UTC(), input.ValidUntil.UTC()
	if input.SignerKeyID != s.keyID || input.KeyEpoch != s.keyEpoch || input.KeeperID != s.keeperID {
		return nil, ErrBinding
	}
	if err := ascpbearer.ValidateActivationInput(input, s.clock().UTC()); err != nil {
		return nil, err
	}
	inputHash, err := ascpbearer.ActivationInputHash(input)
	if err != nil {
		return nil, err
	}
	binding := ActionBinding{OperationID: input.OperationID, ActionID: input.ActionID, InputHash: inputHash,
		Digest: input.Digest, KeyID: input.SignerKeyID, KeyEpoch: input.KeyEpoch,
		IdempotencyKey: hsmIdempotencyKey(input.OperationID, input.ActionID, inputHash), State: "BOUND"}
	unlock := s.lockAction(input.OperationID, input.ActionID)
	defer unlock()
	stored, _, err := s.store.Bind(ctx, binding)
	if err != nil {
		return nil, err
	}
	if !sameBinding(stored, binding) {
		return nil, ErrBinding
	}
	if stored.State == "REFUSED" {
		return nil, &PermanentRefusal{Code: stored.RefusalCode}
	}
	if err := s.verifier.Verify(ctx, input, inputHash); err != nil {
		var refusal *PermanentRefusal
		if errors.As(err, &refusal) {
			if stored.State != "BOUND" {
				return nil, fmt.Errorf("Ring 6 verifier refused after HSM request: %s", refusal.Code)
			}
			if _, markErr := s.store.MarkRefused(ctx, binding, refusal.Code); markErr != nil {
				return nil, fmt.Errorf("persist Ring 6 verifier refusal: %w", markErr)
			}
		}
		return nil, err
	}
	if stored.State == "BOUND" {
		stored, err = s.store.MarkHSMRequested(ctx, binding)
		if err != nil {
			return nil, err
		}
	}
	if stored.State != "HSM_REQUESTED" && stored.State != "SIGNED" {
		return nil, ErrBinding
	}
	result, err := s.hsm.Sign(ctx, HSMRequest{IdempotencyKey: binding.IdempotencyKey, KeyID: binding.KeyID, KeyEpoch: binding.KeyEpoch, Digest: binding.Digest})
	if err != nil {
		return nil, err
	}
	defer clear(result.Signature)
	if result.Digest != binding.Digest || !identifierPattern.MatchString(result.OperationHandle) || !validSignature(result.Signature, binding.Digest, s.signer) {
		return nil, ErrBinding
	}
	if stored.State == "SIGNED" && stored.OperationHandle != result.OperationHandle {
		return nil, ErrBinding
	}
	if _, err := s.store.MarkSigned(ctx, binding, result.OperationHandle); err != nil {
		return nil, err
	}
	return append([]byte(nil), result.Signature...), nil
}

func (s *Service) lockAction(operationID, actionID string) func() {
	key := actionKey(operationID, actionID)
	s.actionsMu.Lock()
	lock := s.actionLocks[key]
	if lock == nil {
		lock = &serviceActionLock{}
		s.actionLocks[key] = lock
	}
	lock.refs++
	s.actionsMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.actionsMu.Lock()
		lock.refs--
		if lock.refs == 0 && s.actionLocks[key] == lock {
			delete(s.actionLocks, key)
		}
		s.actionsMu.Unlock()
	}
}

func hsmIdempotencyKey(operationID, actionID, inputHash string) string {
	digest := sha256.Sum256([]byte("ASCP_RING6_HSM_OPERATION_V1\n" + operationID + "\n" + actionID + "\n" + inputHash))
	return "0x" + hex.EncodeToString(digest[:])
}

func sameBinding(a, b ActionBinding) bool {
	return a.OperationID == b.OperationID && a.ActionID == b.ActionID && a.InputHash == b.InputHash && a.Digest == b.Digest &&
		a.KeyID == b.KeyID && a.KeyEpoch == b.KeyEpoch && a.IdempotencyKey == b.IdempotencyKey
}

func validSignature(signature []byte, digest string, signer common.Address) bool {
	if len(signature) != 65 || signature[64] > 1 ||
		!crypto.ValidateSignatureValues(signature[64], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:64]), true) {
		return false
	}
	publicKey, err := crypto.SigToPub(common.HexToHash(digest).Bytes(), signature)
	return err == nil && crypto.PubkeyToAddress(*publicKey) == signer
}

func validateBinding(binding ActionBinding) error {
	if !hash(binding.OperationID) || !identifierPattern.MatchString(binding.ActionID) || !hash(binding.InputHash) ||
		!hash(binding.Digest) || !identifierPattern.MatchString(binding.KeyID) || binding.KeyEpoch == 0 ||
		!hash(binding.IdempotencyKey) || binding.State != "BOUND" && binding.State != "HSM_REQUESTED" && binding.State != "SIGNED" && binding.State != "REFUSED" ||
		binding.State == "SIGNED" && !identifierPattern.MatchString(binding.OperationHandle) ||
		binding.State == "REFUSED" && !identifierPattern.MatchString(binding.RefusalCode) ||
		binding.State != "SIGNED" && binding.OperationHandle != "" || binding.State != "REFUSED" && binding.RefusalCode != "" {
		return errors.New("Ring 6 binding record is invalid")
	}
	return nil
}

func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32 && common.BytesToHash(decoded) != (common.Hash{})
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
