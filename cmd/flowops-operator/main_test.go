package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperatorClientSendsKeyOnlyToExactConfiguredOrigin(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/operator/chain/resume" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body resumeInput
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Operator != "operator_alice" {
			t.Fatalf("body = %+v, %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chain":{"state":"HEALTHY"}}`))
	}))
	defer server.Close()
	t.Setenv("FLOWOPS_CONTROL_API_URL", server.URL)
	t.Setenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64", key)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"chain-resume"}, strings.NewReader(`{"operator":"operator_alice"}`), &output); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(output.String(), "HEALTHY") || strings.Contains(output.String(), key) {
		t.Fatalf("requests=%d output=%q", requests, output.String())
	}
}

func TestOperatorClientRejectsRedirectsAndDoesNotReplayKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte(`{"error":"redirect"}`))
	}))
	defer server.Close()
	t.Setenv("FLOWOPS_CONTROL_API_URL", server.URL)
	t.Setenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64", key)
	var output bytes.Buffer
	err := run(context.Background(), []string{"chain-halt"}, strings.NewReader(`{"operator":"operator_alice","reason":"drill"}`), &output)
	if err == nil || redirected != 0 {
		t.Fatalf("redirect error=%v redirected=%d", err, redirected)
	}
}

func TestOperatorClientReadsReconciliationAndQuarantinesWithoutClaimingOutcome(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatalf("missing operator key")
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/v1/operator/reconciliation" || r.URL.Query().Get("organizationId") != "org_acme" {
				t.Fatalf("reconciliation request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"reconciliation":{"available":true}}`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/v1/operator/executions/exec_1/quarantine" {
				t.Fatalf("quarantine request = %s %s", r.Method, r.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["disposition"] != "REPLACED_UNPROVEN" || body["organizationId"] != "org_acme" || body["executionId"] != nil {
				t.Fatalf("quarantine body = %+v err=%v", body, err)
			}
			_, _ = w.Write([]byte(`{"execution":{"state":"QUARANTINED"}}`))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()
	t.Setenv("FLOWOPS_CONTROL_API_URL", server.URL)
	t.Setenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64", key)
	if err := run(context.Background(), []string{"reconciliation-status"}, strings.NewReader(`{"organizationId":"org_acme"}`), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	input := `{"organizationId":"org_acme","executionId":"exec_1","operator":"operator_alice","disposition":"REPLACED_UNPROVEN","reason":"replacement nonce is not independently proved"}`
	if err := run(context.Background(), []string{"execution-quarantine"}, strings.NewReader(input), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestOperatorClientValidatesURLKeyAndStrictInput(t *testing.T) {
	for _, raw := range []string{"", "http://example.com", "https://user:pass@example.com", "https://example.com?token=x"} {
		if _, err := controlAPIURL(raw); err == nil {
			t.Errorf("accepted URL %q", raw)
		}
	}
	t.Setenv("FLOWOPS_CONTROL_API_URL", "https://control.example.com")
	for name, input := range map[string]string{
		"unknown":   `{"operator":"operator_alice","extra":true}`,
		"duplicate": `{"operator":"operator_alice","operator":"operator_mallory"}`,
		"trailing":  `{"operator":"operator_alice"}{}`,
		"null":      `null`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32))))
			if err := run(context.Background(), []string{"chain-resume"}, strings.NewReader(input), &bytes.Buffer{}); err == nil {
				t.Fatalf("accepted input %q", input)
			}
		})
	}
	t.Setenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64", "bad")
	if err := run(context.Background(), []string{"chain-resume"}, strings.NewReader(`{"operator":"operator_alice"}`), &bytes.Buffer{}); err == nil {
		t.Fatal("accepted invalid operator key")
	}
}
