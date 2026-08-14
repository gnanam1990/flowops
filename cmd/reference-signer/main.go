package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/pilotlimits"
	"github.com/gnanam1990/flowops/pkg/referencesigner"
	"github.com/gnanam1990/flowops/pkg/referencewallet"
)

const (
	configVersion  = "flowops.reference-signer.v3"
	maxConfigBytes = 256 * 1024
	maxInputBytes  = 256 * 1024
)

type fileConfig struct {
	Version                   string                       `json:"version"`
	OrganizationID            string                       `json:"organizationId"`
	CustomerID                string                       `json:"customerId"`
	TrustKeys                 []trustKeyConfig             `json:"trustKeys"`
	ChainID                   uint64                       `json:"chainId"`
	Rail                      envelope.Rail                `json:"rail"`
	Asset                     string                       `json:"asset"`
	EscrowContract            string                       `json:"escrowContract,omitempty"`
	EscrowReleaseWindow       uint64                       `json:"escrowReleaseWindowSeconds,omitempty"`
	AllowedRecipients         []string                     `json:"allowedRecipients"`
	MaxAmountAtomic           string                       `json:"maxAmountAtomic"`
	MaxOutstandingAtomic      string                       `json:"maxOutstandingAtomic"`
	MaxTTLSeconds             int64                        `json:"maxTtlSeconds"`
	MaxFutureSkewSeconds      int64                        `json:"maxFutureSkewSeconds"`
	BaseRPCProviders          []reconciliation.RPCProvider `json:"baseRpcProviders"`
	PrimaryBaseRPC            string                       `json:"primaryBaseRpc"`
	ObserverQuorum            int                          `json:"observerQuorum"`
	MaxHeadSkew               uint64                       `json:"maxHeadSkew"`
	StallThresholdSeconds     int64                        `json:"stallThresholdSeconds"`
	MaxFutureBlockSkewSeconds int64                        `json:"maxFutureBlockSkewSeconds"`
	WalletRPCURL              string                       `json:"walletRpcUrl"`
	Sender                    string                       `json:"sender"`
	MaxGasLimit               uint64                       `json:"maxGasLimit"`
	MaxFeePerGasWei           string                       `json:"maxFeePerGasWei"`
	MaxPriorityFeePerGasWei   string                       `json:"maxPriorityFeePerGasWei"`
	NonceJournalPath          string                       `json:"nonceJournalPath"`
	AttemptJournalPath        string                       `json:"attemptJournalPath"`
	FreezeFilePath            string                       `json:"freezeFilePath"`
	ReceiptKeyID              string                       `json:"receiptKeyId"`
	ReceiptPrivateKeyPath     string                       `json:"receiptPrivateKeyPath"`
	ControlAPIURL             string                       `json:"controlApiUrl"`
	RequestTimeoutSeconds     int64                        `json:"requestTimeoutSeconds"`
}

type trustKeyConfig struct {
	KeyID        string `json:"keyId"`
	PublicKeyB64 string `json:"publicKeyB64"`
}

type runtime struct {
	executor *referencesigner.Executor
	nonces   *referencesigner.FileNonceStore
	attempts *referencesigner.AttemptJournal
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer, client *http.Client) error {
	if len(args) == 2 && args[0] == "init-receipt-key" {
		return initReceiptKey(args[1], output)
	}
	if len(args) != 2 || (args[0] != "validate-config" && args[0] != "execute" && args[0] != "resume") {
		return errors.New("usage: reference-signer validate-config|execute|resume CONFIG | init-receipt-key PATH")
	}
	config, err := loadConfig(args[1])
	if err != nil {
		return err
	}
	if args[0] == "validate-config" {
		if err := validateExternalFiles(config, client); err != nil {
			return err
		}
		return writeJSON(output, map[string]any{"valid": true, "version": config.Version, "chainId": config.ChainID, "rail": config.Rail, "sender": config.Sender})
	}
	runtime, err := buildRuntime(config, client)
	if err != nil {
		return err
	}
	defer runtime.Close()
	switch args[0] {
	case "execute":
		var signed envelope.SignedAuthorization
		if err := decodeStrict(input, maxInputBytes, &signed); err != nil {
			return errors.New("stdin must contain one strict signed-authorization JSON object")
		}
		attempt, executeErr := runtime.executor.Execute(ctx, signed)
		if attempt.State != "" {
			if err := writeJSON(output, summarize(attempt)); err != nil {
				return err
			}
		}
		return executeErr
	case "resume":
		attempts, resumeErr := runtime.executor.ResumePending(ctx)
		summaries := make([]attemptSummary, 0, len(attempts))
		for _, attempt := range attempts {
			summaries = append(summaries, summarize(attempt))
		}
		if err := writeJSON(output, map[string]any{"attempts": summaries}); err != nil {
			return err
		}
		return resumeErr
	default:
		return errors.New("unsupported command")
	}
}

type attemptSummary struct {
	AuthorizationID string                       `json:"authorizationId"`
	State           referencesigner.AttemptState `json:"state"`
	TransactionHash string                       `json:"transactionHash"`
	Sender          string                       `json:"sender"`
	Outcome         broadcastreceipt.Outcome     `json:"outcome,omitempty"`
	Registered      bool                         `json:"registered"`
}

func summarize(attempt referencesigner.Attempt) attemptSummary {
	result := attemptSummary{AuthorizationID: attempt.Authorization.Authorization.AuthorizationID, State: attempt.State, TransactionHash: attempt.Prepared.TransactionHash, Sender: attempt.Prepared.Sender, Registered: attempt.State == referencesigner.AttemptRegistered}
	if attempt.Receipt != nil {
		result.Outcome = attempt.Receipt.Receipt.Outcome
	}
	return result
}

func buildRuntime(config fileConfig, client *http.Client) (*runtime, error) {
	trustKeys, receiptKey, freeze, observers, wallet, registration, err := buildDependencies(config, client)
	if err != nil {
		return nil, err
	}
	nonces, err := referencesigner.OpenFileNonceStore(config.NonceJournalPath)
	if err != nil {
		return nil, err
	}
	attempts, err := referencesigner.OpenAttemptJournal(config.AttemptJournalPath)
	if err != nil {
		_ = nonces.Close()
		return nil, err
	}
	gate, err := referencesigner.NewQuorumChainGate(referencesigner.QuorumChainGateConfig{
		ChainID: config.ChainID, Source: observers, Quorum: config.ObserverQuorum, MaxHeadSkew: config.MaxHeadSkew,
		StallThreshold: time.Duration(config.StallThresholdSeconds) * time.Second,
		MaxFutureSkew:  time.Duration(config.MaxFutureBlockSkewSeconds) * time.Second,
	})
	if err != nil {
		_ = attempts.Close()
		_ = nonces.Close()
		return nil, err
	}
	verifier, err := referencesigner.New(referencesigner.Config{
		OrganizationID: config.OrganizationID, CustomerID: config.CustomerID, TrustKeys: trustKeys,
		AllowedChainIDs: []uint64{config.ChainID}, AllowedRails: []envelope.Rail{config.Rail},
		AllowedAssets: []string{config.Asset}, AllowedRecipients: config.AllowedRecipients,
		MaxAmountAtomic: config.MaxAmountAtomic, MaxTTL: time.Duration(config.MaxTTLSeconds) * time.Second,
		MaxFutureSkew: time.Duration(config.MaxFutureSkewSeconds) * time.Second,
		ChainGate:     gate, FreezeGate: freeze, Nonces: nonces,
	})
	if err != nil {
		_ = attempts.Close()
		_ = nonces.Close()
		return nil, err
	}
	pilot, err := pilotlimits.Compile(pilotlimits.Config{MaxPerActionAtomic: config.MaxAmountAtomic, MaxOutstandingAtomic: config.MaxOutstandingAtomic})
	if err != nil {
		_ = attempts.Close()
		_ = nonces.Close()
		return nil, err
	}
	executor, err := referencesigner.NewExecutor(referencesigner.ExecutorConfig{
		Verifier: verifier, Wallet: wallet, Registration: registration, Journal: attempts,
		ReceiptKeyID: config.ReceiptKeyID, ReceiptPrivateKey: receiptKey,
		PilotLimits: pilot,
	})
	if err != nil {
		_ = attempts.Close()
		_ = nonces.Close()
		return nil, err
	}
	return &runtime{executor: executor, nonces: nonces, attempts: attempts}, nil
}

func validateExternalFiles(config fileConfig, client *http.Client) error {
	_, _, _, observers, _, _, err := buildDependencies(config, client)
	if err != nil {
		return err
	}
	if _, err := referencesigner.NewQuorumChainGate(referencesigner.QuorumChainGateConfig{
		ChainID: config.ChainID, Source: observers, Quorum: config.ObserverQuorum, MaxHeadSkew: config.MaxHeadSkew,
		StallThreshold: time.Duration(config.StallThresholdSeconds) * time.Second,
		MaxFutureSkew:  time.Duration(config.MaxFutureBlockSkewSeconds) * time.Second,
	}); err != nil {
		return err
	}
	if _, err := pilotlimits.Compile(pilotlimits.Config{MaxPerActionAtomic: config.MaxAmountAtomic, MaxOutstandingAtomic: config.MaxOutstandingAtomic}); err != nil {
		return err
	}
	if config.NonceJournalPath == config.AttemptJournalPath {
		return errors.New("nonce and attempt journals must use different files")
	}
	for _, path := range []string{config.NonceJournalPath, config.AttemptJournalPath} {
		if strings.TrimSpace(path) == "" {
			return errors.New("journal paths are required")
		}
		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			return errors.New("journal parent directory is unavailable")
		}
	}
	return nil
}

func buildDependencies(config fileConfig, client *http.Client) (map[string]ed25519.PublicKey, ed25519.PrivateKey, *referencesigner.FileFreezeGate, *reconciliation.ObserverSet, referencesigner.WalletAdapter, referencesigner.RegistrationSink, error) {
	trustKeys, err := decodeTrustKeys(config.TrustKeys)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	receiptKey, err := loadReceiptKey(config.ReceiptPrivateKeyPath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	freeze, err := referencesigner.NewFileFreezeGate(config.FreezeFilePath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	observers, err := reconciliation.NewObserverSet(config.ChainID, config.BaseRPCProviders, client, nil)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	primaryURL := ""
	for _, provider := range config.BaseRPCProviders {
		if provider.Name == config.PrimaryBaseRPC {
			primaryURL = provider.URL
		}
	}
	if primaryURL == "" {
		return nil, nil, nil, nil, nil, nil, errors.New("primary Base RPC must name a configured observer")
	}
	timeout := time.Duration(config.RequestTimeoutSeconds) * time.Second
	var wallet referencesigner.WalletAdapter
	var registration referencesigner.RegistrationSink
	if config.Rail == envelope.RailDirect {
		wallet, err = referencewallet.NewClefAdapter(referencewallet.ClefConfig{
			ChainID: config.ChainID, Sender: config.Sender, Asset: config.Asset, BaseRPCURL: primaryURL,
			WalletRPCURL: config.WalletRPCURL, MaxGasLimit: config.MaxGasLimit,
			MaxFeePerGasWei: config.MaxFeePerGasWei, MaxPriorityFeePerGasWei: config.MaxPriorityFeePerGasWei,
			RequestTimeout: timeout, HTTPClient: client,
		})
	} else {
		wallet, err = referencewallet.NewEscrowClefAdapter(referencewallet.EscrowClefConfig{
			ChainID: config.ChainID, Sender: config.Sender, Asset: config.Asset, Contract: config.EscrowContract,
			ReleaseWindow: config.EscrowReleaseWindow, BaseRPCURL: primaryURL, WalletRPCURL: config.WalletRPCURL,
			MaxGasLimit: config.MaxGasLimit, MaxFeePerGasWei: config.MaxFeePerGasWei,
			MaxPriorityFeePerGasWei: config.MaxPriorityFeePerGasWei, RequestTimeout: timeout, HTTPClient: client,
		})
	}
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if config.Rail == envelope.RailDirect {
		registration, err = referencesigner.NewHTTPRegistrationSink(config.ControlAPIURL, client)
	} else {
		registration, err = referencesigner.NewHTTPEscrowRegistrationSink(config.ControlAPIURL, client)
	}
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	return trustKeys, receiptKey, freeze, observers, wallet, registration, nil
}

func (r *runtime) Close() error {
	return errors.Join(r.attempts.Close(), r.nonces.Close())
}

func loadConfig(path string) (fileConfig, error) {
	raw, err := readPrivateFile(path, maxConfigBytes)
	if err != nil {
		return fileConfig{}, fmt.Errorf("reference signer config: %w", err)
	}
	var config fileConfig
	if err := decodeStrict(bytes.NewReader(raw), maxConfigBytes, &config); err != nil {
		return fileConfig{}, errors.New("reference signer config must be one strict JSON object")
	}
	if config.Version != configVersion || !envelope.ValidIdentifier(config.OrganizationID) || !envelope.ValidIdentifier(config.CustomerID) || !envelope.ValidIdentifier(config.ReceiptKeyID) {
		return fileConfig{}, errors.New("reference signer identity or config version is invalid")
	}
	if config.Rail != envelope.RailDirect && config.Rail != envelope.RailEscrow {
		return fileConfig{}, errors.New("reference signer rail must be direct_usdc or escrow")
	}
	if config.Rail == envelope.RailDirect && (config.EscrowContract != "" || config.EscrowReleaseWindow != 0) {
		return fileConfig{}, errors.New("direct_usdc config must not contain escrow deployment fields")
	}
	if config.Rail == envelope.RailEscrow && (config.EscrowContract == "" || config.EscrowReleaseWindow == 0) {
		return fileConfig{}, errors.New("escrow config requires the reviewed contract and release window")
	}
	if config.MaxTTLSeconds <= 0 || config.MaxTTLSeconds > 3600 || config.MaxFutureSkewSeconds < 0 || config.MaxFutureSkewSeconds > 300 || config.StallThresholdSeconds <= 0 || config.StallThresholdSeconds > 3600 || config.MaxFutureBlockSkewSeconds < 0 || config.MaxFutureBlockSkewSeconds > 300 || config.RequestTimeoutSeconds <= 0 || config.RequestTimeoutSeconds > 60 {
		return fileConfig{}, errors.New("reference signer duration is outside its safe range")
	}
	if config.ObserverQuorum < 2 || config.ObserverQuorum > 5 || config.MaxHeadSkew > 32 || config.NonceJournalPath == config.AttemptJournalPath {
		return fileConfig{}, errors.New("reference signer observer or journal configuration is invalid")
	}
	if _, err := pilotlimits.Compile(pilotlimits.Config{MaxPerActionAtomic: config.MaxAmountAtomic, MaxOutstandingAtomic: config.MaxOutstandingAtomic}); err != nil {
		return fileConfig{}, fmt.Errorf("reference signer pilot limits: %w", err)
	}
	if config.ChainID == 8453 {
		limits, _ := pilotlimits.Compile(pilotlimits.Config{MaxPerActionAtomic: config.MaxAmountAtomic, MaxOutstandingAtomic: config.MaxOutstandingAtomic})
		if err := limits.RequireInitialBaseMainnetProfile(); err != nil {
			return fileConfig{}, err
		}
	}
	return config, nil
}

func decodeTrustKeys(values []trustKeyConfig) (map[string]ed25519.PublicKey, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one FlowOps trust key is required")
	}
	keys := make(map[string]ed25519.PublicKey, len(values))
	for _, value := range values {
		decoded, err := base64.StdEncoding.DecodeString(value.PublicKeyB64)
		if err != nil || len(decoded) != ed25519.PublicKeySize || value.PublicKeyB64 != base64.StdEncoding.EncodeToString(decoded) || !envelope.ValidIdentifier(value.KeyID) {
			return nil, errors.New("FlowOps trust key is invalid")
		}
		if _, exists := keys[value.KeyID]; exists {
			return nil, errors.New("FlowOps trust key ID is duplicated")
		}
		keys[value.KeyID] = ed25519.PublicKey(decoded)
	}
	return keys, nil
}

func loadReceiptKey(path string) (ed25519.PrivateKey, error) {
	raw, err := readPrivateFile(path, 1024)
	if err != nil {
		return nil, fmt.Errorf("receipt attestation key: %w", err)
	}
	encoded := strings.TrimSpace(string(raw))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PrivateKeySize || encoded != base64.StdEncoding.EncodeToString(decoded) {
		return nil, errors.New("receipt attestation key is invalid")
	}
	canonical := ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])
	if !bytes.Equal(decoded, canonical) {
		return nil, errors.New("receipt attestation key is not canonical")
	}
	return ed25519.PrivateKey(decoded), nil
}

func initReceiptKey(path string, output io.Writer) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("receipt key path is required")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate receipt attestation key")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("create receipt attestation key without overwrite")
	}
	encoded := base64.StdEncoding.EncodeToString(privateKey) + "\n"
	_, writeErr := io.WriteString(file, encoded)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.New("persist receipt attestation key")
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return errors.New("open receipt key directory")
	}
	syncErr = directory.Sync()
	closeErr = directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return errors.New("sync receipt key directory")
	}
	return writeJSON(output, map[string]any{"created": true, "publicKeyB64": base64.StdEncoding.EncodeToString(publicKey)})
}

func readPrivateFile(path string, limit int64) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("path is required")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("private file is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("file must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.New("private file is empty, unreadable, or too large")
	}
	return raw, nil
}

func decodeStrict(input io.Reader, limit int64, target any) error {
	raw, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 || int64(len(raw)) > limit {
		return errors.New("invalid JSON input")
	}
	if err := rejectDuplicateFields(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func rejectDuplicateFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
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
				return errors.New("object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON field")
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(value)
}
