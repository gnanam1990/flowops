package spendauthorization

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
		"domain":           {domain.Hex(), "0x7b3124ffa73fe66d0496edae8e4c80239b7d8bdb4b4610cff4afd23a479e4a82"},
		"lock struct":      {lockStruct.Hex(), "0x6f55169bed36a64f8778eeda978f92cd131e856f1af7f928a5c90e0b8a93e839"},
		"lock digest":      {lockDigest.Hex(), "0xba4ea42568fd8e82f9586900a88e4ede6bde0a7c8b3f3293c51e75fad1d7f37e"},
		"allowance struct": {allowanceStruct.Hex(), "0xf54aed4326512f15fcb3372779efe474152e1052e0a6b8c996868c21f837df96"},
		"allowance digest": {allowanceDigest.Hex(), "0x5f174440bb7236f3922420bd1142502b22cee71975c211b44f888249a7eeaf30"},
	}
	for name, pair := range wants {
		if pair[0] != pair[1] {
			t.Errorf("%s got %s want %s", name, pair[0], pair[1])
		}
	}
}

func TestPublishedVectorsMatchGoEncodedAndCanonicalBytes(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(source), "..", "..")
	for _, fixture := range []struct {
		name       string
		file       string
		typeString string
		value      any
	}{
		{name: "lock", file: "lock-authorization-v1.json", typeString: LockTypeString, value: &LockAuthorization{}},
		{name: "allowance", file: "allowance-authorization-v1.json", typeString: AllowanceTypeString, value: &AllowanceAuthorization{}},
	} {
		raw, err := os.ReadFile(filepath.Join(root, "vectors", fixture.file))
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			TypeString string `json:"typeString"`
			Domain     struct {
				ChainID           string `json:"chainId"`
				VerifyingContract string `json:"verifyingContract"`
				Separator         string `json:"separator"`
			} `json:"domain"`
			Authorization json.RawMessage `json:"authorization"`
			EncodedData   string          `json:"encodedData"`
			CanonicalJSON string          `json:"canonicalJSON"`
			Digest        string          `json:"digest"`
			Signature     string          `json:"signature"`
			Recovered     string          `json:"recoveredSigner"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.TypeString != fixture.typeString {
			t.Fatalf("%s envelope type=%q err=%v", fixture.name, envelope.TypeString, err)
		}
		if err := json.Unmarshal(envelope.Authorization, fixture.value); err != nil {
			t.Fatal(err)
		}
		var encoded, canonical []byte
		var computedDigest common.Hash
		switch value := fixture.value.(type) {
		case *LockAuthorization:
			encoded, err = value.EncodedData()
			if err == nil {
				canonical, err = value.CanonicalJSON()
			}
			if err == nil {
				computedDigest, err = value.Digest(envelope.Domain.ChainID, envelope.Domain.VerifyingContract)
			}
		case *AllowanceAuthorization:
			encoded, err = value.EncodedData()
			if err == nil {
				canonical, err = value.CanonicalJSON()
			}
			if err == nil {
				computedDigest, err = value.Digest(envelope.Domain.ChainID, envelope.Domain.VerifyingContract)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		wantEncoded, err := hex.DecodeString(strings.TrimPrefix(envelope.EncodedData, "0x"))
		domain, domainErr := DomainSeparator(envelope.Domain.ChainID, envelope.Domain.VerifyingContract)
		if err != nil || domainErr != nil || domain.Hex() != envelope.Domain.Separator || computedDigest.Hex() != envelope.Digest ||
			!bytes.Equal(encoded, wantEncoded) || string(canonical) != envelope.CanonicalJSON {
			t.Fatalf("%s published encoded/canonical bytes drifted", fixture.name)
		}
		signature, err := hex.DecodeString(strings.TrimPrefix(envelope.Signature, "0x"))
		if err != nil || len(signature) != crypto.SignatureLength || signature[64] < 27 {
			t.Fatalf("%s signature encoding is invalid", fixture.name)
		}
		signature[64] -= 27
		publicKey, err := crypto.SigToPub(common.HexToHash(envelope.Digest).Bytes(), signature)
		if err != nil || !strings.EqualFold(crypto.PubkeyToAddress(*publicKey).Hex(), envelope.Recovered) {
			t.Fatalf("%s recovered signer drifted: %v", fixture.name, err)
		}
	}
}

func TestAuthorizationRejectsEveryLoadBearingMutation(t *testing.T) {
	base := vectorLock()
	want := mustLockDigest(t, base)
	mutations := []func(*LockAuthorization){
		func(a *LockAuthorization) { a.Safe = "0x9999999999999999999999999999999999999999" },
		func(a *LockAuthorization) { a.Module = "0x9999999999999999999999999999999999999999" },
		func(a *LockAuthorization) { a.OperationID = hash("99") },
		func(a *LockAuthorization) { a.CommitmentHash = hash("98") },
		func(a *LockAuthorization) { a.CalldataHash = hash("97") },
		func(a *LockAuthorization) { a.Escrow = "0x8888888888888888888888888888888888888888" },
		func(a *LockAuthorization) { a.Amount = "401" },
		func(a *LockAuthorization) { a.ValidBefore-- },
		func(a *LockAuthorization) { a.Nonce = "96" },
		func(a *LockAuthorization) { a.LeadershipEpoch++ },
		func(a *LockAuthorization) { a.AuthorizerEpoch++ },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		got, err := candidate.Digest(vectorChain, vectorModule)
		if err == nil && got.Hex() == want {
			t.Fatalf("mutation %d preserved digest", i)
		}
	}
	if got, err := base.Digest(vectorChain, "0x7777777777777777777777777777777777777777"); err == nil || got.Hex() == want {
		t.Fatalf("module substitution got=%s err=%v", got, err)
	}
	if got, err := base.Digest("84532", vectorModule); err != nil || got.Hex() == want {
		t.Fatalf("chain substitution got=%s err=%v", got, err)
	}
}

func TestAllowanceRejectsEveryLoadBearingMutation(t *testing.T) {
	base := vectorAllowance()
	want, err := base.Digest(vectorChain, vectorModule)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*AllowanceAuthorization){
		func(a *AllowanceAuthorization) { a.Safe = "0x9999999999999999999999999999999999999999" },
		func(a *AllowanceAuthorization) { a.Module = "0x9999999999999999999999999999999999999999" },
		func(a *AllowanceAuthorization) { a.AdminOperationID = hash("99") },
		func(a *AllowanceAuthorization) { a.Token = "0x9999999999999999999999999999999999999999" },
		func(a *AllowanceAuthorization) { a.Spender = "0x9999999999999999999999999999999999999999" },
		func(a *AllowanceAuthorization) { a.ExpectedAllowance = "401" },
		func(a *AllowanceAuthorization) { a.NewAllowance = "801" },
		func(a *AllowanceAuthorization) { a.Nonce = "8" },
		func(a *AllowanceAuthorization) { a.ValidBefore-- },
		func(a *AllowanceAuthorization) { a.LeadershipEpoch++ },
		func(a *AllowanceAuthorization) { a.AuthorizerEpoch++ },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		got, digestErr := candidate.Digest(vectorChain, vectorModule)
		if digestErr == nil && got == want {
			t.Fatalf("mutation %d preserved digest", index)
		}
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
	lock = vectorLock()
	lock.Nonce = "0"
	if _, err := lock.StructHash(); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("nonce error=%v", err)
	}
	allowance := vectorAllowance()
	allowance.ExpectedAllowance = "00"
	if _, err := allowance.StructHash(); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("allowance error=%v", err)
	}
}

func vectorLock() LockAuthorization {
	return LockAuthorization{
		OrgDomain: hash("01"), Safe: "0x2222222222222222222222222222222222222222",
		Module:      vectorModule,
		OperationID: hash("02"), CommitmentHash: hash("03"), CalldataHash: hash("04"),
		Escrow: "0x3333333333333333333333333333333333333333", Amount: "400",
		Nonce: "5", ValidAfter: 1_800_000_000, ValidBefore: 1_800_000_600,
		LeadershipEpoch: 7, AuthorizerEpoch: 9,
	}
}

func vectorAllowance() AllowanceAuthorization {
	return AllowanceAuthorization{
		OrgDomain: hash("01"), Safe: "0x2222222222222222222222222222222222222222",
		Module:           vectorModule,
		AdminOperationID: hash("06"), Token: "0x4444444444444444444444444444444444444444",
		Spender:           "0x5555555555555555555555555555555555555555",
		ExpectedAllowance: "400", NewAllowance: "800", Nonce: "7",
		ValidAfter: 1_800_000_000, ValidBefore: 1_800_000_600,
		LeadershipEpoch: 7, AuthorizerEpoch: 9,
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
