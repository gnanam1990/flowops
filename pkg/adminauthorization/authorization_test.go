package adminauthorization

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdminAuthorizationGoldenVector(t *testing.T) {
	a := vectorAuthorization()
	domain, err := DomainSeparator(a.ChainID, a.ContractAddress)
	if err != nil {
		t.Fatal(err)
	}
	structHash, err := a.StructHash()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}

	wants := map[string][2]string{
		"domain": {domain.Hex(), "0x7b3124ffa73fe66d0496edae8e4c80239b7d8bdb4b4610cff4afd23a479e4a82"},
		"struct": {structHash.Hex(), "0x8b7fcce4348d879f4e6ef090719a34351b3f90e5b03ba8eafdd0949701449497"},
		"digest": {digest.Hex(), "0x7b7f08dd98d5de1302ff68757f58617706be5d90c7e36bbb0d42defb51838327"},
	}
	for name, pair := range wants {
		if pair[0] != pair[1] {
			t.Errorf("%s got %s want %s", name, pair[0], pair[1])
		}
	}
}

func TestAdminAuthorizationMutationsChangeDigest(t *testing.T) {
	base := vectorAuthorization()
	want := mustDigest(t, base)
	mutations := []func(*Authorization){
		func(a *Authorization) { a.OrgDomain = hash("09") },
		func(a *Authorization) { a.ContractAddress = "0x2222222222222222222222222222222222222222" },
		func(a *Authorization) { a.ChainID = "84532" },
		func(a *Authorization) { a.AuthorityRole = hash("08") },
		func(a *Authorization) { a.FunctionSelector = "0x4af1aeb9" },
		func(a *Authorization) { a.PayloadHash = hash("07") },
		func(a *Authorization) { a.AdminOperationID = hash("06") },
		func(a *Authorization) { a.AdminNonce = "43" },
		func(a *Authorization) { a.AdminEpoch++ },
		func(a *Authorization) { a.ValidBefore-- },
		func(a *Authorization) { a.WorkflowID = hash("10") },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if got := mustDigest(t, candidate); got == want {
			t.Fatalf("mutation %d preserved digest", i)
		}
	}
}

func TestAdminAuthorizationRejectsAmbiguousAndLongWindowValues(t *testing.T) {
	tests := []func(*Authorization){
		func(a *Authorization) { a.ChainID = "08453" },
		func(a *Authorization) { a.AdminNonce = "00" },
		func(a *Authorization) { a.FunctionSelector = "0x00000000" },
		func(a *Authorization) { a.AdminOperationID = hash("00") },
		func(a *Authorization) { a.ValidBefore = a.ValidAfter + MaximumWindowSeconds + 1 },
		func(a *Authorization) { a.ValidBefore = a.ValidAfter },
		func(a *Authorization) { a.AdminEpoch = 0 },
	}
	for i, mutate := range tests {
		candidate := vectorAuthorization()
		mutate(&candidate)
		if _, err := candidate.Digest(); !errors.Is(err, ErrInvalidAuthorization) {
			t.Fatalf("invalid case %d error=%v", i, err)
		}
	}
	zeroWorkflow := vectorAuthorization()
	zeroWorkflow.WorkflowID = hash("00")
	if _, err := zeroWorkflow.Digest(); err != nil {
		t.Fatalf("optional zero workflow: %v", err)
	}
}

func TestPublishedAdminAuthorizationVector(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find package path")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "vectors", "admin-action-authorization-v1.json"))
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
		Authorization Authorization `json:"authorization"`
		StructHash    string        `json:"structHash"`
		Digest        string        `json:"digest"`
	}
	if err := json.Unmarshal(contents, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.TypeString != TypeString {
		t.Fatalf("type string=%q", vector.TypeString)
	}
	domain, err := DomainSeparator(vector.Domain.ChainID, vector.Domain.VerifyingContract)
	if err != nil || domain.Hex() != vector.Domain.Separator {
		t.Fatalf("domain=%s err=%v", domain.Hex(), err)
	}
	structHash, err := vector.Authorization.StructHash()
	if err != nil || structHash.Hex() != vector.StructHash {
		t.Fatalf("struct=%s err=%v", structHash.Hex(), err)
	}
	digest, err := vector.Authorization.Digest()
	if err != nil || digest.Hex() != vector.Digest {
		t.Fatalf("digest=%s err=%v", digest.Hex(), err)
	}
}

func vectorAuthorization() Authorization {
	return Authorization{
		OrgDomain:        hash("01"),
		ContractAddress:  "0x1111111111111111111111111111111111111111",
		ChainID:          "8453",
		AuthorityRole:    hash("02"),
		FunctionSelector: "0x85045a95",
		PayloadHash:      hash("03"),
		AdminOperationID: hash("04"),
		AdminNonce:       "42",
		AdminEpoch:       7,
		ValidAfter:       1_800_000_000,
		ValidBefore:      1_800_000_600,
		WorkflowID:       hash("05"),
	}
}

func mustDigest(t *testing.T, authorization Authorization) string {
	t.Helper()
	digest, err := authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest.Hex()
}

func hash(suffix string) string {
	return "0x" + strings.Repeat("0", 62) + suffix
}
