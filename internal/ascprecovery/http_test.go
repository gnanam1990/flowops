package ascprecovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascprails"
)

func TestHTTPSReadersEnforceExactContracts(t *testing.T) {
	ref := "ascp/checkpoints/checkpoint_" + strings.Repeat("a", 64) + ".json"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/worm":
			if request.URL.Query().Get("ref") != ref || request.Header.Get("Accept") != "application/octet-stream" {
				t.Error("WORM request lost exact binding")
			}
			writer.Header().Set("Content-Type", "application/octet-stream")
			_, _ = writer.Write([]byte("checkpoint-bytes"))
		case "/head":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"lastSeq":9,"lastEventHash":"` + strings.Repeat("b", 64) + `"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	wormURL, _ := url.Parse(server.URL + "/worm")
	worm := &HTTPSWORMReader{endpoint: wormURL, client: server.Client()}
	got, err := worm.Get(t.Context(), ref)
	if err != nil || string(got) != "checkpoint-bytes" {
		t.Fatalf("WORM body=%q err=%v", got, err)
	}
	if _, err := worm.Get(t.Context(), "../secret"); err == nil {
		t.Fatal("unsafe WORM reference accepted")
	}
	headURL, _ := url.Parse(server.URL + "/head")
	remote := &HTTPSRemoteHeadReader{endpoint: headURL, client: server.Client()}
	head, err := remote.Current(t.Context())
	if err != nil || head.Sequence != 9 || head.EventHash != strings.Repeat("b", 64) {
		t.Fatalf("head=%+v err=%v", head, err)
	}
}

func TestHTTPSRemoteHeadRejectsNonCanonicalAndTrailingJSON(t *testing.T) {
	for _, body := range []string{
		`{"lastSeq":1,"lastSeq":1,"lastEventHash":"` + strings.Repeat("a", 64) + `"}`,
		`{"LastSeq":1,"lastEventHash":"` + strings.Repeat("a", 64) + `"}`,
		`{"lastSeq":1,"lastEventHash":"` + strings.Repeat("a", 64) + `","unknown":true}`,
		`{"lastSeq":1,"lastEventHash":"` + strings.Repeat("a", 64) + `"} {}`,
		`{"lastSeq":0,"lastEventHash":"` + strings.Repeat("0", 64) + `"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(body))
		}))
		endpoint, _ := url.Parse(server.URL)
		reader := &HTTPSRemoteHeadReader{endpoint: endpoint, client: server.Client()}
		if _, err := reader.Current(t.Context()); err == nil {
			server.Close()
			t.Fatalf("invalid head accepted: %s", body)
		}
		server.Close()
	}
}

func TestRestrictedReadersRefusePrivateAndCredentialedURLs(t *testing.T) {
	for _, raw := range []string{"http://example.com/head", "https://127.0.0.1/head", "https://user@example.com/head", "https://example.com:8443/head", "https://example.com/head?token=secret"} {
		if reader, err := NewHTTPSRemoteHeadReader(raw, time.Second); err == nil || reader != nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
	_, client, err := restrictedClient("https://example.com/head", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	original := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/head", nil)
	redirect := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://other.example/head", nil)
	if err := client.CheckRedirect(redirect, []*http.Request{original}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error=%v", err)
	}
}

func TestHandlerReturnsSignedProofAndRedactsFailures(t *testing.T) {
	source := &staticAttestationSource{attestation: ascprails.IntegrityAttestation{SchemaVersion: 1, State: "VERIFIED", LocalSequence: 1}}
	var failures atomic.Int64
	handler, err := NewHandler(source, func(error) { failures.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/recovery", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var got ascprails.IntegrityAttestation
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || got.State != "VERIFIED" ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	source.err = errors.New("database password secret-value")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "secret-value") || failures.Load() != 1 {
		t.Fatalf("failure response=%d body=%s failures=%d", response.Code, response.Body.String(), failures.Load())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/recovery?x=1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("query endpoint status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/recovery", strings.NewReader("unexpected")))
	if response.Code != http.StatusNotFound {
		t.Fatalf("body endpoint status=%d", response.Code)
	}
}

type staticAttestationSource struct {
	attestation ascprails.IntegrityAttestation
	err         error
}

func (s *staticAttestationSource) Latest(context.Context) (ascprails.IntegrityAttestation, error) {
	return s.attestation, s.err
}
