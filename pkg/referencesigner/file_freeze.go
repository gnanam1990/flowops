package referencesigner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	freezeFileVersion = "flowops.freeze.v1"
	maxFreezeFileSize = 64 * 1024
)

// FileFreezeGate re-reads customer-owned freeze state on every check. Removing,
// replacing, loosening permissions on, or corrupting the file fails closed.
type FileFreezeGate struct{ path string }

type freezeState struct {
	Version            string   `json:"version"`
	OrganizationFrozen bool     `json:"organizationFrozen"`
	FrozenAgents       []string `json:"frozenAgents"`
	FrozenTasks        []string `json:"frozenTasks"`
}

func NewFileFreezeGate(path string) (*FileFreezeGate, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("freeze file path is required")
	}
	gate := &FileFreezeGate{path: path}
	if _, err := gate.read(); err != nil {
		return nil, err
	}
	return gate, nil
}

func (g *FileFreezeGate) CheckFrozen(ctx context.Context, authorization envelope.Authorization) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := g.read()
	if err != nil {
		return err
	}
	if state.OrganizationFrozen {
		return errors.New("customer organization is frozen")
	}
	for _, agentID := range state.FrozenAgents {
		if agentID == authorization.AgentID {
			return errors.New("customer agent is frozen")
		}
	}
	for _, taskID := range state.FrozenTasks {
		if taskID == authorization.TaskID {
			return errors.New("customer task is frozen")
		}
	}
	return nil
}

func (g *FileFreezeGate) read() (freezeState, error) {
	file, err := os.OpenFile(g.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return freezeState{}, errors.New("customer freeze file is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return freezeState{}, errors.New("customer freeze file must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFreezeFileSize+1))
	if err != nil || len(raw) == 0 || len(raw) > maxFreezeFileSize {
		return freezeState{}, errors.New("customer freeze file is invalid")
	}
	if err := rejectDuplicateObjectFields(raw); err != nil {
		return freezeState{}, errors.New("customer freeze file has duplicate fields")
	}
	var state freezeState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return freezeState{}, errors.New("customer freeze file is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return freezeState{}, errors.New("customer freeze file must contain one object")
	}
	if state.Version != freezeFileVersion {
		return freezeState{}, errors.New("customer freeze file version is unsupported")
	}
	if err := validateUniqueIdentifiers("frozen agent", state.FrozenAgents); err != nil {
		return freezeState{}, err
	}
	if err := validateUniqueIdentifiers("frozen task", state.FrozenTasks); err != nil {
		return freezeState{}, err
	}
	return state, nil
}

func validateUniqueIdentifiers(kind string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !envelope.ValidIdentifier(value) {
			return fmt.Errorf("%s identifier is invalid", kind)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s identifier is duplicated", kind)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func rejectDuplicateObjectFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("not an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if _, exists := seen[name]; exists {
			return errors.New("duplicate object field")
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return errors.New("unterminated object")
	}
	return nil
}
