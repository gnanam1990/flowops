package referencewallet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCClientRefusesRedirectWithoutContactingDestination(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := newRPCClient(origin.URL, rpcRemoteBase, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output string
	if err := client.call(context.Background(), "eth_chainId", nil, &output); err == nil {
		t.Fatal("redirect was accepted")
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("redirect destination was contacted")
	}
}

func TestRPCClientBoundsAndStrictlyDecodesResponses(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"oversized": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, strings.Repeat("x", maxRPCResponseBytes+1))
		},
		"unknown envelope field": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1","unexpected":true}`)
		},
		"wrong id": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":"0x1"}`)
		},
		"trailing value": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"} {}`)
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			client, err := newRPCClient(server.URL, rpcRemoteBase, time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			var output string
			if err := client.call(context.Background(), "eth_chainId", nil, &output); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}

func TestRPCClientHonorsCancellation(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-request.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()
	client, err := newRPCClient(server.URL, rpcRemoteBase, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		var output string
		finished <- client.call(ctx, "eth_chainId", nil, &output)
	}()
	<-entered
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled RPC returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled RPC did not return")
	}
}
