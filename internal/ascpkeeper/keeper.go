// Package ascpkeeper implements the durable, independently funded relay that
// is the only service allowed to turn an activated ASCP bearer into a chain
// transaction. It deliberately does not decide policy or infer settlement.
package ascpkeeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type Action string

const (
	ActionLock         Action = "LOCK"
	ActionRelease      Action = "RELEASE"
	ActionRefund       Action = "REFUND"
	ActionClaimExpired Action = "CLAIM_EXPIRED"
	ActionAdmin        Action = "ADMIN"
)

type State string

const (
	StateQueued       State = "QUEUED"
	StatePrepared     State = "PREPARED"
	StateBroadcasting State = "BROADCASTING"
	StateSubmitted    State = "SUBMITTED"
	StateConfirmed    State = "CONFIRMED"
	StateFinalized    State = "FINALIZED"
	StateReverted     State = "REVERTED"
	StateReorged      State = "REORGED"
	StateTimedOut     State = "TIMED_OUT"
	StateAmbiguous    State = "AMBIGUOUS"
	StateDeadLetter   State = "DEAD_LETTER"
)

type AttemptState string

const (
	AttemptPrepared     AttemptState = "PREPARED"
	AttemptBroadcasting AttemptState = "BROADCASTING"
	AttemptSubmitted    AttemptState = "SUBMITTED"
	AttemptConfirmed    AttemptState = "CONFIRMED"
	AttemptReplaced     AttemptState = "REPLACED"
	AttemptAmbiguous    AttemptState = "AMBIGUOUS"
	AttemptRejected     AttemptState = "REJECTED"
	AttemptReverted     AttemptState = "REVERTED"
	AttemptReorged      AttemptState = "REORGED"
	AttemptFinalized    AttemptState = "FINALIZED"
)

var (
	ErrInvalidConfig        = errors.New("invalid keeper configuration")
	ErrInvalidJob           = errors.New("invalid keeper job")
	ErrInvalidTransaction   = errors.New("invalid keeper transaction")
	ErrLeaseLost            = errors.New("keeper lease lost")
	ErrNotFound             = errors.New("keeper job not found")
	ErrNoWork               = errors.New("no keeper work available")
	ErrStateConflict        = errors.New("keeper state conflict")
	ErrSignatureUnavailable = errors.New("activated signature unavailable")
	ErrBroadcastAmbiguous   = errors.New("keeper broadcast outcome is ambiguous")
	ErrBroadcastUnderpriced = errors.New("keeper replacement fee is underpriced")
	ErrBroadcastRejected    = errors.New("keeper broadcast was deterministically rejected")
	ErrUnsafeReplacement    = errors.New("keeper replacement is not proven safe")
	ErrFeeBumpsExhausted    = errors.New("keeper fee bump limit exhausted")
)

type Job struct {
	JobID                     string    `json:"jobId"`
	OperationID               string    `json:"operationId"`
	OrganizationID            string    `json:"organizationId"`
	Action                    Action    `json:"action"`
	ChainID                   uint64    `json:"chainId"`
	KeeperID                  string    `json:"keeperId"`
	GasPayer                  string    `json:"gasPayer"`
	Target                    string    `json:"target"`
	ValueWei                  string    `json:"valueWei"`
	CanonicalPayload          []byte    `json:"canonicalPayload"`
	CanonicalPayloadHash      string    `json:"canonicalPayloadHash"`
	AuthorizationDigest       string    `json:"authorizationDigest,omitempty"`
	SignerHandle              string    `json:"signerHandle,omitempty"`
	SignerAddress             string    `json:"signerAddress,omitempty"`
	ValidAfter                time.Time `json:"validAfter"`
	ValidBefore               time.Time `json:"validBefore"`
	EligibleAfter             time.Time `json:"eligibleAfter"`
	EligibilityEvidenceDigest string    `json:"eligibilityEvidenceDigest,omitempty"`
	EligibilityObservedAt     time.Time `json:"eligibilityObservedAt,omitempty"`
	LeadershipEpoch           uint64    `json:"leadershipEpoch"`
	State                     State     `json:"state"`
	LeaseOwner                string    `json:"leaseOwner,omitempty"`
	LeaseToken                string    `json:"leaseToken,omitempty"`
	LeaseExpiresAt            time.Time `json:"leaseExpiresAt,omitempty"`
	AttemptCount              int       `json:"attemptCount"`
	CurrentAttempt            int       `json:"currentAttempt,omitempty"`
	CreatedAt                 time.Time `json:"createdAt"`
	UpdatedAt                 time.Time `json:"updatedAt"`
	LastError                 string    `json:"lastError,omitempty"`
}

// RequiresBearer distinguishes signature-bearing calls from the permissionless
// claimExpired backstop. Every other action must use the activated signer
// channel; accepting signature bytes in EnqueueInput is intentionally impossible.
func (j Job) RequiresBearer() bool { return j.Action != ActionClaimExpired }

type EnqueueInput struct {
	JobID                     string
	OperationID               string
	OrganizationID            string
	Action                    Action
	ChainID                   uint64
	KeeperID                  string
	GasPayer                  string
	Target                    string
	ValueWei                  string
	CanonicalPayload          []byte
	CanonicalPayloadHash      string
	AuthorizationDigest       string
	SignerHandle              string
	SignerAddress             string
	ValidAfter                time.Time
	ValidBefore               time.Time
	EligibleAfter             time.Time
	EligibilityEvidenceDigest string
	EligibilityObservedAt     time.Time
	LeadershipEpoch           uint64
}

type Fee struct {
	MaxFeePerGasWei         string `json:"maxFeePerGasWei"`
	MaxPriorityFeePerGasWei string `json:"maxPriorityFeePerGasWei"`
}

type UnsignedTransaction struct {
	ChainID  uint64 `json:"chainId"`
	From     string `json:"from"`
	To       string `json:"to"`
	ValueWei string `json:"valueWei"`
	Nonce    uint64 `json:"nonce"`
	GasLimit uint64 `json:"gasLimit"`
	Data     []byte `json:"data"`
	Fee      Fee    `json:"fee"`
}

type SignedTransaction struct {
	Hash string
	Raw  []byte
}

type Attempt struct {
	JobID                string       `json:"jobId"`
	Number               int          `json:"number"`
	Nonce                uint64       `json:"nonce"`
	GasPayer             string       `json:"gasPayer"`
	Fee                  Fee          `json:"fee"`
	TransactionHash      string       `json:"transactionHash"`
	SealedRawTransaction []byte       `json:"-"`
	SealingKeyID         string       `json:"sealingKeyId"`
	State                AttemptState `json:"state"`
	PreparedAt           time.Time    `json:"preparedAt"`
	BroadcastAt          time.Time    `json:"broadcastAt,omitempty"`
	LastError            string       `json:"lastError,omitempty"`
	EvidenceDigest       string       `json:"evidenceDigest,omitempty"`
	ObservedAt           time.Time    `json:"observedAt,omitempty"`
}

type Outcome struct {
	JobID           string
	AttemptNumber   int
	TransactionHash string
	State           State
	EvidenceDigest  string
	ObservedAt      time.Time
}

type Lease struct {
	Job   Job
	Token string
}

type Store interface {
	Enqueue(context.Context, EnqueueInput) (Job, bool, error)
	Claim(context.Context, string, time.Duration) (Lease, error)
	ClaimObservation(context.Context, string, time.Duration) (Lease, error)
	AllocateNonce(context.Context, Lease, uint64) (uint64, error)
	RecordPrepared(context.Context, Lease, Attempt) (Job, error)
	RecordReplacement(context.Context, Lease, Attempt, Attempt) (Job, error)
	MarkBroadcasting(context.Context, Lease, int) (Job, error)
	MarkSubmitted(context.Context, Lease, int, string) (Job, error)
	MarkAmbiguous(context.Context, Lease, int, string) (Job, error)
	MarkRejected(context.Context, Lease, int, State, string) (Job, error)
	MarkRecoveryDeadLetter(context.Context, Lease, string) (Job, error)
	ApplyOutcome(context.Context, Lease, Outcome) (Job, error)
	CurrentAttempt(context.Context, string) (Attempt, error)
	ReleaseLease(context.Context, Lease) error
}

// ArtifactSource is the authenticated signer-to-keeper channel. The source is
// responsible for checking the handle state, expiry and exact KeeperID binding.
type ArtifactSource interface {
	Release(context.Context, string, string) ([]byte, error)
}

type Assembler interface {
	Assemble(context.Context, Job, []byte, uint64, Fee) (UnsignedTransaction, error)
}

// BindingVerifier independently decodes the assembled transaction, verifies
// the action-specific authorization signature/digest, and binds every field to
// the durable job. This is intentionally separate from Assembler.
type BindingVerifier interface {
	Verify(context.Context, Job, UnsignedTransaction, []byte) error
}

type Wallet interface {
	Sign(context.Context, UnsignedTransaction) (SignedTransaction, error)
}

type Sealer interface {
	Seal(context.Context, []byte, []byte) (ciphertext []byte, keyID string, err error)
	Open(context.Context, []byte, string, []byte) ([]byte, error)
}

type Broadcaster interface {
	Broadcast(context.Context, []byte) (string, error)
}

type FeePolicy interface {
	Initial(context.Context, Job) (Fee, error)
	Bump(context.Context, Job, Attempt) (Fee, error)
}

// NonceSource returns the quorum-observed pending nonce. Implementations must
// fail closed on provider disagreement; the durable store only moves forward.
type NonceSource interface {
	PendingNonce(context.Context, uint64, string) (uint64, error)
}

// ReplacementGate proves from independent chain observations that a same-nonce
// replacement is safe. It must fail closed if the old transaction may already
// be canonical or the sender nonce has advanced beyond the attempt nonce.
type ReplacementGate interface {
	SafeToReplace(context.Context, Job, Attempt) error
}

// OutcomeSource is an adapter over independently verified settlement evidence.
// It must never treat an uncorroborated RPC status as an outcome.
type OutcomeSource interface {
	Observe(context.Context, Job, Attempt) (Outcome, error)
}

type LeadershipGate interface {
	Current(context.Context, string) (uint64, error)
}

type Config struct {
	KeeperID      string
	GasPayer      string
	LeaseDuration time.Duration
	MaxFeeBumps   int
	MaxGasLimit   uint64
	FeeCap        Fee
	Clock         func() time.Time
}

type Service struct {
	store        Store
	artifacts    ArtifactSource
	assembler    Assembler
	verifier     BindingVerifier
	wallet       Wallet
	sealer       Sealer
	broadcaster  Broadcaster
	fees         FeePolicy
	nonces       NonceSource
	replacements ReplacementGate
	outcomes     OutcomeSource
	leadership   LeadershipGate
	config       Config
}

func NewService(store Store, artifacts ArtifactSource, assembler Assembler, verifier BindingVerifier,
	wallet Wallet, sealer Sealer, broadcaster Broadcaster, fees FeePolicy, nonces NonceSource,
	replacements ReplacementGate, outcomes OutcomeSource, leadership LeadershipGate,
	config Config,
) (*Service, error) {
	if store == nil || artifacts == nil || assembler == nil || verifier == nil || wallet == nil || sealer == nil ||
		broadcaster == nil || fees == nil || nonces == nil || replacements == nil || outcomes == nil || leadership == nil || !identifier(config.KeeperID) ||
		!address(config.GasPayer) || config.LeaseDuration < time.Second || config.LeaseDuration > time.Minute ||
		config.MaxFeeBumps < 0 || config.MaxFeeBumps > 3 || config.MaxGasLimit == 0 || !validFee(config.FeeCap) {
		return nil, ErrInvalidConfig
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Service{store, artifacts, assembler, verifier, wallet, sealer, broadcaster, fees, nonces, replacements, outcomes, leadership, config}, nil
}

func validateOutcome(job Job, attempt Attempt, outcome Outcome, now time.Time) error {
	if outcome.JobID != job.JobID || outcome.AttemptNumber != attempt.Number ||
		outcome.TransactionHash != attempt.TransactionHash || !hash(outcome.EvidenceDigest) ||
		outcome.ObservedAt.IsZero() || outcome.ObservedAt.After(now.Add(time.Minute)) || now.Sub(outcome.ObservedAt) > time.Minute ||
		(outcome.State != StateSubmitted && outcome.State != StateConfirmed && outcome.State != StateFinalized && outcome.State != StateReverted &&
			outcome.State != StateReorged && outcome.State != StateTimedOut) {
		return ErrStateConflict
	}
	if job.State == StateConfirmed && outcome.State != StateConfirmed && outcome.State != StateFinalized && outcome.State != StateReorged {
		return ErrStateConflict
	}
	if outcome.State == StateSubmitted && job.State != StateAmbiguous {
		return ErrStateConflict
	}
	return nil
}

func validateInput(input EnqueueInput, now time.Time) error {
	input.ValidAfter, input.ValidBefore, input.EligibleAfter = input.ValidAfter.UTC(), input.ValidBefore.UTC(), input.EligibleAfter.UTC()
	if !hash(input.JobID) || !hash(input.OperationID) || !identifier(input.OrganizationID) || !validAction(input.Action) ||
		(input.ChainID != 8453 && input.ChainID != 84532) || !identifier(input.KeeperID) || !address(input.GasPayer) ||
		!address(input.Target) || input.ValueWei != "0" || len(input.CanonicalPayload) == 0 ||
		len(input.CanonicalPayload) > 256*1024 || canonicalPayloadHash(input.CanonicalPayload) != input.CanonicalPayloadHash ||
		input.EligibleAfter.IsZero() {
		return ErrInvalidJob
	}
	if input.Action == ActionClaimExpired {
		if input.SignerHandle != "" || input.AuthorizationDigest != "" || input.SignerAddress != "" ||
			!input.ValidAfter.IsZero() || !input.ValidBefore.IsZero() || input.LeadershipEpoch != 0 ||
			!hash(input.EligibilityEvidenceDigest) || input.EligibilityObservedAt.IsZero() {
			return ErrInvalidJob
		}
		return nil
	}
	if input.EligibilityEvidenceDigest != "" || !input.EligibilityObservedAt.IsZero() {
		return ErrInvalidJob
	}
	if !opaque(input.SignerHandle) || !hash(input.AuthorizationDigest) || !address(input.SignerAddress) ||
		input.ValidAfter.IsZero() || input.ValidBefore.IsZero() || !input.ValidBefore.After(input.ValidAfter) ||
		input.ValidBefore.Sub(input.ValidAfter) > 10*time.Minute || !input.ValidBefore.After(now) ||
		input.LeadershipEpoch == 0 || input.LeadershipEpoch > math.MaxInt64 {
		return ErrInvalidJob
	}
	if input.EligibleAfter.Before(input.ValidAfter) || !input.EligibleAfter.Before(input.ValidBefore) {
		return ErrInvalidJob
	}
	return nil
}

func validateUnsigned(job Job, tx UnsignedTransaction, config Config) error {
	if tx.ChainID != job.ChainID || tx.From != job.GasPayer || tx.To != job.Target || tx.ValueWei != job.ValueWei ||
		tx.GasLimit == 0 || tx.GasLimit > config.MaxGasLimit || len(tx.Data) < 4 || len(tx.Data) > 1024*1024 ||
		!validFee(tx.Fee) || feeAboveCap(tx.Fee, config.FeeCap) {
		return ErrInvalidTransaction
	}
	return nil
}

func feeAboveCap(fee, cap Fee) bool {
	feeMax, _ := new(big.Int).SetString(fee.MaxFeePerGasWei, 10)
	capMax, _ := new(big.Int).SetString(cap.MaxFeePerGasWei, 10)
	feeTip, _ := new(big.Int).SetString(fee.MaxPriorityFeePerGasWei, 10)
	capTip, _ := new(big.Int).SetString(cap.MaxPriorityFeePerGasWei, 10)
	return feeMax.Cmp(capMax) > 0 || feeTip.Cmp(capTip) > 0
}

// verifySignedTransaction prevents a wallet/HSM adapter from mutating the
// independently verified transaction while signing it.
func verifySignedTransaction(unsigned UnsignedTransaction, signed SignedTransaction) error {
	if !hash(signed.Hash) || len(signed.Raw) == 0 || len(signed.Raw) > 2*1024*1024 ||
		crypto.Keccak256Hash(signed.Raw).Hex() != signed.Hash {
		return ErrInvalidTransaction
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(signed.Raw); err != nil || tx.Type() != types.DynamicFeeTxType ||
		tx.ChainId() == nil || !tx.ChainId().IsUint64() || tx.ChainId().Uint64() != unsigned.ChainID ||
		tx.Nonce() != unsigned.Nonce || tx.To() == nil || strings.ToLower(tx.To().Hex()) != unsigned.To ||
		tx.Value().String() != unsigned.ValueWei || tx.Gas() != unsigned.GasLimit ||
		len(tx.AccessList()) != 0 || !bytes.Equal(tx.Data(), unsigned.Data) || tx.GasFeeCap().String() != unsigned.Fee.MaxFeePerGasWei ||
		tx.GasTipCap().String() != unsigned.Fee.MaxPriorityFeePerGasWei {
		return ErrInvalidTransaction
	}
	sender, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(unsigned.ChainID)), &tx)
	if err != nil || strings.ToLower(sender.Hex()) != unsigned.From || sender == (common.Address{}) {
		return ErrInvalidTransaction
	}
	return nil
}

func verifyRecoveredSigned(job Job, attempt Attempt, raw []byte) error {
	if len(raw) == 0 || len(raw) > 2*1024*1024 || crypto.Keccak256Hash(raw).Hex() != attempt.TransactionHash {
		return ErrInvalidTransaction
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil || tx.Type() != types.DynamicFeeTxType || tx.ChainId() == nil ||
		!tx.ChainId().IsUint64() || tx.ChainId().Uint64() != job.ChainID || tx.Nonce() != attempt.Nonce ||
		tx.To() == nil || strings.ToLower(tx.To().Hex()) != job.Target || tx.Value().String() != job.ValueWei ||
		len(tx.AccessList()) != 0 || len(tx.Data()) < 4 || len(tx.Data()) > 1024*1024 || tx.Gas() == 0 ||
		tx.GasFeeCap().String() != attempt.Fee.MaxFeePerGasWei ||
		tx.GasTipCap().String() != attempt.Fee.MaxPriorityFeePerGasWei {
		return ErrInvalidTransaction
	}
	sender, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(job.ChainID)), &tx)
	if err != nil || strings.ToLower(sender.Hex()) != job.GasPayer {
		return ErrInvalidTransaction
	}
	return nil
}

func validFee(fee Fee) bool {
	maxFee, ok := new(big.Int).SetString(fee.MaxFeePerGasWei, 10)
	if !ok || maxFee.Sign() <= 0 || maxFee.BitLen() > 256 {
		return false
	}
	tip, ok := new(big.Int).SetString(fee.MaxPriorityFeePerGasWei, 10)
	return ok && tip.Sign() >= 0 && tip.BitLen() <= 256 && tip.Cmp(maxFee) <= 0
}

func strictlyBumped(previous, next Fee) bool {
	previousMax, ok1 := new(big.Int).SetString(previous.MaxFeePerGasWei, 10)
	nextMax, ok2 := new(big.Int).SetString(next.MaxFeePerGasWei, 10)
	previousTip, ok3 := new(big.Int).SetString(previous.MaxPriorityFeePerGasWei, 10)
	nextTip, ok4 := new(big.Int).SetString(next.MaxPriorityFeePerGasWei, 10)
	return ok1 && ok2 && ok3 && ok4 && nextMax.Cmp(previousMax) > 0 && nextTip.Cmp(previousTip) >= 0
}

func validAction(action Action) bool {
	return action == ActionLock || action == ActionRelease || action == ActionRefund ||
		action == ActionClaimExpired || action == ActionAdmin
}

var (
	hashRE       = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	addressRE    = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	identifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	decimalRE    = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)
)

func hash(value string) bool {
	return hashRE.MatchString(value) && value != "0x"+strings.Repeat("0", 64)
}
func address(value string) bool {
	return addressRE.MatchString(value) && value != "0x"+strings.Repeat("0", 40)
}
func identifier(value string) bool { return identifierRE.MatchString(value) }
func opaque(value string) bool {
	return len(value) >= 16 && len(value) <= 512 && !strings.ContainsAny(value, "\r\n\x00")
}
func unsignedDecimal(value string) bool { return decimalRE.MatchString(value) }
func canonicalPayloadHash(value []byte) string {
	return "0x" + hex.EncodeToString(crypto.Keccak256(value))
}

func aad(jobID string, attempt int, txHash string) []byte {
	return []byte(fmt.Sprintf("ASCP_KEEPER_TX_V1\n%s\n%d\n%s", jobID, attempt, txHash))
}
