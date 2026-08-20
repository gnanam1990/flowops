// Package directoryreader establishes quorum-backed, finalized ServiceDirectory
// observations before a SellerQuote can receive directory evidence.
package directoryreader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/pkg/directoryproof"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

var (
	ErrInvalidConfiguration = errors.New("invalid finalized directory reader configuration")
	ErrInvalidObservation   = errors.New("invalid finalized directory observation")
	ErrQuorumUnavailable    = errors.New("finalized directory observer quorum unavailable")
	ErrObserverDisagreement = errors.New("finalized directory observers disagree")
)

// Source is one independently operated observation path. Its Name is a stable
// operator identity and must match the Provider returned in its observation.
// Implementations must read every value at FinalizedBlockNumber, not "latest".
type Source interface {
	Name() string
	ReadFinalized(context.Context) (FinalizedObservation, error)
}

// FinalizedObservation contains all chain and proof data observed at one
// finalized block. DirectoryCodeHash is the runtime bytecode hash from the same
// provider, so upgrades or a wrong contract address cannot silently authorize a
// quote.
type FinalizedObservation struct {
	Provider             string
	ChainID              uint64
	DirectoryContract    string
	DirectoryCodeHash    string
	FinalizedBlockNumber uint64
	FinalizedBlockHash   string
	Directory            directoryproof.Observation
}

type Config struct {
	ChainID           uint64
	Directory         string
	DirectoryCodeHash string
	Sources           []Source
	Quorum            int
}

type Reader struct {
	chainID           uint64
	directory         string
	directoryCodeHash string
	sources           []Source
	quorum            int
}

// Result carries the evidence only after all included observers agree on the
// exact finalized block and directory snapshot. Failures are retained for safe
// operational diagnostics; no evidence is returned on an error.
type Result struct {
	Evidence             sellerquote.DirectoryEvidence
	FinalizedBlockNumber uint64
	FinalizedBlockHash   string
	DirectoryVersion     uint64
	DirectoryRoot        string
	ObservationDigest    string
	Providers            []string
	Failures             map[string]string
}

func New(cfg Config) (*Reader, error) {
	if cfg.ChainID != 8453 && cfg.ChainID != 84532 || !address(cfg.Directory) || !hash(cfg.DirectoryCodeHash) || len(cfg.Sources) < 2 || len(cfg.Sources) > 5 || cfg.Quorum < 2 || cfg.Quorum > len(cfg.Sources) {
		return nil, ErrInvalidConfiguration
	}
	names := make(map[string]struct{}, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if source == nil || !providerName(source.Name()) {
			return nil, ErrInvalidConfiguration
		}
		name := strings.ToLower(source.Name())
		if _, exists := names[name]; exists {
			return nil, ErrInvalidConfiguration
		}
		names[name] = struct{}{}
	}
	return &Reader{chainID: cfg.ChainID, directory: cfg.Directory, directoryCodeHash: cfg.DirectoryCodeHash, sources: append([]Source(nil), cfg.Sources...), quorum: cfg.Quorum}, nil
}

// EvidenceForQuote queries all sources concurrently. A source failure cannot
// be substituted by a mismatched answer: at least Quorum sources must return
// byte-for-byte identical snapshot data for the same finalized block.
func (r *Reader) EvidenceForQuote(ctx context.Context, quote sellerquote.Quote) (Result, error) {
	type response struct {
		name        string
		observation FinalizedObservation
		err         error
	}
	responses := make(chan response, len(r.sources))
	var group sync.WaitGroup
	for _, source := range r.sources {
		source := source
		group.Add(1)
		go func() {
			defer group.Done()
			observation, err := source.ReadFinalized(ctx)
			responses <- response{name: source.Name(), observation: observation, err: err}
		}()
	}
	group.Wait()
	close(responses)

	result := Result{Failures: make(map[string]string)}
	groups := make(map[string][]FinalizedObservation)
	for response := range responses {
		if response.err != nil {
			result.Failures[response.name] = response.err.Error()
			continue
		}
		if err := r.validate(response.name, response.observation); err != nil {
			result.Failures[response.name] = err.Error()
			continue
		}
		digest, err := observationDigest(response.observation)
		if err != nil {
			result.Failures[response.name] = fmt.Errorf("snapshot encoding: %w", err).Error()
			continue
		}
		groups[digest] = append(groups[digest], response.observation)
	}

	var chosenDigest string
	var chosen []FinalizedObservation
	for digest, observations := range groups {
		if len(observations) > len(chosen) {
			chosenDigest, chosen = digest, observations
		}
	}
	if len(chosen) < r.quorum {
		if len(groups) > 1 {
			return Result{}, fmt.Errorf("%w: no snapshot has %d matching observers", ErrObserverDisagreement, r.quorum)
		}
		return Result{}, fmt.Errorf("%w: got %d, need %d", ErrQuorumUnavailable, len(chosen), r.quorum)
	}
	if len(groups) > 1 {
		return Result{}, fmt.Errorf("%w: %d conflicting finalized snapshots", ErrObserverDisagreement, len(groups))
	}

	first := chosen[0]
	evidence, err := directoryproof.EvidenceForQuote(first.Directory, quote)
	if err != nil {
		return Result{}, err
	}
	providers := make([]string, 0, len(chosen))
	for _, observation := range chosen {
		providers = append(providers, observation.Provider)
	}
	sort.Strings(providers)
	if len(result.Failures) == 0 {
		result.Failures = nil
	}
	result.Evidence = evidence
	result.FinalizedBlockNumber = first.FinalizedBlockNumber
	result.FinalizedBlockHash = first.FinalizedBlockHash
	result.DirectoryVersion = first.Directory.Version
	result.DirectoryRoot = first.Directory.Root
	result.ObservationDigest = chosenDigest
	result.Providers = providers
	return result, nil
}

func (r *Reader) validate(sourceName string, observation FinalizedObservation) error {
	if sourceName != observation.Provider || observation.ChainID != r.chainID || observation.DirectoryContract != r.directory || observation.DirectoryCodeHash != r.directoryCodeHash || observation.FinalizedBlockNumber == 0 || !hash(observation.FinalizedBlockHash) {
		return ErrInvalidObservation
	}
	if observation.Directory.DirectoryContract != r.directory || observation.Directory.BlockNumber != observation.FinalizedBlockNumber {
		return ErrInvalidObservation
	}
	return nil
}

func observationDigest(observation FinalizedObservation) (string, error) {
	// Provider intentionally does not take part in the digest: independent
	// providers must agree on chain state, not on their own identity.
	payload := struct {
		ChainID              uint64                     `json:"chainId"`
		DirectoryContract    string                     `json:"directoryContract"`
		DirectoryCodeHash    string                     `json:"directoryCodeHash"`
		FinalizedBlockNumber uint64                     `json:"finalizedBlockNumber"`
		FinalizedBlockHash   string                     `json:"finalizedBlockHash"`
		Directory            directoryproof.Observation `json:"directory"`
	}{observation.ChainID, observation.DirectoryContract, observation.DirectoryCodeHash, observation.FinalizedBlockNumber, observation.FinalizedBlockHash, observation.Directory}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "0x" + hex.EncodeToString(digest[:]), nil
}

func providerName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func address(value string) bool {
	return len(value) == 42 && strings.ToLower(value) == value && common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
