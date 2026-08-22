package ascpgovernancerelay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpkeeper"
	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

const (
	boundaryProtocol = "ASCP_GOVERNANCE_RELAY_BOUNDARY_V1"
	maxBoundaryBytes = 2 << 20
)

type UnixBoundary struct {
	name, path    string
	client        *http.Client
	capability    [32]byte
	hasCapability bool
}

func NewAuthenticatedUnixBoundary(name, path string, timeout time.Duration, capability []byte) (*UnixBoundary, error) {
	boundary, err := NewUnixBoundary(name, path, timeout)
	nonzero := byte(0)
	for _, value := range capability {
		nonzero |= value
	}
	if err != nil || name != "vault" || len(capability) != 32 || nonzero == 0 {
		return nil, ErrInvalidCommand
	}
	copy(boundary.capability[:], capability)
	boundary.hasCapability = true
	return boundary, nil
}

func NewUnixBoundary(name, path string, timeout time.Duration) (*UnixBoundary, error) {
	if !identifierPattern.MatchString(name) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" ||
		timeout < time.Second || timeout > 10*time.Second {
		return nil, ErrInvalidCommand
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 4,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialer.DialContext(ctx, "unix", path) }}
	return &UnixBoundary{name: name, path: path, client: &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (b *UnixBoundary) Check(ctx context.Context) error {
	if err := ascpkeeper.ValidateDistinctSockets(map[string]string{b.name: b.path}); err != nil {
		return err
	}
	var response struct {
		Protocol string `json:"protocol"`
		Boundary string `json:"boundary"`
		Status   string `json:"status"`
	}
	if err := b.call(ctx, http.MethodGet, "/healthz", nil, &response); err != nil {
		return err
	}
	if response.Protocol != boundaryProtocol || response.Boundary != b.name || response.Status != "ok" {
		return ErrInvalidOutcome
	}
	return nil
}

func (b *UnixBoundary) call(ctx context.Context, method, endpoint string, input, output any) error {
	if err := ascpkeeper.ValidateDistinctSockets(map[string]string{b.name: b.path}); err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) == 0 || len(encoded) > maxBoundaryBytes {
			return ErrInvalidCommand
		}
		defer clear(encoded)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if b.hasCapability {
		request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(b.capability[:]))
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("call governance %s boundary: %w", b.name, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBoundaryBytes+1))
	defer clear(raw)
	if err != nil || len(raw) > maxBoundaryBytes {
		return ErrInvalidOutcome
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if strictBoundaryJSON(raw, &failure) != nil || !identifierPattern.MatchString(failure.Code) {
			failure.Code = "UNCLASSIFIED"
		}
		return fmt.Errorf("governance %s boundary rejected request: %s", b.name, failure.Code)
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || strings.ToLower(mediaType) != "application/json" || output == nil {
		return ErrInvalidOutcome
	}
	return strictBoundaryJSON(raw, output)
}

type UnixDirectory struct{ boundary *UnixBoundary }

func NewUnixDirectory(boundary *UnixBoundary) (*UnixDirectory, error) {
	if boundary == nil || boundary.name != "directory" {
		return nil, ErrInvalidCommand
	}
	return &UnixDirectory{boundary}, nil
}
func (d *UnixDirectory) SafeFor(ctx context.Context, organizationID string, chainID uint64) (string, error) {
	var response struct {
		SafeAddress string `json:"safeAddress"`
	}
	err := d.boundary.call(ctx, http.MethodPost, "/v1/safe", struct {
		OrganizationID string `json:"organizationId"`
		ChainID        uint64 `json:"chainId"`
	}{organizationID, chainID}, &response)
	if err != nil || !canonicalAddress(response.SafeAddress) {
		return "", errors.Join(ErrInvalidOutcome, err)
	}
	return response.SafeAddress, nil
}

type UnixSnapshotSource struct{ boundary *UnixBoundary }

func NewUnixSnapshotSource(boundary *UnixBoundary) (*UnixSnapshotSource, error) {
	if boundary == nil || boundary.name != "chain" {
		return nil, ErrInvalidCommand
	}
	return &UnixSnapshotSource{boundary}, nil
}
func (s *UnixSnapshotSource) Observe(ctx context.Context, command ascpworkflow.GovernanceExecutionCommand, safe string) (Snapshot, error) {
	var response Snapshot
	err := s.boundary.call(ctx, http.MethodPost, "/v1/snapshot", struct {
		Binding SnapshotBinding `json:"binding"`
	}{snapshotBinding(command, safe)}, &response)
	return response, err
}

type UnixVault struct{ boundary *UnixBoundary }

func NewUnixVault(boundary *UnixBoundary) (*UnixVault, error) {
	if boundary == nil || boundary.name != "vault" {
		return nil, ErrInvalidCommand
	}
	return &UnixVault{boundary}, nil
}
func (v *UnixVault) Seal(ctx context.Context, value, aad []byte) (string, error) {
	var response struct {
		Handle string `json:"handle"`
	}
	err := v.boundary.call(ctx, http.MethodPost, "/v1/seal", struct {
		Value []byte `json:"value"`
		AAD   []byte `json:"aad"`
	}{value, aad}, &response)
	if err != nil || !identifierPattern.MatchString(response.Handle) {
		return "", errors.Join(ErrInvalidOutcome, err)
	}
	return response.Handle, nil
}
func (v *UnixVault) Open(ctx context.Context, handle string, aad []byte) ([]byte, error) {
	var response struct {
		Value []byte `json:"value"`
	}
	err := v.boundary.call(ctx, http.MethodPost, "/v1/open", struct {
		Handle string `json:"handle"`
		AAD    []byte `json:"aad"`
	}{handle, aad}, &response)
	if err != nil || len(response.Value) == 0 || len(response.Value) > maxBoundaryBytes {
		return nil, errors.Join(ErrInvalidOutcome, err)
	}
	return response.Value, nil
}

type UnixBroadcaster struct{ boundary *UnixBoundary }

func NewUnixBroadcaster(boundary *UnixBoundary) (*UnixBroadcaster, error) {
	if boundary == nil || boundary.name != "broadcast" {
		return nil, ErrInvalidCommand
	}
	return &UnixBroadcaster{boundary}, nil
}
func (b *UnixBroadcaster) Prepare(ctx context.Context, binding RelayBinding, execCalldata []byte) (OuterArtifact, error) {
	var response OuterArtifact
	err := b.boundary.call(ctx, http.MethodPost, "/v1/prepare", struct {
		Binding      RelayBinding `json:"binding"`
		ExecCalldata []byte       `json:"execCalldata"`
	}{binding, execCalldata}, &response)
	return response, err
}
func (b *UnixBroadcaster) Broadcast(ctx context.Context, handle string) (string, error) {
	var response struct {
		TransactionHash string `json:"transactionHash"`
	}
	err := b.boundary.call(ctx, http.MethodPost, "/v1/broadcast", struct {
		Handle string `json:"handle"`
	}{handle}, &response)
	if err != nil || !canonicalHash(response.TransactionHash) {
		return "", errors.Join(ErrInvalidOutcome, err)
	}
	return response.TransactionHash, nil
}

type UnixOutcomeSource struct{ boundary *UnixBoundary }

func NewUnixOutcomeSource(boundary *UnixBoundary) (*UnixOutcomeSource, error) {
	if boundary == nil || boundary.name != "chain" {
		return nil, ErrInvalidCommand
	}
	return &UnixOutcomeSource{boundary}, nil
}
func (s *UnixOutcomeSource) Observe(ctx context.Context, binding OutcomeBinding) (OutcomeEvidence, error) {
	var response OutcomeEvidence
	err := s.boundary.call(ctx, http.MethodPost, "/v1/outcome", struct {
		Binding OutcomeBinding `json:"binding"`
	}{binding}, &response)
	return response, err
}

func strictBoundaryJSON(raw []byte, output any) error {
	if err := rejectDuplicateJSON(raw); err != nil {
		return err
	}
	if err := requireExactTopLevelFields(raw, output); err != nil {
		return err
	}
	return strictJSON(raw, output)
}

// requireExactTopLevelFields closes encoding/json's case-insensitive field
// matching and zero-value acceptance. Every authority-bearing boundary reply
// must contain each declared field exactly once with the exact JSON tag.
func requireExactTopLevelFields(raw []byte, output any) error {
	typeOf := reflect.TypeOf(output)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return ErrInvalidOutcome
	}
	typeOf = typeOf.Elem()
	expected := make(map[string]struct{}, typeOf.NumField())
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" || field.Anonymous {
			return ErrInvalidOutcome
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if name == "-" {
			continue
		}
		if _, duplicate := expected[name]; duplicate {
			return ErrInvalidOutcome
		}
		expected[name] = struct{}{}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil || len(object) != len(expected) {
		return ErrInvalidOutcome
	}
	for name := range expected {
		if _, ok := object[name]; !ok {
			return ErrInvalidOutcome
		}
	}
	return nil
}

func rejectDuplicateJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 64 {
			return ErrInvalidOutcome
		}
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidOutcome
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
					return ErrInvalidOutcome
				}
				if _, duplicate := seen[key]; duplicate {
					return ErrInvalidOutcome
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
			return ErrInvalidOutcome
		}
		_, err = decoder.Token()
		return err
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidOutcome
	}
	return nil
}
