// Package acceptanceexternal verifies release-grade evidence for the ASCP
// acceptance criteria that cannot be closed by repository tests alone.
package acceptanceexternal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	SchemaVersion  = 1
	SigningContext = "FlowOps ASCP external acceptance evidence v1"
	maxJSONBytes   = 8 << 20
)

var (
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`)
	acPattern     = regexp.MustCompile(`^AC-([1-9]|[1-8][0-9])$`)

	requiredAssertions = map[string][]string{
		"AC-1":  {"complete-event-chain", "authorization-live-to-consumed", "final-lock-and-release-receipts", "balanced-journal", "zero-recovery-key-signatures"},
		"AC-2":  {"named-approval-bound", "no-reservation-before-approval", "single-approved-execution"},
		"AC-5":  {"primary-services-stopped", "keeper-only-expiry-claim", "refund-finalized-within-slo", "journal-and-budget-restored", "zero-human-recovery-action"},
		"AC-12": {"manual-outflow-observed", "chain-only-created-within-cycle", "organization-auto-frozen"},
		"AC-19": {"broadcast-response-lost", "duplicate-lock-rejected", "original-receipt-recovered", "single-economic-lock"},
		"AC-24": {"lock-reorg-drop-replace-revert-injected", "no-premature-dispatch", "canonical-state-recovered", "no-double-payment"},
		"AC-31": {"deliver-by-minus-one", "release-near-settle-by", "same-block-terminal-race", "exactly-one-terminal-transfer"},
		"AC-33": {"escrow-and-module-version-migrated", "restart-mid-migration", "old-escrows-remain-terminal", "wrong-address-signing-denied", "dual-indexing-proved"},
		"AC-37": {"safe-lock-reorg-before-finality", "committed-safe-reorged-back", "conservation-proved", "no-duplicate-execution"},
		"AC-46": {"signer-host-destroyed", "spend-authorizer-reattached", "one-recovery-key-destroyed", "two-of-three-owner-rotation-restored", "no-unilateral-takeover"},
		"AC-47": {"production-equivalent-two-of-three-safe", "module-only-lock", "module-only-refund", "zero-safe-owner-spend-signatures"},
		"AC-53": {"keeper-gas-floor-exhausted", "fee-bump-under-reorg", "expiry-refund-within-slo", "exactly-once-recovery"},
		"AC-68": {"every-authority-row-executed", "sender-gas-payer-signer-proved", "epoch-nonce-workflow-calldata-proved", "receipt-and-replay-outcome-proved"},
		"AC-84": {"replica-missing-stale-mutable-hash-wrong", "independent-proof-failed", "governor-approval-denied", "proposal-never-activated"},
	}
)

type DigestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type SafeTrust struct {
	Address   string   `json:"address"`
	Owners    []string `json:"owners"`
	Threshold int      `json:"threshold"`
}

type Profile struct {
	SchemaVersion       int        `json:"schemaVersion"`
	ProfileID           string     `json:"profileId"`
	Network             string     `json:"network"`
	ChainID             uint64     `json:"chainId"`
	SourceCommit        string     `json:"sourceCommit"`
	Deployment          DigestFile `json:"deployment"`
	RequiredCriteria    []string   `json:"requiredCriteria"`
	Safe                SafeTrust  `json:"safe"`
	MinimumProviders    int        `json:"minimumProviders"`
	MaximumEvidenceAgeS uint64     `json:"maximumEvidenceAgeSeconds"`
}

type Artifact struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Assertion struct {
	Name         string   `json:"name"`
	Passed       bool     `json:"passed"`
	EvidenceRefs []string `json:"evidenceRefs"`
}

type CriterionResult struct {
	ID         string      `json:"id"`
	Assertions []Assertion `json:"assertions"`
}

type ProviderObservation struct {
	Provider    string `json:"provider"`
	RPCURL      string `json:"rpcUrl"`
	ObservedAt  string `json:"observedAt"`
	HeadNumber  uint64 `json:"headNumber"`
	HeadHash    string `json:"headHash"`
	EvidenceRef string `json:"evidenceRef"`
}

type OwnerSignature struct {
	Owner        string `json:"owner"`
	SignatureHex string `json:"signatureHex"`
}

type Bundle struct {
	SchemaVersion        int                   `json:"schemaVersion"`
	RunID                string                `json:"runId"`
	ProfileID            string                `json:"profileId"`
	Network              string                `json:"network"`
	ChainID              uint64                `json:"chainId"`
	SourceCommit         string                `json:"sourceCommit"`
	Deployment           DigestFile            `json:"deployment"`
	StartedAt            string                `json:"startedAt"`
	CompletedAt          string                `json:"completedAt"`
	Artifacts            []Artifact            `json:"artifacts"`
	Criteria             []CriterionResult     `json:"criteria"`
	ProviderObservations []ProviderObservation `json:"providerObservations"`
	Signatures           []OwnerSignature      `json:"signatures"`
}

func LoadProfile(path string) (Profile, error) {
	var profile Profile
	if err := decodeStrict(path, &profile); err != nil {
		return Profile{}, err
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func LoadBundle(path string) (Bundle, error) {
	var bundle Bundle
	if err := decodeStrict(path, &bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func decodeStrict(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maxJSONBytes {
		return errors.New("external acceptance JSON exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode external acceptance JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("external acceptance JSON must contain exactly one value")
	}
	return nil
}

func (p Profile) Validate() error {
	if p.SchemaVersion != SchemaVersion || !idPattern.MatchString(p.ProfileID) || p.Network != "base-sepolia" ||
		p.ChainID != 84532 || !commitPattern.MatchString(p.SourceCommit) || p.MinimumProviders < 2 ||
		p.MaximumEvidenceAgeS < 300 || p.MaximumEvidenceAgeS > 90*24*60*60 {
		return errors.New("external acceptance profile identity or operating bounds are invalid")
	}
	if err := validateDigestFile(p.Deployment); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}
	if len(p.RequiredCriteria) != len(requiredAssertions) {
		return errors.New("external acceptance profile does not require the complete criterion set")
	}
	criteria := make(map[string]struct{}, len(p.RequiredCriteria))
	for _, id := range p.RequiredCriteria {
		if _, required := requiredAssertions[id]; !required {
			return fmt.Errorf("profile contains unsupported criterion %s", id)
		}
		if _, duplicate := criteria[id]; duplicate {
			return fmt.Errorf("profile duplicates criterion %s", id)
		}
		criteria[id] = struct{}{}
	}
	if !common.IsHexAddress(p.Safe.Address) || common.HexToAddress(p.Safe.Address) == (common.Address{}) ||
		p.Safe.Address != strings.ToLower(p.Safe.Address) || p.Safe.Threshold < 2 ||
		p.Safe.Threshold > len(p.Safe.Owners) {
		return errors.New("external acceptance Safe trust profile is invalid")
	}
	owners := make(map[string]struct{}, len(p.Safe.Owners))
	for _, owner := range p.Safe.Owners {
		if !common.IsHexAddress(owner) || common.HexToAddress(owner) == (common.Address{}) || owner != strings.ToLower(owner) {
			return errors.New("external acceptance Safe owner is invalid")
		}
		if _, duplicate := owners[owner]; duplicate {
			return errors.New("external acceptance Safe owner is duplicated")
		}
		owners[owner] = struct{}{}
	}
	return nil
}

func Verify(bundle Bundle, profile Profile, repositoryRoot string, now time.Time) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if bundle.SchemaVersion != SchemaVersion || !idPattern.MatchString(bundle.RunID) || bundle.ProfileID != profile.ProfileID ||
		bundle.Network != profile.Network || bundle.ChainID != profile.ChainID || bundle.SourceCommit != profile.SourceCommit ||
		bundle.Deployment != profile.Deployment {
		return errors.New("external acceptance bundle does not match its pinned profile")
	}
	if err := verifyPinnedFile(profile.Deployment, repositoryRoot); err != nil {
		return fmt.Errorf("verify pinned deployment: %w", err)
	}
	startedAt, err := time.Parse(time.RFC3339, bundle.StartedAt)
	if err != nil {
		return errors.New("external acceptance start time is invalid")
	}
	completedAt, err := time.Parse(time.RFC3339, bundle.CompletedAt)
	if err != nil || completedAt.Before(startedAt) || completedAt.After(now.Add(5*time.Minute)) ||
		now.Sub(completedAt) > time.Duration(profile.MaximumEvidenceAgeS)*time.Second {
		return errors.New("external acceptance completion time is invalid or stale")
	}
	artifactIDs, artifactKinds, err := verifyArtifacts(bundle.Artifacts, repositoryRoot)
	if err != nil {
		return err
	}
	for _, globalKind := range []string{"event-chain-export", "manifest"} {
		if artifactKinds[globalKind] == 0 {
			return fmt.Errorf("external acceptance bundle lacks %s artifact", globalKind)
		}
	}
	if err := verifyCriteria(bundle.Criteria, profile.RequiredCriteria, artifactIDs); err != nil {
		return err
	}
	if err := verifyProviderObservations(bundle.ProviderObservations, profile.MinimumProviders, artifactIDs, startedAt, completedAt); err != nil {
		return err
	}
	if err := verifyOwnerSignatures(bundle, profile.Safe); err != nil {
		return err
	}
	return nil
}

func verifyPinnedFile(pinned DigestFile, root string) error {
	if err := validateDigestFile(pinned); err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(rootAbs, filepath.Clean(pinned.Path)))
	if err != nil {
		return err
	}
	within, err := filepath.Rel(rootAbs, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return errors.New("pinned file escapes the evidence root")
	}
	actual, err := fileSHA256(resolved)
	if err != nil {
		return err
	}
	if actual != pinned.SHA256 {
		return errors.New("pinned file digest mismatch")
	}
	return nil
}

func verifyArtifacts(artifacts []Artifact, root string) (map[string]struct{}, map[string]int, error) {
	if len(artifacts) == 0 {
		return nil, nil, errors.New("external acceptance bundle has no artifacts")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, nil, err
	}
	ids := make(map[string]struct{}, len(artifacts))
	kinds := make(map[string]int)
	allowedKinds := map[string]bool{
		"event-chain-export": true, "manifest": true, "rpc-observation": true,
		"operator-record": true, "safe-ceremony": true, "chaos-record": true,
		"signer-recovery-record": true, "publisher-replica-record": true,
	}
	for _, artifact := range artifacts {
		if !idPattern.MatchString(artifact.ID) || !allowedKinds[artifact.Kind] || !hashPattern.MatchString(artifact.SHA256) {
			return nil, nil, errors.New("external acceptance artifact identity is invalid")
		}
		if _, duplicate := ids[artifact.ID]; duplicate {
			return nil, nil, fmt.Errorf("external acceptance artifact %s is duplicated", artifact.ID)
		}
		clean := filepath.Clean(artifact.Path)
		if artifact.Path == "" || filepath.IsAbs(artifact.Path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("external acceptance artifact %s has an invalid path", artifact.ID)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(rootAbs, clean))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve external acceptance artifact %s: %w", artifact.ID, err)
		}
		within, err := filepath.Rel(rootAbs, resolved)
		if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("external acceptance artifact %s escapes the evidence root", artifact.ID)
		}
		actual, err := fileSHA256(resolved)
		if err != nil {
			return nil, nil, fmt.Errorf("hash external acceptance artifact %s: %w", artifact.ID, err)
		}
		if actual != artifact.SHA256 {
			return nil, nil, fmt.Errorf("external acceptance artifact %s digest mismatch", artifact.ID)
		}
		ids[artifact.ID] = struct{}{}
		kinds[artifact.Kind]++
	}
	return ids, kinds, nil
}

func verifyCriteria(results []CriterionResult, required []string, artifacts map[string]struct{}) error {
	if len(results) != len(required) {
		return errors.New("external acceptance bundle criterion coverage is incomplete")
	}
	requiredSet := make(map[string]struct{}, len(required))
	for _, id := range required {
		requiredSet[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if !acPattern.MatchString(result.ID) {
			return errors.New("external acceptance result has an invalid criterion ID")
		}
		if _, expected := requiredSet[result.ID]; !expected {
			return fmt.Errorf("external acceptance result contains unexpected criterion %s", result.ID)
		}
		if _, duplicate := seen[result.ID]; duplicate {
			return fmt.Errorf("external acceptance result duplicates criterion %s", result.ID)
		}
		seen[result.ID] = struct{}{}
		wantAssertions := requiredAssertions[result.ID]
		if len(result.Assertions) != len(wantAssertions) {
			return fmt.Errorf("%s assertion coverage is incomplete", result.ID)
		}
		want := make(map[string]struct{}, len(wantAssertions))
		for _, name := range wantAssertions {
			want[name] = struct{}{}
		}
		assertions := make(map[string]struct{}, len(result.Assertions))
		for _, assertion := range result.Assertions {
			if _, expected := want[assertion.Name]; !expected || !assertion.Passed || len(assertion.EvidenceRefs) == 0 {
				return fmt.Errorf("%s assertion %s is missing, failed, or unsupported", result.ID, assertion.Name)
			}
			if _, duplicate := assertions[assertion.Name]; duplicate {
				return fmt.Errorf("%s assertion %s is duplicated", result.ID, assertion.Name)
			}
			refs := make(map[string]struct{}, len(assertion.EvidenceRefs))
			for _, ref := range assertion.EvidenceRefs {
				if _, exists := artifacts[ref]; !exists {
					return fmt.Errorf("%s assertion %s references unknown artifact %s", result.ID, assertion.Name, ref)
				}
				if _, duplicate := refs[ref]; duplicate {
					return fmt.Errorf("%s assertion %s duplicates evidence reference %s", result.ID, assertion.Name, ref)
				}
				refs[ref] = struct{}{}
			}
			assertions[assertion.Name] = struct{}{}
		}
	}
	return nil
}

func verifyProviderObservations(observations []ProviderObservation, minimum int, artifacts map[string]struct{}, startedAt, completedAt time.Time) error {
	if len(observations) < minimum {
		return errors.New("external acceptance provider quorum is incomplete")
	}
	hosts := make(map[string]struct{}, len(observations))
	providers := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		parsed, err := url.Parse(observation.RPCURL)
		observedAt, timeErr := time.Parse(time.RFC3339, observation.ObservedAt)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
			!idPattern.MatchString(observation.Provider) || timeErr != nil || observedAt.Before(startedAt) || observedAt.After(completedAt) ||
			observation.HeadNumber == 0 || !strings.HasPrefix(observation.HeadHash, "0x") || len(observation.HeadHash) != 66 ||
			!isHex(observation.HeadHash[2:]) {
			return errors.New("external acceptance provider observation is invalid")
		}
		if _, exists := artifacts[observation.EvidenceRef]; !exists {
			return errors.New("external acceptance provider observation lacks its raw evidence artifact")
		}
		provider := strings.ToLower(observation.Provider)
		host := strings.ToLower(parsed.Hostname())
		if _, duplicate := providers[provider]; duplicate {
			return errors.New("external acceptance provider name is duplicated")
		}
		if _, duplicate := hosts[host]; duplicate {
			return errors.New("external acceptance provider host is not independent")
		}
		providers[provider] = struct{}{}
		hosts[host] = struct{}{}
	}
	if len(hosts) < minimum {
		return errors.New("external acceptance provider host quorum is incomplete")
	}
	return nil
}

func verifyOwnerSignatures(bundle Bundle, trust SafeTrust) error {
	if len(bundle.Signatures) < trust.Threshold {
		return errors.New("external acceptance owner signature quorum is incomplete")
	}
	owners := make(map[common.Address]struct{}, len(trust.Owners))
	for _, owner := range trust.Owners {
		owners[common.HexToAddress(owner)] = struct{}{}
	}
	message, err := SigningMessage(bundle)
	if err != nil {
		return err
	}
	digest := accounts.TextHash([]byte(message))
	seen := make(map[common.Address]struct{}, len(bundle.Signatures))
	for _, signed := range bundle.Signatures {
		if !common.IsHexAddress(signed.Owner) || signed.Owner != strings.ToLower(signed.Owner) {
			return errors.New("external acceptance signature owner is invalid")
		}
		owner := common.HexToAddress(signed.Owner)
		if _, trusted := owners[owner]; !trusted {
			return fmt.Errorf("external acceptance signature owner %s is not trusted", signed.Owner)
		}
		if _, duplicate := seen[owner]; duplicate {
			return fmt.Errorf("external acceptance signature owner %s is duplicated", signed.Owner)
		}
		signature, err := decodeSignature(signed.SignatureHex)
		if err != nil {
			return err
		}
		publicKey, err := crypto.SigToPub(digest, signature)
		if err != nil || crypto.PubkeyToAddress(*publicKey) != owner {
			return fmt.Errorf("external acceptance signature for %s is invalid", signed.Owner)
		}
		seen[owner] = struct{}{}
	}
	if len(seen) < trust.Threshold {
		return errors.New("external acceptance signature quorum is incomplete")
	}
	return nil
}

func SigningMessage(bundle Bundle) (string, error) {
	unsigned := bundle
	unsigned.Signatures = nil
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return SigningContext + "\n0x" + hex.EncodeToString(digest[:]), nil
}

// Template returns a deliberately incomplete ceremony bundle. Operators must
// attach real artifacts, mark only demonstrated assertions as passed, record
// the completion time and provider observations, and then obtain owner quorum.
func Template(profile Profile, runID string, now time.Time) (Bundle, error) {
	if err := profile.Validate(); err != nil {
		return Bundle{}, err
	}
	if !idPattern.MatchString(runID) {
		return Bundle{}, errors.New("external acceptance run ID is invalid")
	}
	criteria := make([]CriterionResult, 0, len(profile.RequiredCriteria))
	for _, id := range profile.RequiredCriteria {
		assertions := make([]Assertion, 0, len(requiredAssertions[id]))
		for _, name := range requiredAssertions[id] {
			assertions = append(assertions, Assertion{Name: name, Passed: false, EvidenceRefs: []string{}})
		}
		criteria = append(criteria, CriterionResult{ID: id, Assertions: assertions})
	}
	return Bundle{
		SchemaVersion:        SchemaVersion,
		RunID:                runID,
		ProfileID:            profile.ProfileID,
		Network:              profile.Network,
		ChainID:              profile.ChainID,
		SourceCommit:         profile.SourceCommit,
		Deployment:           profile.Deployment,
		StartedAt:            now.UTC().Format(time.RFC3339),
		Artifacts:            []Artifact{},
		Criteria:             criteria,
		ProviderObservations: []ProviderObservation{},
		Signatures:           []OwnerSignature{},
	}, nil
}

func decodeSignature(raw string) ([]byte, error) {
	if !strings.HasPrefix(raw, "0x") || len(raw) != 132 || !isHex(raw[2:]) {
		return nil, errors.New("external acceptance owner signature encoding is invalid")
	}
	signature, err := hex.DecodeString(raw[2:])
	if err != nil {
		return nil, errors.New("external acceptance owner signature encoding is invalid")
	}
	if signature[64] >= 27 {
		signature[64] -= 27
	}
	if signature[64] > 1 {
		return nil, errors.New("external acceptance owner signature recovery value is invalid")
	}
	return signature, nil
}

func validateDigestFile(file DigestFile) error {
	clean := filepath.Clean(file.Path)
	if file.Path == "" || filepath.IsAbs(file.Path) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) || !hashPattern.MatchString(file.SHA256) {
		return errors.New("digest file is invalid")
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

// RequiredAssertions returns a stable copy used by ceremony tooling and docs.
func RequiredAssertions() map[string][]string {
	result := make(map[string][]string, len(requiredAssertions))
	keys := make([]string, 0, len(requiredAssertions))
	for id := range requiredAssertions {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		result[id] = append([]string(nil), requiredAssertions[id]...)
	}
	return result
}
