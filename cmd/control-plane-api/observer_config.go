package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/internal/rpcadmission"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

const maxObserverProvidersJSONBytes = rpcadmission.MaxJSONBytes

type observerRuntimeConfig struct {
	providers              []reconciliation.RPCProvider
	engine                 reconciliation.Config
	interval               time.Duration
	timeout                time.Duration
	reconciliationInterval time.Duration
	reconciliationTimeout  time.Duration
}

func loadObserverRuntimeConfig() (observerRuntimeConfig, error) {
	providers, err := parseObserverProviders(os.Getenv("FLOWOPS_BASE_RPC_PROVIDERS_JSON"))
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	chainID, err := parseUintEnv("FLOWOPS_BASE_CHAIN_ID", os.Getenv("FLOWOPS_BASE_CHAIN_ID"), 84532)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	admissionRaw := strings.TrimSpace(os.Getenv("FLOWOPS_BASE_RPC_ADMISSION_JSON"))
	if chainID == 8453 {
		admission, err := rpcadmission.DecodeProductionAdmission(admissionRaw)
		if err != nil {
			return observerRuntimeConfig{}, err
		}
		if err := rpcadmission.ValidateProduction(providers, admission); err != nil {
			return observerRuntimeConfig{}, fmt.Errorf("Base mainnet RPC admission: %w", err)
		}
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_CHAIN_ID must remain 84532 until the separate Base mainnet gate is approved")
	}
	if chainID != 84532 {
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_CHAIN_ID supports Base Sepolia or Base mainnet only")
	}
	if admissionRaw != "" {
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_RPC_ADMISSION_JSON must be unset for Base Sepolia")
	}
	escrowContract, escrowAsset, escrowReleaseWindow, err := parseEscrowDeployment()
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	if _, err := reconciliation.NewObserverSet(chainID, providers, nil, nil); err != nil {
		return observerRuntimeConfig{}, fmt.Errorf("Base observer configuration: %w", err)
	}
	observerQuorum, err := parseIntEnv("FLOWOPS_BASE_OBSERVER_QUORUM", os.Getenv("FLOWOPS_BASE_OBSERVER_QUORUM"), 2)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	haltConfirmations, err := parseIntEnv("FLOWOPS_BASE_HALT_CONFIRMATIONS", os.Getenv("FLOWOPS_BASE_HALT_CONFIRMATIONS"), 2)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	recoveryObservations, err := parseIntEnv("FLOWOPS_BASE_RECOVERY_OBSERVATIONS", os.Getenv("FLOWOPS_BASE_RECOVERY_OBSERVATIONS"), 3)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	minConfirmations, err := parseUintEnv("FLOWOPS_BASE_MIN_CONFIRMATIONS", os.Getenv("FLOWOPS_BASE_MIN_CONFIRMATIONS"), 2)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	reorgLookback, err := parseUintEnv("FLOWOPS_BASE_REORG_LOOKBACK", os.Getenv("FLOWOPS_BASE_REORG_LOOKBACK"), 12)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	maxHeadSkew, err := parseUintEnv("FLOWOPS_BASE_MAX_HEAD_SKEW", os.Getenv("FLOWOPS_BASE_MAX_HEAD_SKEW"), 2)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	interval, err := parseDurationEnv("FLOWOPS_BASE_OBSERVER_INTERVAL", os.Getenv("FLOWOPS_BASE_OBSERVER_INTERVAL"), 15*time.Second)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	timeout, err := parseDurationEnv("FLOWOPS_BASE_OBSERVER_TIMEOUT", os.Getenv("FLOWOPS_BASE_OBSERVER_TIMEOUT"), 10*time.Second)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	reconciliationInterval, err := parseDurationEnv("FLOWOPS_BASE_RECONCILIATION_INTERVAL", os.Getenv("FLOWOPS_BASE_RECONCILIATION_INTERVAL"), 20*time.Second)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	reconciliationTimeout, err := parseDurationEnv("FLOWOPS_BASE_RECONCILIATION_TIMEOUT", os.Getenv("FLOWOPS_BASE_RECONCILIATION_TIMEOUT"), 10*time.Second)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	stallThreshold, err := parseDurationEnv("FLOWOPS_BASE_STALL_THRESHOLD", os.Getenv("FLOWOPS_BASE_STALL_THRESHOLD"), 2*time.Minute)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	observationMaxAge, err := parseDurationEnv("FLOWOPS_BASE_OBSERVATION_MAX_AGE", os.Getenv("FLOWOPS_BASE_OBSERVATION_MAX_AGE"), 45*time.Second)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	maxFutureClockSkew, err := parseDurationEnv("FLOWOPS_BASE_MAX_FUTURE_CLOCK_SKEW", os.Getenv("FLOWOPS_BASE_MAX_FUTURE_CLOCK_SKEW"), 15*time.Second)
	if err != nil {
		return observerRuntimeConfig{}, err
	}
	if timeout >= interval {
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_OBSERVER_TIMEOUT must be shorter than FLOWOPS_BASE_OBSERVER_INTERVAL")
	}
	if reconciliationTimeout >= reconciliationInterval {
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_RECONCILIATION_TIMEOUT must be shorter than FLOWOPS_BASE_RECONCILIATION_INTERVAL")
	}
	if observerQuorum > len(providers) {
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_OBSERVER_QUORUM cannot exceed configured provider count")
	}
	if observerQuorum < 2 || observerQuorum > 5 || haltConfirmations < 1 || recoveryObservations < 1 {
		return observerRuntimeConfig{}, errors.New("Base observer transition thresholds are invalid")
	}
	if interval >= observationMaxAge || interval >= stallThreshold {
		return observerRuntimeConfig{}, errors.New("observer interval must be shorter than observation max age and stall threshold")
	}
	return observerRuntimeConfig{
		providers: providers, interval: interval, timeout: timeout,
		reconciliationInterval: reconciliationInterval, reconciliationTimeout: reconciliationTimeout,
		engine: reconciliation.Config{
			ChainID: chainID, EscrowContract: escrowContract, EscrowAsset: escrowAsset, EscrowReleaseWindow: escrowReleaseWindow,
			ObserverQuorum: observerQuorum, HaltConfirmations: haltConfirmations,
			RecoveryObservations: recoveryObservations, MinConfirmations: minConfirmations,
			ReorgLookback: reorgLookback, MaxHeadSkew: maxHeadSkew, StallThreshold: stallThreshold,
			ObservationMaxAge: observationMaxAge, MaxFutureClockSkew: maxFutureClockSkew,
		},
	}, nil
}

func parseEscrowDeployment() (string, string, uint64, error) {
	contract := strings.TrimSpace(os.Getenv("FLOWOPS_ESCROW_CONTRACT"))
	asset := strings.TrimSpace(os.Getenv("FLOWOPS_ESCROW_ASSET"))
	releaseRaw := strings.TrimSpace(os.Getenv("FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS"))
	if contract == "" && asset == "" && releaseRaw == "" {
		return "", "", 0, nil
	}
	if contract == "" || asset == "" || releaseRaw == "" {
		return "", "", 0, errors.New("FLOWOPS_ESCROW_CONTRACT, FLOWOPS_ESCROW_ASSET, and FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS must be configured together")
	}
	normalizedContract, contractErr := envelope.NormalizeAddress(contract)
	normalizedAsset, assetErr := envelope.NormalizeAddress(asset)
	releaseWindow, releaseErr := strconv.ParseUint(releaseRaw, 10, 64)
	if contractErr != nil || assetErr != nil || normalizedContract != contract || normalizedAsset != asset || contract == asset || releaseErr != nil || releaseWindow == 0 || releaseWindow > 30*24*60*60 {
		return "", "", 0, errors.New("escrow deployment tuple must contain distinct canonical lowercase addresses and a 1-second to 30-day release window")
	}
	return contract, asset, releaseWindow, nil
}

func parseObserverProviders(raw string) ([]reconciliation.RPCProvider, error) {
	return rpcadmission.DecodeProviders(raw)
}

func rejectDuplicateJSONFields(raw []byte) error {
	return rpcadmission.RejectDuplicateJSONFields(raw)
}

func parseIntEnv(name, raw string, defaultValue int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseUint(raw, 10, 31)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive decimal integer", name)
	}
	return int(value), nil
}

func parseUintEnv(name, raw string, defaultValue uint64) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive decimal integer", name)
	}
	return value, nil
}

func parseDurationEnv(name, raw string, defaultValue time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return value, nil
}
