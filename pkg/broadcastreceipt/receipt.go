// Package broadcastreceipt defines the customer signer's attestation that one
// already-authorized transaction was submitted to Base. It contains no raw
// transaction and no wallet key material.
package broadcastreceipt

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	Version      = "flowops.broadcast-receipt.v1"
	domainPrefix = "flowops:broadcast-receipt:v1\n"
)

type Outcome string

const (
	OutcomeSubmitted Outcome = "SUBMITTED"
	OutcomeAmbiguous Outcome = "AMBIGUOUS"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
	hashPattern       = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	addressPattern    = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
)

// Receipt is signed by a customer-controlled attestation key after the signer
// has durably entered its one-way broadcast state. BroadcastAt is evidence
// time supplied by that signer; FlowOps still timestamps its own journal append.
type Receipt struct {
	Version             string  `json:"version"`
	OrganizationID      string  `json:"organizationId"`
	CustomerID          string  `json:"customerId"`
	AuthorizationID     string  `json:"authorizationId"`
	AuthorizationDigest string  `json:"authorizationDigest"`
	TransactionHash     string  `json:"transactionHash"`
	Sender              string  `json:"sender"`
	Outcome             Outcome `json:"outcome"`
	BroadcastAt         int64   `json:"broadcastAt"`
}

type SignedReceipt struct {
	Receipt   Receipt `json:"receipt"`
	KeyID     string  `json:"keyId"`
	Signature string  `json:"signature"`
}

func (r Receipt) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("version: got %q, want %q", r.Version, Version)
	}
	for name, value := range map[string]string{
		"organizationId": r.OrganizationID, "customerId": r.CustomerID, "authorizationId": r.AuthorizationID,
	} {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%s: invalid identifier", name)
		}
	}
	if !hashPattern.MatchString(r.AuthorizationDigest) || !hashPattern.MatchString(r.TransactionHash) {
		return errors.New("authorizationDigest and transactionHash must be canonical lowercase 32-byte hashes")
	}
	if !addressPattern.MatchString(r.Sender) {
		return errors.New("sender must be a canonical lowercase EVM address")
	}
	if r.Outcome != OutcomeSubmitted && r.Outcome != OutcomeAmbiguous {
		return errors.New("outcome must be SUBMITTED or AMBIGUOUS")
	}
	if r.BroadcastAt <= 0 {
		return errors.New("broadcastAt must be a positive Unix timestamp")
	}
	return nil
}

func (r Receipt) Digest() ([32]byte, error) {
	if err := r.Validate(); err != nil {
		return [32]byte{}, err
	}
	canonical, err := json.Marshal(r)
	if err != nil {
		return [32]byte{}, err
	}
	message := append([]byte(domainPrefix), canonical...)
	return sha256.Sum256(message), nil
}

func Sign(r Receipt, keyID string, privateKey ed25519.PrivateKey) (SignedReceipt, error) {
	if !identifierPattern.MatchString(keyID) {
		return SignedReceipt{}, errors.New("keyId: invalid identifier")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return SignedReceipt{}, errors.New("private key: invalid Ed25519 length")
	}
	digest, err := signingDigest(r, keyID)
	if err != nil {
		return SignedReceipt{}, err
	}
	return SignedReceipt{Receipt: r, KeyID: keyID, Signature: "0x" + hex.EncodeToString(ed25519.Sign(privateKey, digest[:]))}, nil
}

func Verify(s SignedReceipt, publicKey ed25519.PublicKey) error {
	if !identifierPattern.MatchString(s.KeyID) {
		return errors.New("keyId: invalid identifier")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("public key: invalid Ed25519 length")
	}
	if !strings.HasPrefix(s.Signature, "0x") {
		return errors.New("signature: must use 0x-prefixed lowercase hex")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(s.Signature, "0x"))
	if err != nil || len(raw) != ed25519.SignatureSize || s.Signature != "0x"+hex.EncodeToString(raw) {
		return errors.New("signature: invalid canonical Ed25519 encoding")
	}
	digest, err := signingDigest(s.Receipt, s.KeyID)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, digest[:], raw) {
		return errors.New("signature: verification failed")
	}
	return nil
}

func signingDigest(r Receipt, keyID string) ([32]byte, error) {
	if !identifierPattern.MatchString(keyID) {
		return [32]byte{}, errors.New("keyId: invalid identifier")
	}
	receiptDigest, err := r.Digest()
	if err != nil {
		return [32]byte{}, err
	}
	message := make([]byte, 0, len(domainPrefix)+len(keyID)+1+len(receiptDigest))
	message = append(message, domainPrefix...)
	message = append(message, keyID...)
	message = append(message, '\n')
	message = append(message, receiptDigest[:]...)
	return sha256.Sum256(message), nil
}
