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
	maximumInputBytes   = 4 << 20
	maximumPrivateBytes = 16 << 10
)

func main() {
	var observer directoryrelease.RelayTransactionObserver
	var err error
	if len(os.Args) > 1 && os.Args[1] == "prepare" {
		primary := fallback(strings.TrimSpace(os.Getenv("BASE_SEPOLIA_RPC_URL_PRIMARY")), "https://sepolia.base.org")
		secondary := fallback(strings.TrimSpace(os.Getenv("BASE_SEPOLIA_RPC_URL_SECONDARY")), "https://base-sepolia-rpc.publicnode.com")
		observer, err = directoryrelease.NewBaseSepoliaTransactionObserver(primary, secondary, 20*time.Second)
	}
	if err == nil {
		err = run(context.Background(), os.Args[1:], os.Stdout, observer, time.Now)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, observer directoryrelease.RelayTransactionObserver, clock func() time.Time) error {
	if output == nil || clock == nil || len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "prepare":
		if len(args) != 9 || observer == nil {
			return usage()
		}
		inputs, err := loadInputs(args[1], args[2], args[3], args[4], args[5], args[6], args[7])
		if err != nil {
			return err
		}
		preview, private, err := directoryrelease.PrepareRelayTransactionPreview(ctx, inputs.relay, inputs.presign, inputs.artifact,
			inputs.deployment, inputs.signature, inputs.relayRequest, inputs.transactionRequest, observer, clock)
		inputs.signature.Signature = ""
		if err != nil {
			return err
		}
		if err := writePrivateExclusive(args[8], private); err != nil {
			return err
		}
		return writeJSON(output, preview)
	case "verify":
		if len(args) != 10 {
			return usage()
		}
		previewRaw, err := readLimited(args[1], maximumInputBytes)
		if err != nil {
			return err
		}
		preview, err := directoryrelease.DecodeRelayTransactionPreview(previewRaw)
		if err != nil {
			return err
		}
		privateRaw, err := readPrivate(args[2], maximumPrivateBytes)
		if err != nil {
			return err
		}
		defer clear(privateRaw)
		private, err := directoryrelease.DecodeUnsignedRelayTransaction(privateRaw)
		if err != nil {
			return err
		}
		private.Data = strings.Clone(private.Data)
		inputs, err := loadInputs(args[3], args[4], args[5], args[6], args[7], args[8], args[9])
		if err != nil {
			private.Data = ""
			return err
		}
		err = directoryrelease.VerifyRelayTransactionPreview(preview, private, inputs.relay, inputs.presign, inputs.artifact,
			inputs.deployment, inputs.signature, inputs.relayRequest, inputs.transactionRequest, clock().UTC().Truncate(time.Second))
		private.Data = ""
		inputs.signature.Signature = ""
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output,
			"release=%s chainId=%d relayer=%s nonce=%s calldataHash=%s privateArtifact=required signingRequired=true broadcastAuthorized=false fundingEnabled=false\n",
			preview.ReleaseID, preview.ChainID, preview.RelayerAddress, preview.Nonce, preview.CalldataHash)
		return err
	default:
		return usage()
	}
}

type loadedInputs struct {
	relay              directoryrelease.RelaySimulationEvidence
	presign            directoryrelease.PresignPackage
	artifact           directoryrelease.Artifact
	deployment         []byte
	signature          directoryrelease.PublisherSignature
	relayRequest       directoryrelease.RelaySimulationRequest
	transactionRequest directoryrelease.RelayTransactionRequest
}

func loadInputs(relayPath, presignPath, artifactPath, deploymentPath, signaturePath, relayRequestPath, transactionRequestPath string) (loadedInputs, error) {
	var value loadedInputs
	relayRaw, err := readLimited(relayPath, maximumInputBytes)
	if err != nil {
		return loadedInputs{}, err
	}
	value.relay, err = directoryrelease.DecodeRelaySimulationEvidence(relayRaw)
	if err != nil {
		return loadedInputs{}, err
	}
	presignRaw, err := readLimited(presignPath, maximumInputBytes)
	if err != nil {
		return loadedInputs{}, err
	}
	value.presign, err = directoryrelease.DecodePresignPackage(presignRaw)
	if err != nil {
		return loadedInputs{}, err
	}
	artifactRaw, err := readLimited(artifactPath, maximumInputBytes)
	if err != nil {
		return loadedInputs{}, err
	}
	value.artifact, err = directoryrelease.DecodeArtifact(artifactRaw)
	if err != nil {
		return loadedInputs{}, err
	}
	value.deployment, err = readLimited(deploymentPath, maximumInputBytes)
	if err != nil {
		return loadedInputs{}, err
	}
	signatureRaw, err := readPrivate(signaturePath, 1024)
	if err != nil {
		return loadedInputs{}, err
	}
	defer clear(signatureRaw)
	value.signature, err = directoryrelease.DecodePublisherSignature(signatureRaw)
	if err != nil {
		return loadedInputs{}, err
	}
	relayRequestRaw, err := readLimited(relayRequestPath, 1024)
	if err != nil {
		return loadedInputs{}, err
	}
	value.relayRequest, err = directoryrelease.DecodeRelaySimulationRequest(relayRequestRaw)
	if err != nil {
		return loadedInputs{}, err
	}
	transactionRequestRaw, err := readLimited(transactionRequestPath, 1024)
	if err != nil {
		return loadedInputs{}, err
	}
	value.transactionRequest, err = directoryrelease.DecodeRelayTransactionRequest(transactionRequestRaw)
	if err != nil {
		return loadedInputs{}, err
	}
	return value, nil
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
	parent, base, err := secureParent(path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), base, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("private file is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > limit || !securefile.OwnerAllowed(info) {
		return nil, errors.New("private file must be an owner-controlled regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		clear(raw)
		return nil, errors.New("private file is empty, unreadable, or too large")
	}
	return raw, nil
}

func writePrivateExclusive(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(raw) == 0 || len(raw) > maximumPrivateBytes {
		clear(raw)
		return errors.New("private transaction cannot be encoded")
	}
	raw = append(raw, '\n')
	defer clear(raw)
	parent, base, err := secureParent(path)
	if err != nil {
		return err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return errors.New("private output must not already exist")
	}
	file := os.NewFile(uintptr(fd), path)
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = unix.Unlinkat(int(parent.Fd()), base, 0)
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefile.OwnerAllowed(info) {
		return errors.New("private output is not owner-controlled")
	}
	if _, err := file.Write(raw); err != nil || file.Sync() != nil {
		return errors.New("private output write failed")
	}
	remove = false
	return nil
}

func secureParent(path string) (*os.File, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || filepath.Base(path) == "." {
		return nil, "", errors.New("private path must be a clean absolute file path")
	}
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, "", errors.New("private parent is not securely controlled")
	}
	info, err := parent.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || !securefile.OwnerAllowed(info) {
		_ = parent.Close()
		return nil, "", errors.New("private parent must be owner-controlled")
	}
	return parent, filepath.Base(path), nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() error {
	return errors.New("usage: ascp-directory-transaction-preview prepare <relay-evidence.json> <presign.json> <artifact.json> <deployment.json> <private-signature.json> <relay-request.json> <transaction-request.json> <private-output.json> | verify <preview.json> <private-transaction.json> <relay-evidence.json> <presign.json> <artifact.json> <deployment.json> <private-signature.json> <relay-request.json> <transaction-request.json>")
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
