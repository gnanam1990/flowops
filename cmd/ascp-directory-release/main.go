package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/gnanam1990/flowops/pkg/directoryrelease"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 || (args[0] != "compile" && args[0] != "verify") {
		return errors.New("usage: ascp-directory-release compile <manifest.json> <deployment.json> | verify <artifact.json> <deployment.json>")
	}
	input, err := os.ReadFile(args[1])
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	deployment, err := os.ReadFile(args[2])
	if err != nil {
		return fmt.Errorf("read deployment: %w", err)
	}
	if args[0] == "compile" {
		manifest, err := directoryrelease.DecodeManifest(input)
		if err != nil {
			return err
		}
		artifact, err := directoryrelease.Compile(manifest, deployment)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(encoded))
		return err
	}
	artifact, err := directoryrelease.DecodeArtifact(input)
	if err != nil {
		return err
	}
	if err := directoryrelease.Verify(artifact, deployment); err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "release=%s version=%d root=%s fundingEnabled=false\n", artifact.Manifest.ReleaseID, artifact.Proposal.VersionID, artifact.MerkleRoot)
	return err
}
