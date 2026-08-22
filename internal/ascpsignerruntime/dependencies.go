package ascpsignerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

const DependencyProtocol = "ASCP_SIGNER_DEPENDENCY_V1"

type DependencyError struct {
	Boundary   string
	Code       string
	StatusCode int
}

func (e *DependencyError) Error() string {
	return fmt.Sprintf("signer %s dependency refused with HTTP %d code %s", e.Boundary, e.StatusCode, e.Code)
}

type DependencyBoundary struct {
	name, socketPath string
	client           *http.Client
}

func NewDependencyBoundary(name, socketPath string, timeout time.Duration) (*DependencyBoundary, error) {
	if name != "ring6" && name != "activation" || !cleanAbsoluteSocket(socketPath) || timeout < time.Second || timeout > 10*time.Second {
		return nil, errors.New("signer dependency boundary configuration is invalid")
	}
	dialer := &net.Dialer{Timeout: min(timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &DependencyBoundary{name: name, socketPath: socketPath, client: &http.Client{
		Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (b *DependencyBoundary) Check(ctx context.Context) error {
	var response struct {
		Protocol string `json:"protocol"`
		Boundary string `json:"boundary"`
		Status   string `json:"status"`
	}
	if err := b.call(ctx, http.MethodGet, "/healthz", nil, &response); err != nil {
		return err
	}
	if response.Protocol != DependencyProtocol || response.Boundary != b.name || response.Status != "ok" {
		return errors.New("signer dependency health identity mismatch")
	}
	return nil
}

func (b *DependencyBoundary) call(ctx context.Context, method, endpoint string, input, output any) error {
	if err := validateDependencySocket(b.socketPath); err != nil {
		return fmt.Errorf("validate signer %s dependency: %w", b.name, err)
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) == 0 || len(encoded) > maxBodyBytes {
			clear(encoded)
			return errors.New("signer dependency request is invalid or too large")
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
		return fmt.Errorf("call signer %s dependency: %w", b.name, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	defer clear(raw)
	if err != nil || len(raw) == 0 || len(raw) > maxBodyBytes {
		return errors.New("signer dependency response is invalid or too large")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || strings.ToLower(mediaType) != "application/json" {
		return errors.New("signer dependency response is not JSON")
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if decodeStrictResponse(raw, &failure) != nil || failure.Code == "" {
			failure.Code = "UNCLASSIFIED"
		}
		return &DependencyError{Boundary: b.name, Code: failure.Code, StatusCode: response.StatusCode}
	}
	if output == nil {
		return errors.New("signer dependency output contract is missing")
	}
	return decodeStrictResponse(raw, output)
}

func decodeStrictResponse(raw []byte, output any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("dependency response must contain exactly one JSON value")
	}
	return nil
}

func validateDependencySocket(path string) error {
	_, err := inspectDependencySocket(path)
	return err
}

func inspectDependencySocket(path string) ([2]uint64, error) {
	if err := validateSocketParent(filepath.Dir(path)); err != nil {
		return [2]uint64{}, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o002 != 0 {
		return [2]uint64{}, errors.New("dependency path must be a non-symlink Unix socket that is not world-writable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		return [2]uint64{}, errors.New("dependency socket must be owned by the runtime user or root")
	}
	return [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}, nil
}

func ValidateDependencySockets(boundaries ...*DependencyBoundary) error {
	seenPaths := map[string]struct{}{}
	seenInodes := map[[2]uint64]string{}
	for _, boundary := range boundaries {
		if boundary == nil {
			return errors.New("signer dependency boundary is required")
		}
		if _, exists := seenPaths[boundary.socketPath]; exists {
			return errors.New("signer dependencies must not share a socket path")
		}
		seenPaths[boundary.socketPath] = struct{}{}
		identity, err := inspectDependencySocket(boundary.socketPath)
		if err != nil {
			return err
		}
		if previous, exists := seenInodes[identity]; exists {
			return fmt.Errorf("signer dependencies %s and %s resolve to the same socket", previous, boundary.name)
		}
		seenInodes[identity] = boundary.name
	}
	return nil
}

type UnixRing6Engine struct{ boundary *DependencyBoundary }

func NewUnixRing6Engine(boundary *DependencyBoundary) (*UnixRing6Engine, error) {
	if boundary == nil || boundary.name != "ring6" {
		return nil, errors.New("Ring 6 dependency is required")
	}
	return &UnixRing6Engine{boundary}, nil
}

func (e *UnixRing6Engine) VerifyAndSign(ctx context.Context, input ascpbearer.ActivationInput) ([]byte, error) {
	request := struct {
		Protocol string                     `json:"protocol"`
		Input    ascpbearer.ActivationInput `json:"input"`
	}{DependencyProtocol, input}
	var response struct {
		Signature []byte `json:"signature"`
	}
	if err := e.boundary.call(ctx, http.MethodPost, "/v1/verify-and-sign", request, &response); err != nil {
		clear(response.Signature)
		var dependencyError *DependencyError
		if errors.As(err, &dependencyError) && dependencyError.Code == "SIGNER_REFUSED" && dependencyError.StatusCode == http.StatusUnprocessableEntity {
			return nil, errors.Join(ascpbearer.ErrSignerRefused, err)
		}
		return nil, err
	}
	if len(response.Signature) != 65 {
		clear(response.Signature)
		return nil, errors.New("Ring 6 signature is invalid")
	}
	return response.Signature, nil
}

type PinnedEngine struct {
	engine          ascpbearer.IndependentSigningEngine
	keyID, keeperID string
	epoch           uint64
	expectedSigner  common.Address
}

func NewPinnedEngine(engine ascpbearer.IndependentSigningEngine, keyID string, epoch uint64, keeperID, signerAddress string) (*PinnedEngine, error) {
	if engine == nil || strings.TrimSpace(keyID) == "" || strings.TrimSpace(keeperID) == "" || epoch == 0 ||
		!common.IsHexAddress(signerAddress) || signerAddress != strings.ToLower(signerAddress) ||
		common.HexToAddress(signerAddress) == (common.Address{}) {
		return nil, errors.New("pinned signer route is invalid")
	}
	return &PinnedEngine{engine: engine, keyID: keyID, epoch: epoch, keeperID: keeperID, expectedSigner: common.HexToAddress(signerAddress)}, nil
}

func (e *PinnedEngine) VerifyAndSign(ctx context.Context, input ascpbearer.ActivationInput) ([]byte, error) {
	if input.SignerKeyID != e.keyID || input.KeyEpoch != e.epoch || input.KeeperID != e.keeperID {
		return nil, ascpbearer.ErrActivationBinding
	}
	signature, err := e.engine.VerifyAndSign(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(signature) != 65 || signature[64] > 1 ||
		!crypto.ValidateSignatureValues(signature[64], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:64]), true) {
		clear(signature)
		return nil, ascpbearer.ErrActivationBinding
	}
	publicKey, err := crypto.SigToPub(common.HexToHash(input.Digest).Bytes(), signature)
	if err != nil || crypto.PubkeyToAddress(*publicKey) != e.expectedSigner {
		clear(signature)
		return nil, ascpbearer.ErrActivationBinding
	}
	return signature, nil
}

type UnixActivationVerifier struct{ boundary *DependencyBoundary }

func NewUnixActivationVerifier(boundary *DependencyBoundary) (*UnixActivationVerifier, error) {
	if boundary == nil || boundary.name != "activation" {
		return nil, errors.New("activation dependency is required")
	}
	return &UnixActivationVerifier{boundary}, nil
}

func (v *UnixActivationVerifier) VerifyActivation(ctx context.Context, handle ascpbearer.Handle, proof ascpbearer.ActivationProof) error {
	request := struct {
		Protocol string                     `json:"protocol"`
		Handle   ascpbearer.Handle          `json:"handle"`
		Proof    ascpbearer.ActivationProof `json:"proof"`
	}{DependencyProtocol, handle, proof}
	var response struct {
		Verified bool `json:"verified"`
	}
	if err := v.boundary.call(ctx, http.MethodPost, "/v1/verify-activation", request, &response); err != nil {
		return err
	}
	if !response.Verified {
		return errors.New("activation authority refused proof")
	}
	return nil
}

func (v *UnixActivationVerifier) ProveUnactivated(ctx context.Context, handle ascpbearer.Handle, provenAt time.Time) error {
	request := struct {
		Protocol string            `json:"protocol"`
		Handle   ascpbearer.Handle `json:"handle"`
		ProvenAt time.Time         `json:"provenAt"`
	}{DependencyProtocol, handle, provenAt.UTC()}
	var response struct {
		Unactivated bool `json:"unactivated"`
	}
	if err := v.boundary.call(ctx, http.MethodPost, "/v1/prove-unactivated", request, &response); err != nil {
		return err
	}
	if !response.Unactivated {
		return errors.New("activation authority cannot prove non-activation")
	}
	return nil
}
