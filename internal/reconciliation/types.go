package reconciliation

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
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
)

type Config struct {
	ChainID              uint64
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
	Expected                ExpectedExecution `json:"expected"`
	State                   ExecutionState    `json:"state"`
	BroadcastAt             time.Time         `json:"broadcastAt"`
	ResolvedAt              *time.Time        `json:"resolvedAt,omitempty"`
	BlockNumber             uint64            `json:"blockNumber,omitempty"`
	BlockHash               string            `json:"blockHash,omitempty"`
	Resolution              string            `json:"resolution,omitempty"`
	LedgerTransactionID     string            `json:"ledgerTransactionId,omitempty"`
	CorrectionTransactionID string            `json:"correctionTransactionId,omitempty"`
	ReorgEvidenceDigest     string            `json:"reorgEvidenceDigest,omitempty"`
	FinalityCheckedAt       *time.Time        `json:"finalityCheckedAt,omitempty"`
	FinalityCheckedHead     uint64            `json:"finalityCheckedHead,omitempty"`
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
