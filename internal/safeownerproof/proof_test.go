package safeownerproof

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestVerifyAcceptsFreshTwoOwnerProof(t *testing.T) {
	profile, proof, keys, now := proofFixture(t)
	signProof(t, &proof, keys[:2])
	if err := Verify(proof, profile, now); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsEveryAuthorityAndFreshnessMutation(t *testing.T) {
	tests := map[string]func(*Proof){
		"profile digest": func(proof *Proof) { proof.ProfileSHA256 = "0x" + strings.Repeat("a", 64) },
		"statement":      func(proof *Proof) { proof.Statement = "authorize deployment" },
		"expired":        func(proof *Proof) { proof.ExpiresAt = "2026-08-28T03:00:00Z" },
		"signed payload": func(proof *Proof) { proof.ChallengeID = "owner-control-mutated" },
		"duplicate owner": func(proof *Proof) {
			proof.Signatures[1].Owner = proof.Signatures[0].Owner
			proof.Signatures[1].SignatureHex = proof.Signatures[0].SignatureHex
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			profile, proof, keys, now := proofFixture(t)
			signProof(t, &proof, keys[:2])
			mutate(&proof)
			if err := Verify(proof, profile, now); err == nil {
				t.Fatal("mutated owner proof was accepted")
			}
		})
	}
}

func TestTemplateDoesNotPreclaimOwnerControl(t *testing.T) {
	profile, _, _, now := proofFixture(t)
	proof, err := Template(profile, "owner-control-template", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Signatures) != 0 {
		t.Fatal("template preclaimed owner signatures")
	}
	if err := Verify(proof, profile, now); err == nil {
		t.Fatal("unsigned template was accepted")
	}
}

func TestRepositoryProfilePinsCurrentCandidate(t *testing.T) {
	profile, err := LoadProfile(filepath.Join("..", "..", "deployments", "base-mainnet-safe-owner-control-profile-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if profile.SourceCommit != "ae8ebfdfa8d1e6013888134d72610f9ab9032b53" || profile.Safe.Threshold != 2 {
		t.Fatalf("unexpected repository owner profile: %+v", profile)
	}
}

func TestLoadRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown":   `{"schemaVersion":1,"unknown":true}`,
		"duplicate": `{"schemaVersion":1,"schemaVersion":1}`,
		"trailing":  `{}` + "\n{}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "proof.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProof(path); err == nil {
				t.Fatal("malformed owner proof JSON was accepted")
			}
		})
	}
}

func proofFixture(t *testing.T) (Profile, Proof, []*ecdsa.PrivateKey, time.Time) {
	t.Helper()
	keys := make([]*ecdsa.PrivateKey, 0, 3)
	owners := make([]string, 0, 3)
	for index := 1; index <= 3; index++ {
		key, err := crypto.HexToECDSA(strings.Repeat(string(rune('0'+index)), 64))
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		owners = append(owners, strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()))
	}
	profile := Profile{
		SchemaVersion: SchemaVersion,
		ProfileID:     "base-mainnet-safe-owner-control-test",
		Purpose:       SigningContext,
		Network:       "base-mainnet",
		ChainID:       8453,
		SourceCommit:  strings.Repeat("a", 40),
		Candidate:     DigestFile{Path: "candidate.json", SHA256: "0x" + strings.Repeat("b", 64)},
		Safe: SafeTrust{
			Address:         "0x1111111111111111111111111111111111111111",
			RuntimeCodeHash: "0x" + strings.Repeat("c", 64),
			Implementation:  "0x2222222222222222222222222222222222222222",
			Owners:          owners,
			Threshold:       2,
			Nonce:           0,
		},
		MaximumAgeSeconds:      3600,
		MinimumOwnerSignatures: 2,
	}
	now := time.Date(2026, 8, 28, 4, 30, 0, 0, time.UTC)
	proof, err := Template(profile, "owner-control-test-run", now)
	if err != nil {
		t.Fatal(err)
	}
	return profile, proof, keys, now
}

func signProof(t *testing.T, proof *Proof, keys []*ecdsa.PrivateKey) {
	t.Helper()
	message, err := SigningMessage(*proof)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		signature, err := crypto.Sign(accounts.TextHash([]byte(message)), key)
		if err != nil {
			t.Fatal(err)
		}
		signature[64] += 27
		proof.Signatures = append(proof.Signatures, OwnerSignature{
			Owner:        strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()),
			SignatureHex: "0x" + hex.EncodeToString(signature),
		})
	}
}

func TestCandidateDigestMutationInvalidatesProfileLoad(t *testing.T) {
	root := t.TempDir()
	candidate := []byte(`{"candidate":true}`)
	digest := sha256.Sum256(candidate)
	profile, _, _, _ := proofFixture(t)
	profile.Candidate = DigestFile{Path: "candidate.json", SHA256: "0x" + hex.EncodeToString(digest[:])}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profile.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "candidate.json"), candidate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(filepath.Join(root, "profile.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "candidate.json"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(filepath.Join(root, "profile.json")); err == nil {
		t.Fatal("mutated candidate remained trusted")
	}
}
