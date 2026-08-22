// Package acceptance validates the executable ASCP v3.4 acceptance inventory.
// It deliberately separates local evidence from full acceptance: a passing unit
// test cannot silently promote an external or production requirement.
package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion    = 1
	SpecSHA256       = "77722a0139c08c0755eb48b712aa4c3e3971016c4db4d948e325f49853ffbc8e"
	AcceptanceSHA256 = "58377488c19e2dbe96498e3b61b58048aa236c588620b3810873640fdca3b3f9"
	maxManifestBytes = 4 << 20
)

var (
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	acPattern   = regexp.MustCompile(`^AC-([1-9]|[1-8][0-9])$`)
	reservedACs = map[int]struct{}{20: {}, 35: {}, 39: {}, 49: {}, 52: {}}
	statusNames = map[string]struct{}{
		"local_evidence": {}, "partial": {}, "missing": {},
		"external_required": {}, "reserved": {}, "accepted": {},
	}
	ownerNames = map[string]struct{}{
		"contracts": {}, "schemas_vectors": {}, "controlplane": {},
		"signer": {}, "keeper_chain": {}, "verifier": {},
		"mcp_base_adapter": {}, "dashboard_ops": {}, "seller_sdk": {},
	}
	requiredOwnershipOverrides = map[string]string{
		"AC-1": "controlplane", "AC-6": "controlplane",
		"AC-8": "controlplane", "AC-43": "dashboard_ops",
	}
	artifactEvidenceKinds = map[string]map[string]struct{}{
		"executable-evidence":      {"go_test": {}, "forge_test": {}, "script": {}},
		"evidence-report":          {"evidence": {}, "manual_external": {}},
		"manifest-hash":            {"evidence": {}, "script": {}},
		"event-chain-export":       {"evidence": {}, "manual_external": {}},
		"signed-external-evidence": {"manual_external": {}},
	}
)

type Manifest struct {
	SchemaVersion      int                 `json:"schemaVersion"`
	Specification      Specification       `json:"specification"`
	ReleaseStage       string              `json:"releaseStage"`
	StatusDefinitions  map[string]string   `json:"statusDefinitions"`
	OwnershipOverrides []OwnershipOverride `json:"ownershipOverrides,omitempty"`
	Criteria           []Criterion         `json:"criteria"`
}

type Specification struct {
	Name             string `json:"name"`
	SHA256           string `json:"sha256"`
	AcceptanceSHA256 string `json:"acceptanceSha256"`
}

type OwnershipOverride struct {
	AC        string `json:"ac"`
	Owner     string `json:"owner"`
	Rationale string `json:"rationale"`
}

type Criterion struct {
	ID                string     `json:"id"`
	Number            int        `json:"number"`
	Title             string     `json:"title"`
	Expectation       string     `json:"expectation"`
	Active            bool       `json:"active"`
	Status            string     `json:"status"`
	PrimaryOwner      string     `json:"primaryOwner,omitempty"`
	Participants      []string   `json:"participants,omitempty"`
	Invariants        []string   `json:"invariants,omitempty"`
	Evidence          []Evidence `json:"evidence,omitempty"`
	RequiredArtifacts []string   `json:"requiredArtifacts,omitempty"`
	Gap               string     `json:"gap,omitempty"`
}

type Evidence struct {
	Kind      string   `json:"kind"`
	Path      string   `json:"path"`
	Ref       string   `json:"ref,omitempty"`
	Command   string   `json:"command,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type Summary struct {
	Total    int            `json:"total"`
	Active   int            `json:"active"`
	Reserved int            `json:"reserved"`
	Accepted int            `json:"accepted"`
	ByStatus map[string]int `json:"byStatus"`
}

func Load(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read acceptance manifest: %w", err)
	}
	if len(contents) > maxManifestBytes {
		return Manifest{}, errors.New("acceptance manifest exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode acceptance manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("acceptance manifest must contain exactly one JSON value")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion || strings.TrimSpace(m.Specification.Name) == "" ||
		!hashPattern.MatchString(m.Specification.SHA256) || m.Specification.SHA256 != SpecSHA256 ||
		!hashPattern.MatchString(m.Specification.AcceptanceSHA256) || m.Specification.AcceptanceSHA256 != AcceptanceSHA256 {
		return errors.New("acceptance manifest specification identity is invalid")
	}
	stages := map[string]bool{"inventory": true, "prototype": true, "freeze": true, "production": true}
	if !stages[m.ReleaseStage] {
		return errors.New("acceptance manifest release stage is invalid")
	}
	if len(m.StatusDefinitions) != len(statusNames) {
		return errors.New("acceptance status definitions are incomplete")
	}
	for status := range statusNames {
		if strings.TrimSpace(m.StatusDefinitions[status]) == "" {
			return fmt.Errorf("acceptance status %s lacks a definition", status)
		}
	}
	if len(m.Criteria) != 88 {
		return fmt.Errorf("acceptance manifest has %d criteria, want 88", len(m.Criteria))
	}
	overrides := make(map[string]OwnershipOverride, len(m.OwnershipOverrides))
	for _, override := range m.OwnershipOverrides {
		wantOwner, required := requiredOwnershipOverrides[override.AC]
		if !acPattern.MatchString(override.AC) || !required || override.Owner != wantOwner || strings.TrimSpace(override.Rationale) == "" {
			return errors.New("acceptance ownership override is invalid")
		}
		if _, duplicate := overrides[override.AC]; duplicate {
			return fmt.Errorf("acceptance ownership override %s is duplicated", override.AC)
		}
		overrides[override.AC] = override
	}
	seen := make(map[int]struct{}, 88)
	criteriaByNumber := make(map[int]Criterion, 88)
	for _, criterion := range m.Criteria {
		if err := validateCriterion(criterion, m.ReleaseStage); err != nil {
			return err
		}
		if _, duplicate := seen[criterion.Number]; duplicate {
			return fmt.Errorf("%s is duplicated", criterion.ID)
		}
		seen[criterion.Number] = struct{}{}
		criteriaByNumber[criterion.Number] = criterion
	}
	for number := 1; number <= 88; number++ {
		if _, exists := seen[number]; !exists {
			return fmt.Errorf("AC-%d is missing", number)
		}
	}
	if acceptanceDigest(criteriaByNumber) != m.Specification.AcceptanceSHA256 {
		return errors.New("acceptance criteria do not match the pinned PRD table")
	}
	if len(overrides) != len(requiredOwnershipOverrides) {
		return errors.New("acceptance ownership overrides are incomplete")
	}
	for ac, owner := range requiredOwnershipOverrides {
		override, exists := overrides[ac]
		if !exists || override.Owner != owner {
			return fmt.Errorf("acceptance ownership override %s is missing", ac)
		}
		number := 0
		if _, err := fmt.Sscanf(ac, "AC-%d", &number); err != nil {
			return fmt.Errorf("acceptance ownership override %s cannot be resolved", ac)
		}
		if criterion, exists := criteriaByNumber[number]; !exists || criterion.PrimaryOwner != owner {
			return fmt.Errorf("acceptance ownership override %s does not match its criterion", ac)
		}
	}
	return nil
}

func validateCriterion(c Criterion, stage string) error {
	wantID := fmt.Sprintf("AC-%d", c.Number)
	if c.Number < 1 || c.Number > 88 || c.ID != wantID || !acPattern.MatchString(c.ID) ||
		strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Expectation) == "" {
		return fmt.Errorf("acceptance criterion %q identity is invalid", c.ID)
	}
	_, reserved := reservedACs[c.Number]
	if reserved {
		if c.Active || c.Status != "reserved" || c.PrimaryOwner != "" || len(c.Participants) != 0 ||
			len(c.Invariants) != 0 || len(c.Evidence) != 0 || len(c.RequiredArtifacts) != 0 || strings.TrimSpace(c.Gap) == "" {
			return fmt.Errorf("%s must remain inactive and reserved", c.ID)
		}
		return nil
	}
	if _, knownStatus := statusNames[c.Status]; !c.Active || !knownStatus || c.Status == "reserved" {
		return fmt.Errorf("%s active status is invalid", c.ID)
	}
	if _, knownOwner := ownerNames[c.PrimaryOwner]; !knownOwner ||
		len(c.Participants) == 0 || len(c.Invariants) == 0 || len(c.RequiredArtifacts) == 0 {
		return fmt.Errorf("%s active ownership, invariant, status, or artifact mapping is incomplete", c.ID)
	}
	participants := make(map[string]struct{}, len(c.Participants))
	for _, participant := range c.Participants {
		if _, knownOwner := ownerNames[participant]; !knownOwner {
			return fmt.Errorf("%s has an unknown participant", c.ID)
		}
		if _, duplicate := participants[participant]; duplicate {
			return fmt.Errorf("%s has duplicate participant %s", c.ID, participant)
		}
		participants[participant] = struct{}{}
	}
	if _, exists := participants[c.PrimaryOwner]; !exists {
		return fmt.Errorf("%s primary owner is not a participant", c.ID)
	}
	invariants := make(map[string]struct{}, len(c.Invariants))
	for _, invariant := range c.Invariants {
		if strings.TrimSpace(invariant) == "" {
			return fmt.Errorf("%s has an empty invariant", c.ID)
		}
		if _, duplicate := invariants[invariant]; duplicate {
			return fmt.Errorf("%s has duplicate invariant %s", c.ID, invariant)
		}
		invariants[invariant] = struct{}{}
	}
	requiredArtifacts := make(map[string]struct{}, len(c.RequiredArtifacts))
	for _, artifact := range c.RequiredArtifacts {
		if _, knownArtifact := artifactEvidenceKinds[artifact]; !knownArtifact {
			return fmt.Errorf("%s has an unknown required artifact %s", c.ID, artifact)
		}
		if _, duplicate := requiredArtifacts[artifact]; duplicate {
			return fmt.Errorf("%s has duplicate required artifact %s", c.ID, artifact)
		}
		requiredArtifacts[artifact] = struct{}{}
	}
	coveredArtifacts := make(map[string]struct{}, len(requiredArtifacts))
	evidenceKeys := make(map[string]struct{}, len(c.Evidence))
	for _, evidence := range c.Evidence {
		if err := validateEvidence(c.ID, evidence, requiredArtifacts, coveredArtifacts); err != nil {
			return err
		}
		key := evidence.Kind + "\x00" + evidence.Path + "\x00" + evidence.Ref
		if _, duplicate := evidenceKeys[key]; duplicate {
			return fmt.Errorf("%s has duplicate evidence %q", c.ID, evidence.Path)
		}
		evidenceKeys[key] = struct{}{}
	}
	if c.Status == "local_evidence" && len(c.Evidence) == 0 {
		return fmt.Errorf("%s claims local evidence without an adapter", c.ID)
	}
	if c.Status == "accepted" && (len(c.Evidence) == 0 || strings.TrimSpace(c.Gap) != "") {
		return fmt.Errorf("%s cannot be accepted without evidence and gap closure", c.ID)
	}
	if c.Status == "accepted" && len(coveredArtifacts) != len(requiredArtifacts) {
		return fmt.Errorf("%s cannot be accepted without evidence for every required artifact", c.ID)
	}
	if c.Status != "accepted" && strings.TrimSpace(c.Gap) == "" {
		return fmt.Errorf("%s must state its remaining gap", c.ID)
	}
	if stage == "prototype" && c.Number >= 67 && len(c.Evidence) == 0 {
		return fmt.Errorf("%s lacks the executable evidence required at prototype stage", c.ID)
	}
	if (stage == "freeze" || stage == "production") && c.Status != "accepted" {
		return fmt.Errorf("%s is not accepted at %s stage", c.ID, stage)
	}
	return nil
}

func acceptanceDigest(criteria map[int]Criterion) string {
	hash := sha256.New()
	for number := 1; number <= 88; number++ {
		criterion := criteria[number]
		activity := "reserved"
		if criterion.Active {
			activity = "active"
		}
		_, _ = io.WriteString(hash, criterion.ID+"\x00"+criterion.Title+"\x00"+criterion.Expectation+"\x00"+activity+"\n")
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func validateEvidence(ac string, evidence Evidence, requiredArtifacts, coveredArtifacts map[string]struct{}) error {
	kinds := map[string]bool{"go_test": true, "forge_test": true, "script": true, "evidence": true, "docs": true, "manual_external": true}
	clean := filepath.Clean(evidence.Path)
	if !kinds[evidence.Kind] || evidence.Path == "" || filepath.IsAbs(evidence.Path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s has an invalid evidence adapter", ac)
	}
	if (evidence.Kind == "go_test" || evidence.Kind == "forge_test" || evidence.Kind == "script") && strings.TrimSpace(evidence.Command) == "" {
		return fmt.Errorf("%s executable evidence lacks a command", ac)
	}
	if (evidence.Kind == "go_test" || evidence.Kind == "forge_test") &&
		(strings.TrimSpace(evidence.Ref) == "" || !strings.Contains(evidence.Command, evidence.Ref)) {
		return fmt.Errorf("%s test evidence command does not target its declared reference", ac)
	}
	seenArtifacts := make(map[string]struct{}, len(evidence.Artifacts))
	for _, artifact := range evidence.Artifacts {
		if _, required := requiredArtifacts[artifact]; !required {
			return fmt.Errorf("%s evidence covers unknown artifact %q", ac, artifact)
		}
		allowedKinds, knownArtifact := artifactEvidenceKinds[artifact]
		if !knownArtifact {
			return fmt.Errorf("%s requires unknown artifact type %q", ac, artifact)
		}
		if _, allowed := allowedKinds[evidence.Kind]; !allowed {
			return fmt.Errorf("%s evidence kind %s cannot cover artifact %q", ac, evidence.Kind, artifact)
		}
		if _, duplicate := seenArtifacts[artifact]; duplicate {
			return fmt.Errorf("%s evidence repeats artifact %q", ac, artifact)
		}
		seenArtifacts[artifact] = struct{}{}
		coveredArtifacts[artifact] = struct{}{}
	}
	return nil
}

func (m Manifest) Summary() Summary {
	result := Summary{Total: len(m.Criteria), ByStatus: make(map[string]int)}
	for _, criterion := range m.Criteria {
		result.ByStatus[criterion.Status]++
		if criterion.Active {
			result.Active++
		} else {
			result.Reserved++
		}
		if criterion.Status == "accepted" {
			result.Accepted++
		}
	}
	return result
}

func (s Summary) String() string {
	keys := make([]string, 0, len(s.ByStatus))
	for key := range s.ByStatus {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, s.ByStatus[key]))
	}
	return fmt.Sprintf("total=%d active=%d reserved=%d accepted=%d %s", s.Total, s.Active, s.Reserved, s.Accepted, strings.Join(parts, " "))
}
