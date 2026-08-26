package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/pkg/directoryrelease"
)

const maximumInputBytes = 4 << 20

func main() {
	fetcher, err := directoryrelease.NewHTTPBlobFetcher(20 * time.Second)
	if err == nil {
		primary := fallback(strings.TrimSpace(os.Getenv("BASE_SEPOLIA_RPC_URL_PRIMARY")), "https://sepolia.base.org")
		secondary := fallback(strings.TrimSpace(os.Getenv("BASE_SEPOLIA_RPC_URL_SECONDARY")), "https://base-sepolia-rpc.publicnode.com")
		var observer directoryrelease.LiveStateObserver
		observer, err = directoryrelease.NewBaseSepoliaLiveObserver(primary, secondary, 20*time.Second)
		if err == nil {
			err = run(context.Background(), os.Args[1:], os.Stdout, fetcher, observer, time.Now)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, fetcher directoryrelease.BlobFetcher,
	observer directoryrelease.LiveStateObserver, clock func() time.Time,
) error {
	if output == nil || clock == nil || len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "verify-remote":
		if len(args) != 4 || fetcher == nil {
			return usage()
		}
		artifact, deployment, err := loadArtifact(args[1], args[2])
		if err != nil {
			return err
		}
		gatewaysRaw, err := readLimited(args[3])
		if err != nil {
			return err
		}
		gateways, err := directoryrelease.DecodeGatewayConfig(gatewaysRaw)
		if err != nil {
			return err
		}
		evidence, err := directoryrelease.VerifyRemote(ctx, artifact, deployment, gateways, fetcher)
		if err != nil {
			return err
		}
		return writeJSON(output, evidence)
	case "prepare":
		if len(args) != 5 || fetcher == nil || observer == nil {
			return usage()
		}
		artifact, deployment, err := loadArtifact(args[1], args[2])
		if err != nil {
			return err
		}
		gatewaysRaw, err := readLimited(args[3])
		if err != nil {
			return err
		}
		gateways, err := directoryrelease.DecodeGatewayConfig(gatewaysRaw)
		if err != nil {
			return err
		}
		requestRaw, err := readLimited(args[4])
		if err != nil {
			return err
		}
		request, err := directoryrelease.DecodePresignRequest(requestRaw)
		if err != nil {
			return err
		}
		presign, err := directoryrelease.BuildPresign(ctx, artifact, deployment, gateways, request, fetcher, observer, clock)
		if err != nil {
			return err
		}
		return writeJSON(output, presign)
	case "verify":
		if len(args) != 4 {
			return usage()
		}
		packageRaw, err := readLimited(args[1])
		if err != nil {
			return err
		}
		presign, err := directoryrelease.DecodePresignPackage(packageRaw)
		if err != nil {
			return err
		}
		artifact, deployment, err := loadArtifact(args[2], args[3])
		if err != nil {
			return err
		}
		if err := directoryrelease.VerifyPresign(presign, artifact, deployment, clock().UTC().Truncate(time.Second)); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "release=%s digest=%s signer=%s signature=absent broadcastAuthorized=false fundingEnabled=false\n",
			presign.ReleaseID, presign.Digest, presign.ExpectedSigner)
		return err
	default:
		return usage()
	}
}

func loadArtifact(artifactPath, deploymentPath string) (directoryrelease.Artifact, []byte, error) {
	artifactRaw, err := readLimited(artifactPath)
	if err != nil {
		return directoryrelease.Artifact{}, nil, err
	}
	artifact, err := directoryrelease.DecodeArtifact(artifactRaw)
	if err != nil {
		return directoryrelease.Artifact{}, nil, err
	}
	deployment, err := readLimited(deploymentPath)
	if err != nil {
		return directoryrelease.Artifact{}, nil, err
	}
	return artifact, deployment, nil
}

func readLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximumInputBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maximumInputBytes {
		return nil, errors.New("input is empty, unreadable, or exceeds 4 MiB")
	}
	return raw, nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() error {
	return errors.New("usage: ascp-directory-presign verify-remote <artifact.json> <deployment.json> <gateways.json> | prepare <artifact.json> <deployment.json> <gateways.json> <request.json> | verify <presign.json> <artifact.json> <deployment.json>")
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
