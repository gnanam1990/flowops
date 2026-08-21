package spendauthorization

import (
	"errors"
	"strings"
	"testing"
)

const (
	vectorChain  = "8453"
	vectorModule = "0x1111111111111111111111111111111111111111"
)

func TestAuthorizationGoldenVectors(t *testing.T) {
	lock := vectorLock()
	lockStruct, err := lock.StructHash()
	if err != nil {
		t.Fatal(err)
	}
	lockDigest, err := lock.Digest(vectorChain, vectorModule)
	if err != nil {
		t.Fatal(err)
	}
	allowance := vectorAllowance()
	allowanceStruct, err := allowance.StructHash()
	if err != nil {
		t.Fatal(err)
	}
	allowanceDigest, err := allowance.Digest(vectorChain, vectorModule)
	if err != nil {
		t.Fatal(err)
	}
	domain, err := DomainSeparator(vectorChain, vectorModule)
	if err != nil {
		t.Fatal(err)
	}

	wants := map[string][2]string{
		"domain":           {domain.Hex(), "0x6eed85ec7382a6a2ce11f88fedf9b3ef797442199eae14aa1276ca05dea7bd06"},
		"lock struct":      {lockStruct.Hex(), "0x81974b7fc1bbd8665da56af038a2dda38ed5c58e6a6cf6cdf8ca72830a345146"},
		"lock digest":      {lockDigest.Hex(), "0xef53c6a44868fb0c6e0935448a36d14eb37215e352ccc8b90b5c7e934ed6abd8"},
		"allowance struct": {allowanceStruct.Hex(), "0x434cf8b5e2edce9bcd652c59bef8e8baf51b6442dabad8280c54b2f5dd5accc7"},
		"allowance digest": {allowanceDigest.Hex(), "0x78e52fd73032cf8cd46a5d8e77a205f655719d2edb7c7d58e3586df41f35c17c"},
	}
	for name, pair := range wants {
		if pair[0] != pair[1] {
			t.Errorf("%s got %s want %s", name, pair[0], pair[1])
		}
	}
}

func TestAuthorizationRejectsEveryLoadBearingMutation(t *testing.T) {
	base := vectorLock()
	want := mustLockDigest(t, base)
	mutations := []func(*LockAuthorization){
		func(a *LockAuthorization) { a.Safe = "0x9999999999999999999999999999999999999999" },
		func(a *LockAuthorization) { a.OperationID = hash("99") },
		func(a *LockAuthorization) { a.CommitmentHash = hash("98") },
		func(a *LockAuthorization) { a.CalldataHash = hash("97") },
		func(a *LockAuthorization) { a.Escrow = "0x8888888888888888888888888888888888888888" },
		func(a *LockAuthorization) { a.Amount = "401" },
		func(a *LockAuthorization) { a.ValidBefore-- },
		func(a *LockAuthorization) { a.Nonce = hash("96") },
		func(a *LockAuthorization) { a.LeadershipEpoch++ },
		func(a *LockAuthorization) { a.AuthorizerEpoch++ },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if got := mustLockDigest(t, candidate); got == want {
			t.Fatalf("mutation %d preserved digest", i)
		}
	}
	if got, err := base.Digest(vectorChain, "0x7777777777777777777777777777777777777777"); err != nil || got.Hex() == want {
		t.Fatalf("module substitution got=%s err=%v", got, err)
	}
	if got, err := base.Digest("84532", vectorModule); err != nil || got.Hex() == want {
		t.Fatalf("chain substitution got=%s err=%v", got, err)
	}
}

func TestAuthorizationRejectsAmbiguousOrLongLivedInput(t *testing.T) {
	lock := vectorLock()
	lock.ValidBefore = lock.ValidAfter + MaximumWindowSeconds + 1
	if _, err := lock.StructHash(); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("long window error=%v", err)
	}
	lock = vectorLock()
	lock.Amount = "0400"
	if _, err := lock.StructHash(); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("amount error=%v", err)
	}
	allowance := vectorAllowance()
	allowance.ExpectedCurrentAllowance = "00"
	if _, err := allowance.StructHash(); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("allowance error=%v", err)
	}
}

func vectorLock() LockAuthorization {
	return LockAuthorization{
		OrgDomain: hash("01"), Safe: "0x2222222222222222222222222222222222222222",
		OperationID: hash("02"), CommitmentHash: hash("03"), CalldataHash: hash("04"),
		Escrow: "0x3333333333333333333333333333333333333333", Amount: "400",
		ValidAfter: 1_800_000_000, ValidBefore: 1_800_000_600, Nonce: hash("05"),
		LeadershipEpoch: 7, AuthorizerEpoch: 9,
	}
}

func vectorAllowance() AllowanceAuthorization {
	return AllowanceAuthorization{
		OrgDomain: hash("01"), Safe: "0x2222222222222222222222222222222222222222",
		AdminOperationID: hash("06"), Token: "0x4444444444444444444444444444444444444444",
		Spender:                  "0x5555555555555555555555555555555555555555",
		ExpectedCurrentAllowance: "400", NewAllowance: "800",
		ValidAfter: 1_800_000_000, ValidBefore: 1_800_000_600, Nonce: hash("07"), AuthorizerEpoch: 9,
	}
}

func mustLockDigest(t *testing.T, authorization LockAuthorization) string {
	t.Helper()
	digest, err := authorization.Digest(vectorChain, vectorModule)
	if err != nil {
		t.Fatal(err)
	}
	return digest.Hex()
}

func hash(suffix string) string {
	return "0x" + strings.Repeat("0", 62) + suffix
}
