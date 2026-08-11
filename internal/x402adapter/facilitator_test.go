package x402adapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFacilitatorSupportedAndConformance(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/facilitator/supported" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"kinds":[{"x402Version":2,"scheme":"exact","network":"eip155:84532"}],"extensions":["builder-code"],"signers":{"eip155:*":["0x1111111111111111111111111111111111111111"]}}`)
	}))
	defer server.Close()
	client, err := NewFacilitatorClient(server.URL+"/facilitator", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	supported, err := client.Supported(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conformance := testAdapter(t).CheckFacilitator(supported)
	if !conformance.Ready || !conformance.BuilderCode || !conformance.V2ExactNetwork || len(conformance.Signers) != 1 {
		t.Fatalf("unexpected conformance: %+v", conformance)
	}
}

func TestFacilitatorFailsClosed(t *testing.T) {
	if _, err := NewFacilitatorClient("http://facilitator.example", nil); err == nil {
		t.Fatal("expected non-HTTPS URL rejection")
	}
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"http error", http.StatusBadGateway, `{}`},
		{"malformed", http.StatusOK, `{`},
		{"invalid kind", http.StatusOK, `{"kinds":[{"x402Version":2}],"extensions":[],"signers":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			client, err := NewFacilitatorClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Supported(context.Background()); err == nil {
				t.Fatal("expected facilitator response rejection")
			}
		})
	}
}

func TestFacilitatorRejectsRedirectAndInvalidSigner(t *testing.T) {
	final := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"kinds":[{"x402Version":2,"scheme":"exact","network":"eip155:84532"}],"extensions":["builder-code"],"signers":{"eip155:*":["not-an-address"]}}`)
	}))
	defer final.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, final.URL, http.StatusFound)
	}))
	defer redirect.Close()
	client, err := NewFacilitatorClient(redirect.URL, redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Supported(context.Background()); err == nil {
		t.Fatal("expected redirect rejection")
	}

	direct, err := NewFacilitatorClient(final.URL, final.Client())
	if err != nil {
		t.Fatal(err)
	}
	supported, err := direct.Supported(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if conformance := testAdapter(t).CheckFacilitator(supported); conformance.Ready {
		t.Fatalf("invalid signer made facilitator ready: %+v", conformance)
	}
}
