// base-observer performs read-only, independent Base JSON-RPC liveness checks.
// It accepts no wallet key and cannot sign, settle, or broadcast transactions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type providerFlags []string

func (f *providerFlags) String() string { return strings.Join(*f, ",") }
func (f *providerFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func main() {
	chainID := flag.Uint64("chain-id", 84532, "Base chain ID (84532 Sepolia or 8453 mainnet)")
	timeout := flag.Duration("timeout", 15*time.Second, "overall observation timeout")
	var rawProviders providerFlags
	flag.Var(&rawProviders, "rpc", "independent provider as name=https://endpoint (repeat at least twice; use public URLs only on the command line)")
	flag.Parse()
	providers, err := parseProviders(rawProviders)
	check(err)
	observers, err := reconciliation.NewObserverSet(*chainID, providers, nil, nil)
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result := observers.Snapshot(ctx)
	encoded, err := json.MarshalIndent(result, "", "  ")
	check(err)
	fmt.Println(string(encoded))
	if len(result.Observations) < 2 {
		os.Exit(2)
	}
}

func parseProviders(values []string) ([]reconciliation.RPCProvider, error) {
	providers := make([]reconciliation.RPCProvider, 0, len(values))
	for _, value := range values {
		name, endpoint, found := strings.Cut(value, "=")
		if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(endpoint) == "" {
			return nil, fmt.Errorf("RPC provider must use name=https://endpoint syntax")
		}
		providers = append(providers, reconciliation.RPCProvider{Name: strings.TrimSpace(name), URL: strings.TrimSpace(endpoint)})
	}
	return providers, nil
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
