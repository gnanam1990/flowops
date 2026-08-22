package mcp

import (
	"os/exec"
	"strings"
	"testing"
)

var forbiddenBoundaryDependencies = []string{
	"github.com/ethereum/go-ethereum",
	"github.com/gnanam1990/flowops/internal/ascpkeeper",
	"github.com/gnanam1990/flowops/internal/ascprails",
	"github.com/gnanam1990/flowops/internal/ascpsignerruntime",
	"github.com/gnanam1990/flowops/internal/reconciliation",
	"github.com/gnanam1990/flowops/internal/x402adapter",
	"github.com/gnanam1990/flowops/pkg/referencesigner",
	"github.com/gnanam1990/flowops/pkg/referencewallet",
}

func TestMCPProductionDependencyGraphHasNoWalletChainOrSignerPath(t *testing.T) {
	for _, packagePath := range []string{".", "../basemcp"} {
		command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", packagePath)
		output, err := command.Output()
		if err != nil {
			t.Fatalf("list %s production dependencies: %v", packagePath, err)
		}
		if dependency := firstForbiddenDependency(strings.Fields(string(output)), forbiddenBoundaryDependencies); dependency != "" {
			t.Fatalf("%s imports forbidden production capability %s", packagePath, dependency)
		}
	}
}

func TestDependencyGraphMetaTestDetectsPlantedViolation(t *testing.T) {
	planted := "github.com/gnanam1990/flowops/internal/ascpkeeper/runtime"
	dependencies := []string{"context", "net/http", planted}
	if got := firstForbiddenDependency(dependencies, forbiddenBoundaryDependencies); got != planted {
		t.Fatalf("planted dependency result=%q", got)
	}
}

func firstForbiddenDependency(dependencies, forbidden []string) string {
	for _, dependency := range dependencies {
		for _, prefix := range forbidden {
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				return dependency
			}
		}
	}
	return ""
}
