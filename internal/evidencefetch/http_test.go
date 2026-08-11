package evidencefetch

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestHandlerSmoke(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("evidence delivered"))
	}))
	defer upstream.Close()
	service := testService(t, upstream, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Config{MaxResponseBytes: 1024, MaxOutputBytes: 512})
	server := httptest.NewServer(Handler(service))
	defer server.Close()

	response, err := http.Post(server.URL+"/v1/fetch", "application/json", strings.NewReader(`{"url":"http://public.example/proof","requestDigest":"`+testRequestDigest+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status = %d headers = %#v", response.StatusCode, response.Header)
	}
}

func TestHandlerRejectsUnknownFieldsAndUnsafeURL(t *testing.T) {
	t.Parallel()
	service, err := New(Config{Resolver: staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(service)
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
	}{
		{"unknown field", "application/json", `{"url":"http://public.example/","requestDigest":"` + testRequestDigest + `","extra":true}`, http.StatusBadRequest},
		{"multiple values", "application/json", `{} {}`, http.StatusBadRequest},
		{"wrong content type", "text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"unsafe URL", "application/json", `{"url":"http://127.0.0.1/","requestDigest":"` + testRequestDigest + `"}`, http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/v1/fetch", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

func TestHealthDoesNotFetch(t *testing.T) {
	t.Parallel()
	service, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	Handler(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
}
