// escrow-conformance verifies a completed CallEscrow lifecycle against two or
// more independent Base RPC providers. It is read-only and cannot sign or
// broadcast a transaction.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

const maxManifestBytes = 128 * 1024

type providerFlags []string

func (f *providerFlags) String() string { return strings.Join(*f, ",") }
func (f *providerFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func main() {
	manifestPath := flag.String("manifest", "", "completed escrow lifecycle manifest")
	timeout := flag.Duration("timeout", 30*time.Second, "overall verification timeout")
	var rawProviders providerFlags
	flag.Var(&rawProviders, "rpc", "independent provider as name=https://endpoint (repeat at least twice; public URLs only)")
	flag.Parse()
	if strings.TrimSpace(*manifestPath) == "" {
		fail(errors.New("-manifest is required"))
	}
	manifest, err := readManifest(*manifestPath)
	fail(err)
	providers, err := parseProviders(rawProviders)
	fail(err)
	observers, err := reconciliation.NewObserverSet(manifest.Transitions[0].ChainID, providers, nil, nil)
	fail(err)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, verifyErr := reconciliation.VerifyEscrowLifecycle(ctx, observers, manifest)
	encoded, err := json.MarshalIndent(struct {
		Result reconciliation.EscrowLifecycleResult `json:"result"`
		Error  string                               `json:"error,omitempty"`
	}{Result: result, Error: errorString(verifyErr)}, "", "  ")
	fail(err)
	fmt.Println(string(encoded))
	if verifyErr != nil || !result.Ready {
		os.Exit(2)
	}
}

func readManifest(path string) (reconciliation.EscrowLifecycleManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return reconciliation.EscrowLifecycleManifest{}, fmt.Errorf("open escrow manifest: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return reconciliation.EscrowLifecycleManifest{}, fmt.Errorf("read escrow manifest: %w", err)
	}
	if len(raw) > maxManifestBytes {
		return reconciliation.EscrowLifecycleManifest{}, errors.New("escrow manifest exceeds 128 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest reconciliation.EscrowLifecycleManifest
	if err := decoder.Decode(&manifest); err != nil {
		return reconciliation.EscrowLifecycleManifest{}, fmt.Errorf("decode escrow manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return reconciliation.EscrowLifecycleManifest{}, errors.New("escrow manifest must contain exactly one JSON value")
	}
	if err := manifest.Validate(); err != nil {
		return reconciliation.EscrowLifecycleManifest{}, err
	}
	return manifest, nil
}

func parseProviders(values []string) ([]reconciliation.RPCProvider, error) {
	providers := make([]reconciliation.RPCProvider, 0, len(values))
	for _, value := range values {
		name, endpoint, found := strings.Cut(value, "=")
		if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(endpoint) == "" {
			return nil, errors.New("RPC provider must use name=https://endpoint syntax")
		}
		providers = append(providers, reconciliation.RPCProvider{Name: strings.TrimSpace(name), URL: strings.TrimSpace(endpoint)})
	}
	return providers, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
