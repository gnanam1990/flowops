package broadcastreceipt

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Receipt, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := hex.DecodeString(strings.Repeat("19", ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return Receipt{
		Version: Version, OrganizationID: "org_acme", CustomerID: "cust_acme", AuthorizationID: "auth_1",
		AuthorizationDigest: "0x" + strings.Repeat("a", 64), TransactionHash: "0x" + strings.Repeat("b", 64),
		Sender: "0x1111111111111111111111111111111111111111", Outcome: OutcomeAmbiguous, BroadcastAt: 1786521600,
	}, privateKey.Public().(ed25519.PublicKey), privateKey
}

func TestReceiptRoundTripAndSubstitutionRejection(t *testing.T) {
	receipt, publicKey, privateKey := fixture(t)
	signed, err := Sign(receipt, "customer_signer_1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signed, publicKey); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*SignedReceipt){
		"organization":         func(s *SignedReceipt) { s.Receipt.OrganizationID = "org_other" },
		"customer":             func(s *SignedReceipt) { s.Receipt.CustomerID = "cust_other" },
		"authorization":        func(s *SignedReceipt) { s.Receipt.AuthorizationID = "auth_2" },
		"authorization digest": func(s *SignedReceipt) { s.Receipt.AuthorizationDigest = "0x" + strings.Repeat("c", 64) },
		"transaction":          func(s *SignedReceipt) { s.Receipt.TransactionHash = "0x" + strings.Repeat("d", 64) },
		"sender":               func(s *SignedReceipt) { s.Receipt.Sender = "0x2222222222222222222222222222222222222222" },
		"outcome":              func(s *SignedReceipt) { s.Receipt.Outcome = OutcomeSubmitted },
		"time":                 func(s *SignedReceipt) { s.Receipt.BroadcastAt++ },
		"key ID":               func(s *SignedReceipt) { s.KeyID = "customer_signer_2" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := signed
			mutate(&changed)
			if err := Verify(changed, publicKey); err == nil {
				t.Fatal("substituted receipt verified")
			}
		})
	}
}

func TestReceiptRejectsNonCanonicalAndInvalidFields(t *testing.T) {
	receipt, _, privateKey := fixture(t)
	tests := map[string]func(*Receipt){
		"uppercase hash": func(r *Receipt) { r.TransactionHash = "0x" + strings.Repeat("A", 64) },
		"bad sender":     func(r *Receipt) { r.Sender = strings.ToUpper(r.Sender) },
		"failed outcome": func(r *Receipt) { r.Outcome = "FAILED" },
		"zero time":      func(r *Receipt) { r.BroadcastAt = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			if _, err := Sign(changed, "customer_signer_1", privateKey); err == nil {
				t.Fatal("invalid receipt signed")
			}
		})
	}
}
