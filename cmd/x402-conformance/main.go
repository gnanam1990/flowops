// x402-conformance performs read-only facilitator and calldata checks. It never
// accepts a private key and cannot verify, settle, or broadcast a payment.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/x402adapter"
)

func main() {
	facilitatorURL := flag.String("facilitator", "https://x402.org/facilitator", "facilitator base URL")
	serviceCodes := flag.String("service-codes", "", "comma-separated expected FlowOps service codes")
	appCode := flag.String("app-code", "", "expected resource app code")
	calldata := flag.String("calldata", "", "optional canonical settlement calldata")
	flag.Parse()

	codes := splitCodes(*serviceCodes)
	adapter, err := x402adapter.New(x402adapter.Config{
		Network: x402adapter.BaseSepoliaNetwork, ChainID: x402adapter.BaseSepoliaChainID,
		USDCAddress: x402adapter.BaseSepoliaUSDC, MaxAmountAtomic: "1000000",
		MaxTimeoutSeconds: 300, ServiceCodes: codes,
	})
	check(err)
	client, err := x402adapter.NewFacilitatorClient(*facilitatorURL, nil)
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	supported, err := client.Supported(ctx)
	check(err)

	output := struct {
		Facilitator x402adapter.FacilitatorConformance `json:"facilitator"`
		Attribution *x402adapter.AttributionEvidence   `json:"attribution,omitempty"`
	}{Facilitator: adapter.CheckFacilitator(supported)}
	if *calldata != "" {
		evidence := x402adapter.ClassifyCalldata(*calldata, *appCode, codes, *appCode != "")
		output.Attribution = &evidence
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	check(err)
	fmt.Println(string(encoded))
	if !output.Facilitator.Ready || output.Attribution != nil && output.Attribution.State != x402adapter.AttributionVerifiedSuffix {
		os.Exit(2)
	}
}

func splitCodes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	output := make([]string, 0, len(parts))
	for _, part := range parts {
		output = append(output, strings.TrimSpace(part))
	}
	return output
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
