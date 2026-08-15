package dbreadiness

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func TestProviderEvidenceRequiresFreshSignedCompleteControls(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	evidence, err := SignProviderEvidence(ProviderEvidence{
		SchemaVersion: 1, Provider: "managed-postgres", ProjectRef: "project_opaque", Region: "us-east",
		ObservedAt: now.Add(-time.Hour), ExpiresAt: now.Add(7 * 24 * time.Hour), SignerKeyID: "operator_2026_08",
		Controls: []ProviderControl{
			{Name: "BACKUPS", Enabled: true, EvidenceURL: "https://provider.example/evidence/backups"},
			{Name: "PITR", Enabled: true, EvidenceURL: "https://provider.example/evidence/pitr"},
			{Name: "ENCRYPTION_AT_REST", Enabled: true, EvidenceURL: "https://provider.example/evidence/encryption"},
			{Name: "MONITORING", Enabled: true, EvidenceURL: "https://provider.example/evidence/monitoring"},
		},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if report := VerifyProviderEvidence(evidence, publicKey, now); !report.Ready {
		t.Fatalf("valid evidence rejected: %+v", report)
	}

	mutations := map[string]func(*ProviderEvidence){
		"disabled backup":   func(e *ProviderEvidence) { e.Controls[0].Enabled = false },
		"missing pitr":      func(e *ProviderEvidence) { e.Controls = append(e.Controls[:1], e.Controls[2:]...) },
		"query secret":      func(e *ProviderEvidence) { e.Controls[0].EvidenceURL += "?token=secret" },
		"expired":           func(e *ProviderEvidence) { e.ExpiresAt = now },
		"tampered provider": func(e *ProviderEvidence) { e.Provider = "other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := evidence
			copy.Controls = append([]ProviderControl(nil), evidence.Controls...)
			mutate(&copy)
			if report := VerifyProviderEvidence(copy, publicKey, now); report.Ready {
				t.Fatalf("mutation accepted: %+v", report)
			}
		})
	}
}

func TestDecodeProviderEvidenceRejectsUnknownAndTrailingFields(t *testing.T) {
	valid, _ := json.Marshal(ProviderEvidence{SchemaVersion: 1})
	if _, err := DecodeProviderEvidence(valid); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		[]byte(`{"schemaVersion":1,"unknown":true}`),
		append(valid, []byte(` {}`)...),
	} {
		if _, err := DecodeProviderEvidence(data); err == nil {
			t.Fatalf("invalid evidence accepted: %s", data)
		}
	}
}
