package main

import "testing"

func TestParseProviders(t *testing.T) {
	t.Parallel()
	providers, err := parseProviders([]string{"alpha=https://alpha.example/v1", "beta=https://beta.example/v1"})
	if err != nil || len(providers) != 2 || providers[1].Name != "beta" {
		t.Fatalf("parseProviders() = %+v, %v", providers, err)
	}
	if _, err := parseProviders([]string{"missing-separator"}); err == nil {
		t.Fatal("invalid provider succeeded")
	}
}
