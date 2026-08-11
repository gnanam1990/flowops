package policy

import (
	"strings"
	"testing"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	baseSepoliaUSDC = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	evidenceFetch   = "0x1111111111111111111111111111111111111111"
)

func testConfig() Config {
	return Config{
		Version:                    "policy_7",
		Enabled:                    true,
		AllowedChainIDs:            []uint64{84532},
		AllowedRails:               []envelope.Rail{envelope.RailX402, envelope.RailEscrow},
		AllowedAssets:              []string{baseSepoliaUSDC},
		AllowedRecipients:          []string{evidenceFetch},
		BlockedCategories:          []string{"gambling", "weapons"},
		ApprovalRequiredRails:      []envelope.Rail{envelope.RailEscrow},
		PerActionLimitAtomic:       "1000000",
		AutoApproveThresholdAtomic: "100000",
		TaskBudgetAtomic:           "5000000",
		DailyBudgetAtomic:          "10000000",
	}
}

func testIntent() Intent {
	return Intent{
		OrganizationID: "org_demo",
		CustomerID:     "cust_acme",
		AgentID:        "agent_research",
		TaskID:         "task_104",
		ActionID:       "action_fetch_1",
		Rail:           envelope.RailX402,
		ChainID:        84532,
		Recipient:      evidenceFetch,
		Asset:          baseSepoliaUSDC,
		AmountAtomic:   "100000",
		Resource:       "https://evidence.flowops.example/v1/fetch",
		Category:       "research_data",
	}
}

func emptySpend() SpendSnapshot {
	return SpendSnapshot{
		TaskSpentAtomic:     "0",
		TaskReservedAtomic:  "0",
		DailySpentAtomic:    "0",
		DailyReservedAtomic: "0",
	}
}

func TestEvaluateDeterministicOutcomes(t *testing.T) {
	engine, err := Compile(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Intent, *SpendSnapshot)
		want   Decision
	}{
		{name: "auto approve at threshold", mutate: func(*Intent, *SpendSnapshot) {}, want: Decision{AutoApprove, ReasonAllowed, "policy_7"}},
		{name: "human approval above threshold", mutate: func(i *Intent, _ *SpendSnapshot) { i.AmountAtomic = "100001" }, want: Decision{RequireApproval, ReasonHumanApprovalThreshold, "policy_7"}},
		{name: "escrow always requires approval", mutate: func(i *Intent, _ *SpendSnapshot) { i.Rail = envelope.RailEscrow }, want: Decision{RequireApproval, ReasonRailRequiresApproval, "policy_7"}},
		{name: "unknown chain", mutate: func(i *Intent, _ *SpendSnapshot) { i.ChainID = 8453 }, want: Decision{Deny, ReasonUnsupportedChain, "policy_7"}},
		{name: "unknown rail", mutate: func(i *Intent, _ *SpendSnapshot) { i.Rail = envelope.RailDirect }, want: Decision{Deny, ReasonUnsupportedRail, "policy_7"}},
		{name: "wrong asset", mutate: func(i *Intent, _ *SpendSnapshot) { i.Asset = "0x2222222222222222222222222222222222222222" }, want: Decision{Deny, ReasonAssetNotAllowed, "policy_7"}},
		{name: "noncanonical asset", mutate: func(i *Intent, _ *SpendSnapshot) { i.Asset = strings.ToUpper(i.Asset) }, want: Decision{Deny, ReasonAssetNotAllowed, "policy_7"}},
		{name: "wrong recipient", mutate: func(i *Intent, _ *SpendSnapshot) { i.Recipient = "0x2222222222222222222222222222222222222222" }, want: Decision{Deny, ReasonRecipientNotAllowed, "policy_7"}},
		{name: "blocked category", mutate: func(i *Intent, _ *SpendSnapshot) { i.Category = "GAMBLING" }, want: Decision{Deny, ReasonBlockedCategory, "policy_7"}},
		{name: "invalid amount", mutate: func(i *Intent, _ *SpendSnapshot) { i.AmountAtomic = "0100" }, want: Decision{Deny, ReasonInvalidAmount, "policy_7"}},
		{name: "per action exceeded", mutate: func(i *Intent, _ *SpendSnapshot) { i.AmountAtomic = "1000001" }, want: Decision{Deny, ReasonPerActionLimit, "policy_7"}},
		{name: "task reservations counted", mutate: func(_ *Intent, s *SpendSnapshot) { s.TaskSpentAtomic = "4800000"; s.TaskReservedAtomic = "100001" }, want: Decision{Deny, ReasonTaskBudget, "policy_7"}},
		{name: "daily reservations counted", mutate: func(_ *Intent, s *SpendSnapshot) { s.DailySpentAtomic = "9800000"; s.DailyReservedAtomic = "100001" }, want: Decision{Deny, ReasonDailyBudget, "policy_7"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intent, spend := testIntent(), emptySpend()
			tc.mutate(&intent, &spend)
			if got := engine.Evaluate(intent, spend); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPolicyDisabledFailsClosed(t *testing.T) {
	cfg := testConfig()
	cfg.Enabled = false
	engine, err := Compile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{Deny, ReasonPolicyDisabled, "policy_7"}
	if got := engine.Evaluate(testIntent(), emptySpend()); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCompileRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"missing version":               func(c *Config) { c.Version = "" },
		"zero chain":                    func(c *Config) { c.AllowedChainIDs = []uint64{0} },
		"unknown rail":                  func(c *Config) { c.AllowedRails = append(c.AllowedRails, "bridge") },
		"approval rail not allowed":     func(c *Config) { c.ApprovalRequiredRails = []envelope.Rail{envelope.RailDirect} },
		"noncanonical recipient":        func(c *Config) { c.AllowedRecipients = []string{strings.ToUpper(evidenceFetch)} },
		"auto approve above action cap": func(c *Config) { c.AutoApproveThresholdAtomic = "1000001" },
		"action cap above task budget":  func(c *Config) { c.PerActionLimitAtomic = "5000001" },
		"task above daily budget":       func(c *Config) { c.TaskBudgetAtomic = "10000001" },
		"empty allowlist":               func(c *Config) { c.AllowedRecipients = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			mutate(&cfg)
			if _, err := Compile(cfg); err == nil {
				t.Fatal("unsafe config compiled")
			}
		})
	}
}

func TestCompileCopiesConfiguration(t *testing.T) {
	cfg := testConfig()
	engine, err := Compile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AllowedRecipients[0] = "0x2222222222222222222222222222222222222222"
	if got := engine.Evaluate(testIntent(), emptySpend()); got.Outcome != AutoApprove {
		t.Fatalf("caller mutation changed compiled policy: %+v", got)
	}
}
