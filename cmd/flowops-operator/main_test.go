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
