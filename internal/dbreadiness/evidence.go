package dbreadiness

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

const providerEvidenceDomain = "flowops:managed-postgres-evidence:v1\n"

type ProviderControl struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	EvidenceURL string `json:"evidenceUrl"`
}

type ProviderEvidence struct {
	SchemaVersion int               `json:"schemaVersion"`
	Provider      string            `json:"provider"`
	ProjectRef    string            `json:"projectRef"`
	Region        string            `json:"region"`
	ObservedAt    time.Time         `json:"observedAt"`
	ExpiresAt     time.Time         `json:"expiresAt"`
	SignerKeyID   string            `json:"signerKeyId"`
	Controls      []ProviderControl `json:"controls"`
	Signature     string            `json:"signature"`
}

type ProviderReport struct {
	SchemaVersion int     `json:"schemaVersion"`
	Ready         bool    `json:"ready"`
	Checks        []Check `json:"checks"`
}

func (e ProviderEvidence) signingBytes() ([]byte, error) {
	unsigned := e
	unsigned.Signature = ""
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return append([]byte(providerEvidenceDomain), encoded...), nil
}

func SignProviderEvidence(e ProviderEvidence, key ed25519.PrivateKey) (ProviderEvidence, error) {
	if len(key) != ed25519.PrivateKeySize {
		return ProviderEvidence{}, errors.New("Ed25519 private key must be 64 bytes")
	}
	payload, err := e.signingBytes()
	if err != nil {
		return ProviderEvidence{}, err
	}
	e.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	return e, nil
}

func VerifyProviderEvidence(e ProviderEvidence, publicKey ed25519.PublicKey, now time.Time) ProviderReport {
	report := ProviderReport{SchemaVersion: 1, Ready: true}
	add := func(name string, passed bool, detail string) {
		report.Checks = append(report.Checks, Check{Name: name, Passed: passed, Detail: detail})
		report.Ready = report.Ready && passed
	}
	add("schema", e.SchemaVersion == 1, "provider evidence schema must be 1")
	add("identity", validLabel(e.Provider) && validLabel(e.ProjectRef) && validLabel(e.Region) && validLabel(e.SignerKeyID),
		"provider, opaque project reference, region, and signer key id are required")
	add("time_window", !e.ObservedAt.IsZero() && !e.ExpiresAt.IsZero() && !now.Before(e.ObservedAt) && now.Before(e.ExpiresAt) && e.ExpiresAt.Sub(e.ObservedAt) <= 31*24*time.Hour,
		"evidence must be current and expire within 31 days")

	required := map[string]bool{"BACKUPS": false, "PITR": false, "ENCRYPTION_AT_REST": false, "MONITORING": false}
	unique := true
	validURLs := true
	for _, control := range e.Controls {
		if _, ok := required[control.Name]; !ok || required[control.Name] {
			unique = false
			continue
		}
		required[control.Name] = control.Enabled
		parsed, err := url.Parse(control.EvidenceURL)
		validURLs = validURLs && err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
	}
	allEnabled := len(e.Controls) == len(required)
	for _, enabled := range required {
		allEnabled = allEnabled && enabled
	}
	add("required_controls", unique && allEnabled, "backups, PITR, encryption at rest, and monitoring must each be enabled exactly once")
	add("evidence_urls", validURLs && len(e.Controls) == len(required), "each control needs a credential-free HTTPS evidence reference")

	payload, err := e.signingBytes()
	signature, decodeErr := base64.StdEncoding.DecodeString(e.Signature)
	signatureOK := err == nil && decodeErr == nil && len(publicKey) == ed25519.PublicKeySize && ed25519.Verify(publicKey, payload, signature)
	add("operator_signature", signatureOK, "trusted operator Ed25519 signature must cover the exact evidence bytes")
	sort.Slice(report.Checks, func(i, j int) bool { return report.Checks[i].Name < report.Checks[j].Name })
	return report
}

func DecodeProviderEvidence(data []byte) (ProviderEvidence, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var evidence ProviderEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return ProviderEvidence{}, fmt.Errorf("decode provider evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ProviderEvidence{}, errors.New("provider evidence contains trailing JSON")
		}
		return ProviderEvidence{}, fmt.Errorf("decode provider evidence trailer: %w", err)
	}
	return evidence, nil
}

func validLabel(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, "\r\n\x00")
}
