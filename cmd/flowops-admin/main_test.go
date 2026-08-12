package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeStrictJSONRejectsUnknownTrailingAndOversizedInput(t *testing.T) {
	var target struct {
		Value string `json:"value"`
	}
	if err := decodeStrictJSON(strings.NewReader(`{"value":"safe"}`), &target); err != nil || target.Value != "safe" {
		t.Fatalf("valid request = %q, %v", target.Value, err)
	}
	for _, input := range []string{
		`{"value":"safe","unknown":true}`,
		`{"value":"safe","value":"substituted"}`,
		`{"value":"safe"}{"value":"replay"}`,
	} {
		if err := decodeStrictJSON(strings.NewReader(input), &target); err == nil {
			t.Fatalf("invalid request accepted: %s", input)
		}
	}
	oversized := bytes.Repeat([]byte("x"), maxAdminInputBytes+1)
	if err := decodeStrictJSON(bytes.NewReader(oversized), &target); err == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestRunRejectsUnknownCommandBeforeReadingSecrets(t *testing.T) {
	if err := run(t.Context(), []string{"unknown"}, strings.NewReader("{}"), &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command accepted")
	}
}
