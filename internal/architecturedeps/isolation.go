// Package architecturedeps enforces FlowOps' compile-time authority boundaries.
package architecturedeps

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const ModulePath = "github.com/gnanam1990/flowops"

// Graph contains production-package imports. Test-only imports are deliberately
// excluded because tests may assemble multiple isolated components in-process.
type Graph map[string][]string

type IsolationRule struct {
	Name      string
	Roots     []string
	Forbidden []string
}

type SharedPathRule struct {
	Name   string
	Roots  []string
	Shared string
}

type Policy struct {
	Isolation  []IsolationRule
	SharedPath []SharedPathRule
}

type Violation struct {
	Rule string
	Path []string
}

func (v Violation) Error() string {
	return fmt.Sprintf("%s: %s", v.Rule, strings.Join(v.Path, " -> "))
}

// ProductionPolicy is the normative AC-9 package policy. A prefix names the
// package and all of its descendants.
func ProductionPolicy() Policy {
	agentForbidden := []string{
		ModulePath + "/internal/ascpapproval",
		ModulePath + "/internal/ascpbearer",
		ModulePath + "/internal/ascpexecauth",
		ModulePath + "/internal/ascpkeeper",
		ModulePath + "/internal/ascprails",
		ModulePath + "/internal/ascpring6",
		ModulePath + "/internal/ascpsignerruntime",
		ModulePath + "/internal/ascpworkflow",
		ModulePath + "/internal/controlapi",
		ModulePath + "/pkg/referencesigner",
		ModulePath + "/pkg/referencewallet",
		"github.com/ethereum/go-ethereum/ethclient",
		"github.com/ethereum/go-ethereum/rpc",
	}
	verifierForbidden := append(append([]string{}, agentForbidden...),
		ModulePath+"/internal/ascpagent",
		ModulePath+"/internal/ascpintake",
	)
	relayForbidden := []string{
		ModulePath + "/internal/ascpkeeper",
		ModulePath + "/pkg/referencesigner",
		ModulePath + "/pkg/referencewallet",
		"github.com/ethereum/go-ethereum/ethclient",
		"github.com/ethereum/go-ethereum/rpc",
	}
	return Policy{
		Isolation: []IsolationRule{
			{Name: "MCP agent boundary cannot reach owner or execution authority", Roots: []string{ModulePath + "/internal/mcp"}, Forbidden: agentForbidden},
			{Name: "Base MCP adapter cannot reach owner or execution authority", Roots: []string{ModulePath + "/internal/basemcp"}, Forbidden: agentForbidden},
			{Name: "agent application cannot reach owner or execution authority", Roots: []string{ModulePath + "/internal/ascpagent"}, Forbidden: agentForbidden},
			{Name: "verifier cannot reach intent, spend signer, seller egress, or relay", Roots: []string{ModulePath + "/cmd/ascp-verifier"}, Forbidden: verifierForbidden},
			{Name: "control plane cannot import a chain relay", Roots: []string{ModulePath + "/cmd/control-plane-api"}, Forbidden: relayForbidden},
			{Name: "signer runtime cannot import a chain relay", Roots: []string{ModulePath + "/cmd/ascp-signer-runtime"}, Forbidden: relayForbidden},
			{Name: "seller worker cannot import a chain relay", Roots: []string{ModulePath + "/cmd/ascp-seller-worker"}, Forbidden: relayForbidden},
			{Name: "verifier runtime cannot import a chain relay", Roots: []string{ModulePath + "/cmd/ascp-verifier"}, Forbidden: relayForbidden},
		},
		SharedPath: []SharedPathRule{
			{Name: "agent uses shared PurchaseSpec JCS and URL canonicalization", Roots: []string{ModulePath + "/internal/ascpagent"}, Shared: ModulePath + "/pkg/purchasespec"},
			{Name: "intake uses shared PurchaseSpec JCS and URL canonicalization", Roots: []string{ModulePath + "/internal/ascpintake"}, Shared: ModulePath + "/pkg/purchasespec"},
			{Name: "authorization revalidation uses shared PurchaseSpec canonicalization", Roots: []string{ModulePath + "/internal/ascpexecauth"}, Shared: ModulePath + "/pkg/purchasespec"},
			{Name: "seller egress uses shared PurchaseSpec canonicalization", Roots: []string{ModulePath + "/internal/ascprails"}, Shared: ModulePath + "/pkg/purchasespec"},
			{Name: "directory reader uses shared Merkle verification", Roots: []string{ModulePath + "/internal/directoryreader"}, Shared: ModulePath + "/pkg/directoryproof"},
		},
	}
}

func LoadProductionGraph(root string) (Graph, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	graph := Graph{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && ignoredDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("source path escaped repository root")
		}
		packagePath := ModulePath
		if relative != "." {
			packagePath += "/" + filepath.ToSlash(relative)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse imports %s: %w", path, err)
		}
		imports := graph[packagePath]
		for _, spec := range parsed.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("decode import %s: %w", path, err)
			}
			imports = append(imports, value)
		}
		graph[packagePath] = uniqueSorted(imports)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return graph, nil
}

func Validate(graph Graph, policy Policy) []Violation {
	var violations []Violation
	for _, rule := range policy.Isolation {
		for _, root := range rule.Roots {
			if _, exists := graph[root]; !exists {
				violations = append(violations, Violation{Rule: rule.Name, Path: []string{root, "<missing root>"}})
				continue
			}
			if path := firstForbiddenPath(graph, root, rule.Forbidden); len(path) != 0 {
				violations = append(violations, Violation{Rule: rule.Name, Path: path})
			}
		}
	}
	for _, rule := range policy.SharedPath {
		for _, root := range rule.Roots {
			if _, exists := graph[root]; !exists {
				violations = append(violations, Violation{Rule: rule.Name, Path: []string{root, "<missing root>"}})
				continue
			}
			if !reachable(graph, root, rule.Shared) {
				violations = append(violations, Violation{Rule: rule.Name, Path: []string{root, "<does not reach>", rule.Shared}})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Error() < violations[j].Error() })
	return violations
}

func firstForbiddenPath(graph Graph, root string, forbidden []string) []string {
	queue := []string{root}
	parent := map[string]string{root: ""}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if current != root && matchesAny(current, forbidden) {
			return buildPath(parent, current)
		}
		for _, next := range graph[current] {
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = current
			queue = append(queue, next)
		}
	}
	return nil
}

func reachable(graph Graph, root, target string) bool {
	queue := []string{root}
	seen := map[string]struct{}{root: {}}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if matchesPrefix(current, target) {
			return true
		}
		for _, next := range graph[current] {
			if _, exists := seen[next]; !exists {
				seen[next] = struct{}{}
				queue = append(queue, next)
			}
		}
	}
	return false
}

func buildPath(parent map[string]string, last string) []string {
	path := []string{last}
	for parent[last] != "" {
		last = parent[last]
		path = append(path, last)
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

func matchesAny(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if matchesPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func matchesPrefix(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	output := values[:0]
	for _, value := range values {
		if len(output) == 0 || output[len(output)-1] != value {
			output = append(output, value)
		}
	}
	return output
}

func ignoredDirectory(name string) bool {
	return name == ".git" || name == "node_modules" || name == "vendor" || name == "out" || name == "cache" || strings.HasPrefix(name, ".")
}
