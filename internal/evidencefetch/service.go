package evidencefetch

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

const fetchDomain = "flowops:evidence-fetch:v1\n"

type Config struct {
	MaxResponseBytes int64
	MaxOutputBytes   int64
	MaxRedirects     int
	Timeout          time.Duration
	UserAgent        string
	Resolver         Resolver
	Connector        Connector
	Now              func() time.Time
}

type Service struct {
	config Config
	client *http.Client
}

func New(config Config) (*Service, error) {
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 2 << 20
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 256 << 10
	}
	if config.MaxRedirects == 0 {
		config.MaxRedirects = 5
	}
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	if config.UserAgent == "" {
		config.UserAgent = "FlowOps-Evidence-Fetch/1.0"
	}
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Connector == nil {
		config.Connector = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.MaxResponseBytes < 1 || config.MaxOutputBytes < 1 || config.MaxOutputBytes > config.MaxResponseBytes {
		return nil, fmt.Errorf("evidencefetch: byte limits are invalid")
	}
	if config.MaxRedirects < 1 || config.MaxRedirects > 10 {
		return nil, fmt.Errorf("evidencefetch: max redirects must be between 1 and 10")
	}
	if config.Timeout <= 0 || config.Timeout > time.Minute {
		return nil, fmt.Errorf("evidencefetch: timeout must be between zero and one minute")
	}
	guard := networkGuard{resolver: config.Resolver, connector: config.Connector}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            guard.DialContext,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           20,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  config.Timeout,
		MaxResponseHeaderBytes: 64 << 10,
		ExpectContinueTimeout:  time.Second,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	service := &Service{config: config}
	service.client = &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > config.MaxRedirects {
				return newError(CodeUnsafeURL, "redirect limit exceeded", nil)
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && request.URL.Scheme == "http" {
				return newError(CodeUnsafeURL, "HTTPS redirects may not downgrade to HTTP", nil)
			}
			_, err := validateURL(request.Context(), request.URL.String(), config.Resolver)
			return err
		},
	}
	return service, nil
}

func (s *Service) Fetch(ctx context.Context, input Request) (Result, error) {
	if err := input.validate(s.config.MaxOutputBytes); err != nil {
		return Result{}, err
	}
	parsed, err := validateURL(ctx, input.URL, s.config.Resolver)
	if err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Result{}, newError(CodeInvalidRequest, "fetch request could not be created", err)
	}
	request.Header.Set("Accept", "text/html, text/plain, application/json, application/xml;q=0.9, text/*;q=0.8")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", s.config.UserAgent)
	response, err := s.client.Do(request)
	if err != nil {
		var fetchErr *Error
		if errors.As(err, &fetchErr) {
			return Result{}, fetchErr
		}
		return Result{}, newError(CodeUpstreamFailure, "upstream request failed", err)
	}
	defer response.Body.Close()
	if encoding := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Encoding"))); encoding != "" && encoding != "identity" {
		return Result{}, newError(CodeUnsupportedContent, "encoded response bodies are not supported", nil)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return Result{}, newError(CodeUpstreamFailure, fmt.Sprintf("upstream returned HTTP %d", response.StatusCode), nil)
	}
	if response.ContentLength > s.config.MaxResponseBytes {
		return Result{}, newError(CodeResponseTooLarge, "response exceeds the configured byte limit", nil)
	}
	mediaType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return Result{}, newError(CodeUnsupportedContent, "response Content-Type is invalid", err)
	}
	mediaType = strings.ToLower(mediaType)
	if !allowedMediaType(mediaType) {
		return Result{}, newError(CodeUnsupportedContent, "response Content-Type is not supported", nil)
	}
	if charset := strings.ToLower(parameters["charset"]); charset != "" && charset != "utf-8" && charset != "utf8" {
		return Result{}, newError(CodeUnsupportedContent, "only UTF-8 response content is supported", nil)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, s.config.MaxResponseBytes+1))
	if err != nil {
		return Result{}, newError(CodeUpstreamFailure, "response body could not be read", err)
	}
	if int64(len(body)) > s.config.MaxResponseBytes {
		return Result{}, newError(CodeResponseTooLarge, "response exceeds the configured byte limit", nil)
	}
	normalized, err := normalize(body, mediaType)
	if err != nil {
		return Result{}, err
	}
	if normalized == "" {
		return Result{}, newError(CodeEmptyEvidence, "response did not contain deliverable text", nil)
	}
	outputLimit := input.MaxOutputBytes
	if outputLimit == 0 {
		outputLimit = s.config.MaxOutputBytes
	}
	output, truncated, err := truncateUTF8(normalized, outputLimit)
	if err != nil {
		return Result{}, newError(CodeUpstreamFailure, "normalized response could not be bounded", err)
	}
	contentHash := sha256.Sum256([]byte(normalized))
	sourceHash := sha256.Sum256(body)
	fetchDigest, err := digestRequest(input, parsed.String(), outputLimit)
	if err != nil {
		return Result{}, newError(CodeInvalidRequest, "fetch digest could not be created", err)
	}
	return Result{
		SourceURL: input.URL, FinalURL: response.Request.URL.String(), FetchedAt: s.config.Now().UTC(),
		RequestDigest: input.RequestDigest, FetchDigest: fetchDigest, Text: output, TextTruncated: truncated,
		ContentSHA256: "0x" + hex.EncodeToString(contentHash[:]), SourceSHA256: "0x" + hex.EncodeToString(sourceHash[:]),
		HTTP: HTTPMetadata{StatusCode: response.StatusCode, ContentType: mediaType, ContentLength: int64(len(body)), ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified")},
	}, nil
}

func digestRequest(input Request, canonicalURL string, outputLimit int64) (string, error) {
	payload := struct {
		URL            string `json:"url"`
		Mode           string `json:"mode"`
		MaxOutputBytes int64  `json:"maxOutputBytes"`
		RequestDigest  string `json:"requestDigest"`
	}{canonicalURL, effectiveMode(input.Mode), outputLimit, input.RequestDigest}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(fetchDomain), encoded...))
	return "0x" + hex.EncodeToString(sum[:]), nil
}

func effectiveMode(mode string) string {
	if mode == "" {
		return ModeAuto
	}
	return mode
}

func allowedMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" || mediaType == "application/xhtml+xml"
}
