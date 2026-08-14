// rpc-admission-check validates Base mainnet observer admission without making
// network requests. Both inputs are read from the environment so a
// credential-bearing provider URL never appears in process arguments.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gnanam1990/flowops/internal/rpcadmission"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if strings.TrimSpace(os.Getenv("FLOWOPS_BASE_CHAIN_ID")) != "8453" {
		return errors.New("FLOWOPS_BASE_CHAIN_ID must be 8453 for production RPC admission")
	}
	providers, err := rpcadmission.DecodeProviders(os.Getenv("FLOWOPS_BASE_RPC_PROVIDERS_JSON"))
	if err != nil {
		return err
	}
	admission, err := rpcadmission.DecodeProductionAdmission(os.Getenv("FLOWOPS_BASE_RPC_ADMISSION_JSON"))
	if err != nil {
		return err
	}
	if err := rpcadmission.ValidateProduction(providers, admission); err != nil {
		return err
	}
	result, err := json.Marshal(struct {
		ChainID            uint64 `json:"chainId"`
		ProviderCount      int    `json:"providerCount"`
		ProductionAdmitted bool   `json:"productionAdmitted"`
	}{ChainID: 8453, ProviderCount: len(providers), ProductionAdmitted: true})
	if err != nil {
		return err
	}
	fmt.Println(string(result))
	return nil
}
