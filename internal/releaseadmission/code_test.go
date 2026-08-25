package releaseadmission

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

func TestVerifyCodeQuorumRequiresEveryProviderToMatchEveryBinding(t *testing.T) {
	manifest := validManifest(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	codes := make(map[string]string)
	for index := range manifest.Contracts {
		code := fmt.Sprintf("0x60%02x", index+1)
		manifest.Contracts[index].RuntimeCodeHash = crypto.Keccak256Hash(mustCode(t, code)).Hex()
		codes[manifest.Contracts[index].Address] = code
	}
	assetCode := "0x6010"
	manifest.Asset.RuntimeCodeHash = crypto.Keccak256Hash(mustCode(t, assetCode)).Hex()
	codes[manifest.Asset.Address] = assetCode

	first := releaseRPCServer(t, codes, "")
	second := releaseRPCServer(t, codes, "")
	providers := []reconciliation.RPCProvider{{Name: "rpc_alpha", URL: first.URL}, {Name: "rpc_beta", URL: second.URL}}
	if err := VerifyCodeQuorum(t.Context(), providers, manifest, first.Client()); err != nil {
		t.Fatal(err)
	}

	mismatch := releaseRPCServer(t, codes, manifest.Contracts[2].Address)
	providers[1].URL = mismatch.URL
	if err := VerifyCodeQuorum(t.Context(), providers, manifest, first.Client()); err == nil || !strings.Contains(err.Error(), "runtime code hash mismatch") {
		t.Fatalf("mismatch error=%v", err)
	}
}

func TestVerifyCodeQuorumRejectsWrongChainRedirectAndPartialSet(t *testing.T) {
	manifest := validManifest(time.Now().UTC())
	wrongChain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x14a34"})
	}))
	t.Cleanup(wrongChain.Close)
	providers := []reconciliation.RPCProvider{{Name: "rpc_alpha", URL: wrongChain.URL}, {Name: "rpc_beta", URL: wrongChain.URL}}
	if err := VerifyCodeQuorum(t.Context(), providers, manifest, wrongChain.Client()); err == nil || !strings.Contains(err.Error(), "wrong chain") {
		t.Fatalf("wrong-chain error=%v", err)
	}
	if err := VerifyCodeQuorum(t.Context(), providers[:1], manifest, wrongChain.Client()); err == nil {
		t.Fatal("single-provider code verification accepted")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, wrongChain.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	providers = []reconciliation.RPCProvider{{Name: "rpc_alpha", URL: redirect.URL}, {Name: "rpc_beta", URL: redirect.URL}}
	if err := VerifyCodeQuorum(t.Context(), providers, manifest, redirect.Client()); err == nil {
		t.Fatal("redirecting bytecode provider was accepted")
	}
}

func TestHardenedClientUsesBoundedDirectTLSTransport(t *testing.T) {
	client := hardenedClient(nil)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 ||
		transport.TLSHandshakeTimeout != 5*time.Second || transport.ResponseHeaderTimeout != 8*time.Second ||
		transport.MaxResponseHeaderBytes != 64<<10 {
		t.Fatalf("unhardened release RPC transport: %+v", transport)
	}
}

func releaseRPCServer(t *testing.T, codes map[string]string, mismatchAddress string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input rpcRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		result := any("0x2105")
		if input.Method == "eth_getCode" {
			address, _ := input.Params[0].(string)
			result = codes[address]
			if address == mismatchAddress {
				result = "0x60ff"
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": input.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func mustCode(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hexutil.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
