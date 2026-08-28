// Package safeownerproof verifies a no-transaction EIP-191 challenge signed by
// the reviewed Base mainnet Safe owner quorum. It never receives private keys.
package safeownerproof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/rpcadmission"
)

const (
	SchemaVersion  = 1
	SigningContext = "FLOWOPS BASE MAINNET SAFE OWNER CONTROL V1"
	MaxJSONBytes   = 32 * 1024
)

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)
	hashPattern   = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type DigestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type SafeTrust struct {
	Address         string   `json:"address"`
	RuntimeCodeHash string   `json:"runtimeCodeHash"`
	Implementation  string   `json:"implementation"`
	Owners          []string `json:"owners"`
	Threshold       int      `json:"threshold"`
	Nonce           uint64   `json:"nonce"`
}

type Profile struct {
	SchemaVersion          int        `json:"schemaVersion"`
	ProfileID              string     `json:"profileId"`
	Purpose                string     `json:"purpose"`
	Network                string     `json:"network"`
	ChainID                uint64     `json:"chainId"`
	SourceCommit           string     `json:"sourceCommit"`
	Candidate              DigestFile `json:"candidate"`
	Safe                   SafeTrust  `json:"safe"`
	MaximumAgeSeconds      uint64     `json:"maximumAgeSeconds"`
	MinimumOwnerSignatures int        `json:"minimumOwnerSignatures"`
}

type OwnerSignature struct {
	Owner        string `json:"owner"`
	SignatureHex string `json:"signatureHex"`
}

type Proof struct {
	SchemaVersion int              `json:"schemaVersion"`
	ProfileID     string           `json:"profileId"`
	ProfileSHA256 string           `json:"profileSha256"`
	ChallengeID   string           `json:"challengeId"`
	IssuedAt      string           `json:"issuedAt"`
	ExpiresAt     string           `json:"expiresAt"`
	Statement     string           `json:"statement"`
	Signatures    []OwnerSignature `json:"signatures"`
}

func LoadProfile(path string) (Profile, error) {
	var profile Profile
	if err := decodeStrict(path, &profile); err != nil {
		return Profile{}, err
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	if err := verifyDigestFile(filepath.Dir(path), profile.Candidate); err != nil {
		return Profile{}, fmt.Errorf("safe owner profile candidate: %w", err)
	}
	return profile, nil
}

func LoadProof(path string) (Proof, error) {
	var proof Proof
	if err := decodeStrict(path, &proof); err != nil {
		return Proof{}, err
	}
	return proof, nil
}

func (profile Profile) Validate() error {
	if profile.SchemaVersion != SchemaVersion || !idPattern.MatchString(profile.ProfileID) {
		return errors.New("safe owner profile identity is invalid")
	}
	if profile.Purpose != SigningContext || profile.Network != "base-mainnet" || profile.ChainID != 8453 {
		return errors.New("safe owner profile target is invalid")
	}
	if !commitPattern.MatchString(profile.SourceCommit) || profile.MaximumAgeSeconds == 0 || profile.MaximumAgeSeconds > 3600 {
		return errors.New("safe owner profile source or lifetime is invalid")
	}
	if filepath.Base(profile.Candidate.Path) != profile.Candidate.Path || !hashPattern.MatchString(profile.Candidate.SHA256) {
		return errors.New("safe owner profile candidate binding is invalid")
	}
	if !canonicalAddress(profile.Safe.Address) || !canonicalAddress(profile.Safe.Implementation) || !hashPattern.MatchString(profile.Safe.RuntimeCodeHash) || profile.Safe.Nonce != 0 {
		return errors.New("safe owner profile Safe binding is invalid")
	}
	if len(profile.Safe.Owners) != 3 || profile.Safe.Threshold != 2 || profile.MinimumOwnerSignatures != profile.Safe.Threshold {
		return errors.New("safe owner profile quorum is invalid")
	}
	seen := make(map[string]struct{}, len(profile.Safe.Owners))
	for _, owner := range profile.Safe.Owners {
		if !canonicalAddress(owner) {
			return errors.New("safe owner profile owner is invalid")
		}
		if _, exists := seen[owner]; exists {
			return errors.New("safe owner profile owners must be unique")
		}
		seen[owner] = struct{}{}
	}
	return nil
}

func ProfileSHA256(profile Profile) (string, error) {
	encoded, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "0x" + hex.EncodeToString(digest[:]), nil
}

func Template(profile Profile, challengeID string, now time.Time) (Proof, error) {
	if err := profile.Validate(); err != nil {
		return Proof{}, err
	}
	if !idPattern.MatchString(challengeID) {
		return Proof{}, errors.New("safe owner challenge ID is invalid")
	}
	profileDigest, err := ProfileSHA256(profile)
	if err != nil {
		return Proof{}, err
	}
	now = now.UTC().Truncate(time.Second)
	return Proof{
		SchemaVersion: SchemaVersion,
		ProfileID:     profile.ProfileID,
		ProfileSHA256: profileDigest,
		ChallengeID:   challengeID,
		IssuedAt:      now.Format(time.RFC3339),
		ExpiresAt:     now.Add(time.Duration(profile.MaximumAgeSeconds) * time.Second).Format(time.RFC3339),
		Statement:     "I control this reviewed Safe owner address. This proof creates no transaction and authorizes no deployment, funding, token approval, module activation, or asset movement.",
		Signatures:    []OwnerSignature{},
	}, nil
}

func SigningMessage(proof Proof) (string, error) {
	unsigned := proof
	unsigned.Signatures = nil
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return SigningContext + "\n0x" + hex.EncodeToString(digest[:]), nil
}

func Verify(proof Proof, profile Profile, now time.Time) error {
	if err := ValidateUnsigned(proof, profile, now); err != nil {
		return err
	}
	if len(proof.Signatures) < profile.MinimumOwnerSignatures || len(proof.Signatures) > len(profile.Safe.Owners) {
		return errors.New("safe owner proof signature quorum is incomplete")
	}
	message, err := SigningMessage(proof)
	if err != nil {
		return err
	}
	digest := accounts.TextHash([]byte(message))
	trusted := make(map[common.Address]struct{}, len(profile.Safe.Owners))
	for _, owner := range profile.Safe.Owners {
		trusted[common.HexToAddress(owner)] = struct{}{}
	}
	seen := make(map[common.Address]struct{}, len(proof.Signatures))
	for _, signed := range proof.Signatures {
		if !canonicalAddress(signed.Owner) {
			return errors.New("safe owner proof signature owner is invalid")
		}
		owner := common.HexToAddress(signed.Owner)
		if _, exists := trusted[owner]; !exists {
			return fmt.Errorf("safe owner proof signer %s is not a reviewed owner", signed.Owner)
		}
		if _, exists := seen[owner]; exists {
			return fmt.Errorf("safe owner proof signer %s is duplicated", signed.Owner)
		}
		signature, err := decodeSignature(signed.SignatureHex)
		if err != nil {
			return err
		}
		publicKey, err := crypto.SigToPub(digest, signature)
		if err != nil || crypto.PubkeyToAddress(*publicKey) != owner {
			return fmt.Errorf("safe owner proof signature for %s is invalid", signed.Owner)
		}
		seen[owner] = struct{}{}
	}
	return nil
}

// ValidateUnsigned proves that a wallet will sign only the current committed
// profile, candidate and non-authorizing statement before a message is shown.
func ValidateUnsigned(proof Proof, profile Profile, now time.Time) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	profileDigest, err := ProfileSHA256(profile)
	if err != nil {
		return err
	}
	if proof.SchemaVersion != SchemaVersion || proof.ProfileID != profile.ProfileID || proof.ProfileSHA256 != profileDigest || !idPattern.MatchString(proof.ChallengeID) {
		return errors.New("safe owner proof profile binding is invalid")
	}
	if proof.Statement != "I control this reviewed Safe owner address. This proof creates no transaction and authorizes no deployment, funding, token approval, module activation, or asset movement." {
		return errors.New("safe owner proof statement is invalid")
	}
	issuedAt, err := time.Parse(time.RFC3339, proof.IssuedAt)
	if err != nil {
		return errors.New("safe owner proof issuedAt is invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, proof.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > time.Duration(profile.MaximumAgeSeconds)*time.Second {
		return errors.New("safe owner proof expiry is invalid")
	}
	now = now.UTC()
	if issuedAt.After(now.Add(5*time.Minute)) || !expiresAt.After(now) {
		return errors.New("safe owner proof is not currently valid")
	}
	return nil
}

func decodeStrict(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > MaxJSONBytes {
		return errors.New("safe owner JSON exceeds 32 KiB")
	}
	if err := rpcadmission.RejectDuplicateJSONFields(raw); err != nil {
		return errors.New("safe owner JSON contains duplicate or trailing values")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("safe owner JSON contains trailing values")
	}
	return nil
}

func verifyDigestFile(root string, file DigestFile) error {
	raw, err := os.ReadFile(filepath.Join(root, file.Path))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if "0x"+hex.EncodeToString(digest[:]) != file.SHA256 {
		return errors.New("candidate digest mismatch")
	}
	return nil
}

func decodeSignature(raw string) ([]byte, error) {
	if !strings.HasPrefix(raw, "0x") || len(raw) != 132 {
		return nil, errors.New("safe owner proof signature encoding is invalid")
	}
	signature, err := hex.DecodeString(raw[2:])
	if err != nil {
		return nil, errors.New("safe owner proof signature encoding is invalid")
	}
	if signature[64] >= 27 {
		signature[64] -= 27
	}
	if signature[64] > 1 {
		return nil, errors.New("safe owner proof signature recovery value is invalid")
	}
	return signature, nil
}

func canonicalAddress(value string) bool {
	return common.IsHexAddress(value) && value == strings.ToLower(value)
}
