package directoryrelease

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	TransactionPreviewSchemaVersion = 1
	MinimumRelayGasLimit            = 450_000
	MaximumRelayGasLimit            = 500_000
	MaximumRelayFeePerGasWei        = 10_000_000_000
	MaximumRelayPriorityFeeWei      = 2_000_000_000
	MaximumRelayGasSpendWei         = 5_000_000_000_000_000
	MaximumTransactionPreviewAge    = 2 * time.Minute
	MinimumTransactionPreviewLife   = 30 * time.Second
)

type RelayTransactionRequest struct {
	SchemaVersion           int       `json:"schemaVersion"`
	ExpectedNonce           string    `json:"expectedNonce"`
	GasLimit                uint64    `json:"gasLimit"`
	MaxFeePerGasWei         string    `json:"maxFeePerGasWei"`
	MaxPriorityFeePerGasWei string    `json:"maxPriorityFeePerGasWei"`
	MaxWorstCaseGasSpendWei string    `json:"maxWorstCaseGasSpendWei"`
	ValidUntil              time.Time `json:"validUntil"`
}

type RelayTransactionProviderObservation struct {
	ProviderID        string `json:"providerId"`
	BlockNumber       uint64 `json:"blockNumber"`
	BlockHash         string `json:"blockHash"`
	BlockTimestamp    uint64 `json:"blockTimestamp"`
	ChainID           uint64 `json:"chainId"`
	RelayerAddress    string `json:"relayerAddress"`
	PendingNonce      string `json:"pendingNonce"`
	BaseFeePerGasWei  string `json:"baseFeePerGasWei"`
	MetadataOnly      bool   `json:"metadataOnly"`
	CalldataDisclosed bool   `json:"calldataDisclosed"`
}

type UnsignedRelayTransaction struct {
	SchemaVersion           int       `json:"schemaVersion"`
	TransactionType         string    `json:"transactionType"`
	ChainID                 uint64    `json:"chainId"`
	From                    string    `json:"from"`
	To                      string    `json:"to"`
	ValueWei                string    `json:"valueWei"`
	Nonce                   string    `json:"nonce"`
	GasLimit                uint64    `json:"gasLimit"`
	MaxFeePerGasWei         string    `json:"maxFeePerGasWei"`
	MaxPriorityFeePerGasWei string    `json:"maxPriorityFeePerGasWei"`
	Data                    string    `json:"data"`
	ValidUntil              time.Time `json:"validUntil"`
	SigningRequired         bool      `json:"signingRequired"`
	BroadcastAuthorized     bool      `json:"broadcastAuthorized"`
}

type RelayTransactionPreview struct {
	SchemaVersion           int                                   `json:"schemaVersion"`
	ReleaseID               string                                `json:"releaseId"`
	RelayEvidenceHash       string                                `json:"relayEvidenceHash"`
	PrivateTransactionHash  string                                `json:"privateTransactionHash"`
	SigningHash             string                                `json:"signingHash"`
	ChainID                 uint64                                `json:"chainId"`
	RelayerAddress          string                                `json:"relayerAddress"`
	ContractAddress         string                                `json:"contractAddress"`
	CalldataHash            string                                `json:"calldataHash"`
	Nonce                   string                                `json:"nonce"`
	GasLimit                uint64                                `json:"gasLimit"`
	MaxFeePerGasWei         string                                `json:"maxFeePerGasWei"`
	MaxPriorityFeePerGasWei string                                `json:"maxPriorityFeePerGasWei"`
	WorstCaseGasSpendWei    string                                `json:"worstCaseGasSpendWei"`
	ValidUntil              time.Time                             `json:"validUntil"`
	PreparedAt              time.Time                             `json:"preparedAt"`
	Observations            []RelayTransactionProviderObservation `json:"observations"`
	PrivateArtifactRequired bool                                  `json:"privateArtifactRequired"`
	CalldataDisclosed       bool                                  `json:"calldataDisclosed"`
	SigningRequired         bool                                  `json:"signingRequired"`
	BroadcastAuthorized     bool                                  `json:"broadcastAuthorized"`
	FundingEnabled          bool                                  `json:"fundingEnabled"`
}

type RelayTransactionTarget struct {
	ChainID              uint64
	RelayerAddress       string
	ExpectedNonce        string
	MaxFeePerGasWei      string
	MaxPriorityFeeWei    string
	ValidUntil           time.Time
	PreviousObservations []RelayProviderObservation
}

type RelayTransactionObserver interface {
	ObserveTransaction(context.Context, RelayTransactionTarget) ([]RelayTransactionProviderObservation, error)
}

type BaseSepoliaTransactionObserver struct{ providers []liveRPCProvider }

func DecodeRelayTransactionRequest(raw []byte) (RelayTransactionRequest, error) {
	var value RelayTransactionRequest
	if decodeStrict(raw, &value) != nil || validateRelayTransactionRequest(value, time.Time{}) != nil {
		return RelayTransactionRequest{}, ErrInvalidRelaySimulation
	}
	return value, nil
}

func DecodeUnsignedRelayTransaction(raw []byte) (UnsignedRelayTransaction, error) {
	var value UnsignedRelayTransaction
	if decodeStrict(raw, &value) != nil || validateUnsignedRelayTransaction(value) != nil {
		return UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	return value, nil
}

func DecodeRelayTransactionPreview(raw []byte) (RelayTransactionPreview, error) {
	var value RelayTransactionPreview
	if decodeStrict(raw, &value) != nil {
		return RelayTransactionPreview{}, ErrInvalidRelaySimulation
	}
	return value, nil
}

func NewBaseSepoliaTransactionObserver(primaryRPC, secondaryRPC string, timeout time.Duration) (*BaseSepoliaTransactionObserver, error) {
	base, err := NewBaseSepoliaLiveObserver(primaryRPC, secondaryRPC, timeout)
	if err != nil {
		return nil, ErrInvalidRelaySimulation
	}
	return &BaseSepoliaTransactionObserver{providers: base.providers}, nil
}

func PrepareRelayTransactionPreview(ctx context.Context, relay RelaySimulationEvidence, presign PresignPackage, artifact Artifact,
	deploymentJSON []byte, publisherSignature PublisherSignature, relayRequest RelaySimulationRequest, request RelayTransactionRequest,
	observer RelayTransactionObserver, clock func() time.Time,
) (RelayTransactionPreview, UnsignedRelayTransaction, error) {
	if observer == nil || clock == nil {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	startedAt := clock().UTC().Truncate(time.Second)
	if VerifyRelaySimulation(relay, presign, artifact, deploymentJSON, publisherSignature, relayRequest, startedAt) != nil ||
		validateRelayTransactionRequest(request, startedAt) != nil ||
		!request.ValidUntil.Before(time.Unix(int64(presign.Authorization.ValidBefore), 0)) {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	signature, _, err := verifyPublisherSignature(presign, publisherSignature)
	if err != nil {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	defer clear(signature)
	calldata, err := encodeProposeVersionCalldata(presign, signature)
	if err != nil || strings.ToLower(crypto.Keccak256Hash(calldata).Hex()) != relay.CalldataHash {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	defer clear(calldata)
	target := RelayTransactionTarget{ChainID: BaseSepoliaChainID, RelayerAddress: relayRequest.RelayerAddress,
		ExpectedNonce: request.ExpectedNonce, MaxFeePerGasWei: request.MaxFeePerGasWei,
		MaxPriorityFeeWei: request.MaxPriorityFeePerGasWei, ValidUntil: request.ValidUntil,
		PreviousObservations: relay.Observations}
	observations, err := observer.ObserveTransaction(ctx, target)
	if err != nil {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	preparedAt := clock().UTC().Truncate(time.Second)
	if verifyRelayTransactionObservations(target, observations, preparedAt) != nil || preparedAt.After(request.ValidUntil) {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	private, preview, err := buildRelayTransactionPreview(relay, presign, relayRequest, request, calldata, observations, preparedAt)
	if err != nil || VerifyRelayTransactionPreview(preview, private, relay, presign, artifact, deploymentJSON,
		publisherSignature, relayRequest, request, preparedAt) != nil {
		return RelayTransactionPreview{}, UnsignedRelayTransaction{}, ErrInvalidRelaySimulation
	}
	return preview, private, nil
}

func VerifyRelayTransactionPreview(preview RelayTransactionPreview, private UnsignedRelayTransaction, relay RelaySimulationEvidence,
	presign PresignPackage, artifact Artifact, deploymentJSON []byte, publisherSignature PublisherSignature,
	relayRequest RelaySimulationRequest, request RelayTransactionRequest, at time.Time,
) error {
	at = at.UTC().Truncate(time.Second)
	if VerifyRelaySimulation(relay, presign, artifact, deploymentJSON, publisherSignature, relayRequest, at) != nil ||
		validateRelayTransactionRequest(request, at) != nil || validateUnsignedRelayTransaction(private) != nil {
		return ErrInvalidRelaySimulation
	}
	signature, _, err := verifyPublisherSignature(presign, publisherSignature)
	if err != nil {
		return ErrInvalidRelaySimulation
	}
	defer clear(signature)
	calldata, err := encodeProposeVersionCalldata(presign, signature)
	if err != nil {
		return ErrInvalidRelaySimulation
	}
	defer clear(calldata)
	expectedPrivate, expectedPreview, err := buildRelayTransactionPreview(relay, presign, relayRequest, request, calldata, preview.Observations, preview.PreparedAt)
	if err != nil || !reflect.DeepEqual(private, expectedPrivate) || !reflect.DeepEqual(preview, expectedPreview) ||
		!canonicalSecond(preview.PreparedAt) || preview.PreparedAt.After(at) || at.Sub(preview.PreparedAt) > MaximumTransactionPreviewAge ||
		verifyRelayTransactionObservations(RelayTransactionTarget{ChainID: BaseSepoliaChainID, RelayerAddress: relayRequest.RelayerAddress,
			ExpectedNonce: request.ExpectedNonce, MaxFeePerGasWei: request.MaxFeePerGasWei,
			MaxPriorityFeeWei: request.MaxPriorityFeePerGasWei, ValidUntil: request.ValidUntil,
			PreviousObservations: relay.Observations}, preview.Observations, at) != nil {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func (o *BaseSepoliaTransactionObserver) ObserveTransaction(ctx context.Context, target RelayTransactionTarget) ([]RelayTransactionProviderObservation, error) {
	if o == nil || len(o.providers) != 2 || validateRelayTransactionTarget(target) != nil {
		return nil, ErrInvalidRelaySimulation
	}
	values := make([]RelayTransactionProviderObservation, 0, 2)
	for _, provider := range o.providers {
		value, err := observeRelayTransactionProvider(ctx, provider, target)
		if err != nil {
			return nil, ErrInvalidRelaySimulation
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ProviderID < values[j].ProviderID })
	return values, nil
}

func observeRelayTransactionProvider(ctx context.Context, provider liveRPCProvider, target RelayTransactionTarget) (RelayTransactionProviderObservation, error) {
	var chainHex, nonceHex string
	if rpcCall(ctx, provider, 201, "eth_chainId", []any{}, &chainHex) != nil ||
		rpcCall(ctx, provider, 202, "eth_getTransactionCount", []any{target.RelayerAddress, "pending"}, &nonceHex) != nil {
		return RelayTransactionProviderObservation{}, ErrInvalidRelaySimulation
	}
	chainID, chainErr := parseHexUint64(chainHex)
	nonce, nonceErr := parseHexQuantity(nonceHex)
	var block struct {
		Number        string `json:"number"`
		Hash          string `json:"hash"`
		Timestamp     string `json:"timestamp"`
		BaseFeePerGas string `json:"baseFeePerGas"`
	}
	if chainErr != nil || nonceErr != nil || rpcCall(ctx, provider, 203, "eth_getBlockByNumber", []any{"latest", false}, &block) != nil {
		return RelayTransactionProviderObservation{}, ErrInvalidRelaySimulation
	}
	blockNumber, numberErr := parseHexUint64(block.Number)
	blockTimestamp, timestampErr := parseHexUint64(block.Timestamp)
	baseFee, baseFeeErr := parseHexQuantity(block.BaseFeePerGas)
	if numberErr != nil || timestampErr != nil || baseFeeErr != nil || blockNumber == 0 || !validHash(block.Hash, false) ||
		block.Hash != strings.ToLower(block.Hash) || baseFee.Sign() <= 0 {
		return RelayTransactionProviderObservation{}, ErrInvalidRelaySimulation
	}
	var confirmed struct {
		Number        string `json:"number"`
		Hash          string `json:"hash"`
		Timestamp     string `json:"timestamp"`
		BaseFeePerGas string `json:"baseFeePerGas"`
	}
	var confirmedNonceHex string
	if rpcCall(ctx, provider, 204, "eth_getBlockByNumber", []any{block.Number, false}, &confirmed) != nil ||
		rpcCall(ctx, provider, 205, "eth_getTransactionCount", []any{target.RelayerAddress, "pending"}, &confirmedNonceHex) != nil ||
		confirmed.Number != block.Number || confirmed.Hash != block.Hash || confirmed.Timestamp != block.Timestamp ||
		confirmed.BaseFeePerGas != block.BaseFeePerGas || confirmedNonceHex != nonceHex {
		return RelayTransactionProviderObservation{}, ErrInvalidRelaySimulation
	}
	return RelayTransactionProviderObservation{ProviderID: provider.id, BlockNumber: blockNumber, BlockHash: block.Hash,
		BlockTimestamp: blockTimestamp, ChainID: chainID, RelayerAddress: target.RelayerAddress,
		PendingNonce: nonce.String(), BaseFeePerGasWei: baseFee.String(), MetadataOnly: true, CalldataDisclosed: false}, nil
}

func buildRelayTransactionPreview(relay RelaySimulationEvidence, presign PresignPackage, relayRequest RelaySimulationRequest,
	request RelayTransactionRequest, calldata []byte, observations []RelayTransactionProviderObservation, preparedAt time.Time,
) (UnsignedRelayTransaction, RelayTransactionPreview, error) {
	nonce, _ := new(big.Int).SetString(request.ExpectedNonce, 10)
	fee, _ := new(big.Int).SetString(request.MaxFeePerGasWei, 10)
	priority, _ := new(big.Int).SetString(request.MaxPriorityFeePerGasWei, 10)
	private := UnsignedRelayTransaction{SchemaVersion: TransactionPreviewSchemaVersion, TransactionType: "eip1559",
		ChainID: BaseSepoliaChainID, From: relayRequest.RelayerAddress, To: presign.Authorization.ContractAddress,
		ValueWei: "0", Nonce: request.ExpectedNonce, GasLimit: request.GasLimit,
		MaxFeePerGasWei: request.MaxFeePerGasWei, MaxPriorityFeePerGasWei: request.MaxPriorityFeePerGasWei,
		Data: "0x" + hex.EncodeToString(calldata), ValidUntil: request.ValidUntil,
		SigningRequired: true, BroadcastAuthorized: false}
	privateRaw, err := json.Marshal(private)
	if err != nil {
		return UnsignedRelayTransaction{}, RelayTransactionPreview{}, ErrInvalidRelaySimulation
	}
	to := common.HexToAddress(private.To)
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: new(big.Int).SetUint64(BaseSepoliaChainID), Nonce: nonce.Uint64(),
		GasTipCap: priority, GasFeeCap: fee, Gas: request.GasLimit, To: &to, Value: new(big.Int), Data: calldata})
	signingHash := types.LatestSignerForChainID(new(big.Int).SetUint64(BaseSepoliaChainID)).Hash(tx)
	relayRaw, err := json.Marshal(relay)
	if err != nil {
		return UnsignedRelayTransaction{}, RelayTransactionPreview{}, ErrInvalidRelaySimulation
	}
	worst := new(big.Int).Mul(new(big.Int).SetUint64(request.GasLimit), fee)
	preview := RelayTransactionPreview{SchemaVersion: TransactionPreviewSchemaVersion, ReleaseID: relay.ReleaseID,
		RelayEvidenceHash:      strings.ToLower(crypto.Keccak256Hash(relayRaw).Hex()),
		PrivateTransactionHash: strings.ToLower(crypto.Keccak256Hash(privateRaw).Hex()), SigningHash: strings.ToLower(signingHash.Hex()),
		ChainID: BaseSepoliaChainID, RelayerAddress: relayRequest.RelayerAddress, ContractAddress: presign.Authorization.ContractAddress,
		CalldataHash: relay.CalldataHash, Nonce: request.ExpectedNonce, GasLimit: request.GasLimit,
		MaxFeePerGasWei: request.MaxFeePerGasWei, MaxPriorityFeePerGasWei: request.MaxPriorityFeePerGasWei,
		WorstCaseGasSpendWei: worst.String(), ValidUntil: request.ValidUntil, PreparedAt: preparedAt,
		Observations: observations, PrivateArtifactRequired: true, CalldataDisclosed: false,
		SigningRequired: true, BroadcastAuthorized: false, FundingEnabled: false}
	return private, preview, nil
}

func verifyRelayTransactionObservations(target RelayTransactionTarget, values []RelayTransactionProviderObservation, at time.Time) error {
	if validateRelayTransactionTarget(target) != nil || len(values) != 2 || values[0].ProviderID >= values[1].ProviderID {
		return ErrInvalidRelaySimulation
	}
	previous := make(map[string]RelayProviderObservation, len(target.PreviousObservations))
	for _, item := range target.PreviousObservations {
		previous[item.ProviderID] = item
	}
	now := uint64(at.Unix())
	fee, _ := new(big.Int).SetString(target.MaxFeePerGasWei, 10)
	priority, _ := new(big.Int).SetString(target.MaxPriorityFeeWei, 10)
	var firstTimestamp uint64
	for index, value := range values {
		prior, exists := previous[value.ProviderID]
		base, baseOK := new(big.Int).SetString(value.BaseFeePerGasWei, 10)
		required := new(big.Int)
		if baseOK {
			required.Add(new(big.Int).Mul(base, big.NewInt(2)), priority)
		}
		if !exists || value.BlockNumber < prior.BlockNumber || !validHash(value.ProviderID, false) ||
			!validHash(value.BlockHash, false) || value.BlockTimestamp < prior.BlockTimestamp ||
			value.BlockTimestamp > now+uint64(MaximumRelayFutureBlockSkew/time.Second) ||
			(now >= value.BlockTimestamp && now-value.BlockTimestamp > uint64(MaximumTransactionPreviewAge/time.Second)) ||
			value.ChainID != target.ChainID || value.RelayerAddress != target.RelayerAddress || value.PendingNonce != target.ExpectedNonce ||
			!baseOK || base.Sign() <= 0 || required.Cmp(fee) > 0 || !value.MetadataOnly || value.CalldataDisclosed {
			return ErrInvalidRelaySimulation
		}
		if index == 0 {
			firstTimestamp = value.BlockTimestamp
		} else {
			difference := int64(value.BlockTimestamp) - int64(firstTimestamp)
			if difference < 0 {
				difference = -difference
			}
			if time.Duration(difference)*time.Second > maximumLiveBlockTimestampSkew {
				return ErrInvalidRelaySimulation
			}
		}
	}
	return nil
}

func validateRelayTransactionTarget(value RelayTransactionTarget) error {
	request := RelayTransactionRequest{SchemaVersion: TransactionPreviewSchemaVersion, ExpectedNonce: value.ExpectedNonce,
		GasLimit: MinimumRelayGasLimit, MaxFeePerGasWei: value.MaxFeePerGasWei,
		MaxPriorityFeePerGasWei: value.MaxPriorityFeeWei, MaxWorstCaseGasSpendWei: big.NewInt(MaximumRelayGasSpendWei).String(),
		ValidUntil: value.ValidUntil}
	if value.ChainID != BaseSepoliaChainID || !validAddress(value.RelayerAddress) || len(value.PreviousObservations) != 2 ||
		validateRelayTransactionRequest(request, time.Time{}) != nil {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func validateRelayTransactionRequest(value RelayTransactionRequest, at time.Time) error {
	nonce, nonceOK := canonicalDecimal(value.ExpectedNonce, true)
	fee, feeOK := canonicalDecimal(value.MaxFeePerGasWei, false)
	priority, priorityOK := canonicalDecimal(value.MaxPriorityFeePerGasWei, false)
	spend, spendOK := canonicalDecimal(value.MaxWorstCaseGasSpendWei, false)
	if value.SchemaVersion != TransactionPreviewSchemaVersion || !nonceOK || nonce.BitLen() > 64 ||
		value.GasLimit < MinimumRelayGasLimit || value.GasLimit > MaximumRelayGasLimit || !feeOK || !priorityOK || !spendOK ||
		fee.Cmp(big.NewInt(MaximumRelayFeePerGasWei)) > 0 || priority.Cmp(big.NewInt(MaximumRelayPriorityFeeWei)) > 0 ||
		priority.Cmp(fee) > 0 || spend.Cmp(big.NewInt(MaximumRelayGasSpendWei)) > 0 ||
		new(big.Int).Mul(new(big.Int).SetUint64(value.GasLimit), fee).Cmp(spend) > 0 || !canonicalSecond(value.ValidUntil) {
		return ErrInvalidRelaySimulation
	}
	if !at.IsZero() && (value.ValidUntil.Before(at.Add(MinimumTransactionPreviewLife)) ||
		value.ValidUntil.After(at.Add(MaximumTransactionPreviewAge))) {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func validateUnsignedRelayTransaction(value UnsignedRelayTransaction) error {
	if value.SchemaVersion != TransactionPreviewSchemaVersion || value.TransactionType != "eip1559" ||
		value.ChainID != BaseSepoliaChainID || !validAddress(value.From) || !validAddress(value.To) || value.ValueWei != "0" ||
		value.GasLimit < MinimumRelayGasLimit || value.GasLimit > MaximumRelayGasLimit || !canonicalSecond(value.ValidUntil) ||
		!value.SigningRequired || value.BroadcastAuthorized || !strings.HasPrefix(value.Data, "0x") || value.Data != strings.ToLower(value.Data) {
		return ErrInvalidRelaySimulation
	}
	data, err := hex.DecodeString(strings.TrimPrefix(value.Data, "0x"))
	if err != nil || len(data) < 4 {
		return ErrInvalidRelaySimulation
	}
	request := RelayTransactionRequest{SchemaVersion: TransactionPreviewSchemaVersion, ExpectedNonce: value.Nonce,
		GasLimit: value.GasLimit, MaxFeePerGasWei: value.MaxFeePerGasWei, MaxPriorityFeePerGasWei: value.MaxPriorityFeePerGasWei,
		MaxWorstCaseGasSpendWei: big.NewInt(MaximumRelayGasSpendWei).String(), ValidUntil: value.ValidUntil}
	return validateRelayTransactionRequest(request, time.Time{})
}

func canonicalDecimal(value string, allowZero bool) (*big.Int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return nil, false
	}
	number, ok := new(big.Int).SetString(value, 10)
	if !ok || number.Sign() < 0 || (!allowZero && number.Sign() == 0) || number.BitLen() > 256 {
		return nil, false
	}
	return number, true
}

func parseHexQuantity(value string) (*big.Int, error) {
	if len(value) < 3 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) || value == "0x00" ||
		(len(value) > 3 && value[2] == '0') {
		return nil, ErrInvalidRelaySimulation
	}
	number, ok := new(big.Int).SetString(value[2:], 16)
	if !ok || number.BitLen() > 256 {
		return nil, ErrInvalidRelaySimulation
	}
	return number, nil
}
