package ascpring6

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
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/securefile"
)

const ComponentProtocol = "ASCP_RING6_COMPONENT_V1"

type ComponentBoundary struct {
	name, path string
	identity   socketIdentity
	client     *http.Client
}

func NewComponentBoundary(name, path string, timeout time.Duration) (*ComponentBoundary, error) {
	if name != "verifier" && name != "hsm" || !cleanAbsoluteSocket(path) || timeout < time.Second || timeout > 10*time.Second {
		return nil, errors.New("Ring 6 component configuration is invalid")
	}
	identity, err := inspectSocket(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Ring 6 %s component: %w", name, err)
	}
	dialer := &net.Dialer{Timeout: min(timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
	}
	return &ComponentBoundary{name: name, path: path, identity: identity, client: &http.Client{
		Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (b *ComponentBoundary) Check(ctx context.Context) error {
	var response struct {
		Protocol string `json:"protocol"`
		Boundary string `json:"boundary"`
		Status   string `json:"status"`
	}
	if err := b.call(ctx, http.MethodGet, "/healthz", nil, &response); err != nil {
		return err
	}
	if response.Protocol != ComponentProtocol || response.Boundary != b.name || response.Status != "ok" {
		return errors.New("Ring 6 component health identity mismatch")
	}
	return nil
}

func ValidateComponentSockets(boundaries ...*ComponentBoundary) error {
	seenPaths := map[string]struct{}{}
	seenInodes := map[socketIdentity]string{}
	for _, boundary := range boundaries {
		if boundary == nil {
			return errors.New("Ring 6 component is required")
		}
		if _, exists := seenPaths[boundary.path]; exists {
			return errors.New("Ring 6 components must not share a socket path")
		}
		seenPaths[boundary.path] = struct{}{}
		identity, err := boundary.inspectPinned()
		if err != nil {
			return err
		}
		if previous, exists := seenInodes[identity]; exists {
			return fmt.Errorf("Ring 6 components %s and %s share one socket", previous, boundary.name)
		}
		seenInodes[identity] = boundary.name
	}
	return nil
}

type UnixVerifier struct{ boundary *ComponentBoundary }

func NewUnixVerifier(boundary *ComponentBoundary) (*UnixVerifier, error) {
	if boundary == nil || boundary.name != "verifier" {
		return nil, errors.New("Ring 6 verifier component is required")
	}
	return &UnixVerifier{boundary: boundary}, nil
}

func (v *UnixVerifier) Verify(ctx context.Context, input ascpbearer.ActivationInput, inputHash string) error {
	request := struct {
		Protocol  string                     `json:"protocol"`
		Input     ascpbearer.ActivationInput `json:"input"`
		InputHash string                     `json:"inputHash"`
	}{ComponentProtocol, input, inputHash}
	var response struct {
		Verified  bool   `json:"verified"`
		InputHash string `json:"inputHash"`
	}
	err := v.boundary.call(ctx, http.MethodPost, "/v1/verify", request, &response)
	if err != nil {
		var component *componentError
		if errors.As(err, &component) && component.status == http.StatusUnprocessableEntity && component.canonical {
			return &PermanentRefusal{Code: component.code}
		}
		return err
	}
	if !response.Verified || response.InputHash != inputHash {
		return ErrBinding
	}
	return nil
}

type UnixHSM struct{ boundary *ComponentBoundary }

func NewUnixHSM(boundary *ComponentBoundary) (*UnixHSM, error) {
	if boundary == nil || boundary.name != "hsm" {
		return nil, errors.New("Ring 6 HSM component is required")
	}
	return &UnixHSM{boundary: boundary}, nil
}

func (h *UnixHSM) Sign(ctx context.Context, request HSMRequest) (HSMResult, error) {
	var response HSMResult
	err := h.boundary.call(ctx, http.MethodPost, "/v1/sign", struct {
		Protocol string     `json:"protocol"`
		Request  HSMRequest `json:"request"`
	}{ComponentProtocol, request}, &response)
	if err != nil {
		clear(response.Signature)
		return HSMResult{}, err
	}
	return response, nil
}

type componentError struct {
	status    int
	code      string
	canonical bool
}

func (e *componentError) Error() string {
	return fmt.Sprintf("Ring 6 component HTTP %d: %s", e.status, e.code)
}

func (b *ComponentBoundary) call(ctx context.Context, method, endpoint string, input, output any) error {
	if _, err := b.inspectPinned(); err != nil {
		return fmt.Errorf("validate Ring 6 %s component: %w", b.name, err)
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) == 0 || len(encoded) > maxBodyBytes {
			clear(encoded)
			return errors.New("Ring 6 component request invalid")
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
		return fmt.Errorf("call Ring 6 %s component: %w", b.name, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	defer clear(raw)
	if err != nil || len(raw) == 0 || len(raw) > maxBodyBytes {
		return errors.New("Ring 6 component response invalid")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || strings.ToLower(mediaType) != "application/json" {
		return errors.New("Ring 6 component response is not JSON")
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		canonical := decodeStrict(raw, &failure) == nil && identifierPattern.MatchString(failure.Code)
		if !canonical {
			failure.Code = "UNCLASSIFIED"
		}
		return &componentError{status: response.StatusCode, code: failure.Code, canonical: canonical}
	}
	if output == nil {
		return errors.New("Ring 6 component output contract is missing")
	}
	return decodeStrict(raw, output)
}

func (b *ComponentBoundary) inspectPinned() (socketIdentity, error) {
	identity, err := inspectSocket(b.path)
	if err != nil {
		return socketIdentity{}, err
	}
	if identity != b.identity {
		return socketIdentity{}, errors.New("Ring 6 component socket identity changed")
	}
	return identity, nil
}

func inspectSocket(path string) (socketIdentity, error) {
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return socketIdentity{}, err
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Stat()
	if err != nil || info.Mode().Perm() != 0o700 || !securefile.OwnerAllowed(info) {
		return socketIdentity{}, errors.New("Ring 6 socket parent must be private and owner controlled")
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !securefile.OwnerAllowed(info) {
		return socketIdentity{}, errors.New("Ring 6 component must be a private owner-controlled Unix socket")
	}
	identity, ok := identityFromFileInfo(info)
	if !ok {
		return socketIdentity{}, errors.New("Ring 6 component identity unavailable")
	}
	return identity, nil
}
