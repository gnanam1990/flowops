package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/releaseadmission"
)

func main() {
	if err := run(os.Args[1:], time.Now().UTC()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, now time.Time) error {
	if len(args) != 2 {
		return errors.New("usage: release-manifest sign|verify|digest <manifest.json>")
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return fmt.Errorf("read release manifest: %w", err)
	}
	manifest, err := releaseadmission.Decode(string(raw))
	if err != nil {
		return err
	}
	switch args[0] {
	case "sign":
		if manifest.Signature != "" {
			return errors.New("refusing to replace an existing release manifest signature")
		}
		if err := releaseadmission.ValidateUnsigned(manifest, now); err != nil {
			return err
		}
		privateKey, err := decodePrivateKey(os.Getenv("FLOWOPS_BASE_MAINNET_RELEASE_PRIVATE_KEY_B64"))
		if err != nil {
			return err
		}
		signed, err := releaseadmission.Sign(manifest, privateKey)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(signed, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(encoded))
		return err
	case "verify":
		publicKey, err := releaseadmission.DecodePublicKey(os.Getenv("FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64"))
		if err != nil {
			return err
		}
		if err := releaseadmission.Verify(manifest, publicKey, now); err != nil {
			return err
		}
		digest, err := releaseadmission.CanonicalSHA256(manifest)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "release=%s chainId=%d fundingEnabled=%t sha256=%s\n", manifest.ReleaseID, manifest.ChainID, manifest.Pilot.FundingEnabled, digest)
		return err
	case "digest":
		digest, err := releaseadmission.CanonicalSHA256(manifest)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, digest)
		return err
	default:
		return errors.New("usage: release-manifest sign|verify|digest <manifest.json>")
	}
}

func decodePrivateKey(raw string) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("FLOWOPS_BASE_MAINNET_RELEASE_PRIVATE_KEY_B64 must encode exactly 64 bytes")
	}
	return ed25519.PrivateKey(append([]byte(nil), decoded...)), nil
}
