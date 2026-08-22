package ascpbearer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	runtimeBoundaryProtocol = "ASCP_BEARER_RUNTIME_V1"
	maxRuntimeBoundaryBody  = 2 << 20
)

type RuntimeBoundaryError struct {
	Boundary   string
	Code       string
	StatusCode int
}

func (e *RuntimeBoundaryError) Error() string {
	return fmt.Sprintf("bearer %s boundary failed with HTTP %d code %s", e.Boundary, e.StatusCode, e.Code)
}

func (e *RuntimeBoundaryError) Unwrap() error { return ErrRuntimeBoundary }

type RuntimeUnixBoundary struct {
	name, socketPath string
	client           *http.Client
}

func NewRuntimeUnixBoundary(name, socketPath string, timeout time.Duration) (*RuntimeUnixBoundary, error) {
	path := strings.TrimSpace(socketPath)
	if !identifier(name) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" ||
		timeout < time.Second || timeout > 10*time.Second {
		return nil, ErrActivationInput
	}
	dialer := &net.Dialer{Timeout: min(timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
		DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout,
		MaxResponseHeaderBytes: 16 << 10,
	}
	return &RuntimeUnixBoundary{name: name, socketPath: path, client: &http.Client{
		Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (b *RuntimeUnixBoundary) Check(ctx context.Context) error {
	var response struct {
		Protocol string `json:"protocol"`
		Boundary string `json:"boundary"`
		Status   string `json:"status"`
	}
	if err := b.call(ctx, http.MethodGet, "/healthz", nil, &response); err != nil {
		return err
	}
	if response.Protocol != runtimeBoundaryProtocol || response.Boundary != b.name || response.Status != "ok" {
		return errors.Join(ErrRuntimeBoundary, errors.New("bearer boundary health identity mismatch"))
	}
	return nil
}

func (b *RuntimeUnixBoundary) call(ctx context.Context, method, endpoint string, input, output any) error {
	if err := validateRuntimeSocket(b.socketPath); err != nil {
		return errors.Join(ErrRuntimeBoundary, fmt.Errorf("validate %s socket: %w", b.name, err))
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) == 0 || len(encoded) > maxRuntimeBoundaryBody {
			clear(encoded)
			return errors.Join(ErrRuntimeBoundary, errors.New("bearer boundary request is invalid or too large"))
		}
		defer clear(encoded)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+endpoint, body)
	if err != nil {
		return errors.Join(ErrRuntimeBoundary, err)
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return errors.Join(ErrRuntimeBoundary, fmt.Errorf("call bearer %s boundary: %w", b.name, err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRuntimeBoundaryBody+1))
	defer clear(raw)
	if err != nil || len(raw) > maxRuntimeBoundaryBody {
		return errors.Join(ErrRuntimeBoundary, errors.New("bearer boundary response is unreadable or too large"))
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || strings.ToLower(mediaType) != "application/json" {
		return errors.Join(ErrRuntimeBoundary, errors.New("bearer boundary response is not JSON"))
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if decodeStrictRuntimeJSON(raw, &failure) != nil || !identifier(failure.Code) {
			failure.Code = "UNCLASSIFIED"
		}
		return &RuntimeBoundaryError{Boundary: b.name, Code: failure.Code, StatusCode: response.StatusCode}
	}
	if output == nil {
		return errors.Join(ErrRuntimeBoundary, errors.New("bearer boundary output contract is missing"))
	}
	if err := decodeStrictRuntimeJSON(raw, output); err != nil {
		return errors.Join(ErrRuntimeBoundary, fmt.Errorf("decode bearer %s response: %w", b.name, err))
	}
	return nil
}

func decodeStrictRuntimeJSON(raw []byte, output any) error {
	if err := rejectDuplicateRuntimeKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("bearer boundary response must contain exactly one JSON value")
	}
	return nil
}

func rejectDuplicateRuntimeKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 64 {
			return errors.New("bearer boundary JSON nesting exceeds 64 levels")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("bearer boundary response is not valid JSON")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return errors.New("bearer boundary response object is invalid")
				}
				if _, exists := seen[key]; exists {
					return errors.New("bearer boundary response contains duplicate fields")
				}
				seen[key] = struct{}{}
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("bearer boundary response delimiter is invalid")
		}
		if _, err := decoder.Token(); err != nil {
			return errors.New("bearer boundary response delimiter is not closed")
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("bearer boundary response must contain exactly one JSON value")
	}
	return nil
}

type runtimeSocketIdentity struct {
	device uint64
	inode  uint64
}

func validateRuntimeSocket(path string) error {
	_, err := inspectRuntimeSocket(path)
	return err
}

func inspectRuntimeSocket(path string) (runtimeSocketIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o002 != 0 {
		return runtimeSocketIdentity{}, errors.New("path must be a non-symlink Unix socket that is not world-writable")
	}
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 {
		return runtimeSocketIdentity{}, errors.New("socket parent must be a non-symlink directory that is not group- or world-writable")
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != uint32(os.Geteuid()) && parentStat.Uid != 0 {
		return runtimeSocketIdentity{}, errors.New("socket parent must be owned by the runtime user or root")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		return runtimeSocketIdentity{}, errors.New("socket must be owned by the runtime user or root")
	}
	return runtimeSocketIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func ValidateRuntimeSockets(paths map[string]string) error {
	seen := make(map[runtimeSocketIdentity]string, len(paths))
	for name, path := range paths {
		identity, err := inspectRuntimeSocket(path)
		if err != nil {
			return fmt.Errorf("inspect bearer %s boundary socket: %w", name, err)
		}
		if previous, exists := seen[identity]; exists {
			return fmt.Errorf("bearer boundaries %s and %s resolve to the same Unix socket", previous, name)
		}
		seen[identity] = name
	}
	return nil
}

type RuntimeUnixSigner struct{ boundary *RuntimeUnixBoundary }

func NewRuntimeUnixSigner(boundary *RuntimeUnixBoundary) (*RuntimeUnixSigner, error) {
	if boundary == nil || boundary.name != "signer" {
		return nil, ErrActivationInput
	}
	return &RuntimeUnixSigner{boundary: boundary}, nil
}

func (s *RuntimeUnixSigner) Prepare(ctx context.Context, input ActivationInput) (string, error) {
	input.ValidAfter, input.ValidUntil = input.ValidAfter.UTC(), input.ValidUntil.UTC()
	if err := validateActivationInput(input, time.Now().UTC()); err != nil {
		return "", err
	}
	request := struct {
		Protocol string          `json:"protocol"`
		Input    ActivationInput `json:"input"`
	}{runtimeBoundaryProtocol, input}
	var response struct {
		HandleID string `json:"handleId"`
	}
	if err := s.boundary.call(ctx, http.MethodPost, "/v1/prepare", request, &response); err != nil {
		return "", err
	}
	if !opaqueHandle(response.HandleID) {
		return "", errors.Join(ErrRuntimeBoundary, ErrActivationBinding)
	}
	return response.HandleID, nil
}

func (s *RuntimeUnixSigner) AcknowledgeActivation(ctx context.Context, proof ActivationProof) error {
	proof.ActivationOccurredAt = proof.ActivationOccurredAt.UTC()
	if !hash(proof.RequestID) || !opaqueHandle(proof.HandleID) || !hash(proof.OperationID) ||
		!hash(proof.Digest) || !hash(proof.Nonce) || !hash(proof.PrimaryMirrorDigest) || proof.ActivationOccurredAt.IsZero() {
		return ErrActivationInput
	}
	request := struct {
		Protocol string          `json:"protocol"`
		Proof    ActivationProof `json:"proof"`
	}{runtimeBoundaryProtocol, proof}
	var response struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := s.boundary.call(ctx, http.MethodPost, "/v1/acknowledge", request, &response); err != nil {
		return err
	}
	if !response.Acknowledged {
		return errors.Join(ErrRuntimeBoundary, ErrActivationBinding)
	}
	return nil
}

func (s *RuntimeUnixSigner) ProveUnactivated(ctx context.Context, activation ActivationRequest) (UnactivatedProof, error) {
	if !hash(activation.RequestID) || !hash(activation.OperationID) || !identifier(activation.ActionID) || !hash(activation.InputHash) {
		return UnactivatedProof{}, ErrActivationInput
	}
	request := struct {
		Protocol    string `json:"protocol"`
		RequestID   string `json:"requestId"`
		OperationID string `json:"operationId"`
		ActionID    string `json:"actionId"`
		InputHash   string `json:"inputHash"`
	}{runtimeBoundaryProtocol, activation.RequestID, activation.OperationID, activation.ActionID, activation.InputHash}
	var response struct {
		Proof UnactivatedProof `json:"proof"`
	}
	if err := s.boundary.call(ctx, http.MethodPost, "/v1/prove-unactivated", request, &response); err != nil {
		return UnactivatedProof{}, err
	}
	return response.Proof, nil
}

type RuntimeUnixMirror struct{ boundary *RuntimeUnixBoundary }

func NewRuntimeUnixMirror(boundary *RuntimeUnixBoundary) (*RuntimeUnixMirror, error) {
	if boundary == nil || boundary.name != "mirror" {
		return nil, ErrActivationInput
	}
	return &RuntimeUnixMirror{boundary: boundary}, nil
}

func (m *RuntimeUnixMirror) PutPrimary(ctx context.Context, objectKey string, payload []byte, digest string) error {
	const prefix, suffix = "bearer-registry/", ".json"
	objectDigest := strings.TrimSuffix(strings.TrimPrefix(objectKey, prefix), suffix)
	payloadDigest := sha256.Sum256(append([]byte("ASCP_BEARER_REGISTRY_MIRROR_V1\n"), payload...))
	expectedDigest := "0x" + hex.EncodeToString(payloadDigest[:])
	if objectKey != prefix+objectDigest+suffix || !hash(objectDigest) || objectDigest != digest ||
		!hash(digest) || expectedDigest != digest || len(payload) == 0 || len(payload) > maxRuntimeBoundaryBody {
		return ErrActivationInput
	}
	request := struct {
		Protocol  string `json:"protocol"`
		ObjectKey string `json:"objectKey"`
		Payload   []byte `json:"payload"`
		Digest    string `json:"digest"`
	}{runtimeBoundaryProtocol, objectKey, payload, digest}
	var response struct {
		Outcome string `json:"outcome"`
		Digest  string `json:"digest"`
	}
	if err := m.boundary.call(ctx, http.MethodPost, "/v1/put-primary", request, &response); err != nil {
		return err
	}
	if response.Digest != digest || response.Outcome != "CREATED" && response.Outcome != "EXISTS_EXACT" {
		return errors.Join(ErrRuntimeBoundary, ErrActivationBinding)
	}
	return nil
}

var _ RuntimeSigner = (*RuntimeUnixSigner)(nil)
var _ PrimaryRegistryMirror = (*RuntimeUnixMirror)(nil)
