package pilotlimits

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"
)

func TestLimitsCheckExactBoundaries(t *testing.T) {
	limits, err := Compile(Config{MaxPerActionAtomic: "1000000", MaxOutstandingAtomic: "10000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := limits.Check("1000000", "9000000"); err != nil {
		t.Fatalf("exact limits rejected: %v", err)
	}
	if err := limits.Check("1000001", "0"); !errors.Is(err, ErrPerActionExceeded) {
		t.Fatalf("per-action overflow = %v", err)
	}
	if err := limits.Check("1", "10000000"); !errors.Is(err, ErrOutstandingExceeded) {
		t.Fatalf("outstanding overflow = %v", err)
	}
}

func TestLimitsRejectMalformedAndOverflowValues(t *testing.T) {
	for name, cfg := range map[string]Config{
		"missing":      {},
		"zero action":  {MaxPerActionAtomic: "0", MaxOutstandingAtomic: "1"},
		"leading zero": {MaxPerActionAtomic: "01", MaxOutstandingAtomic: "10"},
		"inverted":     {MaxPerActionAtomic: "11", MaxOutstandingAtomic: "10"},
		"outside uint": {MaxPerActionAtomic: "1", MaxOutstandingAtomic: new(big.Int).Lsh(big.NewInt(1), 256).String()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(cfg); err == nil {
				t.Fatal("unsafe pilot limits accepted")
			}
		})
	}
	limits, _ := Compile(Config{MaxPerActionAtomic: "10", MaxOutstandingAtomic: "20"})
	for _, value := range []string{"", "-1", "01", "1.0"} {
		if err := limits.Check("1", value); err == nil {
			t.Fatalf("invalid outstanding %q accepted", value)
		}
	}
}

func TestInitialBaseMainnetProfileIsExact(t *testing.T) {
	limits, err := Compile(Config{MaxPerActionAtomic: BaseMainnetMaxPerActionAtomic, MaxOutstandingAtomic: BaseMainnetMaxOutstandingAtomic})
	if err != nil || limits.RequireInitialBaseMainnetProfile() != nil {
		t.Fatalf("canonical profile = %v, %v", limits, err)
	}
	wrong, _ := Compile(Config{MaxPerActionAtomic: "1000000", MaxOutstandingAtomic: "10000001"})
	if wrong.RequireInitialBaseMainnetProfile() == nil {
		t.Fatal("changed Base mainnet profile accepted")
	}
}

func TestBaseMainnetReadinessRecordMatchesProfile(t *testing.T) {
	raw, err := os.ReadFile("../../deployments/base-mainnet-readiness.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Pilot struct {
			ProfileSelected         bool   `json:"profileSelected"`
			LimitsEnforced          bool   `json:"limitsEnforced"`
			MaximumPerCallUSDC      string `json:"maximumPerCallUsdc"`
			MaximumOutstandingUSDC  string `json:"maximumOutstandingUsdc"`
			SignerAccountingPosture string `json:"signerAccountingPosture"`
			ExposureScope           string `json:"exposureScope"`
			ControlPlaneEnforced    bool   `json:"controlPlaneEnforced"`
			DirectSignerEnforced    bool   `json:"directUsdcSignerEnforced"`
			EscrowSignerEnforced    bool   `json:"escrowSignerEnforced"`
		} `json:"pilot"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if !record.Pilot.ProfileSelected || record.Pilot.LimitsEnforced || record.Pilot.MaximumPerCallUSDC != "1.000000" ||
		record.Pilot.MaximumOutstandingUSDC != "10.000000" || record.Pilot.ExposureScope != "per-customer-signer" ||
		record.Pilot.SignerAccountingPosture != "conservative-lifetime-reservation" || !record.Pilot.ControlPlaneEnforced ||
		!record.Pilot.DirectSignerEnforced || !record.Pilot.EscrowSignerEnforced {
		t.Fatalf("readiness pilot profile drifted: %+v", record.Pilot)
	}
}
