package ascpgovernancerelay

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBoundaryStrictJSONRejectsDuplicateUnknownAndTrailingFields(t *testing.T) {
	type response struct {
		Value string `json:"value"`
	}
	for name, raw := range map[string]string{
		"duplicate":    `{"value":"a","value":"b"}`,
		"case variant": `{"Value":"a"}`,
		"missing":      `{}`,
		"unknown":      `{"value":"a","extra":true}`,
		"trailing":     `{"value":"a"}{"value":"b"}`,
		"deep":         strings.Repeat(`[`, 66) + strings.Repeat(`]`, 66),
	} {
		t.Run(name, func(t *testing.T) {
			var output response
			if err := strictBoundaryJSON([]byte(raw), &output); err == nil {
				t.Fatal("ambiguous boundary JSON was accepted")
			}
		})
	}
	var output response
	if err := strictBoundaryJSON([]byte(`{"value":"a"}`), &output); err != nil || output.Value != "a" {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	type nestedResponse struct {
		Outer response `json:"outer"`
	}
	var nested nestedResponse
	if err := strictBoundaryJSON([]byte(`{"outer":{"Value":"a"}}`), &nested); err == nil {
		t.Fatal("nested case-variant boundary field was accepted")
	}
}

func TestVaultBoundaryRequiresNonzeroExactCapability(t *testing.T) {
	path := "/private/tmp/flowops-governance-vault.sock"
	for name, capability := range map[string][]byte{
		"missing": nil, "short": make([]byte, 31), "zero": make([]byte, 32),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAuthenticatedUnixBoundary("vault", path, time.Second, capability); !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("capability error=%v", err)
			}
		})
	}
	capability := make([]byte, 32)
	capability[0] = 1
	boundary, err := NewAuthenticatedUnixBoundary("vault", path, time.Second, capability)
	if err != nil || !boundary.hasCapability {
		t.Fatalf("boundary=%+v err=%v", boundary, err)
	}
	if _, err := NewAuthenticatedUnixBoundary("chain", path, time.Second, capability); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("non-vault capability error=%v", err)
	}
}

func TestChainSnapshotBindingOmitsApprovalPrincipalAndIdempotencyData(t *testing.T) {
	command := relayCommand(t, time.Unix(1_800_000_100, 0).UTC())
	encoded, err := json.Marshal(snapshotBinding(command, "0x1111111111111111111111111111111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{command.ApprovedBy, command.ApprovalActionHash, command.OrganizationID} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot boundary leaked control-plane metadata %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), command.PayloadHash) || !strings.Contains(string(encoded), command.Calldata) {
		t.Fatalf("snapshot boundary omitted chain-verifiable binding: %s", encoded)
	}
}
