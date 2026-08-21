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
	valid := map[string]request{
		"status":    {OrganizationID: "org"},
		"bootstrap": {OrganizationID: "org", Actor: "operator", EvidenceDigest: "digest"},
		"drain":     {OrganizationID: "org", ExpectedEpoch: 1, Actor: "operator", EvidenceDigest: "digest"},
		"advance":   {OrganizationID: "org", ExpectedEpoch: 1, Actor: "operator", EvidenceDigest: "digest"},
	}
	for command, payload := range valid {
		if err := payload.validateFor(command); err != nil {
			t.Fatalf("valid %s request: %v", command, err)
		}
	}
	if err := (request{OrganizationID: "org", Actor: "ignored"}).validateFor("status"); err == nil {
		t.Fatal("status accepted mutation fields")
	}
	if err := (request{OrganizationID: "org", ExpectedEpoch: 1, Actor: "operator", EvidenceDigest: "digest"}).validateFor("bootstrap"); err == nil {
		t.Fatal("bootstrap accepted an ignored expected epoch")
	}
	if err := (request{OrganizationID: "org", Actor: "operator", EvidenceDigest: "digest"}).validateFor("drain"); err == nil {
		t.Fatal("drain accepted a missing expected epoch")
	}
}
