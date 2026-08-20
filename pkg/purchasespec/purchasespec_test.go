package purchasespec

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildGoldenPurchaseSpec(t *testing.T) {
	result, err := Build(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Spec.CanonicalURL, "https://xn--exmple-cua.com/a/c/~?a=1&a=~&b=2"; got != want {
		t.Fatalf("canonical URL %q, want %q", got, want)
	}
	if got, want := strings.Join(result.StrippedTransportHeader, ","), "accept-encoding,traceparent"; got != want {
		t.Fatalf("stripped headers %q, want %q", got, want)
	}
	if got, want := string(result.CanonicalJSON), `{"orgId":"org_1","agentId":"agent_1","taskId":"task_1","method":"POST","canonicalURL":"https://xn--exmple-cua.com/a/c/~?a=1&a=~&b=2","requestBodyHash":"0x249a5e4bc4b0e6f0e331d79be8d3ecf838873594d09bc9b2debdb09ebeabec8d","headerBindings":[{"lowercaseName":"content-type","valueHash":"0x82e6a468c95da6cfe399f69ee0782fd009e354a8030ea5636ea9c7db0edcf7f5"},{"lowercaseName":"x-request-id","valueHash":"0x5521a70e306e4cf143fc3b54739d4053a40cc9765dbc2f1d2c86a2ea55e5dfe0"}],"responseContract":{"contentType":"application/json","schemaRef":"schema:result-v1"},"category":"research","reasonRef":{"blobRef":"blob://reasons/1","contentHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`; got != want {
		t.Fatalf("canonical JSON %s, want %s", got, want)
	}
	if got, want := result.PurchaseSpecHash, "0x27edff0b7e17cf874b69f6b98838dc4f2b556232b1a73461ccb3ad753b0e7d7a"; got != want {
		t.Fatalf("purchaseSpecHash %s, want %s", got, want)
	}
}

func TestPublishedVector(t *testing.T) {
	root := repositoryRoot(t)
	vectorBytes, err := os.ReadFile(filepath.Join(root, "vectors", "purchase-spec-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		DomainTag        string `json:"domainTag"`
		CanonicalJSON    string `json:"canonicalJSON"`
		PurchaseSpecHash string `json:"purchaseSpecHash"`
	}
	if err := json.Unmarshal(vectorBytes, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.DomainTag != DomainTag {
		t.Fatalf("domain tag %q", vector.DomainTag)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(vector.CanonicalJSON), &spec); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(spec)
	if err != nil || string(canonical) != vector.CanonicalJSON {
		t.Fatalf("vector canonical JSON %s err=%v", canonical, err)
	}
	if got := keccakHex(canonical); got != vector.PurchaseSpecHash {
		t.Fatalf("vector hash %s, want %s", got, vector.PurchaseSpecHash)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "artifacts", "purchase-spec-v1.manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	expectedPaths := map[string]struct{}{"schemas/purchase-spec.schema.json": {}, "vectors/purchase-spec-v1.json": {}}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatalf("invalid manifest line %q", line)
		}
		if _, found := expectedPaths[fields[1]]; !found {
			t.Fatalf("unexpected manifest path %q", fields[1])
		}
		delete(expectedPaths, fields[1])
		contents, err := os.ReadFile(filepath.Join(root, fields[1]))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256Hex(contents); got != fields[0] {
			t.Fatalf("manifest hash for %s = %s", fields[1], got)
		}
	}
	if len(expectedPaths) != 0 {
		t.Fatalf("manifest misses %v", expectedPaths)
	}
}

func TestValidatePersistedRejectsByteAndCanonicalizationDrift(t *testing.T) {
	input := testInput()
	result, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePersisted(result.CanonicalJSON, input.Body); err != nil {
		t.Fatalf("valid persisted spec error = %v", err)
	}
	if _, err := ValidatePersisted(result.CanonicalJSON, []byte(`{"query":"changed"}`)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("changed persisted body error = %v", err)
	}
	if _, err := ValidatePersisted(append([]byte(" "), result.CanonicalJSON...), input.Body); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("noncanonical persisted JSON error = %v", err)
	}
}

func TestBuildRejectsAgentHeaderAndBindsExactBody(t *testing.T) {
	input := testInput()
	input.Headers = append(input.Headers, Header{Name: "Authorization", Value: "credential-must-never-reach-seller"})
	if _, err := Build(input); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("prohibited header error = %v", err)
	}
	input = testInput()
	first, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Body = []byte(`{"query":"changed"}`)
	second, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PurchaseSpecHash == second.PurchaseSpecHash || first.Spec.RequestBodyHash == second.Spec.RequestBodyHash {
		t.Fatal("changed exact body did not change purchase binding")
	}
	input.Method = "GET"
	if _, err := Build(input); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("GET with body error = %v", err)
	}
	input.Body = nil
	third, err := Build(input)
	if err != nil || third.Spec.RequestBodyHash != "" {
		t.Fatalf("GET body binding=%q err=%v", third.Spec.RequestBodyHash, err)
	}
}

func TestCanonicalURLRejectsAmbiguityAndPreservesDuplicateQueryKeys(t *testing.T) {
	for _, raw := range []string{
		"http://seller.example/a", "https://seller.example/a#fragment", "https://user@seller.example/a",
		"https://seller.example/a?x=%zz", "https://seller.example/a?&&", "https://seller.example/a\n",
	} {
		if _, err := CanonicalURL(raw); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("CanonicalURL(%q) error = %v", raw, err)
		}
	}
	got, err := CanonicalURL("https://[2001:db8::1]:8443/a/../b?z=2&z=1&x=%7e")
	if err != nil || got != "https://[2001:db8::1]:8443/b?x=~&z=1&z=2" {
		t.Fatalf("IPv6/query canonical URL %q err=%v", got, err)
	}
	got, err = CanonicalURL("https://seller.example/a?q=é")
	if err != nil || got != "https://seller.example/a?q=%C3%A9" {
		t.Fatalf("Unicode query canonical URL %q err=%v", got, err)
	}
}

func TestBuildRejectsDuplicateHeadersAndInvalidReason(t *testing.T) {
	input := testInput()
	input.Headers = append(input.Headers, Header{Name: "X-Request-ID", Value: "other"})
	if _, err := Build(input); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("duplicate header error = %v", err)
	}
	input = testInput()
	input.Headers = append(input.Headers, Header{Name: "traceparent", Value: "duplicate-generated-header"})
	if _, err := Build(input); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("duplicate transport header error = %v", err)
	}
	input = testInput()
	input.ReasonRef.ContentHash = "0x01"
	if _, err := Build(input); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("invalid reason hash error = %v", err)
	}
	input = testInput()
	input.ReasonRef.ContentHash = "0x" + strings.Repeat("0", 64)
	if _, err := Build(input); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("zero reason hash error = %v", err)
	}
}

func testInput() Input {
	return Input{
		OrgID: "org_1", AgentID: "agent_1", TaskID: "task_1", Method: "post",
		URL:  "HTTPS://Exämple.com:443/a/./b/../c/%7e?b=2&a=%7e&a=1",
		Body: []byte(`{"query":"status"}`),
		Headers: []Header{
			{Name: "X-Request-ID", Value: " req-1 "}, {Name: "Content-Type", Value: "application/json"},
			{Name: "Traceparent", Value: "agent-value-is-stripped"}, {Name: "Accept-Encoding", Value: "agent-value-is-stripped"},
		},
		Response: ResponseContract{ContentType: "application/json", SchemaRef: "schema:result-v1"}, Category: "research",
		ReasonRef: &ReasonRef{BlobRef: "blob://reasons/1", ContentHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
