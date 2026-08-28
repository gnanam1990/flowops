package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/safeownerproof"
)

func TestRunCreatesNonClaimingTemplateAndSigningMessage(t *testing.T) {
	now := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	profilePath := filepath.Join("..", "..", "deployments", "base-mainnet-safe-owner-control-profile-v1.json")
	var output bytes.Buffer
	if err := run([]string{"template", profilePath, "owner-control-cli-test"}, &output, now); err != nil {
		t.Fatal(err)
	}
	var proof safeownerproof.Proof
	if err := json.Unmarshal(output.Bytes(), &proof); err != nil {
		t.Fatal(err)
	}
	if len(proof.Signatures) != 0 || proof.ExpiresAt != "2026-08-28T06:00:00Z" {
		t.Fatalf("unexpected owner proof template: %+v", proof)
	}
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	if err := os.WriteFile(proofPath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"digest", proofPath, profilePath}, &output, now); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), safeownerproof.SigningContext+"\n0x") {
		t.Fatalf("unexpected signing message: %q", output.String())
	}
}

func TestRunRefusesToPrintMessageForMutatedStatement(t *testing.T) {
	now := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	profilePath := filepath.Join("..", "..", "deployments", "base-mainnet-safe-owner-control-profile-v1.json")
	var output bytes.Buffer
	if err := run([]string{"template", profilePath, "owner-control-cli-test"}, &output, now); err != nil {
		t.Fatal(err)
	}
	var proof safeownerproof.Proof
	if err := json.Unmarshal(output.Bytes(), &proof); err != nil {
		t.Fatal(err)
	}
	proof.Statement = "authorize deployment"
	encoded, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	if err := os.WriteFile(proofPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"digest", proofPath, profilePath}, &bytes.Buffer{}, now); err == nil {
		t.Fatal("printed a signing message for a mutated owner-control statement")
	}
}

func TestRunRejectsUnsignedVerificationAndUnknownCommand(t *testing.T) {
	now := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	profilePath := filepath.Join("..", "..", "deployments", "base-mainnet-safe-owner-control-profile-v1.json")
	var output bytes.Buffer
	if err := run([]string{"template", profilePath, "owner-control-cli-test"}, &output, now); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	if err := os.WriteFile(proofPath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", proofPath, profilePath}, &bytes.Buffer{}, now); err == nil {
		t.Fatal("unsigned owner proof was accepted")
	}
	if err := run([]string{"unknown"}, &bytes.Buffer{}, now); err == nil {
		t.Fatal("unknown command was accepted")
	}
}
