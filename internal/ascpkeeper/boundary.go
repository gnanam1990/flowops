package ascpkeeper

import (
	"bytes"
	"context"
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

	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

const (
	boundaryProtocolVersion = "ASCP_KEEPER_BOUNDARY_V1"
	maxBoundaryBodyBytes    = 2 << 20
)

type BoundaryError struct {
	Boundary string
	Code     string
}

func (e *BoundaryError) Error() string {
	return fmt.Sprintf("keeper %s boundary failed with code %s", e.Boundary, e.Code)
}

type UnixBoundary struct {
	name, socketPath string
	client           *http.Client
}

func NewUnixBoundary(name, socketPath string, timeout time.Duration) (*UnixBoundary, error) {
	path := strings.TrimSpace(socketPath)
	if !identifier(name) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || timeout < time.Second || timeout > time.Minute {
		return nil, ErrInvalidConfig
	}
	dialer := &net.Dialer{Timeout: min(timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
		DisableCompression: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 4,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout,
		MaxResponseHeaderBytes: 16 << 10,
	}
	return &UnixBoundary{name: name, socketPath: path, client: &http.Client{
		Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (b *UnixBoundary) Check(ctx context.Context) error {
	if err := validateSocket(b.socketPath); err != nil {
		return fmt.Errorf("validate %s boundary socket: %w", b.name, err)
	}
	var response struct {
		Protocol string `json:"protocol"`
		Boundary string `json:"boundary"`
		Status   string `json:"status"`
	}
	if err := b.call(ctx, http.MethodGet, "/healthz", nil, &response); err != nil {
		return err
	}
	if response.Protocol != boundaryProtocolVersion || response.Boundary != b.name || response.Status != "ok" {
		return errors.New("keeper boundary health identity mismatch")
	}
	return nil
}

func (b *UnixBoundary) call(ctx context.Context, method, endpoint string, input, output any) error {
	if err := validateSocket(b.socketPath); err != nil {
		return fmt.Errorf("validate %s boundary socket: %w", b.name, err)
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) == 0 || len(encoded) > maxBoundaryBodyBytes {
			return errors.New("keeper boundary request is invalid or too large")
		}
		defer clear(encoded)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("call keeper %s boundary: %w", b.name, err)
	}
	defer func() { _ = response.Body.Close() }()
	limited := io.LimitReader(response.Body, maxBoundaryBodyBytes+1)
	raw, err := io.ReadAll(limited)
	defer clear(raw)
	if err != nil || len(raw) > maxBoundaryBodyBytes {
		return errors.New("keeper boundary response is unreadable or too large")
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if decodeStrictBoundary(raw, &failure) != nil || !identifier(failure.Code) {
			failure.Code = "UNCLASSIFIED"
		}
		return &BoundaryError{Boundary: b.name, Code: failure.Code}
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || strings.ToLower(mediaType) != "application/json" {
		return errors.New("keeper boundary response is not JSON")
	}
	if output == nil {
		return errors.New("keeper boundary output contract is missing")
	}
	if err := decodeStrictBoundary(raw, output); err != nil {
		return fmt.Errorf("decode keeper %s boundary response: %w", b.name, err)
	}
	return nil
}

func decodeStrictBoundary(raw []byte, output any) error {
	if err := rejectDuplicateBoundaryKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("boundary response must contain exactly one JSON value")
	}
	return nil
}

func rejectDuplicateBoundaryKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 64 {
			return errors.New("boundary JSON nesting exceeds 64 levels")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("boundary response is not valid JSON")
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
					return errors.New("boundary response object is invalid")
				}
				if _, exists := seen[key]; exists {
					return errors.New("boundary response contains duplicate fields")
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
			return errors.New("boundary response delimiter is invalid")
		}
		if _, err := decoder.Token(); err != nil {
			return errors.New("boundary response delimiter is not closed")
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("boundary response must contain exactly one JSON value")
	}
	return nil
}

func validateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o002 != 0 {
		return errors.New("path must be a non-symlink Unix socket that is not world-writable")
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o002 != 0 {
		return errors.New("socket parent must be a non-symlink directory that is not world-writable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		return errors.New("socket must be owned by the runtime user or root")
	}
	return nil
}

type UnixArtifactClient struct{ boundary *UnixBoundary }

func NewUnixArtifactClient(boundary *UnixBoundary) (*UnixArtifactClient, error) {
	if boundary == nil || boundary.name != "artifact" {
		return nil, ErrInvalidConfig
	}
	return &UnixArtifactClient{boundary}, nil
}

func (c *UnixArtifactClient) Release(ctx context.Context, handleID, keeperID string) (ascpbearer.Handle, []byte, error) {
	request := struct {
		Protocol string `json:"protocol"`
		HandleID string `json:"handleId"`
		KeeperID string `json:"keeperId"`
	}{boundaryProtocolVersion, handleID, keeperID}
	var response struct {
		Handle   ascpbearer.Handle `json:"handle"`
		Artifact []byte            `json:"artifact"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/release", request, &response)
	if err != nil {
		clear(response.Artifact)
		return ascpbearer.Handle{}, nil, err
	}
	return response.Handle, response.Artifact, err
}

type UnixAssembler struct{ boundary *UnixBoundary }

func NewUnixAssembler(boundary *UnixBoundary) (*UnixAssembler, error) {
	if boundary == nil || boundary.name != "assembler" {
		return nil, ErrInvalidConfig
	}
	return &UnixAssembler{boundary}, nil
}

func (c *UnixAssembler) Assemble(ctx context.Context, job Job, artifact []byte, nonce uint64, fee Fee) (UnsignedTransaction, error) {
	request := struct {
		Protocol string `json:"protocol"`
		Job      Job    `json:"job"`
		Artifact []byte `json:"artifact,omitempty"`
		Nonce    uint64 `json:"nonce"`
		Fee      Fee    `json:"fee"`
	}{boundaryProtocolVersion, boundaryJob(job), artifact, nonce, fee}
	var response struct {
		Transaction UnsignedTransaction `json:"transaction"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/assemble", request, &response)
	if err != nil {
		clear(response.Transaction.Data)
		return UnsignedTransaction{}, err
	}
	return response.Transaction, err
}

type UnixBindingVerifier struct{ boundary *UnixBoundary }

func NewUnixBindingVerifier(boundary *UnixBoundary) (*UnixBindingVerifier, error) {
	if boundary == nil || boundary.name != "verifier" {
		return nil, ErrInvalidConfig
	}
	return &UnixBindingVerifier{boundary}, nil
}

func (c *UnixBindingVerifier) Verify(ctx context.Context, job Job, transaction UnsignedTransaction, artifact []byte) error {
	request := struct {
		Protocol    string              `json:"protocol"`
		Job         Job                 `json:"job"`
		Transaction UnsignedTransaction `json:"transaction"`
		Artifact    []byte              `json:"artifact,omitempty"`
	}{boundaryProtocolVersion, boundaryJob(job), transaction, artifact}
	var response struct {
		Verified bool `json:"verified"`
	}
	if err := c.boundary.call(ctx, http.MethodPost, "/v1/verify", request, &response); err != nil {
		return err
	}
	if !response.Verified {
		return ErrInvalidTransaction
	}
	return nil
}

type UnixWallet struct{ boundary *UnixBoundary }

func NewUnixWallet(boundary *UnixBoundary) (*UnixWallet, error) {
	if boundary == nil || boundary.name != "wallet" {
		return nil, ErrInvalidConfig
	}
	return &UnixWallet{boundary}, nil
}

func (c *UnixWallet) Sign(ctx context.Context, transaction UnsignedTransaction) (SignedTransaction, error) {
	request := struct {
		Protocol    string              `json:"protocol"`
		Transaction UnsignedTransaction `json:"transaction"`
	}{boundaryProtocolVersion, transaction}
	var response struct {
		Hash string `json:"hash"`
		Raw  []byte `json:"raw"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/sign", request, &response)
	if err != nil {
		clear(response.Raw)
		return SignedTransaction{}, err
	}
	return SignedTransaction{Hash: response.Hash, Raw: response.Raw}, err
}

type UnixSealer struct{ boundary *UnixBoundary }

func NewUnixSealer(boundary *UnixBoundary) (*UnixSealer, error) {
	if boundary == nil || boundary.name != "sealer" {
		return nil, ErrInvalidConfig
	}
	return &UnixSealer{boundary}, nil
}

func (c *UnixSealer) Seal(ctx context.Context, raw, aad []byte) ([]byte, string, error) {
	request := struct {
		Protocol string `json:"protocol"`
		Raw      []byte `json:"raw"`
		AAD      []byte `json:"aad"`
	}{boundaryProtocolVersion, raw, aad}
	var response struct {
		Ciphertext []byte `json:"ciphertext"`
		KeyID      string `json:"keyId"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/seal", request, &response)
	if err != nil {
		clear(response.Ciphertext)
		return nil, "", err
	}
	return response.Ciphertext, response.KeyID, err
}

func (c *UnixSealer) Open(ctx context.Context, ciphertext []byte, keyID string, aad []byte) ([]byte, error) {
	request := struct {
		Protocol   string `json:"protocol"`
		Ciphertext []byte `json:"ciphertext"`
		KeyID      string `json:"keyId"`
		AAD        []byte `json:"aad"`
	}{boundaryProtocolVersion, ciphertext, keyID, aad}
	var response struct {
		Raw []byte `json:"raw"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/open", request, &response)
	if err != nil {
		clear(response.Raw)
		return nil, err
	}
	return response.Raw, err
}

type UnixBroadcaster struct{ boundary *UnixBoundary }

func NewUnixBroadcaster(boundary *UnixBoundary) (*UnixBroadcaster, error) {
	if boundary == nil || boundary.name != "broadcast" {
		return nil, ErrInvalidConfig
	}
	return &UnixBroadcaster{boundary}, nil
}

func (c *UnixBroadcaster) Broadcast(ctx context.Context, raw []byte) (string, error) {
	request := struct {
		Protocol string `json:"protocol"`
		Raw      []byte `json:"raw"`
	}{boundaryProtocolVersion, raw}
	var response struct {
		Hash string `json:"hash"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/broadcast", request, &response)
	var boundaryErr *BoundaryError
	if errors.As(err, &boundaryErr) {
		switch boundaryErr.Code {
		case "REJECTED":
			err = errors.Join(ErrBroadcastRejected, err)
		case "UNDERPRICED":
			err = errors.Join(ErrBroadcastUnderpriced, err)
		default:
			err = errors.Join(ErrBroadcastAmbiguous, err)
		}
	}
	return response.Hash, err
}

type UnixChainBoundary struct{ boundary *UnixBoundary }

func NewUnixChainBoundary(boundary *UnixBoundary) (*UnixChainBoundary, error) {
	if boundary == nil || boundary.name != "chain" {
		return nil, ErrInvalidConfig
	}
	return &UnixChainBoundary{boundary}, nil
}

func (c *UnixChainBoundary) Initial(ctx context.Context, job Job) (Fee, error) {
	request := struct {
		Protocol string `json:"protocol"`
		Job      Job    `json:"job"`
	}{boundaryProtocolVersion, boundaryJob(job)}
	var response struct {
		Fee Fee `json:"fee"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/fees/initial", request, &response)
	return response.Fee, err
}

func (c *UnixChainBoundary) Bump(ctx context.Context, job Job, attempt Attempt) (Fee, error) {
	request := struct {
		Protocol string  `json:"protocol"`
		Job      Job     `json:"job"`
		Attempt  Attempt `json:"attempt"`
	}{boundaryProtocolVersion, boundaryJob(job), boundaryAttempt(attempt)}
	var response struct {
		Fee Fee `json:"fee"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/fees/bump", request, &response)
	return response.Fee, err
}

func (c *UnixChainBoundary) PendingNonce(ctx context.Context, chainID uint64, address string) (uint64, error) {
	request := struct {
		Protocol string `json:"protocol"`
		ChainID  uint64 `json:"chainId"`
		Address  string `json:"address"`
	}{boundaryProtocolVersion, chainID, address}
	var response struct {
		Nonce *uint64 `json:"nonce"`
	}
	if err := c.boundary.call(ctx, http.MethodPost, "/v1/nonce", request, &response); err != nil {
		return 0, err
	}
	if response.Nonce == nil {
		return 0, errors.New("keeper chain boundary omitted pending nonce")
	}
	return *response.Nonce, nil
}

func (c *UnixChainBoundary) SafeToReplace(ctx context.Context, job Job, attempt Attempt) error {
	request := struct {
		Protocol string  `json:"protocol"`
		Job      Job     `json:"job"`
		Attempt  Attempt `json:"attempt"`
	}{boundaryProtocolVersion, boundaryJob(job), boundaryAttempt(attempt)}
	var response struct {
		Safe bool `json:"safe"`
	}
	if err := c.boundary.call(ctx, http.MethodPost, "/v1/replacement", request, &response); err != nil {
		return err
	}
	if !response.Safe {
		return ErrUnsafeReplacement
	}
	return nil
}

func (c *UnixChainBoundary) Observe(ctx context.Context, job Job, attempt Attempt) (Outcome, error) {
	request := struct {
		Protocol string  `json:"protocol"`
		Job      Job     `json:"job"`
		Attempt  Attempt `json:"attempt"`
	}{boundaryProtocolVersion, boundaryJob(job), boundaryAttempt(attempt)}
	var response struct {
		Outcome Outcome `json:"outcome"`
	}
	err := c.boundary.call(ctx, http.MethodPost, "/v1/outcome", request, &response)
	return response.Outcome, err
}

func (c *UnixChainBoundary) Eligible(ctx context.Context, limit int) ([]ExpiredCall, error) {
	request := struct {
		Protocol string `json:"protocol"`
		Limit    int    `json:"limit"`
	}{boundaryProtocolVersion, limit}
	var response struct {
		Calls *[]ExpiredCall `json:"calls"`
	}
	if err := c.boundary.call(ctx, http.MethodPost, "/v1/expiries", request, &response); err != nil {
		return nil, err
	}
	if response.Calls == nil {
		return nil, errors.New("keeper chain boundary omitted expiry evidence list")
	}
	return append([]ExpiredCall(nil), (*response.Calls)...), nil
}

func boundaryJob(job Job) Job {
	redacted := job
	redacted.CanonicalPayload = append([]byte(nil), job.CanonicalPayload...)
	redacted.SignerHandle = ""
	redacted.LeaseOwner = ""
	redacted.LeaseToken = ""
	redacted.LeaseExpiresAt = time.Time{}
	redacted.LastError = ""
	return redacted
}

func boundaryAttempt(attempt Attempt) Attempt {
	redacted := attempt
	redacted.SealingKeyID = ""
	redacted.LastError = ""
	return redacted
}

var _ ActivatedSignerClient = (*UnixArtifactClient)(nil)
var _ Assembler = (*UnixAssembler)(nil)
var _ BindingVerifier = (*UnixBindingVerifier)(nil)
var _ Wallet = (*UnixWallet)(nil)
var _ Sealer = (*UnixSealer)(nil)
var _ Broadcaster = (*UnixBroadcaster)(nil)
var _ FeePolicy = (*UnixChainBoundary)(nil)
var _ NonceSource = (*UnixChainBoundary)(nil)
var _ ReplacementGate = (*UnixChainBoundary)(nil)
var _ OutcomeSource = (*UnixChainBoundary)(nil)
var _ ExpirySource = (*UnixChainBoundary)(nil)
