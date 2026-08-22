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
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/securefile"
)

const ComponentProtocol = "ASCP_RING6_COMPONENT_V1"

type ComponentBoundary struct {
	name, path string
	client     *http.Client
}

func NewComponentBoundary(name, path string, timeout time.Duration) (*ComponentBoundary, error) {
	if name != "verifier" && name != "hsm" || !cleanAbsoluteSocket(path) || timeout < time.Second || timeout > 10*time.Second {
		return nil, errors.New("Ring 6 component configuration is invalid")
	}
	dialer := &net.Dialer{Timeout: min(timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
	}
	return &ComponentBoundary{name: name, path: path, client: &http.Client{
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
	seenInodes := map[[2]uint64]string{}
	for _, boundary := range boundaries {
		if boundary == nil {
			return errors.New("Ring 6 component is required")
		}
		if _, exists := seenPaths[boundary.path]; exists {
			return errors.New("Ring 6 components must not share a socket path")
		}
		seenPaths[boundary.path] = struct{}{}
		identity, err := inspectSocket(boundary.path)
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
		if errors.As(err, &component) && component.status == http.StatusUnprocessableEntity && identifierPattern.MatchString(component.code) {
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
	status int
	code   string
}

func (e *componentError) Error() string {
	return fmt.Sprintf("Ring 6 component HTTP %d: %s", e.status, e.code)
}

func (b *ComponentBoundary) call(ctx context.Context, method, endpoint string, input, output any) error {
	if _, err := inspectSocket(b.path); err != nil {
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
	defer response.Body.Close()
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
		if decodeStrict(raw, &failure) != nil || failure.Code == "" {
			failure.Code = "UNCLASSIFIED"
		}
		return &componentError{status: response.StatusCode, code: failure.Code}
	}
	if output == nil {
		return errors.New("Ring 6 component output contract is missing")
	}
	return decodeStrict(raw, output)
}

func inspectSocket(path string) ([2]uint64, error) {
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return [2]uint64{}, err
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil || info.Mode().Perm() != 0o700 || !securefile.OwnerAllowed(info) {
		return [2]uint64{}, errors.New("Ring 6 socket parent must be private and owner controlled")
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !securefile.OwnerAllowed(info) {
		return [2]uint64{}, errors.New("Ring 6 component must be a private owner-controlled Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return [2]uint64{}, errors.New("Ring 6 component identity unavailable")
	}
	return [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}, nil
}
