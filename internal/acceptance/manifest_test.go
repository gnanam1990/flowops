package acceptance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRepositoryManifestIsCompleteAndTruthful(t *testing.T) {
	manifest, err := loadRepositoryManifest()
	if err != nil {
		t.Fatal(err)
	}
	summary := manifest.Summary()
	if summary.Total != 88 || summary.Active != 83 || summary.Reserved != 5 || summary.Accepted != 0 {
		t.Fatalf("unexpected inventory summary: %+v", summary)
	}
}

func TestValidationRejectsPromotedClaimsWithoutEvidence(t *testing.T) {
	manifest, err := loadRepositoryManifest()
	if err != nil {
		t.Fatal(err)
	}
	copy := manifest
	copy.Criteria = append([]Criterion(nil), manifest.Criteria...)
	copy.Criteria[0].Status = "accepted"
	copy.Criteria[0].Gap = ""
	if err := copy.Validate(); err == nil {
		t.Fatal("accepted criterion without artifact coverage was allowed")
	}

	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("manifest JSON round trip failed: %v", err)
	}
}

func TestValidationRejectsInventoryTampering(t *testing.T) {
	tests := map[string]func(*Manifest){
		"specification hash": func(manifest *Manifest) {
			manifest.Specification.SHA256 = strings.Repeat("0", 64)
		},
		"acceptance clause weakened": func(manifest *Manifest) {
			manifest.Criteria[0].Expectation = "something passed"
		},
		"missing ownership override": func(manifest *Manifest) {
			manifest.OwnershipOverrides = manifest.OwnershipOverrides[1:]
		},
		"reserved criterion activated": func(manifest *Manifest) {
			manifest.Criteria[19].Active = true
		},
		"duplicate criterion": func(manifest *Manifest) {
			manifest.Criteria[1].Number = manifest.Criteria[0].Number
			manifest.Criteria[1].ID = manifest.Criteria[0].ID
		},
		"path traversal": func(manifest *Manifest) {
			manifest.Criteria[2].Evidence[0].Path = "../outside"
		},
		"unknown artifact coverage": func(manifest *Manifest) {
			manifest.Criteria[2].Evidence[0].Artifacts = []string{"invented-proof"}
		},
		"unknown required artifact": func(manifest *Manifest) {
			manifest.Criteria[2].RequiredArtifacts[0] = "invented-proof"
		},
		"duplicate invariant": func(manifest *Manifest) {
			manifest.Criteria[2].Invariants = append(manifest.Criteria[2].Invariants, manifest.Criteria[2].Invariants[0])
		},
		"wrong artifact evidence kind": func(manifest *Manifest) {
			manifest.Criteria[2].Evidence[0].Artifacts = []string{"manifest-hash"}
		},
		"stale evidence command": func(manifest *Manifest) {
			manifest.Criteria[2].Evidence[0].Command = "go test ./internal/ascpapproval -run '^WrongTest$'"
		},
		"freeze with open criteria": func(manifest *Manifest) {
			manifest.ReleaseStage = "freeze"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest, err := loadRepositoryManifest()
			if err != nil {
				t.Fatal(err)
			}
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("tampered manifest was allowed")
			}
		})
	}
}

func TestValidationDoesNotDependOnCriterionOrder(t *testing.T) {
	manifest, err := loadRepositoryManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Criteria[0], manifest.Criteria[87] = manifest.Criteria[87], manifest.Criteria[0]
	if err := manifest.Validate(); err != nil {
		t.Fatalf("criterion reordering changed manifest validity: %v", err)
	}
}

func TestLoadRejectsTrailingAndOversizedInput(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "docs", "acceptance", "ascp-v3.4.json")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"trailing JSON": append(append([]byte(nil), contents...), []byte("\n{}")...),
		"oversized":     bytes.Repeat([]byte("x"), maxManifestBytes+1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, input, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("malformed manifest was allowed")
			}
		})
	}
}

func TestRepositoryManifestEvidenceReferencesResolve(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	manifest, err := Load(filepath.Join(repositoryRoot, "docs", "acceptance", "ascp-v3.4.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, criterion := range manifest.Criteria {
		for _, evidence := range criterion.Evidence {
			path := filepath.Join(repositoryRoot, evidence.Path)
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s evidence %q cannot be read: %v", criterion.ID, evidence.Path, err)
				continue
			}
			if evidence.Ref != "" {
				prefix := "func"
				if evidence.Kind == "forge_test" {
					prefix = "function"
				}
				declaration := regexp.MustCompile(`\b` + prefix + `\s+` + regexp.QuoteMeta(evidence.Ref) + `\s*\(`)
				if !declaration.Match(contents) {
					t.Errorf("%s evidence declaration %q is absent from %q", criterion.ID, evidence.Ref, evidence.Path)
				}
			}
		}
	}
}

func loadRepositoryManifest() (Manifest, error) {
	return Load(filepath.Join("..", "..", "docs", "acceptance", "ascp-v3.4.json"))
}
