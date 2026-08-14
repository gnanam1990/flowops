package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunAdmitsMainnetWithoutPrintingSecretURLs(t *testing.T) {
	t.Setenv("FLOWOPS_BASE_CHAIN_ID", "8453")
	t.Setenv("FLOWOPS_BASE_RPC_PROVIDERS_JSON", `[{"name":"rpc_alpha","url":"https://alpha.vendor.example/v1/top-secret-alpha"},{"name":"rpc_beta","url":"https://beta.vendor.example/v1/top-secret-beta"}]`)
	t.Setenv("FLOWOPS_BASE_RPC_ADMISSION_JSON", `{"schemaVersion":1,"providers":[{"name":"rpc_alpha","operator":"vendor_alpha","failureDomain":"vendor_alpha_global","serviceTier":"paid","productionEligible":true},{"name":"rpc_beta","operator":"vendor_beta","failureDomain":"vendor_beta_global","serviceTier":"paid","productionEligible":true}]}`)
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandOutputDoesNotContainProviderSecrets(t *testing.T) {
	if os.Getenv("FLOWOPS_RPC_ADMISSION_SUBPROCESS") == "1" {
		if err := run(); err != nil {
			panic(err)
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=TestCommandOutputDoesNotContainProviderSecrets")
	baseEnv := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "FLOWOPS_RPC_ADMISSION_SUBPROCESS=") || strings.HasPrefix(value, "FLOWOPS_BASE_CHAIN_ID=") || strings.HasPrefix(value, "FLOWOPS_BASE_RPC_PROVIDERS_JSON=") || strings.HasPrefix(value, "FLOWOPS_BASE_RPC_ADMISSION_JSON=") {
			continue
		}
		baseEnv = append(baseEnv, value)
	}
	command.Env = append(baseEnv,
		"FLOWOPS_RPC_ADMISSION_SUBPROCESS=1",
		"FLOWOPS_BASE_CHAIN_ID=8453",
		`FLOWOPS_BASE_RPC_PROVIDERS_JSON=[{"name":"rpc_alpha","url":"https://alpha.vendor.example/v1/top-secret-alpha"},{"name":"rpc_beta","url":"https://beta.vendor.example/v1/top-secret-beta"}]`,
		`FLOWOPS_BASE_RPC_ADMISSION_JSON={"schemaVersion":1,"providers":[{"name":"rpc_alpha","operator":"vendor_alpha","failureDomain":"vendor_alpha_global","serviceTier":"paid","productionEligible":true},{"name":"rpc_beta","operator":"vendor_beta","failureDomain":"vendor_beta_global","serviceTier":"paid","productionEligible":true}]}`,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("subprocess: %v: %s", err, output.String())
	}
	if strings.Contains(output.String(), "top-secret") || strings.Contains(output.String(), "vendor.example") {
		t.Fatalf("secret-bearing URL escaped: %s", output.String())
	}
}
