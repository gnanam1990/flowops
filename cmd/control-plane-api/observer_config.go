package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

const maxObserverProvidersJSONBytes = 16 * 1024

type observerRuntimeConfig struct {
	providers []reconciliation.RPCProvider
	engine    reconciliation.Config
	interval  time.Duration
	timeout   time.Duration
}

type observerProviderInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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
	if chainID != 84532 {
		return observerRuntimeConfig{}, errors.New("FLOWOPS_BASE_CHAIN_ID must remain 84532 until the separate Base mainnet gate is approved")
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
		engine: reconciliation.Config{
			ChainID: chainID, ObserverQuorum: observerQuorum, HaltConfirmations: haltConfirmations,
			RecoveryObservations: recoveryObservations, MinConfirmations: minConfirmations,
			ReorgLookback: reorgLookback, MaxHeadSkew: maxHeadSkew, StallThreshold: stallThreshold,
			ObservationMaxAge: observationMaxAge, MaxFutureClockSkew: maxFutureClockSkew,
		},
	}, nil
}

func parseObserverProviders(raw string) ([]reconciliation.RPCProvider, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON is required")
	}
	if len(raw) > maxObserverProvidersJSONBytes {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON exceeds 16 KiB")
	}
	if err := rejectDuplicateJSONFields([]byte(raw)); err != nil {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON must not contain duplicate object fields")
	}
	if err := rejectNonCanonicalProviderFields([]byte(raw)); err != nil {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON provider fields must be exactly name and url")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var inputs []observerProviderInput
	if err := decoder.Decode(&inputs); err != nil {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON must be a strict provider array")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("FLOWOPS_BASE_RPC_PROVIDERS_JSON must contain one JSON value")
	}
	providers := make([]reconciliation.RPCProvider, len(inputs))
	for index, input := range inputs {
		providers[index] = reconciliation.RPCProvider{Name: strings.TrimSpace(input.Name), URL: strings.TrimSpace(input.URL)}
	}
	if _, err := reconciliation.NewObserverSet(84532, providers, nil, nil); err != nil {
		return nil, fmt.Errorf("FLOWOPS_BASE_RPC_PROVIDERS_JSON: %w", err)
	}
	return providers, nil
}

func rejectNonCanonicalProviderFields(raw []byte) error {
	var providers []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &providers); err != nil {
		return err
	}
	for _, provider := range providers {
		for field := range provider {
			if field != "name" && field != "url" {
				return errors.New("non-canonical provider field")
			}
		}
	}
	return nil
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate object field")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("invalid object closing token")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("invalid array closing token")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
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
