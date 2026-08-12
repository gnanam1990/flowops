package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

const maxSignerKeysJSONBytes = 16 * 1024

type signerKeyInput struct {
	OrganizationID string `json:"organizationId"`
	CustomerID     string `json:"customerId"`
	KeyID          string `json:"keyId"`
	PublicKeyB64   string `json:"publicKeyB64"`
}

// parseSignerKeys intentionally accepts an empty value so production can ship
// the endpoint fail-closed before a design partner provisions its public key.
func parseSignerKeys(raw string) ([]controlapi.BroadcastKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxSignerKeysJSONBytes {
		return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON exceeds 16 KiB")
	}
	if err := rejectDuplicateJSONFields([]byte(raw)); err != nil {
		return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON must not contain duplicate object fields")
	}
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON must be a strict key array")
	}
	for _, item := range fields {
		if len(item) != 4 {
			return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON key fields must be exactly organizationId, customerId, keyId, and publicKeyB64")
		}
		for field := range item {
			switch field {
			case "organizationId", "customerId", "keyId", "publicKeyB64":
			default:
				return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON key fields must be exactly organizationId, customerId, keyId, and publicKeyB64")
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var inputs []signerKeyInput
	if err := decoder.Decode(&inputs); err != nil {
		return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON must be a strict key array")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON must contain one JSON value")
	}
	keys := make([]controlapi.BroadcastKey, len(inputs))
	for index, input := range inputs {
		input.OrganizationID = strings.TrimSpace(input.OrganizationID)
		input.CustomerID = strings.TrimSpace(input.CustomerID)
		input.KeyID = strings.TrimSpace(input.KeyID)
		if !envelope.ValidIdentifier(input.OrganizationID) || !envelope.ValidIdentifier(input.CustomerID) || !envelope.ValidIdentifier(input.KeyID) {
			return nil, fmt.Errorf("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON key %d has an invalid identity", index)
		}
		publicKey, err := base64.StdEncoding.DecodeString(input.PublicKeyB64)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON key %d must encode one Ed25519 public key", index)
		}
		keys[index] = controlapi.BroadcastKey{OrganizationID: input.OrganizationID, CustomerID: input.CustomerID, KeyID: input.KeyID, PublicKey: ed25519.PublicKey(append([]byte(nil), publicKey...))}
	}
	if _, err := controlapi.NewStaticBroadcastKeys(keys); err != nil {
		return nil, fmt.Errorf("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON: %w", err)
	}
	return keys, nil
}
