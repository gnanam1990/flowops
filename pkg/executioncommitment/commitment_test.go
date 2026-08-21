package executioncommitment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const escrowContract = "0x1111111111111111111111111111111111111111"

func TestExecutionCommitmentGoldenValues(t *testing.T) {
	c := testCommitment()
	domain, err := DomainSeparator(c.ChainID, escrowContract)
	if err != nil {
		t.Fatal(err)
	}
	structHash, err := c.StructHash()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := c.Digest(escrowContract, c.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := domain.Hex(), "0x38fc76dc6879cd78bd1138e65f61e972f99be7612cd3661abc5d194886acc722"; got != want {
		t.Fatalf("domain separator %s", got)
	}
	if got, want := structHash.Hex(), "0x52530cc9fd292526ed273b18d321b5ac107a37c5e8d0241d034cbed0d556a66e"; got != want {
		t.Fatalf("struct hash %s", got)
	}
	if got, want := digest.Hex(), "0xa12a57a1ebc376e573b7ebaaee4bec4ca7dfcdec16fbef024852e9028882337c"; got != want {
		t.Fatalf("digest %s", got)
	}
}

func TestExecutionCommitmentRejectsWireAndDomainSubstitution(t *testing.T) {
	c := testCommitment()
	if _, err := c.Digest("0x2222222222222222222222222222222222222222", c.ChainID); !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("escrow substitution error = %v", err)
	}
	if _, err := c.Digest(escrowContract, "8453"); !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("chain substitution error = %v", err)
	}
	c.PayTo = "0x2222222222222222222222222222222222222222"
	first, err := c.Digest(escrowContract, c.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	if first == mustDigest(t, testCommitment()) {
		t.Fatal("payout substitution did not change digest")
	}
	c = testCommitment()
	c.Amount = "042"
	if err := c.Validate(); !errors.Is(err, ErrInvalidCommitment) {
		t.Fatalf("amount encoding error = %v", err)
	}
	c = testCommitment()
	c.AcceptBy = c.DeliverBy
	if err := c.Validate(); !errors.Is(err, ErrInvalidCommitment) {
		t.Fatalf("deadline order error = %v", err)
	}
	c = testCommitment()
	c.Rail = 2
	if err := c.Validate(); !errors.Is(err, ErrInvalidCommitment) {
		t.Fatalf("rail error = %v", err)
	}
}

func TestPublishedVectorAndIntegrityManifest(t *testing.T) {
	root := repositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "vectors", "execution-commitment-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		TypeString string `json:"typeString"`
		Domain     struct {
			ChainID           string `json:"chainId"`
			VerifyingContract string `json:"verifyingContract"`
			Separator         string `json:"separator"`
		} `json:"domain"`
		Commitment Commitment `json:"commitment"`
		StructHash string     `json:"structHash"`
		Digest     string     `json:"digest"`
	}
	if err := json.Unmarshal(contents, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.TypeString != TypeString {
		t.Fatalf("vector type string drifted: %q", vector.TypeString)
	}
	domain, err := DomainSeparator(vector.Domain.ChainID, vector.Domain.VerifyingContract)
	if err != nil || domain.Hex() != vector.Domain.Separator {
		t.Fatalf("vector domain=%s err=%v", domain.Hex(), err)
	}
	structHash, err := vector.Commitment.StructHash()
	if err != nil || structHash.Hex() != vector.StructHash {
		t.Fatalf("vector struct hash=%s err=%v", structHash.Hex(), err)
	}
	digest, err := vector.Commitment.Digest(vector.Domain.VerifyingContract, vector.Domain.ChainID)
	if err != nil || digest.Hex() != vector.Digest {
		t.Fatalf("vector digest=%s err=%v", digest.Hex(), err)
	}

	manifest, err := os.ReadFile(filepath.Join(root, "artifacts", "execution-commitment-v1.manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	expectedPaths := map[string]struct{}{"schemas/execution-commitment.schema.json": {}, "vectors/execution-commitment-v1.json": {}}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatalf("invalid manifest line %q", line)
		}
		if _, ok := expectedPaths[fields[1]]; !ok {
			t.Fatalf("unexpected manifest path %q", fields[1])
		}
		delete(expectedPaths, fields[1])
		artifact, err := os.ReadFile(filepath.Join(root, fields[1]))
		if err != nil {
			t.Fatal(err)
		}
		if ArtifactSHA256(artifact) != fields[0] {
			t.Fatalf("manifest hash mismatch for %s", fields[1])
		}
	}
	if len(expectedPaths) != 0 {
		t.Fatalf("manifest does not cover %v", expectedPaths)
	}
}

func testCommitment() Commitment {
	return Commitment{
		OrgDomain: hash(1), OperationID: hash(2), Rail: RailEscrow, SchemeVersion: SchemeVersionV1, Protection: ProtectionEscrow,
		EscrowContract: escrowContract, PurchaseSpecHash: hash(3), QuoteHash: hash(4), VerificationSpecHash: hash(5),
		DeclaredWorkTime: 300, VerificationBudgetSeconds: 120, DirectoryVersion: 9, SellerID: hash(6), ResourceID: hash(7),
		PayTo: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444",
		Amount: "42", ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		QuoteExpiresAt: 1_900_000_000, AcceptBy: 1_900_000_100, DeliverBy: 1_900_000_500, SettleBy: 1_900_002_400,
	}
}

func mustDigest(t *testing.T, c Commitment) [32]byte {
	t.Helper()
	value, err := c.Digest(escrowContract, c.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func hash(value uint64) string {
	return "0x000000000000000000000000000000000000000000000000000000000000000" + string(rune('0'+value))
}
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
