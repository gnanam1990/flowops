package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestValidateLeadershipURLRequiresExactVerifiedTLS(t *testing.T) {
	postgresURL := func(query string) string {
		return "postgresql" + "://leader@db.example/flowops?" + query
	}
	if err := validateLeadershipURL(postgresURL("sslmode=verify-full")); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", "https://db.example/flowops?sslmode=verify-full",
		postgresURL("sslmode=require"),
		postgresURL("sslmode=verify-full&sslmode=disable"),
	} {
		if err := validateLeadershipURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
}

func TestDecodeStrictRejectsUnknownDuplicateAndTrailingInput(t *testing.T) {
	for _, raw := range []string{
		`{"organizationId":"org","unknown":true}`,
		`{"organizationId":"org","organizationId":"other"}`,
		`{"organizationId":"org"}{"organizationId":"other"}`,
		`[]`,
		`null`,
		`"org"`,
	} {
		var request request
		if err := decodeStrict(strings.NewReader(raw), &request); err == nil {
			t.Fatalf("invalid input accepted: %s", raw)
		}
	}
	var request request
	if err := decodeStrict(strings.NewReader(`{"organizationId":"org","expectedEpoch":1}`), &request); err != nil {
		t.Fatal(err)
	}
}

func TestRunFailsBeforeDatabaseForUsageAndMissingURL(t *testing.T) {
	t.Setenv("FLOWOPS_LEADERSHIP_DATABASE_URL", "")
	if err := run(context.Background(), []string{"unknown"}, strings.NewReader(`{}`), &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := run(context.Background(), []string{"status"}, strings.NewReader(`{"organizationId":"org"}`), &bytes.Buffer{}); err == nil {
		t.Fatal("missing database URL accepted")
	}
}

func TestRequestValidationIsCommandSpecific(t *testing.T) {
	one := uint64(1)
	zero := uint64(0)
	actor := "operator"
	digest := "digest"
	effectID := "effect"
	valid := map[string]request{
		"status":         {OrganizationID: "org"},
		"bootstrap":      withFields(request{OrganizationID: "org", Actor: &actor, EvidenceDigest: &digest}, "actor", "evidenceDigest"),
		"drain":          withFields(request{OrganizationID: "org", ExpectedEpoch: &one, Actor: &actor, EvidenceDigest: &digest}, "expectedEpoch", "actor", "evidenceDigest"),
		"advance":        withFields(request{OrganizationID: "org", ExpectedEpoch: &one, Actor: &actor, EvidenceDigest: &digest}, "expectedEpoch", "actor", "evidenceDigest"),
		"abandon-effect": withFields(request{OrganizationID: "org", ExpectedEpoch: &one, Actor: &actor, EvidenceDigest: &digest, EffectID: &effectID}, "expectedEpoch", "actor", "evidenceDigest", "effectId"),
	}
	for command, payload := range valid {
		if err := payload.validateFor(command); err != nil {
			t.Fatalf("valid %s request: %v", command, err)
		}
	}
	if err := withFields(request{OrganizationID: "org", Actor: &actor}, "actor").validateFor("status"); err == nil {
		t.Fatal("status accepted mutation fields")
	}
	if err := withFields(request{OrganizationID: "org", ExpectedEpoch: &zero, Actor: &actor, EvidenceDigest: &digest}, "expectedEpoch", "actor", "evidenceDigest").validateFor("bootstrap"); err == nil {
		t.Fatal("bootstrap accepted an ignored expected epoch")
	}
	if err := withFields(request{OrganizationID: "org", ExpectedEpoch: &zero, Actor: &actor, EvidenceDigest: &digest}, "expectedEpoch", "actor", "evidenceDigest").validateFor("drain"); err == nil {
		t.Fatal("drain accepted a missing expected epoch")
	}
	if err := withFields(request{OrganizationID: "", ExpectedEpoch: &one, Actor: &actor, EvidenceDigest: &digest}, "expectedEpoch", "actor", "evidenceDigest").validateFor("advance"); err == nil {
		t.Fatal("advance accepted an empty organization")
	}
	var explicitNull request
	if err := decodeStrict(strings.NewReader(`{"organizationId":"org","expectedEpoch":null}`), &explicitNull); err != nil {
		t.Fatal(err)
	}
	if err := explicitNull.validateFor("status"); err == nil {
		t.Fatal("status accepted an explicitly null forbidden field")
	}
}

func withFields(payload request, fields ...string) request {
	payload.fields = make(map[string]struct{}, len(fields))
	for _, field := range fields {
		payload.fields[field] = struct{}{}
	}
	return payload
}
