package evidencefetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRequestDigest = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type targetConnector struct {
	target string
	mu     sync.Mutex
	calls  int
}

func (c *targetConnector) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, c.target)
}

type sequenceResolver struct {
	mu        sync.Mutex
	sequences map[string][][]netip.Addr
	calls     map[string]int
}

func (r *sequenceResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.calls[host]
	r.calls[host]++
	sequence := r.sequences[host]
	if len(sequence) == 0 {
		return nil, fmt.Errorf("host %s not found", host)
	}
	if index >= len(sequence) {
		index = len(sequence) - 1
	}
	return append([]netip.Addr(nil), sequence[index]...), nil
}

func TestFetchProducesDeterministicEvidence(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != "public.example" {
			t.Errorf("Host = %q, want public.example", request.Host)
		}
		if request.Header.Get("User-Agent") != "FlowOps-Test/1.0" {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", request.Header.Get("Accept-Encoding"))
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("ETag", `"proof-v1"`)
		_, _ = writer.Write([]byte(`<html><style>hidden</style><body><h1>FlowOps</h1><p>Hello   world &amp; proof</p><script>steal()</script></body></html>`))
	}))
	defer upstream.Close()

	now := time.Date(2026, 8, 11, 8, 30, 0, 0, time.UTC)
	service := testService(t, upstream, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Config{
		MaxResponseBytes: 1024, MaxOutputBytes: 512, UserAgent: "FlowOps-Test/1.0", Now: func() time.Time { return now },
	})
	result, err := service.Fetch(context.Background(), Request{URL: "http://public.example/article", RequestDigest: testRequestDigest})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	const normalized = "FlowOps Hello world & proof"
	if result.Text != normalized || result.TextTruncated {
		t.Fatalf("text = %q, truncated = %v", result.Text, result.TextTruncated)
	}
	if result.SourceURL != "http://public.example/article" || result.FinalURL != result.SourceURL {
		t.Fatalf("URLs = source %q final %q", result.SourceURL, result.FinalURL)
	}
	if !result.FetchedAt.Equal(now) || result.HTTP.StatusCode != http.StatusOK || result.HTTP.ContentType != "text/html" || result.HTTP.ETag != `"proof-v1"` {
		t.Fatalf("metadata = %#v at %s", result.HTTP, result.FetchedAt)
	}
	expectedHash := sha256.Sum256([]byte(normalized))
	if result.ContentSHA256 != "0x"+hex.EncodeToString(expectedHash[:]) {
		t.Fatalf("ContentSHA256 = %s", result.ContentSHA256)
	}
	if !digestPattern.MatchString(result.FetchDigest) || !digestPattern.MatchString(result.SourceSHA256) {
		t.Fatalf("digests are not canonical: %#v", result)
	}
}

func TestRedirectPolicyLimitsHopsAndRejectsDowngrade(t *testing.T) {
	t.Parallel()
	resolver := staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}
	service, err := New(Config{MaxRedirects: 2, Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://public.example/next", nil)
	via := []*http.Request{
		httptest.NewRequest(http.MethodGet, "https://public.example/start", nil),
		httptest.NewRequest(http.MethodGet, "https://public.example/middle", nil),
	}
	if err := service.client.CheckRedirect(request, via); err != nil {
		t.Fatalf("second redirect error = %v", err)
	}
	via = append(via, httptest.NewRequest(http.MethodGet, "https://public.example/last", nil))
	if err := service.client.CheckRedirect(request, via); err == nil || ErrorCode(err) != CodeUnsafeURL {
		t.Fatalf("redirect-limit error = %v", err)
	}
	downgrade := httptest.NewRequest(http.MethodGet, "http://public.example/insecure", nil)
	if err := service.client.CheckRedirect(downgrade, via[:1]); err == nil || ErrorCode(err) != CodeUnsafeURL {
		t.Fatalf("downgrade error = %v", err)
	}
}

func TestFetchRevalidatesDNSAtConnectionTime(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsafe connection reached upstream")
	}))
	defer upstream.Close()
	resolver := &sequenceResolver{sequences: map[string][][]netip.Addr{
		"rebind.example": {{netip.MustParseAddr("8.8.8.8")}, {netip.MustParseAddr("127.0.0.1")}},
	}, calls: make(map[string]int)}
	connector := &targetConnector{target: upstream.Listener.Addr().String()}
	service, err := New(Config{MaxResponseBytes: 1024, MaxOutputBytes: 512, Resolver: resolver, Connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Fetch(context.Background(), Request{URL: "http://rebind.example/", RequestDigest: testRequestDigest})
	if err == nil || ErrorCode(err) != CodeUnsafeURL {
		t.Fatalf("Fetch() error = %v, want %s", err, CodeUnsafeURL)
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.calls != 0 {
		t.Fatalf("connector calls = %d, want 0", connector.calls)
	}
}

func TestFetchRevalidatesRedirectTarget(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer upstream.Close()
	service := testService(t, upstream, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Config{MaxResponseBytes: 1024, MaxOutputBytes: 512})
	_, err := service.Fetch(context.Background(), Request{URL: "http://public.example/redirect", RequestDigest: testRequestDigest})
	if err == nil || ErrorCode(err) != CodeUnsafeURL {
		t.Fatalf("Fetch() error = %v, want %s", err, CodeUnsafeURL)
	}
}

func TestFetchFailureStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		maxBytes    int64
		code        Code
	}{
		{"non-2xx", http.StatusUnauthorized, "text/plain", "login", 1024, CodeUpstreamFailure},
		{"unsupported content", http.StatusOK, "image/png", "not really an image", 1024, CodeUnsupportedContent},
		{"oversized", http.StatusOK, "text/plain", strings.Repeat("x", 65), 64, CodeResponseTooLarge},
		{"empty HTML", http.StatusOK, "text/html", "<html><script>only()</script></html>", 1024, CodeEmptyEvidence},
		{"non UTF-8", http.StatusOK, "text/plain; charset=iso-8859-1", "hello", 1024, CodeUnsupportedContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer upstream.Close()
			service := testService(t, upstream, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Config{MaxResponseBytes: test.maxBytes, MaxOutputBytes: min(test.maxBytes, 32)})
			_, err := service.Fetch(context.Background(), Request{URL: "http://public.example/resource", RequestDigest: testRequestDigest})
			if err == nil || ErrorCode(err) != test.code {
				t.Fatalf("Fetch() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestFetchRejectsUnexpectedContentEncoding(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.Header().Set("Content-Encoding", "gzip")
		_, _ = writer.Write([]byte("not compressed"))
	}))
	defer upstream.Close()
	service := testService(t, upstream, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Config{MaxResponseBytes: 1024, MaxOutputBytes: 512})
	_, err := service.Fetch(context.Background(), Request{URL: "http://public.example/resource", RequestDigest: testRequestDigest})
	if err == nil || ErrorCode(err) != CodeUnsupportedContent {
		t.Fatalf("Fetch() error = %v, want %s", err, CodeUnsupportedContent)
	}
}

func TestFetchTruncatesAtUTF8BoundaryButHashesFullEvidence(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("abc தமிழ் proof"))
	}))
	defer upstream.Close()
	service := testService(t, upstream, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Config{MaxResponseBytes: 1024, MaxOutputBytes: 64})
	result, err := service.Fetch(context.Background(), Request{URL: "http://public.example/text", MaxOutputBytes: 7, RequestDigest: testRequestDigest})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TextTruncated || result.Text != "abc த" {
		t.Fatalf("text = %q truncated = %v", result.Text, result.TextTruncated)
	}
	expected := sha256.Sum256([]byte("abc தமிழ் proof"))
	if result.ContentSHA256 != "0x"+hex.EncodeToString(expected[:]) {
		t.Fatalf("ContentSHA256 = %s", result.ContentSHA256)
	}
}

func testService(t *testing.T, upstream *httptest.Server, resolver Resolver, config Config) *Service {
	t.Helper()
	config.Resolver = resolver
	config.Connector = &targetConnector{target: upstream.Listener.Addr().String()}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
