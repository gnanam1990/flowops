package envelope

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func fixtureAuthorization() Authorization {
	return Authorization{
		Version:         Version,
		AuthorizationID: "auth_01k2flowops",
		OrganizationID:  "org_demo",
		CustomerID:      "cust_acme",
		AgentID:         "agent_research",
		TaskID:          "task_104",
		ActionID:        "action_fetch_1",
		Rail:            RailX402,
		ChainID:         84532,
		Recipient:       "0x1111111111111111111111111111111111111111",
		Asset:           "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		AmountAtomic:    "1000",
		Resource:        "https://evidence.flowops.example/v1/fetch",
		PolicyVersion:   "policy_7",
		Nonce:           "0x" + strings.Repeat("ab", 32),
		IssuedAt:        1786456800,
		ExpiresAt:       1786457100,
	}
}

func fixtureKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := hex.DecodeString(strings.Repeat("01", ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func TestCanonicalGoldenVector(t *testing.T) {
	a := fixtureAuthorization()
	canonical, err := a.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":"flowops.authorization.v1","authorizationId":"auth_01k2flowops","organizationId":"org_demo","customerId":"cust_acme","agentId":"agent_research","taskId":"task_104","actionId":"action_fetch_1","rail":"x402","chainId":84532,"recipient":"0x1111111111111111111111111111111111111111","asset":"0x036cbd53842c5426634e7929541ec2318f3dcf7e","amountAtomic":"1000","resource":"https://evidence.flowops.example/v1/fetch","policyVersion":"policy_7","nonce":"0xabababababababababababababababababababababababababababababababab","issuedAt":1786456800,"expiresAt":1786457100}`
	if string(canonical) != want {
		t.Fatalf("canonical bytes changed\n got: %s\nwant: %s", canonical, want)
	}
	digest, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got, wantDigest := hex.EncodeToString(digest[:]), "bfb2365b927420d28409095aed511f801aa9f145fcd5e72789bb28d34539d8a7"; got != wantDigest {
		t.Fatalf("digest = %s, want %s", got, wantDigest)
	}
}

func TestSignVerifyAndSubstitutionResistance(t *testing.T) {
	publicKey, privateKey := fixtureKeys(t)
	signed, err := Sign(fixtureAuthorization(), "flowops_control_1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signed, publicKey); err != nil {
		t.Fatalf("verify: %v", err)
	}

	mutations := map[string]func(*Authorization){
		"organization": func(a *Authorization) { a.OrganizationID = "org_attacker" },
		"customer":     func(a *Authorization) { a.CustomerID = "cust_attacker" },
		"task":         func(a *Authorization) { a.TaskID = "task_other" },
		"rail":         func(a *Authorization) { a.Rail = RailDirect },
		"chain":        func(a *Authorization) { a.ChainID = 8453 },
		"recipient":    func(a *Authorization) { a.Recipient = "0x2222222222222222222222222222222222222222" },
		"asset":        func(a *Authorization) { a.Asset = "0x3333333333333333333333333333333333333333" },
		"amount":       func(a *Authorization) { a.AmountAtomic = "1001" },
		"resource":     func(a *Authorization) { a.Resource += "/other" },
		"policy":       func(a *Authorization) { a.PolicyVersion = "policy_8" },
		"nonce":        func(a *Authorization) { a.Nonce = "0x" + strings.Repeat("cd", 32) },
		"expiry":       func(a *Authorization) { a.ExpiresAt++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := signed
			mutate(&changed.Authorization)
			if err := Verify(changed, publicKey); err == nil {
				t.Fatal("mutated authorization verified")
			}
		})
	}
}

func TestValidationRejectsNonCanonicalAuthority(t *testing.T) {
	tests := map[string]func(*Authorization){
		"zero amount":       func(a *Authorization) { a.AmountAtomic = "0" },
		"leading zero":      func(a *Authorization) { a.AmountAtomic = "01000" },
		"negative amount":   func(a *Authorization) { a.AmountAtomic = "-1" },
		"uppercase address": func(a *Authorization) { a.Asset = strings.ToUpper(a.Asset) },
		"bad nonce":         func(a *Authorization) { a.Nonce = "0x01" },
		"unknown rail":      func(a *Authorization) { a.Rail = "bridge" },
		"expired at issue":  func(a *Authorization) { a.ExpiresAt = a.IssuedAt },
		"empty resource":    func(a *Authorization) { a.Resource = "   " },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			a := fixtureAuthorization()
			mutate(&a)
			if err := a.Validate(); err == nil {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
}

func TestNormalizeAddress(t *testing.T) {
	got, err := NormalizeAddress("  0x036CbD53842c5426634e7929541eC2318f3dCF7e  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := "0x036cbd53842c5426634e7929541ec2318f3dcf7e"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
