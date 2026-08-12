package referencesigner

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
)

func testSignedReceipt(t *testing.T) broadcastreceipt.SignedReceipt {
	t.Helper()
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("h", ed25519.SeedSize)))
	receipt, err := broadcastreceipt.Sign(broadcastreceipt.Receipt{
		Version:             broadcastreceipt.Version,
		OrganizationID:      "org_demo",
		CustomerID:          "cust_acme",
		AuthorizationID:     "auth_http_1",
		AuthorizationDigest: "0x" + strings.Repeat("1", 64),
		TransactionHash:     "0x" + strings.Repeat("2", 64),
		Sender:              "0x2222222222222222222222222222222222222222",
		Outcome:             broadcastreceipt.OutcomeSubmitted,
		BroadcastAt:         1786528800,
	}, "customer_receipt_1", key)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestHTTPRegistrationSinkPostsExactReceipt(t *testing.T) {
	want := testSignedReceipt(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/signer/broadcasts" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(raw), want.Signature) {
			t.Error("signed receipt was not posted")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"accepted"}`))
	}))
	defer server.Close()
	sink, err := NewHTTPRegistrationSink(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Register(context.Background(), want); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRegistrationSinkFailsClosed(t *testing.T) {
	receipt := testSignedReceipt(t)
	for name, handler := range map[string]http.HandlerFunc{
		"redirect": func(writer http.ResponseWriter, _ *http.Request) {
			http.Redirect(writer, &http.Request{}, "http://example.com/stolen", http.StatusTemporaryRedirect)
		},
		"error": func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "secret server detail", http.StatusServiceUnavailable)
		},
		"invalid json": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte("accepted"))
		},
		"oversized": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"padding":"` + strings.Repeat("a", maxRegistrationResponseBytes) + `"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			sink, err := NewHTTPRegistrationSink(server.URL, &http.Client{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.Register(context.Background(), receipt); err == nil || strings.Contains(err.Error(), "secret server detail") {
				t.Fatalf("fail-closed error = %v", err)
			}
		})
	}
}

func TestHTTPRegistrationSinkRejectsUnsafeConfiguration(t *testing.T) {
	for _, endpoint := range []string{
		"http://flowops.example",
		"https://user@example.com",
		"https://flowops.example/api",
		"https://flowops.example?token=" + base64.StdEncoding.EncodeToString([]byte("secret")),
	} {
		if _, err := NewHTTPRegistrationSink(endpoint, &http.Client{Timeout: time.Second}); err == nil {
			t.Fatalf("unsafe endpoint %q accepted", endpoint)
		}
	}
	if _, err := NewHTTPRegistrationSink("https://flowops.example", &http.Client{}); err == nil {
		t.Fatal("client without timeout accepted")
	}
}
