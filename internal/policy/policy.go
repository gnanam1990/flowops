// Package policy implements FlowOps' deterministic authorization decision.
// It is intentionally pure: callers supply a spend snapshot and atomically
// reserve money only after this package returns a decision.
package policy

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

type Outcome string

const (
	Deny            Outcome = "DENY"
	RequireApproval Outcome = "REQUIRE_APPROVAL"
	AutoApprove     Outcome = "AUTO_APPROVE"
)

type Reason string

const (
	ReasonAllowed                Reason = "ALLOWED"
	ReasonPolicyDisabled         Reason = "POLICY_DISABLED"
	ReasonUnsupportedChain       Reason = "UNSUPPORTED_CHAIN"
	ReasonUnsupportedRail        Reason = "UNSUPPORTED_RAIL"
	ReasonAssetNotAllowed        Reason = "ASSET_NOT_ALLOWED"
	ReasonRecipientNotAllowed    Reason = "RECIPIENT_NOT_ALLOWED"
	ReasonBlockedCategory        Reason = "BLOCKED_CATEGORY"
	ReasonInvalidAmount          Reason = "INVALID_AMOUNT"
	ReasonPerActionLimit         Reason = "PER_ACTION_LIMIT_EXCEEDED"
	ReasonTaskBudget             Reason = "TASK_BUDGET_EXCEEDED"
	ReasonDailyBudget            Reason = "DAILY_BUDGET_EXCEEDED"
	ReasonHumanApprovalThreshold Reason = "HUMAN_APPROVAL_THRESHOLD"
	ReasonRailRequiresApproval   Reason = "RAIL_REQUIRES_APPROVAL"
)

type Config struct {
	Version                    string
	Enabled                    bool
	AllowedChainIDs            []uint64
	AllowedRails               []envelope.Rail
	AllowedAssets              []string
	AllowedRecipients          []string
	BlockedCategories          []string
	ApprovalRequiredRails      []envelope.Rail
	PerActionLimitAtomic       string
	AutoApproveThresholdAtomic string
	TaskBudgetAtomic           string
	DailyBudgetAtomic          string
}

type Intent struct {
	OrganizationID string
	CustomerID     string
	AgentID        string
	TaskID         string
	ActionID       string
	Rail           envelope.Rail
	ChainID        uint64
	Recipient      string
	Asset          string
	AmountAtomic   string
	Resource       string
	Category       string
}

type SpendSnapshot struct {
	TaskSpentAtomic     string
	TaskReservedAtomic  string
	DailySpentAtomic    string
	DailyReservedAtomic string
}

type Decision struct {
	Outcome       Outcome `json:"outcome"`
	Reason        Reason  `json:"reason"`
	PolicyVersion string  `json:"policyVersion"`
}

type Engine struct {
	version               string
	enabled               bool
	chains                map[uint64]struct{}
	rails                 map[envelope.Rail]struct{}
	assets                map[string]struct{}
	recipients            map[string]struct{}
	blockedCategories     map[string]struct{}
	approvalRequiredRails map[envelope.Rail]struct{}
	perActionLimit        *big.Int
	autoApproveThreshold  *big.Int
	taskBudget            *big.Int
	dailyBudget           *big.Int
}

func Compile(cfg Config) (*Engine, error) {
	if strings.TrimSpace(cfg.Version) == "" {
		return nil, errors.New("policy version is required")
	}
	perAction, err := parsePositive("per-action limit", cfg.PerActionLimitAtomic)
	if err != nil {
		return nil, err
	}
	autoApprove, err := parseNonNegative("auto-approve threshold", cfg.AutoApproveThresholdAtomic)
	if err != nil {
		return nil, err
	}
	taskBudget, err := parsePositive("task budget", cfg.TaskBudgetAtomic)
	if err != nil {
		return nil, err
	}
	dailyBudget, err := parsePositive("daily budget", cfg.DailyBudgetAtomic)
	if err != nil {
		return nil, err
	}
	if autoApprove.Cmp(perAction) > 0 {
		return nil, errors.New("auto-approve threshold cannot exceed per-action limit")
	}
	if perAction.Cmp(taskBudget) > 0 {
		return nil, errors.New("per-action limit cannot exceed task budget")
	}
	if taskBudget.Cmp(dailyBudget) > 0 {
		return nil, errors.New("task budget cannot exceed daily budget")
	}

	e := &Engine{
		version:               cfg.Version,
		enabled:               cfg.Enabled,
		chains:                make(map[uint64]struct{}, len(cfg.AllowedChainIDs)),
		rails:                 make(map[envelope.Rail]struct{}, len(cfg.AllowedRails)),
		assets:                make(map[string]struct{}, len(cfg.AllowedAssets)),
		recipients:            make(map[string]struct{}, len(cfg.AllowedRecipients)),
		blockedCategories:     make(map[string]struct{}, len(cfg.BlockedCategories)),
		approvalRequiredRails: make(map[envelope.Rail]struct{}, len(cfg.ApprovalRequiredRails)),
		perActionLimit:        perAction,
		autoApproveThreshold:  autoApprove,
		taskBudget:            taskBudget,
		dailyBudget:           dailyBudget,
	}
	for _, chainID := range cfg.AllowedChainIDs {
		if chainID == 0 {
			return nil, errors.New("allowed chain ID cannot be zero")
		}
		e.chains[chainID] = struct{}{}
	}
	for _, rail := range cfg.AllowedRails {
		if rail != envelope.RailX402 && rail != envelope.RailDirect && rail != envelope.RailEscrow {
			return nil, fmt.Errorf("unsupported configured rail %q", rail)
		}
		e.rails[rail] = struct{}{}
	}
	for _, rail := range cfg.ApprovalRequiredRails {
		if _, ok := e.rails[rail]; !ok {
			return nil, fmt.Errorf("approval-required rail %q is not allowed", rail)
		}
		e.approvalRequiredRails[rail] = struct{}{}
	}
	for _, asset := range cfg.AllowedAssets {
		canonical, err := canonicalAddress(asset)
		if err != nil {
			return nil, fmt.Errorf("allowed asset: %w", err)
		}
		e.assets[canonical] = struct{}{}
	}
	for _, recipient := range cfg.AllowedRecipients {
		canonical, err := canonicalAddress(recipient)
		if err != nil {
			return nil, fmt.Errorf("allowed recipient: %w", err)
		}
		e.recipients[canonical] = struct{}{}
	}
	for _, category := range cfg.BlockedCategories {
		category = strings.TrimSpace(strings.ToLower(category))
		if category == "" {
			return nil, errors.New("blocked category cannot be empty")
		}
		e.blockedCategories[category] = struct{}{}
	}
	if len(e.chains) == 0 || len(e.rails) == 0 || len(e.assets) == 0 || len(e.recipients) == 0 {
		return nil, errors.New("policy must allow at least one chain, rail, asset, and recipient")
	}
	return e, nil
}

func (e *Engine) Evaluate(intent Intent, spend SpendSnapshot) Decision {
	decision := func(outcome Outcome, reason Reason) Decision {
		return Decision{Outcome: outcome, Reason: reason, PolicyVersion: e.version}
	}
	if !e.enabled {
		return decision(Deny, ReasonPolicyDisabled)
	}
	if _, ok := e.chains[intent.ChainID]; !ok {
		return decision(Deny, ReasonUnsupportedChain)
	}
	if _, ok := e.rails[intent.Rail]; !ok {
		return decision(Deny, ReasonUnsupportedRail)
	}
	asset, err := canonicalAddress(intent.Asset)
	if err != nil {
		return decision(Deny, ReasonAssetNotAllowed)
	}
	if _, ok := e.assets[asset]; !ok {
		return decision(Deny, ReasonAssetNotAllowed)
	}
	recipient, err := canonicalAddress(intent.Recipient)
	if err != nil {
		return decision(Deny, ReasonRecipientNotAllowed)
	}
	if _, ok := e.recipients[recipient]; !ok {
		return decision(Deny, ReasonRecipientNotAllowed)
	}
	if _, blocked := e.blockedCategories[strings.TrimSpace(strings.ToLower(intent.Category))]; blocked {
		return decision(Deny, ReasonBlockedCategory)
	}
	amount, err := parsePositive("amount", intent.AmountAtomic)
	if err != nil {
		return decision(Deny, ReasonInvalidAmount)
	}
	if amount.Cmp(e.perActionLimit) > 0 {
		return decision(Deny, ReasonPerActionLimit)
	}
	taskTotal, ok := projectedTotal(amount, spend.TaskSpentAtomic, spend.TaskReservedAtomic)
	if !ok {
		return decision(Deny, ReasonInvalidAmount)
	}
	if taskTotal.Cmp(e.taskBudget) > 0 {
		return decision(Deny, ReasonTaskBudget)
	}
	dailyTotal, ok := projectedTotal(amount, spend.DailySpentAtomic, spend.DailyReservedAtomic)
	if !ok {
		return decision(Deny, ReasonInvalidAmount)
	}
	if dailyTotal.Cmp(e.dailyBudget) > 0 {
		return decision(Deny, ReasonDailyBudget)
	}
	if _, required := e.approvalRequiredRails[intent.Rail]; required {
		return decision(RequireApproval, ReasonRailRequiresApproval)
	}
	if amount.Cmp(e.autoApproveThreshold) > 0 {
		return decision(RequireApproval, ReasonHumanApprovalThreshold)
	}
	return decision(AutoApprove, ReasonAllowed)
}

func projectedTotal(amount *big.Int, values ...string) (*big.Int, bool) {
	total := new(big.Int).Set(amount)
	for _, value := range values {
		n, err := parseNonNegative("spend snapshot", value)
		if err != nil {
			return nil, false
		}
		total.Add(total, n)
		if total.BitLen() > 256 {
			return nil, false
		}
	}
	return total, true
}

func parsePositive(name, value string) (*big.Int, error) {
	n, err := parseNonNegative(name, value)
	if err != nil {
		return nil, err
	}
	if n.Sign() <= 0 {
		return nil, fmt.Errorf("%s must be positive", name)
	}
	return n, nil
}

func parseNonNegative(name, value string) (*big.Int, error) {
	if value == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	if len(value) > 1 && value[0] == '0' {
		return nil, fmt.Errorf("%s has a leading zero", name)
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, fmt.Errorf("%s must contain decimal digits only", name)
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return nil, fmt.Errorf("%s is outside uint256", name)
	}
	return n, nil
}

func canonicalAddress(value string) (string, error) {
	normalized, err := envelope.NormalizeAddress(value)
	if err != nil {
		return "", err
	}
	if value != normalized {
		return "", errors.New("address is not in canonical lowercase form")
	}
	return normalized, nil
}
