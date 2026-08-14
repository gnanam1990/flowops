// Package pilotlimits implements FlowOps' deployment-wide capped-pilot
// boundary. It deliberately uses canonical atomic integer amounts only.
package pilotlimits

import (
	"errors"
	"fmt"
	"math/big"
)

var (
	ErrPerActionExceeded   = errors.New("pilot per-action limit exceeded")
	ErrOutstandingExceeded = errors.New("pilot outstanding limit exceeded")
)

const (
	// Initial Base mainnet pilot profile: 1 USDC per action and 10 USDC of
	// conservative customer-signer exposure, expressed in native USDC units.
	BaseMainnetMaxPerActionAtomic   = "1000000"
	BaseMainnetMaxOutstandingAtomic = "10000000"
)

type Config struct {
	MaxPerActionAtomic   string `json:"maxPerActionAtomic"`
	MaxOutstandingAtomic string `json:"maxOutstandingAtomic"`
}

type Limits struct {
	maxPerAction   *big.Int
	maxOutstanding *big.Int
}

func Compile(cfg Config) (*Limits, error) {
	perAction, err := parsePositive("pilot per-action limit", cfg.MaxPerActionAtomic)
	if err != nil {
		return nil, err
	}
	outstanding, err := parsePositive("pilot outstanding limit", cfg.MaxOutstandingAtomic)
	if err != nil {
		return nil, err
	}
	if perAction.Cmp(outstanding) > 0 {
		return nil, errors.New("pilot per-action limit cannot exceed outstanding limit")
	}
	return &Limits{maxPerAction: perAction, maxOutstanding: outstanding}, nil
}

// Check refuses an action when either the action itself or the projected
// conservative outstanding exposure crosses the configured pilot ceiling.
func (l *Limits) Check(amountAtomic, outstandingAtomic string) error {
	if l == nil || l.maxPerAction == nil || l.maxOutstanding == nil {
		return errors.New("pilot limits are unavailable")
	}
	amount, err := parsePositive("amount", amountAtomic)
	if err != nil {
		return err
	}
	outstanding, err := parseNonNegative("outstanding amount", outstandingAtomic)
	if err != nil {
		return err
	}
	if amount.Cmp(l.maxPerAction) > 0 {
		return ErrPerActionExceeded
	}
	projected := new(big.Int).Add(outstanding, amount)
	if projected.BitLen() > 256 || projected.Cmp(l.maxOutstanding) > 0 {
		return ErrOutstandingExceeded
	}
	return nil
}

func (l *Limits) MaxPerActionAtomic() string {
	if l == nil || l.maxPerAction == nil {
		return ""
	}
	return l.maxPerAction.String()
}

func (l *Limits) MaxOutstandingAtomic() string {
	if l == nil || l.maxOutstanding == nil {
		return ""
	}
	return l.maxOutstanding.String()
}

func (l *Limits) RequireInitialBaseMainnetProfile() error {
	if l == nil || l.MaxPerActionAtomic() != BaseMainnetMaxPerActionAtomic || l.MaxOutstandingAtomic() != BaseMainnetMaxOutstandingAtomic {
		return errors.New("pilot limits do not match the initial Base mainnet profile")
	}
	return nil
}

func parsePositive(name, value string) (*big.Int, error) {
	n, err := parseNonNegative(name, value)
	if err != nil {
		return nil, err
	}
	if n.Sign() == 0 {
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
