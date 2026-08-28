package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gnanam1990/flowops/internal/safeownerproof"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer, now time.Time) error {
	if len(args) == 3 && args[0] == "template" {
		profile, err := safeownerproof.LoadProfile(args[1])
		if err != nil {
			return err
		}
		proof, err := safeownerproof.Template(profile, args[2], now)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(proof, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	}
	if len(args) == 3 && args[0] == "digest" {
		proof, err := safeownerproof.LoadProof(args[1])
		if err != nil {
			return err
		}
		profile, err := safeownerproof.LoadProfile(args[2])
		if err != nil {
			return err
		}
		if err := safeownerproof.ValidateUnsigned(proof, profile, now); err != nil {
			return err
		}
		message, err := safeownerproof.SigningMessage(proof)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, message)
		return err
	}
	if len(args) == 3 && args[0] == "verify" {
		proof, err := safeownerproof.LoadProof(args[1])
		if err != nil {
			return err
		}
		profile, err := safeownerproof.LoadProfile(args[2])
		if err != nil {
			return err
		}
		if err := safeownerproof.Verify(proof, profile, now); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "safe owner control proof %s verified with %d reviewed owners; no transaction authorized\n", proof.ChallengeID, len(proof.Signatures))
		return err
	}
	return errors.New("usage: ascp-safe-owner-proof template <profile.json> <challenge-id> | digest <proof.json> <profile.json> | verify <proof.json> <profile.json>")
}
