package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/dbreadiness"
)

func TestRunRejectsMissingInputsWithoutOpeningDatabase(t *testing.T) {
	t.Setenv("FLOWOPS_DATABASE_URL", "")
	for _, args := range [][]string{nil, {"unknown"}, {"sql"}, {"install-root-ca"}, {"provider-evidence"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("args %v were accepted", args)
		}
	}
}

func TestReadLimitedRejectsOversizedEvidence(t *testing.T) {
	if _, err := readLimited(io.LimitReader(strings.NewReader(strings.Repeat("x", 1024*1024+1)), 1024*1024+1)); err == nil {
		t.Fatal("oversized provider evidence accepted")
	}
}

func TestRunSignsProviderEvidenceWithoutEmittingPrivateKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOWOPS_DB_EVIDENCE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	evidence := dbreadiness.ProviderEvidence{
		SchemaVersion: 1, Provider: "provider", ProjectRef: "opaque", Region: "region", SignerKeyID: "key",
		ObservedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour),
		Controls: []dbreadiness.ProviderControl{
			{Name: "BACKUPS", Enabled: true, EvidenceURL: "https://provider.example/backups"},
			{Name: "PITR", Enabled: true, EvidenceURL: "https://provider.example/pitr"},
			{Name: "ENCRYPTION_AT_REST", Enabled: true, EvidenceURL: "https://provider.example/encryption"},
			{Name: "MONITORING", Enabled: true, EvidenceURL: "https://provider.example/monitoring"},
		},
	}
	input, _ := json.Marshal(evidence)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"provider-evidence-sign"}, bytes.NewReader(input), &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), base64.StdEncoding.EncodeToString(privateKey)) {
		t.Fatal("signer emitted the private key")
	}
	signed, err := dbreadiness.DecodeProviderEvidence(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if report := dbreadiness.VerifyProviderEvidence(signed, publicKey, time.Now().UTC()); !report.Ready {
		t.Fatalf("signed output failed verification: %+v", report)
	}
}
