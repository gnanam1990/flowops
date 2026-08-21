package ascprecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpevents"
	"github.com/gnanam1990/flowops/internal/ascprails"
)

const (
	maxWORMBytes       = 32 << 10
	maxRemoteHeadBytes = 4 << 10
)

var (
	wormReferencePattern = regexp.MustCompile(`^ascp/checkpoints/checkpoint_[0-9a-f]{64}\.json$`)
	rawHashPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type HTTPSWORMReader struct {
	endpoint *url.URL
	client   *http.Client
}

func NewHTTPSWORMReader(rawURL string, timeout time.Duration) (*HTTPSWORMReader, error) {
	endpoint, client, err := restrictedClient(rawURL, timeout)
	if err != nil {
		return nil, err
	}
	return &HTTPSWORMReader{endpoint: endpoint, client: client}, nil
}

func (r *HTTPSWORMReader) Get(ctx context.Context, ref string) ([]byte, error) {
	if !wormReferencePattern.MatchString(ref) {
		return nil, ascpevents.ErrIntegrity
	}
	requestURL := *r.endpoint
	query := requestURL.Query()
	query.Set("ref", ref)
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Host = request.URL.Host
	request.Header.Set("Accept", "application/octet-stream")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, errors.New("immutable checkpoint request failed")
	}
	defer func() { _ = response.Body.Close() }()
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || mediaType != "application/octet-stream" {
		return nil, errors.New("immutable checkpoint endpoint returned an invalid response")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxWORMBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxWORMBytes {
		return nil, errors.New("immutable checkpoint response could not be read safely")
	}
	return body, nil
}

type HTTPSRemoteHeadReader struct {
	endpoint *url.URL
	client   *http.Client
}

func NewHTTPSRemoteHeadReader(rawURL string, timeout time.Duration) (*HTTPSRemoteHeadReader, error) {
	endpoint, client, err := restrictedClient(rawURL, timeout)
	if err != nil {
		return nil, err
	}
	return &HTTPSRemoteHeadReader{endpoint: endpoint, client: client}, nil
}

func (r *HTTPSRemoteHeadReader) Current(ctx context.Context) (ascpevents.Head, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint.String(), nil)
	if err != nil {
		return ascpevents.Head{}, err
	}
	request.Host = request.URL.Host
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return ascpevents.Head{}, errors.New("remote event-head request failed")
	}
	defer func() { _ = response.Body.Close() }()
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || mediaType != "application/json" {
		return ascpevents.Head{}, errors.New("remote event-head endpoint returned an invalid response")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteHeadBytes+1))
	if err != nil || len(raw) > maxRemoteHeadBytes || rejectHeadKeys(raw) != nil {
		return ascpevents.Head{}, errors.New("remote event-head response is not strict JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var head ascpevents.Head
	if err := decoder.Decode(&head); err != nil {
		return ascpevents.Head{}, errors.New("remote event-head response is not strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || head.Sequence == 0 || !rawHash(head.EventHash) {
		return ascpevents.Head{}, errors.New("remote event-head response is invalid")
	}
	return head, nil
}

func restrictedClient(rawURL string, timeout time.Duration) (*url.URL, *http.Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.RawQuery != "" || (parsed.Port() != "" && parsed.Port() != "443") || timeout < time.Second || timeout > 30*time.Second {
		return nil, nil, ErrInvalidConfig
	}
	if err := ascprails.ValidateRestrictedURLShape(parsed.String()); err != nil {
		return nil, nil, ErrInvalidConfig
	}
	transport, err := ascprails.NewRestrictedTransport()
	if err != nil {
		return nil, nil, err
	}
	client := &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return parsed, client, nil
}

func rejectHeadKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("head must be an object")
	}
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{"lastSeq": {}, "lastEventHash": {}}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return errors.New("head key is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("head key is duplicated")
		}
		if _, ok := allowed[key]; !ok {
			return errors.New("head key is unknown")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func rawHash(value string) bool {
	return rawHashPattern.MatchString(value) && value != strings.Repeat("0", 64)
}

type AttestationSource interface {
	Latest(context.Context) (ascprails.IntegrityAttestation, error)
}

type Handler struct {
	source  AttestationSource
	onError func(error)
}

func NewHandler(source AttestationSource, onError func(error)) (*Handler, error) {
	if isNil(source) || onError == nil {
		return nil, ErrInvalidConfig
	}
	return &Handler{source: source, onError: onError}, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet || request.URL.Path != "/v1/recovery" || request.URL.RawQuery != "" ||
		request.ContentLength > 0 || len(request.TransferEncoding) > 0 || request.Body != nil && request.Body != http.NoBody {
		http.NotFound(writer, request)
		return
	}
	attestation, err := h.source.Latest(request.Context())
	if err != nil {
		h.onError(err)
		http.Error(writer, "event recovery unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(attestation); err != nil {
		h.onError(err)
	}
}

var _ ascpevents.WORMReader = (*HTTPSWORMReader)(nil)
var _ ascpevents.RemoteHeadReader = (*HTTPSRemoteHeadReader)(nil)
var _ http.Handler = (*Handler)(nil)
