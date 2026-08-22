package architecturedeps

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAC9ManifestPinsExecutableEvidence(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "artifacts", "ac9-structural-isolation.manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 {
		t.Fatalf("manifest entries = %d, want 4", len(lines))
	}
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 || filepath.IsAbs(fields[1]) || strings.Contains(fields[1], "..") {
			t.Fatalf("invalid manifest line %q", line)
		}
		if _, duplicate := seen[fields[1]]; duplicate {
			t.Fatalf("duplicate manifest path %q", fields[1])
		}
		seen[fields[1]] = struct{}{}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fields[1])))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		if hex.EncodeToString(digest[:]) != fields[0] {
			t.Fatalf("manifest hash mismatch for %s", fields[1])
		}
	}
}

func TestProductionDependencyGraphSatisfiesStructuralIsolation(t *testing.T) {
	root := repositoryRoot(t)
	graph, err := LoadProductionGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	if violations := Validate(graph, ProductionPolicy()); len(violations) != 0 {
		messages := make([]string, len(violations))
		for index, violation := range violations {
			messages[index] = violation.Error()
		}
		t.Fatalf("structural isolation failed:\n%s", strings.Join(messages, "\n"))
	}
	assertNoEmbeddedChainWriter(t, root)
}

func TestMetaTestCatchesPlantedDirectAndTransitiveViolations(t *testing.T) {
	const (
		agent  = ModulePath + "/internal/ascpagent"
		bridge = ModulePath + "/internal/innocentbridge"
		signer = ModulePath + "/internal/ascpsignerruntime"
		keeper = ModulePath + "/internal/ascpkeeper"
		shared = ModulePath + "/pkg/purchasespec"
	)
	policy := Policy{Isolation: []IsolationRule{{Name: "agent fence", Roots: []string{agent}, Forbidden: []string{signer, keeper}}}, SharedPath: []SharedPathRule{{Name: "shared canonicalization", Roots: []string{agent}, Shared: shared}}}

	direct := Graph{agent: {signer, shared}, signer: nil, shared: nil}
	violations := Validate(direct, policy)
	if len(violations) != 1 || strings.Join(violations[0].Path, " -> ") != agent+" -> "+signer {
		t.Fatalf("direct planted violation not caught: %+v", violations)
	}

	transitive := Graph{agent: {bridge, shared}, bridge: {keeper}, keeper: nil, shared: nil}
	violations = Validate(transitive, policy)
	if len(violations) != 1 || strings.Join(violations[0].Path, " -> ") != agent+" -> "+bridge+" -> "+keeper {
		t.Fatalf("transitive planted violation not caught: %+v", violations)
	}

	missingShared := Graph{agent: nil}
	violations = Validate(missingShared, policy)
	if len(violations) != 1 || !strings.Contains(violations[0].Error(), "does not reach") {
		t.Fatalf("shared-library bypass not caught: %+v", violations)
	}

	directRPC := Graph{agent: {"github.com/ethereum/go-ethereum/rpc", shared}, shared: nil}
	rpcPolicy := Policy{Isolation: []IsolationRule{{Name: "agent RPC fence", Roots: []string{agent}, Forbidden: []string{"github.com/ethereum/go-ethereum/rpc"}}}}
	violations = Validate(directRPC, rpcPolicy)
	if len(violations) != 1 || !strings.Contains(violations[0].Error(), "go-ethereum/rpc") {
		t.Fatalf("direct chain RPC import not caught: %+v", violations)
	}
}

func TestLoaderExcludesTestOnlyAuthorityAssembly(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/agent/agent.go", "package agent\nimport _ \""+ModulePath+"/pkg/shared\"\n")
	write("internal/agent/agent_test.go", "package agent\nimport _ \""+ModulePath+"/internal/keeper\"\n")
	graph, err := LoadProductionGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	want := ModulePath + "/pkg/shared"
	if imports := graph[ModulePath+"/internal/agent"]; len(imports) != 1 || imports[0] != want {
		t.Fatalf("production imports = %v, want [%s]", imports, want)
	}
}

func assertNoEmbeddedChainWriter(t *testing.T, root string) {
	t.Helper()
	allowed := filepath.Join(root, "internal", "ascpkeeper") + string(filepath.Separator)
	for _, base := range []string{filepath.Join(root, "cmd"), filepath.Join(root, "internal")} {
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if ignoredDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") || !strings.HasSuffix(path, ".go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "eth_sendRawTransaction") && !strings.HasPrefix(path, allowed) {
				t.Errorf("chain-write RPC embedded outside keeper relay: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for current := working; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("repository root not found")
		}
	}
}
