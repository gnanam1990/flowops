// Package releaseadmission authenticates the exact, reviewed Base mainnet
// release tuple before any FlowOps production runtime can start. It is an
// admission boundary, not deployment evidence: the runtime must separately
// prove the declared bytecode through the admitted observer quorum.
package releaseadmission

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/rpcadmission"
)

const (
	MaxJSONBytes  = 64 * 1024
	signingDomain = "flowops:base-mainnet-release:v1\n"

	BaseMainnetChainID = uint64(8453)
	BaseMainnetNetwork = "base-mainnet"
	BaseMainnetUSDC    = "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
	USDCDecimals       = uint8(6)

	InitialMaxPerActionAtomic   = "1000000"
	InitialMaxOutstandingAtomic = "10000000"
	TypedDataManifestSHA256     = "0x87eee19267c1684f91e10454a8f1a26880a2434e65f5609791c54b803154bff5"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
	hexDigestPattern  = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type ContractBinding struct {
	Name            string `json:"name"`
	Address         string `json:"address"`
	RuntimeCodeHash string `json:"runtimeCodeHash"`
	DeploymentTx    string `json:"deploymentTx"`
	DeploymentBlock uint64 `json:"deploymentBlock"`
	SourceVerified  bool   `json:"sourceVerified"`
}

type SafeBinding struct {
	Address   string   `json:"address"`
	Owners    []string `json:"owners"`
	Threshold uint8    `json:"threshold"`
}

type AuthorityBinding struct {
	Governor           string `json:"governor"`
	DirectoryPublisher string `json:"directoryPublisher"`
	DirectoryPauser    string `json:"directoryPauser"`
	RegistryAdmin      string `json:"registryAdmin"`
	SpendAuthorizer    string `json:"spendAuthorizer"`
}

type AssetBinding struct {
	Address         string `json:"address"`
	Symbol          string `json:"symbol"`
	Decimals        uint8  `json:"decimals"`
	RuntimeCodeHash string `json:"runtimeCodeHash"`
}

type PilotBinding struct {
	MaxPerActionAtomic   string `json:"maxPerActionAtomic"`
	MaxOutstandingAtomic string `json:"maxOutstandingAtomic"`
	FundingEnabled       bool   `json:"fundingEnabled"`
	FundedEvidenceSHA256 string `json:"fundedEvidenceSha256,omitempty"`
}

type ObserverBinding struct {
	Quorum                        int    `json:"quorum"`
	HaltConfirmations             int    `json:"haltConfirmations"`
	RecoveryObservations          int    `json:"recoveryObservations"`
	MinConfirmations              uint64 `json:"minConfirmations"`
	ReorgLookback                 uint64 `json:"reorgLookback"`
	MaxHeadSkew                   uint64 `json:"maxHeadSkew"`
	ObserverIntervalSeconds       uint64 `json:"observerIntervalSeconds"`
	ObserverTimeoutSeconds        uint64 `json:"observerTimeoutSeconds"`
	ReconciliationIntervalSeconds uint64 `json:"reconciliationIntervalSeconds"`
	ReconciliationTimeoutSeconds  uint64 `json:"reconciliationTimeoutSeconds"`
	StallThresholdSeconds         uint64 `json:"stallThresholdSeconds"`
	ObservationMaxAgeSeconds      uint64 `json:"observationMaxAgeSeconds"`
	MaxFutureClockSkewSeconds     uint64 `json:"maxFutureClockSkewSeconds"`
}

func InitialObserverProfile() ObserverBinding {
	return ObserverBinding{
		Quorum: 2, HaltConfirmations: 2, RecoveryObservations: 3,
		MinConfirmations: 2, ReorgLookback: 12, MaxHeadSkew: 2,
		ObserverIntervalSeconds: 15, ObserverTimeoutSeconds: 10,
		ReconciliationIntervalSeconds: 20, ReconciliationTimeoutSeconds: 10,
		StallThresholdSeconds: 120, ObservationMaxAgeSeconds: 45, MaxFutureClockSkewSeconds: 15,
	}
}

type Manifest struct {
	SchemaVersion           int               `json:"schemaVersion"`
	ReleaseID               string            `json:"releaseId"`
	Network                 string            `json:"network"`
	ChainID                 uint64            `json:"chainId"`
	SourceCommit            string            `json:"sourceCommit"`
	TypedDataManifestSHA256 string            `json:"typedDataManifestSha256"`
	ExternalReviewSHA256    string            `json:"externalReviewSha256"`
	RPCAdmissionSHA256      string            `json:"rpcAdmissionSha256"`
	GovernanceFromBlock     uint64            `json:"governanceFromBlock"`
	SettlementWindowSeconds uint64            `json:"settlementWindowSeconds"`
	ReviewedAt              time.Time         `json:"reviewedAt"`
	ExpiresAt               time.Time         `json:"expiresAt"`
	RuntimeEnabled          bool              `json:"runtimeEnabled"`
	Asset                   AssetBinding      `json:"asset"`
	Contracts               []ContractBinding `json:"contracts"`
	Deployer                string            `json:"deployer"`
	Safe                    SafeBinding       `json:"safe"`
	Authorities             AuthorityBinding  `json:"authorities"`
	Pilot                   PilotBinding      `json:"pilot"`
	Observer                ObserverBinding   `json:"observer"`
	SignerKeyID             string            `json:"signerKeyId"`
	Signature               string            `json:"signature"`
}

type RuntimeBindings struct {
	EscrowAsset             string
	DirectoryContract       string
	AgentRegistry           string
	CallEscrow              string
	SpendModule             string
	PilotPerAction          string
	PilotOutstanding        string
	GovernanceFromBlock     uint64
	SettlementWindowSeconds uint64
}

type ObserverRuntimeBindings struct {
	EscrowAsset, CallEscrow                       string
	SettlementWindowSeconds                       uint64
	Quorum, HaltConfirmations                     int
	RecoveryObservations                          int
	MinConfirmations, ReorgLookback               uint64
	MaxHeadSkew                                   uint64
	ObserverInterval, ObserverTimeout             time.Duration
	ReconciliationInterval, ReconciliationTimeout time.Duration
	StallThreshold, ObservationMaxAge             time.Duration
	MaxFutureClockSkew                            time.Duration
}

func Decode(raw string) (Manifest, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Manifest{}, errors.New("FLOWOPS_BASE_MAINNET_RELEASE_MANIFEST_JSON is required for Base mainnet")
	}
	if len(raw) > MaxJSONBytes {
		return Manifest{}, errors.New("Base mainnet release manifest exceeds 64 KiB")
	}
	if err := rpcadmission.RejectDuplicateJSONFields([]byte(raw)); err != nil {
		return Manifest{}, errors.New("Base mainnet release manifest contains duplicate or trailing JSON")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode Base mainnet release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("Base mainnet release manifest contains trailing JSON")
	}
	return manifest, nil
}

func DecodePublicKey(raw string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64 must encode exactly 32 bytes")
	}
	return ed25519.PublicKey(append([]byte(nil), decoded...)), nil
}

func Sign(manifest Manifest, privateKey ed25519.PrivateKey) (Manifest, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Manifest{}, errors.New("Ed25519 private key must be 64 bytes")
	}
	payload, err := manifest.signingBytes()
	if err != nil {
		return Manifest{}, err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return manifest, nil
}

func ValidateUnsigned(manifest Manifest, now time.Time) error {
	if manifest.SchemaVersion != 1 || manifest.Network != BaseMainnetNetwork || manifest.ChainID != BaseMainnetChainID {
		return errors.New("release manifest must identify Base mainnet schema 1")
	}
	if !identifierPattern.MatchString(manifest.ReleaseID) || !identifierPattern.MatchString(manifest.SignerKeyID) {
		return errors.New("release and signer key identifiers must be canonical")
	}
	if !commitPattern.MatchString(manifest.SourceCommit) || manifest.SourceCommit == strings.Repeat("0", 40) ||
		manifest.TypedDataManifestSHA256 != TypedDataManifestSHA256 || !nonZeroDigest(manifest.ExternalReviewSHA256) ||
		!nonZeroDigest(manifest.RPCAdmissionSHA256) {
		return errors.New("source and reviewed evidence digests must be canonical and non-empty")
	}
	if !manifest.RuntimeEnabled {
		return errors.New("release manifest does not authorize the Base mainnet runtime")
	}
	if manifest.ReviewedAt.IsZero() || manifest.ExpiresAt.IsZero() || now.Before(manifest.ReviewedAt) || !now.Before(manifest.ExpiresAt) ||
		manifest.ExpiresAt.Sub(manifest.ReviewedAt) > 31*24*time.Hour {
		return errors.New("release manifest must be current and expire within 31 days")
	}
	if manifest.Asset.Address != BaseMainnetUSDC || manifest.Asset.Symbol != "USDC" || manifest.Asset.Decimals != USDCDecimals ||
		!nonZeroDigest(manifest.Asset.RuntimeCodeHash) {
		return errors.New("release manifest must bind canonical Base mainnet USDC")
	}
	if err := validateContracts(manifest.Contracts); err != nil {
		return err
	}
	minimumDeploymentBlock := manifest.Contracts[0].DeploymentBlock
	for _, contract := range manifest.Contracts[1:] {
		if contract.DeploymentBlock < minimumDeploymentBlock {
			minimumDeploymentBlock = contract.DeploymentBlock
		}
	}
	if manifest.GovernanceFromBlock == 0 || manifest.GovernanceFromBlock > minimumDeploymentBlock ||
		manifest.SettlementWindowSeconds < 30*60 || manifest.SettlementWindowSeconds > 30*24*60*60 {
		return errors.New("release manifest has invalid governance or settlement observation bounds")
	}
	if err := validateSafeAndAuthorities(manifest.Deployer, manifest.Safe, manifest.Authorities, manifest.Contracts); err != nil {
		return err
	}
	if manifest.Pilot.MaxPerActionAtomic != InitialMaxPerActionAtomic || manifest.Pilot.MaxOutstandingAtomic != InitialMaxOutstandingAtomic {
		return errors.New("release manifest must bind the initial capped pilot profile")
	}
	if manifest.Pilot.FundingEnabled {
		if !nonZeroDigest(manifest.Pilot.FundedEvidenceSHA256) {
			return errors.New("funding authorization requires reviewed funded-pilot evidence")
		}
	} else if manifest.Pilot.FundedEvidenceSHA256 != "" {
		return errors.New("disabled funding must not claim funded-pilot evidence")
	}
	if err := validateObserver(manifest.Observer); err != nil {
		return err
	}
	return nil
}

func Verify(manifest Manifest, publicKey ed25519.PublicKey, now time.Time) error {
	if err := ValidateUnsigned(manifest, now); err != nil {
		return err
	}
	payload, err := manifest.signingBytes()
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("release manifest signature is invalid")
	}
	return nil
}

func CanonicalSHA256(manifest Manifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("0x%x", digest[:]), nil
}

func BindRuntime(manifest Manifest, bindings RuntimeBindings) error {
	contracts := make(map[string]string, len(manifest.Contracts))
	for _, contract := range manifest.Contracts {
		contracts[contract.Name] = contract.Address
	}
	if bindings.EscrowAsset != manifest.Asset.Address || bindings.DirectoryContract != contracts["service_directory"] ||
		bindings.AgentRegistry != contracts["agent_registry"] || bindings.CallEscrow != contracts["ascp_call_escrow"] ||
		bindings.SpendModule != contracts["ascp_spend_module"] || bindings.PilotPerAction != manifest.Pilot.MaxPerActionAtomic ||
		bindings.PilotOutstanding != manifest.Pilot.MaxOutstandingAtomic || bindings.GovernanceFromBlock != manifest.GovernanceFromBlock ||
		bindings.SettlementWindowSeconds != manifest.SettlementWindowSeconds {
		return errors.New("runtime configuration does not match the signed Base mainnet release manifest")
	}
	return nil
}

func BindObserver(manifest Manifest, bindings ObserverRuntimeBindings) error {
	contracts := make(map[string]string, len(manifest.Contracts))
	for _, contract := range manifest.Contracts {
		contracts[contract.Name] = contract.Address
	}
	observer := manifest.Observer
	if bindings.EscrowAsset != manifest.Asset.Address || bindings.CallEscrow != contracts["ascp_call_escrow"] ||
		bindings.SettlementWindowSeconds != manifest.SettlementWindowSeconds || bindings.Quorum != observer.Quorum ||
		bindings.HaltConfirmations != observer.HaltConfirmations || bindings.RecoveryObservations != observer.RecoveryObservations ||
		bindings.MinConfirmations != observer.MinConfirmations || bindings.ReorgLookback != observer.ReorgLookback ||
		bindings.MaxHeadSkew != observer.MaxHeadSkew || durationSeconds(bindings.ObserverInterval) != observer.ObserverIntervalSeconds ||
		durationSeconds(bindings.ObserverTimeout) != observer.ObserverTimeoutSeconds ||
		durationSeconds(bindings.ReconciliationInterval) != observer.ReconciliationIntervalSeconds ||
		durationSeconds(bindings.ReconciliationTimeout) != observer.ReconciliationTimeoutSeconds ||
		durationSeconds(bindings.StallThreshold) != observer.StallThresholdSeconds ||
		durationSeconds(bindings.ObservationMaxAge) != observer.ObservationMaxAgeSeconds ||
		durationSeconds(bindings.MaxFutureClockSkew) != observer.MaxFutureClockSkewSeconds {
		return errors.New("observer configuration does not match the signed Base mainnet release manifest")
	}
	return nil
}

func BindRPCAdmission(manifest Manifest, admission rpcadmission.ProductionAdmission) error {
	digest, err := RPCAdmissionSHA256(admission)
	if err != nil {
		return err
	}
	if digest != manifest.RPCAdmissionSHA256 {
		return errors.New("production RPC admission does not match the signed Base mainnet release manifest")
	}
	if manifest.Observer.Quorum > len(admission.Providers) {
		return errors.New("signed observer quorum exceeds the admitted production provider set")
	}
	return nil
}

func validateObserver(observer ObserverBinding) error {
	if observer.Quorum < 2 || observer.Quorum > 5 || observer.HaltConfirmations < 1 || observer.HaltConfirmations > 100 ||
		observer.RecoveryObservations < 1 || observer.RecoveryObservations > 100 || observer.MinConfirmations < 1 || observer.MinConfirmations > 1_000 ||
		observer.ReorgLookback < 1 || observer.ReorgLookback > 10_000 || observer.MaxHeadSkew < 1 || observer.MaxHeadSkew > 100 ||
		observer.ObserverTimeoutSeconds == 0 || observer.ObserverTimeoutSeconds >= observer.ObserverIntervalSeconds ||
		observer.ReconciliationTimeoutSeconds == 0 || observer.ReconciliationTimeoutSeconds >= observer.ReconciliationIntervalSeconds ||
		observer.ObserverIntervalSeconds >= observer.ObservationMaxAgeSeconds || observer.ObserverIntervalSeconds >= observer.StallThresholdSeconds ||
		observer.ObserverIntervalSeconds > 600 || observer.ReconciliationIntervalSeconds > 600 ||
		observer.StallThresholdSeconds > 3_600 || observer.ObservationMaxAgeSeconds > 600 || observer.MaxFutureClockSkewSeconds < 1 || observer.MaxFutureClockSkewSeconds > 60 {
		return errors.New("release manifest observer profile is outside the initial production safety bounds")
	}
	return nil
}

func durationSeconds(value time.Duration) uint64 {
	if value < 0 || value%time.Second != 0 {
		return ^uint64(0)
	}
	return uint64(value / time.Second)
}

func RPCAdmissionSHA256(admission rpcadmission.ProductionAdmission) (string, error) {
	encoded, err := json.Marshal(admission)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("0x%x", digest[:]), nil
}

func (manifest Manifest) signingBytes() ([]byte, error) {
	unsigned := manifest
	unsigned.Signature = ""
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return append([]byte(signingDomain), encoded...), nil
}

func validateContracts(contracts []ContractBinding) error {
	required := map[string]bool{
		"service_directory": false,
		"agent_registry":    false,
		"ascp_call_escrow":  false,
		"ascp_spend_module": false,
	}
	addresses := make(map[string]struct{}, len(contracts))
	for _, contract := range contracts {
		if _, exists := required[contract.Name]; !exists || required[contract.Name] {
			return errors.New("release manifest contract names must be complete and unique")
		}
		if !canonicalAddress(contract.Address) || !nonZeroDigest(contract.RuntimeCodeHash) ||
			!nonZeroDigest(contract.DeploymentTx) || contract.DeploymentBlock == 0 || !contract.SourceVerified {
			return fmt.Errorf("release manifest contract %s is incomplete", contract.Name)
		}
		if _, duplicate := addresses[contract.Address]; duplicate {
			return errors.New("release manifest contract addresses must be distinct")
		}
		addresses[contract.Address] = struct{}{}
		required[contract.Name] = true
	}
	if len(contracts) != len(required) {
		return errors.New("release manifest must bind every ASCP contract exactly once")
	}
	for _, present := range required {
		if !present {
			return errors.New("release manifest must bind every ASCP contract exactly once")
		}
	}
	return nil
}

func validateSafeAndAuthorities(deployer string, safe SafeBinding, authorities AuthorityBinding, contracts []ContractBinding) error {
	if !canonicalAddress(deployer) || !canonicalAddress(safe.Address) || len(safe.Owners) < 3 || safe.Threshold < 2 || int(safe.Threshold) > len(safe.Owners) ||
		int(safe.Threshold)*3 < len(safe.Owners)*2 {
		return errors.New("release deployer and Safe must be canonical with a two-of-three-or-stronger threshold")
	}
	seen := map[string]struct{}{deployer: {}, safe.Address: {}}
	if deployer == safe.Address {
		return errors.New("release deployer, Safe, owners, and authorities must be independently assigned")
	}
	for _, owner := range safe.Owners {
		if !canonicalAddress(owner) {
			return errors.New("release Safe owners must be canonical non-zero addresses")
		}
		if _, duplicate := seen[owner]; duplicate {
			return errors.New("release deployer, Safe owners, and Safe address must be distinct")
		}
		seen[owner] = struct{}{}
	}
	if authorities.Governor != safe.Address {
		return errors.New("release governor must be the reviewed Safe")
	}
	roles := []string{authorities.DirectoryPublisher, authorities.DirectoryPauser, authorities.RegistryAdmin, authorities.SpendAuthorizer}
	for _, role := range roles {
		if !canonicalAddress(role) {
			return errors.New("release authorities must be canonical non-zero addresses")
		}
		if _, duplicate := seen[role]; duplicate {
			return errors.New("release authority roles must be independently assigned")
		}
		seen[role] = struct{}{}
	}
	for _, contract := range contracts {
		if _, duplicate := seen[contract.Address]; duplicate {
			return errors.New("release contracts must not overlap Safe, owners, or authorities")
		}
		seen[contract.Address] = struct{}{}
	}
	return nil
}

func canonicalAddress(value string) bool {
	return len(value) == 42 && strings.ToLower(value) == value && common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func nonZeroDigest(value string) bool {
	return hexDigestPattern.MatchString(value) && value != "0x"+strings.Repeat("0", 64)
}

// ContractCodeBindings returns a stable copy suitable for quorum bytecode
// verification without exposing signature or authority metadata.
func ContractCodeBindings(manifest Manifest) []ContractBinding {
	result := append([]ContractBinding(nil), manifest.Contracts...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	result = append(result, ContractBinding{Name: "canonical_usdc", Address: manifest.Asset.Address, RuntimeCodeHash: manifest.Asset.RuntimeCodeHash})
	return result
}
