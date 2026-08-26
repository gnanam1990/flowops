package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/securefile"
	"github.com/gnanam1990/flowops/pkg/directoryrelease"
	"golang.org/x/sys/unix"
)

const (
	maximumInputBytes     = 4 << 20
	maximumSignatureBytes = 1024
)

func main() {
	var simulator directoryrelease.RelayStateSimulator
	var err error
	if len(os.Args) > 1 && os.Args[1] == "simulate" {
		primary := fallback(strings.TrimSpace(os.Getenv("BASE_SEPOLIA_RPC_URL_PRIMARY")), "https://sepolia.base.org")
		secondary := fallback(strings.TrimSpace(os.Getenv("BASE_SEPOLIA_RPC_URL_SECONDARY")), "https://base-sepolia-rpc.publicnode.com")
		simulator, err = directoryrelease.NewBaseSepoliaRelaySimulator(primary, secondary, 20*time.Second)
	}
	if err == nil {
		err = run(context.Background(), os.Args[1:], os.Stdout, simulator, time.Now)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, simulator directoryrelease.RelayStateSimulator, clock func() time.Time) error {
	if output == nil || clock == nil || len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "simulate":
		if len(args) != 6 || simulator == nil {
			return usage()
		}
		presign, artifact, deployment, signed, request, err := loadInputs(args[1], args[2], args[3], args[4], args[5])
		if err != nil {
			return err
		}
		signed.Signature = strings.Clone(signed.Signature)
		evidence, err := directoryrelease.PrepareRelaySimulation(ctx, presign, artifact, deployment, signed, request, simulator, clock)
		signed.Signature = ""
		if err != nil {
			return err
		}
		return writeJSON(output, evidence)
	case "verify":
		if len(args) != 7 {
			return usage()
		}
		evidenceRaw, err := readLimited(args[1], maximumInputBytes)
		if err != nil {
			return err
		}
		evidence, err := directoryrelease.DecodeRelaySimulationEvidence(evidenceRaw)
		if err != nil {
			return err
		}
		presign, artifact, deployment, signed, request, err := loadInputs(args[2], args[3], args[4], args[5], args[6])
		if err != nil {
			return err
		}
		if err := directoryrelease.VerifyRelaySimulation(evidence, presign, artifact, deployment, signed, request,
			clock().UTC().Truncate(time.Second)); err != nil {
			signed.Signature = ""
			return err
		}
		signed.Signature = ""
		_, err = fmt.Fprintf(output,
			"release=%s calldataHash=%s signer=%s signature=private calldata=withheld broadcastAuthorized=false fundingEnabled=false\n",
			evidence.ReleaseID, evidence.CalldataHash, evidence.RecoveredSigner)
		return err
	default:
		return usage()
	}
}

func loadInputs(presignPath, artifactPath, deploymentPath, signaturePath, requestPath string) (
	directoryrelease.PresignPackage, directoryrelease.Artifact, []byte, directoryrelease.PublisherSignature,
	directoryrelease.RelaySimulationRequest, error,
) {
	presignRaw, err := readLimited(presignPath, maximumInputBytes)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	presign, err := directoryrelease.DecodePresignPackage(presignRaw)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	artifactRaw, err := readLimited(artifactPath, maximumInputBytes)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	artifact, err := directoryrelease.DecodeArtifact(artifactRaw)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	deployment, err := readLimited(deploymentPath, maximumInputBytes)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	signatureRaw, err := readPrivate(signaturePath, maximumSignatureBytes)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	defer clear(signatureRaw)
	signed, err := directoryrelease.DecodePublisherSignature(signatureRaw)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	requestRaw, err := readLimited(requestPath, maximumSignatureBytes)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	request, err := directoryrelease.DecodeRelaySimulationRequest(requestRaw)
	if err != nil {
		return directoryrelease.PresignPackage{}, directoryrelease.Artifact{}, nil, directoryrelease.PublisherSignature{}, directoryrelease.RelaySimulationRequest{}, err
	}
	return presign, artifact, deployment, signed, request, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("input file is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.New("input is empty, unreadable, or too large")
	}
	return raw, nil
}

func readPrivate(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || limit < 1 || limit > maximumSignatureBytes {
		return nil, errors.New("signature path must be a clean absolute file path")
	}
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("signature parent is not securely controlled")
	}
	defer parent.Close()
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 || !securefile.OwnerAllowed(parentInfo) {
		return nil, errors.New("signature parent must be private and owner-controlled")
	}
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("signature file is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > limit || !securefile.OwnerAllowed(info) {
		return nil, errors.New("signature file must be a private owner-controlled regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		clear(raw)
		return nil, errors.New("signature file is empty, unreadable, or too large")
	}
	return raw, nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() error {
	return errors.New("usage: ascp-directory-relay simulate <presign.json> <artifact.json> <deployment.json> <private-signature.json> <request.json> | verify <evidence.json> <presign.json> <artifact.json> <deployment.json> <private-signature.json> <request.json>")
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
