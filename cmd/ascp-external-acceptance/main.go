package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gnanam1990/flowops/internal/acceptanceexternal"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer, now time.Time) error {
	if len(args) == 1 && args[0] == "requirements" {
		encoded, err := json.MarshalIndent(acceptanceexternal.RequiredAssertions(), "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	}
	if len(args) == 2 && args[0] == "digest" {
		bundle, err := acceptanceexternal.LoadBundle(args[1])
		if err != nil {
			return err
		}
		message, err := acceptanceexternal.SigningMessage(bundle)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, message)
		return err
	}
	if len(args) == 3 && args[0] == "template" {
		profile, err := acceptanceexternal.LoadProfile(args[1])
		if err != nil {
			return err
		}
		bundle, err := acceptanceexternal.Template(profile, args[2], now)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	}
	if len(args) == 4 && args[0] == "verify" {
		bundle, err := acceptanceexternal.LoadBundle(args[1])
		if err != nil {
			return err
		}
		profile, err := acceptanceexternal.LoadProfile(args[2])
		if err != nil {
			return err
		}
		if err := acceptanceexternal.Verify(bundle, profile, args[3], now); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "external acceptance evidence %s verified for %d criteria\n", bundle.RunID, len(bundle.Criteria))
		return err
	}
	return errors.New("usage: ascp-external-acceptance requirements | template <profile.json> <run-id> | digest <bundle.json> | verify <bundle.json> <profile.json> <evidence-root>")
}
