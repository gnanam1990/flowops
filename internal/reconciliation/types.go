package reconciliation

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

type ChainState string

const (
	StateHealthy        ChainState = "HEALTHY"
	StateSuspectedStall ChainState = "SUSPECTED_STALL"
	StateHalted         ChainState = "HALTED"
	StateRecovering     ChainState = "RECOVERING"
)

type ExecutionState string

const (
	ExecutionBroadcast            ExecutionState = "BROADCAST"
	ExecutionPendingChainRecovery ExecutionState = "PENDING_CHAIN_RECOVERY"
	ExecutionSettled              ExecutionState = "SETTLED"
	ExecutionReverted             ExecutionState = "REVERTED"
	ExecutionQuarantined          ExecutionState = "QUARANTINED"
)

type LedgerKind string

const (
	LedgerReservation LedgerKind = "RESERVATION"
	LedgerSettlement  LedgerKind = "SETTLEMENT"
	LedgerRefund      LedgerKind = "REFUND"
	LedgerFunding     LedgerKind = "FUNDING"
	LedgerCorrection  LedgerKind = "CORRECTION"
	LedgerSuspense    LedgerKind = "SUSPENSE"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
	hashPattern       = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	addressPattern    = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
)

var (
	ErrChainUnavailable  = errors.New("Base canonical state is not healthy")
	ErrConflict          = errors.New("idempotency conflict")
	ErrUnknownExecution  = errors.New("unknown execution")
	ErrUnsafeFinality    = errors.New("canonical finality evidence is insufficient")
	ErrResumeBlocked     = errors.New("autonomous resume is blocked")
	ErrInvalidOperator   = errors.New("operator is invalid")
	ErrInvalidHaltReason = errors.New("halt reason is invalid")
	ErrEscrowTransition  = errors.New("escrow transition is invalid")
	ErrEscrowDeployment  = errors.New("escrow deployment is not admitted")
	ErrEscrowFinality    = errors.New("escrow transition finality is unresolved")
)

type Config struct {
	ChainID              uint64
	EscrowContract       string
	EscrowAsset          string
	EscrowReleaseWindow  uint64
	ObserverQuorum       int
	HaltConfirmations    int
	RecoveryObservations int
	MinConfirmations     uint64
	ReorgLookback        uint64
	MaxHeadSkew          uint64
	StallThreshold       time.Duration
	ObservationMaxAge    time.Duration
	MaxFutureClockSkew   time.Duration
	Clock                func() time.Time
}

func (c *Config) defaults() error {
	if c.ChainID != 8453 && c.ChainID != 84532 {
		return errors.New("reconciliation supports Base mainnet or Base Sepolia only")
	}
	escrowFields := 0
	for _, present := range []bool{c.EscrowContract != "", c.EscrowAsset != "", c.EscrowReleaseWindow != 0} {
		if present {
			escrowFields++
		}
	}
	if escrowFields != 0 && escrowFields != 3 {
		return errors.New("escrow deployment contract, asset, and release window must be configured together")
	}
	if escrowFields == 3 && (!addressPattern.MatchString(c.EscrowContract) || !addressPattern.MatchString(c.EscrowAsset) || c.EscrowContract == c.EscrowAsset || c.EscrowReleaseWindow > 30*24*60*60) {
		return errors.New("configured escrow deployment tuple is invalid")
	}
	if c.ObserverQuorum == 0 {
		c.ObserverQuorum = 2
	}
	if c.HaltConfirmations == 0 {
		c.HaltConfirmations = 2
	}
	if c.RecoveryObservations == 0 {
		c.RecoveryObservations = 3
	}
	if c.MinConfirmations == 0 {
		c.MinConfirmations = 2
	}
	if c.ReorgLookback == 0 {
		c.ReorgLookback = 12
	}
	if c.MaxHeadSkew == 0 {
		c.MaxHeadSkew = 2
	}
	if c.StallThreshold == 0 {
		c.StallThreshold = 2 * time.Minute
	}
	if c.ObservationMaxAge == 0 {
		c.ObservationMaxAge = 30 * time.Second
	}
	if c.MaxFutureClockSkew == 0 {
		c.MaxFutureClockSkew = 15 * time.Second
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.ObserverQuorum < 2 || c.ObserverQuorum > 5 || c.HaltConfirmations < 1 || c.RecoveryObservations < 1 {
		return errors.New("observer and transition thresholds are invalid")
	}
	if c.StallThreshold <= 0 || c.ObservationMaxAge <= 0 || c.MaxFutureClockSkew < 0 {
		return errors.New("observation durations are invalid")
	}
	return nil
}

type Observation struct {
	Provider     string    `json:"provider"`
	ChainID      uint64    `json:"chainId"`
	HeadNumber   uint64    `json:"headNumber"`
	HeadHash     string    `json:"headHash"`
	HeadTime     time.Time `json:"headTime"`
	AnchorNumber uint64    `json:"anchorNumber"`
	AnchorHash   string    `json:"anchorHash"`
	AnchorTime   time.Time `json:"anchorTime"`
	ObservedAt   time.Time `json:"observedAt"`
}

type Checkpoint struct {
	BlockNumber uint64    `json:"blockNumber"`
	BlockHash   string    `json:"blockHash"`
	BlockTime   time.Time `json:"blockTime"`
	ObservedAt  time.Time `json:"observedAt"`
	Cursor      uint64    `json:"cursor"`
}

type ChainStatus struct {
	ChainID                 uint64      `json:"chainId"`
	State                   ChainState  `json:"state"`
	Reason                  string      `json:"reason"`
	LastTrusted             *Checkpoint `json:"lastTrusted,omitempty"`
	RequiredObserverQuorum  int         `json:"requiredObserverQuorum"`
	RespondingObservers     int         `json:"respondingObservers"`
	LastObservationAt       time.Time   `json:"lastObservationAt,omitempty"`
	StateChangedAt          time.Time   `json:"stateChangedAt"`
	ConsecutiveUnhealthy    int         `json:"consecutiveUnhealthy"`
	ConsecutiveRecovery     int         `json:"consecutiveRecovery"`
	AffectedExecutions      int         `json:"affectedExecutions"`
	ReadyForManualResume    bool        `json:"readyForManualResume"`
	AuthorizationsPaused    bool        `json:"authorizationsPaused"`
	BroadcastsPaused        bool        `json:"broadcastsPaused"`
	FinalizationPaused      bool        `json:"finalizationPaused"`
	RefundRecognitionPaused bool        `json:"refundRecognitionPaused"`
}

type ExpectedExecution struct {
	ExecutionID     string `json:"executionId"`
	OrganizationID  string `json:"organizationId"`
	AgentID         string `json:"agentId"`
	TaskID          string `json:"taskId"`
	IntentDigest    string `json:"intentDigest"`
	TransactionHash string `json:"transactionHash"`
	ChainID         uint64 `json:"chainId"`
	Sender          string `json:"sender"`
	Asset           string `json:"asset"`
	Recipient       string `json:"recipient"`
	AmountAtomic    string `json:"amountAtomic"`
}

func (e ExpectedExecution) validate(chainID uint64) error {
	for name, value := range map[string]string{"executionId": e.ExecutionID, "organizationId": e.OrganizationID, "agentId": e.AgentID, "taskId": e.TaskID} {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if !hashPattern.MatchString(e.IntentDigest) || !hashPattern.MatchString(e.TransactionHash) {
		return errors.New("intent or transaction hash is invalid")
	}
	if e.ChainID != chainID {
		return errors.New("execution chain does not match the configured Base chain")
	}
	for name, value := range map[string]string{"sender": e.Sender, "asset": e.Asset, "recipient": e.Recipient} {
		if !addressPattern.MatchString(value) {
			return fmt.Errorf("%s must be a canonical lowercase EVM address", name)
		}
	}
	if _, err := positiveInteger(e.AmountAtomic); err != nil {
		return fmt.Errorf("amountAtomic: %w", err)
	}
	return nil
}

type ReceiptEvidence struct {
	Provider        string `json:"provider"`
	ChainID         uint64 `json:"chainId"`
	TransactionHash string `json:"transactionHash"`
	BlockNumber     uint64 `json:"blockNumber"`
	BlockHash       string `json:"blockHash"`
	ConfirmedHead   uint64 `json:"confirmedHead"`
	Success         bool   `json:"success"`
	Sender          string `json:"sender"`
	Asset           string `json:"asset"`
	Recipient       string `json:"recipient"`
	AmountAtomic    string `json:"amountAtomic"`
}

type Execution struct {
	Expected                ExpectedExecution     `json:"expected"`
	State                   ExecutionState        `json:"state"`
	BroadcastAt             time.Time             `json:"broadcastAt"`
	BroadcastAttestation    *BroadcastAttestation `json:"broadcastAttestation,omitempty"`
	ResolvedAt              *time.Time            `json:"resolvedAt,omitempty"`
	BlockNumber             uint64                `json:"blockNumber,omitempty"`
	BlockHash               string                `json:"blockHash,omitempty"`
	Resolution              string                `json:"resolution,omitempty"`
	LedgerTransactionID     string                `json:"ledgerTransactionId,omitempty"`
	CorrectionTransactionID string                `json:"correctionTransactionId,omitempty"`
	ReorgEvidenceDigest     string                `json:"reorgEvidenceDigest,omitempty"`
	FinalityCheckedAt       *time.Time            `json:"finalityCheckedAt,omitempty"`
	FinalityCheckedHead     uint64                `json:"finalityCheckedHead,omitempty"`
}

// BroadcastAttestation preserves the exact customer proof accepted when an
// already-submitted transaction entered reconciliation. The public key is
// evidence, not current authority; current acceptance still comes from the
// control-plane's tenant-scoped key registry.
type BroadcastAttestation struct {
	SignedReceipt broadcastreceipt.SignedReceipt `json:"signedReceipt"`
	Authorization envelope.Authorization         `json:"authorization"`
	PublicKeyB64  string                         `json:"publicKeyB64"`
}

// EscrowBroadcastAttestation is the durable customer proof for the one
// money-increasing CallEscrow transition. Provider and permissionless follow-up
// transitions do not use customer spend authority.
type EscrowBroadcastAttestation struct {
	SignedReceipt broadcastreceipt.SignedReceipt `json:"signedReceipt"`
	Authorization envelope.Authorization         `json:"authorization"`
	PublicKeyB64  string                         `json:"publicKeyB64"`
}

func (a EscrowBroadcastAttestation) validate(expected EscrowExpectedReceipt) error {
	publicKey, err := base64.StdEncoding.DecodeString(a.PublicKeyB64)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || a.PublicKeyB64 != base64.StdEncoding.EncodeToString(publicKey) {
		return errors.New("escrow broadcast attestation public key is invalid")
	}
	if err := broadcastreceipt.Verify(a.SignedReceipt, ed25519.PublicKey(publicKey)); err != nil {
		return fmt.Errorf("escrow broadcast attestation signature: %w", err)
	}
	receipt := a.SignedReceipt.Receipt
	authorizationDigest, err := a.Authorization.Digest()
	if err != nil {
		return fmt.Errorf("escrow broadcast authorization: %w", err)
	}
	if receipt.AuthorizationID != a.Authorization.AuthorizationID || receipt.AuthorizationDigest != "0x"+hex.EncodeToString(authorizationDigest[:]) ||
		receipt.OrganizationID != a.Authorization.OrganizationID || receipt.CustomerID != a.Authorization.CustomerID {
		return errors.New("escrow broadcast receipt does not match its authorization")
	}
	if a.Authorization.Rail != envelope.RailEscrow || a.Authorization.Escrow == nil || expected.Action != EscrowFund {
		return errors.New("escrow broadcast attestation requires an escrow funding authorization")
	}
	broadcastAt := time.Unix(receipt.BroadcastAt, 0)
	if broadcastAt.Before(time.Unix(a.Authorization.IssuedAt, 0)) || !broadcastAt.Before(time.Unix(a.Authorization.ExpiresAt, 0)) {
		return errors.New("escrow broadcast receipt time is outside its authorization window")
	}
	t := a.Authorization.Escrow
	if receipt.TransactionHash != expected.TransactionHash || receipt.Sender != expected.Buyer ||
		a.Authorization.ChainID != expected.ChainID || a.Authorization.Asset != expected.Asset || a.Authorization.Recipient != expected.Provider || a.Authorization.AmountAtomic != expected.AmountAtomic ||
		t.Contract != expected.Contract || t.Buyer != expected.Buyer || t.Provider != expected.Provider || t.CallID != expected.CallID ||
		t.TaskDigest != expected.TaskDigest || t.RequestDigest != expected.RequestDigest || t.AcknowledgeBy != expected.AcknowledgeBy || t.DeliverBy != expected.DeliverBy || t.ReleaseWindow != expected.ReleaseWindow {
		return errors.New("escrow broadcast attestation does not match expected funding")
	}
	return nil
}

func (a EscrowBroadcastAttestation) validateIntent(intent EscrowIntent) error {
	authorization := a.Authorization
	if authorization.OrganizationID != intent.OrganizationID || authorization.CustomerID != intent.CustomerID || authorization.AgentID != intent.AgentID ||
		authorization.TaskID != intent.TaskID || authorization.AuthorizationID != intent.AuthorizationID {
		return errors.New("escrow broadcast attestation does not match durable intent identity")
	}
	return nil
}

func (a BroadcastAttestation) validate(expected ExpectedExecution) error {
	publicKey, err := base64.StdEncoding.DecodeString(a.PublicKeyB64)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || a.PublicKeyB64 != base64.StdEncoding.EncodeToString(publicKey) {
		return errors.New("broadcast attestation public key is invalid")
	}
	if err := broadcastreceipt.Verify(a.SignedReceipt, ed25519.PublicKey(publicKey)); err != nil {
		return fmt.Errorf("broadcast attestation signature: %w", err)
	}
	receipt := a.SignedReceipt.Receipt
	authorizationDigest, err := a.Authorization.Digest()
	if err != nil {
		return fmt.Errorf("broadcast authorization: %w", err)
	}
	canonicalAuthorizationDigest := "0x" + hex.EncodeToString(authorizationDigest[:])
	if receipt.AuthorizationID != a.Authorization.AuthorizationID || receipt.AuthorizationDigest != canonicalAuthorizationDigest || receipt.OrganizationID != a.Authorization.OrganizationID || receipt.CustomerID != a.Authorization.CustomerID {
		return errors.New("broadcast receipt does not match its authorization")
	}
	if a.Authorization.Rail != envelope.RailDirect {
		return errors.New("broadcast attestation requires a direct_usdc authorization")
	}
	broadcastAt := time.Unix(receipt.BroadcastAt, 0)
	if broadcastAt.Before(time.Unix(a.Authorization.IssuedAt, 0)) || !broadcastAt.Before(time.Unix(a.Authorization.ExpiresAt, 0)) {
		return errors.New("broadcast receipt time is outside its authorization window")
	}
	if receipt.OrganizationID != expected.OrganizationID || receipt.TransactionHash != expected.TransactionHash || receipt.Sender != expected.Sender ||
		a.Authorization.OrganizationID != expected.OrganizationID || a.Authorization.AgentID != expected.AgentID || a.Authorization.TaskID != expected.TaskID ||
		a.Authorization.ChainID != expected.ChainID || a.Authorization.Asset != expected.Asset || a.Authorization.Recipient != expected.Recipient || a.Authorization.AmountAtomic != expected.AmountAtomic {
		return errors.New("broadcast attestation does not match expected execution")
	}
	return nil
}

type ReorgEvidence struct {
	Provider            string `json:"provider"`
	ChainID             uint64 `json:"chainId"`
	TransactionHash     string `json:"transactionHash"`
	OriginalBlockNumber uint64 `json:"originalBlockNumber"`
	OriginalBlockHash   string `json:"originalBlockHash"`
	CanonicalBlockHash  string `json:"canonicalBlockHash"`
	ObservedHead        uint64 `json:"observedHead"`
}

type Posting struct {
	Account      string `json:"account"`
	AmountAtomic string `json:"amountAtomic"`
}

type LedgerTransaction struct {
	TransactionID         string     `json:"transactionId"`
	OrganizationID        string     `json:"organizationId"`
	Kind                  LedgerKind `json:"kind"`
	ReferenceID           string     `json:"referenceId"`
	ReversesTransactionID string     `json:"reversesTransactionId,omitempty"`
	Postings              []Posting  `json:"postings"`
	RecordedAt            time.Time  `json:"recordedAt"`
}

func (t LedgerTransaction) validate() error {
	if !identifierPattern.MatchString(t.TransactionID) || !identifierPattern.MatchString(t.OrganizationID) || !identifierPattern.MatchString(t.ReferenceID) {
		return errors.New("ledger identifiers are invalid")
	}
	switch t.Kind {
	case LedgerReservation, LedgerSettlement, LedgerRefund, LedgerFunding, LedgerCorrection, LedgerSuspense:
	default:
		return errors.New("ledger kind is invalid")
	}
	if len(t.Postings) < 2 || len(t.Postings) > 32 {
		return errors.New("ledger transaction must contain 2 to 32 postings")
	}
	sum := new(big.Int)
	for _, posting := range t.Postings {
		if !identifierPattern.MatchString(posting.Account) {
			return errors.New("ledger account is invalid")
		}
		amount, err := signedInteger(posting.AmountAtomic)
		if err != nil {
			return err
		}
		sum.Add(sum, amount)
	}
	if sum.Sign() != 0 {
		return errors.New("ledger postings do not balance to zero")
	}
	if t.RecordedAt.IsZero() {
		return errors.New("ledger recordedAt is required")
	}
	return nil
}

func positiveInteger(value string) (*big.Int, error) {
	if value == "" || value[0] == '0' {
		return nil, errors.New("must be a canonical positive integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, errors.New("must contain decimal digits only")
		}
	}
	valueInt, ok := new(big.Int).SetString(value, 10)
	if !ok || valueInt.Sign() <= 0 || valueInt.BitLen() > 256 {
		return nil, errors.New("must fit uint256")
	}
	return valueInt, nil
}

func signedInteger(value string) (*big.Int, error) {
	if value == "" || value == "0" || value == "-0" || strings.HasPrefix(value, "+") {
		return nil, errors.New("posting amount must be a canonical non-zero integer")
	}
	digits := value
	if value[0] == '-' {
		digits = value[1:]
	}
	if digits == "" || digits[0] == '0' {
		return nil, errors.New("posting amount must be a canonical non-zero integer")
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return nil, errors.New("posting amount must contain decimal digits only")
		}
	}
	valueInt, ok := new(big.Int).SetString(value, 10)
	if !ok || valueInt.Sign() == 0 || valueInt.BitLen() > 256 {
		return nil, errors.New("posting amount exceeds supported range")
	}
	return valueInt, nil
}
