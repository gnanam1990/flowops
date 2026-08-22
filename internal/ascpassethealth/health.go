// Package ascpassethealth owns the fail-closed state machine for the configured
// Base native-USDC dependency. It does not sign, relay, or redirect funds.
package ascpassethealth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type State string

const (
	Normal               State = "NORMAL"
	TokenPaused          State = "TOKEN_PAUSED"
	AssetTransferBlocked State = "ASSET_TRANSFER_BLOCKED"
	Recovering           State = "RECOVERING"
)

var (
	ErrInvalidConfiguration = errors.New("invalid asset-health configuration")
	ErrQuorumUnavailable    = errors.New("asset-health quorum unavailable")
	ErrObserverDisagreement = errors.New("asset-health observers disagree")
	ErrRecoveryIncomplete   = errors.New("asset-health recovery is incomplete")
)

type Config struct {
	ChainID             uint64
	Asset               string
	ProxyImplementation string
	RuntimeCodeHash     string
	Quorum              int
	MaxObservationAge   time.Duration
}

type Observation struct {
	Provider            string
	ChainID             uint64
	Asset               string
	ProxyImplementation string
	RuntimeCodeHash     string
	Paused              bool
	BuyerBlacklisted    bool
	EscrowBlacklisted   bool
	TransferFailure     bool
	FinalizedBlock      uint64
	FinalizedBlockHash  string
	ObservedAt          time.Time
}

type Decision struct {
	State          State
	EvidenceDigest string
	Providers      []string
	FinalizedBlock uint64
	ObservedAt     time.Time
}

type Record struct {
	Config
	State          State
	Epoch          uint64
	EvidenceDigest string
	Providers      []string
	FinalizedBlock uint64
	ObservedAt     time.Time
	UpdatedAt      time.Time
}

type RecoveryProof struct {
	ChainID             uint64
	Asset               string
	HealthEpoch         uint64
	CleanEvidenceDigest string
	CleanFinalizedBlock uint64
	ReconciledAt        time.Time
	EvidenceDigest      string
}

type Store interface {
	Transition(context.Context, Config, Decision, time.Time) (Record, error)
	CompleteRecovery(context.Context, Config, RecoveryProof, time.Time) (Record, error)
	Get(context.Context, uint64, string) (Record, error)
}

type RecoveryVerifier interface {
	VerifyRecovery(context.Context, Record) (RecoveryProof, error)
}

type Service struct {
	config   Config
	store    Store
	verifier RecoveryVerifier
	clock    func() time.Time
}

func New(config Config, store Store, verifier RecoveryVerifier, clock func() time.Time) (*Service, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("durable asset-health store is required")
	}
	if verifier == nil {
		return nil, errors.New("asset-health recovery verifier is required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Service{config: config, store: store, verifier: verifier, clock: clock}, nil
}

func (s *Service) Observe(ctx context.Context, observations []Observation) (Record, error) {
	now := s.clock().UTC()
	decision, err := Evaluate(s.config, observations, now)
	if err != nil {
		return Record{}, err
	}
	return s.store.Transition(ctx, s.config, decision, now)
}

func (s *Service) CompleteRecovery(ctx context.Context) (Record, error) {
	record, err := s.store.Get(ctx, s.config.ChainID, s.config.Asset)
	if err != nil {
		return Record{}, fmt.Errorf("read asset recovery state: %w", err)
	}
	if record.State != Recovering {
		return Record{}, ErrRecoveryIncomplete
	}
	proof, err := s.verifier.VerifyRecovery(ctx, record)
	if err != nil {
		return Record{}, fmt.Errorf("verify asset recovery: %w", err)
	}
	completeAt := s.clock().UTC()
	if err := validateRecoveryProof(record, proof, completeAt); err != nil {
		return Record{}, err
	}
	return s.store.CompleteRecovery(ctx, s.config, proof, completeAt)
}

func Evaluate(config Config, observations []Observation, now time.Time) (Decision, error) {
	if err := validateConfig(config); err != nil {
		return Decision{}, err
	}
	if len(observations) < config.Quorum || len(observations) > 5 {
		return Decision{}, ErrQuorumUnavailable
	}
	canonical := observations[0]
	providers := make([]string, 0, len(observations))
	seen := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		if observation.Provider == "" || observation.ObservedAt.IsZero() || observation.FinalizedBlock == 0 || !canonicalHash(observation.FinalizedBlockHash) {
			return Decision{}, ErrObserverDisagreement
		}
		if observation.ObservedAt.After(now.Add(time.Minute)) || now.Sub(observation.ObservedAt) > config.MaxObservationAge {
			return Decision{}, ErrQuorumUnavailable
		}
		if _, duplicate := seen[observation.Provider]; duplicate {
			return Decision{}, ErrObserverDisagreement
		}
		seen[observation.Provider] = struct{}{}
		providers = append(providers, observation.Provider)
		if !sameObservation(canonical, observation) {
			return Decision{}, ErrObserverDisagreement
		}
		if observation.ObservedAt.Before(canonical.ObservedAt) {
			canonical.ObservedAt = observation.ObservedAt
		}
	}
	if len(providers) < config.Quorum || canonical.ChainID != config.ChainID || canonical.Asset != config.Asset {
		return Decision{}, ErrQuorumUnavailable
	}
	state := Normal
	if canonical.Paused {
		state = TokenPaused
	} else if canonical.BuyerBlacklisted || canonical.EscrowBlacklisted || canonical.TransferFailure ||
		canonical.ProxyImplementation != config.ProxyImplementation || canonical.RuntimeCodeHash != config.RuntimeCodeHash {
		state = AssetTransferBlocked
	}
	sort.Strings(providers)
	payload := struct {
		Version     string
		Observation Observation
		Providers   []string
	}{"ASCP_ASSET_HEALTH_V1", canonical, providers}
	payload.Observation.Provider = ""
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return Decision{State: state, EvidenceDigest: "0x" + hex.EncodeToString(sum[:]), Providers: providers,
		FinalizedBlock: canonical.FinalizedBlock, ObservedAt: canonical.ObservedAt.UTC()}, nil
}

func validateConfig(config Config) error {
	if (config.ChainID != 8453 && config.ChainID != 84532) || !canonicalAddress(config.Asset) ||
		!canonicalAddress(config.ProxyImplementation) || !canonicalHash(config.RuntimeCodeHash) || config.Quorum < 2 || config.Quorum > 5 ||
		config.MaxObservationAge <= 0 || config.MaxObservationAge > 5*time.Minute {
		return ErrInvalidConfiguration
	}
	return nil
}

func sameObservation(left, right Observation) bool {
	left.Provider, right.Provider = "", ""
	return left.ChainID == right.ChainID && left.Asset == right.Asset && left.ProxyImplementation == right.ProxyImplementation &&
		left.RuntimeCodeHash == right.RuntimeCodeHash && left.Paused == right.Paused && left.BuyerBlacklisted == right.BuyerBlacklisted &&
		left.EscrowBlacklisted == right.EscrowBlacklisted && left.TransferFailure == right.TransferFailure &&
		left.FinalizedBlock == right.FinalizedBlock && left.FinalizedBlockHash == right.FinalizedBlockHash
}

func canonicalAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil && value != "0x"+strings.Repeat("0", 40)
}

func canonicalHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32 && value != "0x"+strings.Repeat("0", 64)
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{records: make(map[string]Record)} }

func (s *MemoryStore) Transition(ctx context.Context, config Config, decision Decision, now time.Time) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%d\x00%s", config.ChainID, config.Asset)
	record, found := s.records[key]
	if !found {
		record = Record{Config: config, State: Normal}
	}
	if decision.State == Normal && record.State == Recovering {
		return record, nil
	}
	target := decision.State
	if decision.State == Normal && record.State != Normal {
		target = Recovering
	}
	if target != record.State {
		record.Epoch++
	}
	record.State, record.EvidenceDigest, record.Providers = target, decision.EvidenceDigest, append([]string(nil), decision.Providers...)
	record.FinalizedBlock, record.ObservedAt, record.UpdatedAt = decision.FinalizedBlock, decision.ObservedAt, now
	s.records[key] = record
	return record, nil
}

func (s *MemoryStore) CompleteRecovery(ctx context.Context, config Config, proof RecoveryProof, now time.Time) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%d\x00%s", config.ChainID, config.Asset)
	record, found := s.records[key]
	if !found || record.State != Recovering {
		return Record{}, ErrRecoveryIncomplete
	}
	if err := validateRecoveryProof(record, proof, now); err != nil {
		return Record{}, err
	}
	record.State, record.Epoch, record.EvidenceDigest, record.UpdatedAt = Normal, record.Epoch+1, proof.EvidenceDigest, now
	s.records[key] = record
	return record, nil
}

func (s *MemoryStore) Get(ctx context.Context, chainID uint64, asset string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[fmt.Sprintf("%d\x00%s", chainID, asset)]
	if !found {
		return Record{}, sql.ErrNoRows
	}
	record.Providers = append([]string(nil), record.Providers...)
	return record, nil
}
