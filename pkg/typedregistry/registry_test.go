package typedregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gnanam1990/flowops/internal/ascpverifier"
	"github.com/gnanam1990/flowops/pkg/adminauthorization"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	"github.com/gnanam1990/flowops/pkg/spendauthorization"
)

func TestNormativeRegistryPinsEveryGoTypeAndArtifact(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(source), "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "schemas", "ascp-typed-data-v4.registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Domain struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			TypeString string `json:"typeString"`
		} `json:"domain"`
		Types map[string]struct {
			TypeString string `json:"typeString"`
			Schema     string `json:"schema"`
			Vector     string `json:"vector"`
		} `json:"types"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	if registry.Domain.Name != "ASCP" || registry.Domain.Version != "4" ||
		registry.Domain.TypeString != "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)" {
		t.Fatal("normative EIP-712 domain drifted")
	}
	wants := map[string]string{
		"ExecutionCommitment":      executioncommitment.TypeString,
		"SellerQuote":              sellerquote.TypeString,
		"LockAuthorization":        spendauthorization.LockTypeString,
		"AllowanceAuthorization":   spendauthorization.AllowanceTypeString,
		"AdminActionAuthorization": adminauthorization.TypeString,
		"VerdictAttestation":       ascpverifier.VerdictAttestationTypeString,
	}
	if len(registry.Types) != len(wants) {
		t.Fatalf("registry has %d types, want %d", len(registry.Types), len(wants))
	}
	for name, want := range wants {
		entry, ok := registry.Types[name]
		if !ok || entry.TypeString != want {
			t.Fatalf("%s type string drifted", name)
		}
		for _, artifact := range []string{entry.Schema, entry.Vector} {
			if artifact == "" {
				t.Fatalf("%s has an empty artifact path", name)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact))); err != nil {
				t.Fatalf("%s artifact %s: %v", name, artifact, err)
			}
		}
		vectorRaw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Vector)))
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			TypeString string `json:"typeString"`
		}
		if err := json.Unmarshal(vectorRaw, &vector); err != nil || vector.TypeString != want {
			t.Fatalf("%s vector type string drifted: %v", name, err)
		}
	}
}

func TestTypedDataArtifactManifestPinsRegistrySchemasAndVectors(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(source), "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "artifacts", "ascp-typed-data-v4.manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(raw)
	if hex.EncodeToString(manifestSum[:]) != ManifestSHA256 {
		t.Fatal("compiled Go manifest pin drifted")
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 13 {
		t.Fatalf("manifest has %d entries, want 13", len(lines))
	}
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid manifest line %q", line)
		}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fields[1])))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(contents)
		if hex.EncodeToString(sum[:]) != fields[0] {
			t.Fatalf("manifest hash mismatch for %s", fields[1])
		}
		if _, duplicate := seen[fields[1]]; duplicate {
			t.Fatalf("manifest repeats %s", fields[1])
		}
		seen[fields[1]] = struct{}{}
	}
}
