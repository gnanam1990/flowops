package directoryrelease

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascprails"
)

const maximumLiveBlockTimestampSkew = 2 * time.Minute

type LivePresignTarget struct {
	ChainID            uint64
	ContractAddress    string
	OrganizationDomain string
	ExpectedPublisher  string
	PublisherEpoch     uint64
	VersionID          uint64
	PreviousVersion    uint64
	PreviousRoot       string
	AuthorityRole      string
	AdminOperationID   string
	AdminNonce         string
	ValidAfter         uint64
	ValidBefore        uint64
}

type LiveProviderObservation struct {
	ProviderID         string `json:"providerId"`
	BlockNumber        uint64 `json:"blockNumber"`
	BlockHash          string `json:"blockHash"`
	BlockTimestamp     uint64 `json:"blockTimestamp"`
	ChainID            uint64 `json:"chainId"`
	ContractAddress    string `json:"contractAddress"`
	OrganizationDomain string `json:"organizationDomain"`
	DirectoryPublisher string `json:"directoryPublisher"`
	PublisherEpoch     uint64 `json:"publisherEpoch"`
	CurrentVersion     uint64 `json:"currentVersion"`
	CurrentRoot        string `json:"currentRoot"`
	LatestProposalHash string `json:"latestProposalHash"`
	AdminOperationUsed bool   `json:"adminOperationUsed"`
	AdminNonceUsed     bool   `json:"adminNonceUsed"`
}

type LivePresignEvidence struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Observations  []LiveProviderObservation `json:"observations"`
}

type LiveStateObserver interface {
	Observe(context.Context, LivePresignTarget) (LivePresignEvidence, error)
}

type liveRPCProvider struct {
	id       string
	endpoint *url.URL
	client   *http.Client
}

type BaseSepoliaLiveObserver struct {
	providers []liveRPCProvider
}

func NewBaseSepoliaLiveObserver(primaryRPC, secondaryRPC string, timeout time.Duration) (*BaseSepoliaLiveObserver, error) {
	if timeout < time.Second || timeout > 30*time.Second {
		return nil, ErrInvalidPresign
	}
	providers := make([]liveRPCProvider, 0, 2)
	for _, raw := range []string{primaryRPC, secondaryRPC} {
		endpoint, client, err := ascprails.NewRestrictedHTTPSClient(strings.TrimSpace(raw), timeout)
		if err != nil {
			return nil, ErrInvalidPresign
		}
		origin := strings.ToLower(endpoint.Scheme + "://" + endpoint.Host)
		providerID := strings.ToLower(crypto.Keccak256Hash([]byte(origin)).Hex())
		providers = append(providers, liveRPCProvider{id: providerID, endpoint: endpoint, client: client})
	}
	if providers[0].id == providers[1].id {
		return nil, ErrInvalidPresign
	}
	return &BaseSepoliaLiveObserver{providers: providers}, nil
}

func (o *BaseSepoliaLiveObserver) Observe(ctx context.Context, target LivePresignTarget) (LivePresignEvidence, error) {
	if o == nil || len(o.providers) != 2 || validateLiveTarget(target) != nil {
		return LivePresignEvidence{}, ErrInvalidPresign
	}
	observations := make([]LiveProviderObservation, 0, 2)
	for _, provider := range o.providers {
		observation, err := observeLiveProvider(ctx, provider, target)
		if err != nil {
			return LivePresignEvidence{}, ErrInvalidPresign
		}
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ProviderID < observations[j].ProviderID })
	evidence := LivePresignEvidence{SchemaVersion: PresignSchemaVersion, Observations: observations}
	if verifyLiveEvidence(target, evidence) != nil {
		return LivePresignEvidence{}, ErrInvalidPresign
	}
	return evidence, nil
}

func observeLiveProvider(ctx context.Context, provider liveRPCProvider, target LivePresignTarget) (LiveProviderObservation, error) {
	var chainHex string
	if err := rpcCall(ctx, provider, 1, "eth_chainId", []any{}, &chainHex); err != nil {
		return LiveProviderObservation{}, err
	}
	chainID, err := parseHexUint64(chainHex)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	var block struct {
		Number    string `json:"number"`
		Hash      string `json:"hash"`
		Timestamp string `json:"timestamp"`
	}
	if err := rpcCall(ctx, provider, 2, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
		return LiveProviderObservation{}, err
	}
	blockNumber, numberErr := parseHexUint64(block.Number)
	blockTimestamp, timestampErr := parseHexUint64(block.Timestamp)
	if numberErr != nil || timestampErr != nil || blockNumber == 0 || !validHash(strings.ToLower(block.Hash), false) || block.Hash != strings.ToLower(block.Hash) {
		return LiveProviderObservation{}, ErrInvalidPresign
	}
	call := func(id int, signature string, arguments []byte) ([]byte, error) {
		data, decodeErr := hex.DecodeString(strings.TrimPrefix(selector(signature), "0x"))
		if decodeErr != nil {
			return nil, decodeErr
		}
		data = append(data, arguments...)
		var result string
		if rpcErr := rpcCall(ctx, provider, id, "eth_call", []any{map[string]string{"to": target.ContractAddress, "data": "0x" + hex.EncodeToString(data)}, block.Number}, &result); rpcErr != nil {
			return nil, rpcErr
		}
		decoded, decodeErr := hex.DecodeString(strings.TrimPrefix(result, "0x"))
		if decodeErr != nil || len(decoded) != 32 || result != strings.ToLower(result) {
			return nil, ErrInvalidPresign
		}
		return decoded, nil
	}
	orgWord, err := call(3, "orgDomain()", nil)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	publisherWord, err := call(4, "directoryPublisher()", nil)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	epochWord, err := call(5, "directoryPublisherEpoch()", nil)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	versionWord, err := call(6, "currentVersion()", nil)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	rootWord, err := call(7, "currentRoot()", nil)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	latestWord, err := call(8, "latestProposalHash(uint64)", common.LeftPadBytes(new(big.Int).SetUint64(target.VersionID).Bytes(), 32))
	if err != nil {
		return LiveProviderObservation{}, err
	}
	operationWord, err := call(9, "usedAdminOperationIds(bytes32)", common.HexToHash(target.AdminOperationID).Bytes())
	if err != nil {
		return LiveProviderObservation{}, err
	}
	nonce, _ := new(big.Int).SetString(target.AdminNonce, 10)
	nonceArgs := append(common.HexToHash(target.AuthorityRole).Bytes(), common.LeftPadBytes(nonce.Bytes(), 32)...)
	nonceWord, err := call(10, "usedAdminNonces(bytes32,uint256)", nonceArgs)
	if err != nil {
		return LiveProviderObservation{}, err
	}
	var confirmedBlock struct {
		Number    string `json:"number"`
		Hash      string `json:"hash"`
		Timestamp string `json:"timestamp"`
	}
	if err := rpcCall(ctx, provider, 11, "eth_getBlockByNumber", []any{block.Number, false}, &confirmedBlock); err != nil ||
		confirmedBlock.Number != block.Number || confirmedBlock.Hash != block.Hash || confirmedBlock.Timestamp != block.Timestamp {
		return LiveProviderObservation{}, ErrInvalidPresign
	}
	if !bytes.Equal(publisherWord[:12], make([]byte, 12)) {
		return LiveProviderObservation{}, ErrInvalidPresign
	}
	publisher := common.BytesToAddress(publisherWord[12:])
	epoch, epochErr := wordUint64(epochWord)
	version, versionErr := wordUint64(versionWord)
	operationUsed, operationErr := wordBool(operationWord)
	nonceUsed, nonceErr := wordBool(nonceWord)
	if epochErr != nil || versionErr != nil || operationErr != nil || nonceErr != nil {
		return LiveProviderObservation{}, ErrInvalidPresign
	}
	return LiveProviderObservation{ProviderID: provider.id, BlockNumber: blockNumber, BlockHash: block.Hash,
		BlockTimestamp: blockTimestamp, ChainID: chainID, ContractAddress: target.ContractAddress,
		OrganizationDomain: strings.ToLower(common.BytesToHash(orgWord).Hex()),
		DirectoryPublisher: strings.ToLower(publisher.Hex()), PublisherEpoch: epoch, CurrentVersion: version,
		CurrentRoot: strings.ToLower(common.BytesToHash(rootWord).Hex()), LatestProposalHash: strings.ToLower(common.BytesToHash(latestWord).Hex()),
		AdminOperationUsed: operationUsed, AdminNonceUsed: nonceUsed}, nil
}

func verifyLiveEvidence(target LivePresignTarget, evidence LivePresignEvidence) error {
	zeroHash := "0x" + strings.Repeat("0", 64)
	if validateLiveTarget(target) != nil || evidence.SchemaVersion != PresignSchemaVersion || len(evidence.Observations) != 2 ||
		evidence.Observations[0].ProviderID >= evidence.Observations[1].ProviderID {
		return ErrInvalidPresign
	}
	var firstTimestamp uint64
	for index, observation := range evidence.Observations {
		if !validHash(observation.ProviderID, false) || observation.BlockNumber == 0 || !validHash(observation.BlockHash, false) ||
			observation.BlockTimestamp < target.ValidAfter || observation.BlockTimestamp >= target.ValidBefore ||
			target.ValidBefore-observation.BlockTimestamp < uint64(MinimumPresignRemaining/time.Second) ||
			observation.ChainID != target.ChainID || observation.ContractAddress != target.ContractAddress ||
			observation.OrganizationDomain != target.OrganizationDomain || observation.DirectoryPublisher != target.ExpectedPublisher ||
			observation.PublisherEpoch != target.PublisherEpoch || observation.CurrentVersion != target.PreviousVersion ||
			observation.CurrentRoot != target.PreviousRoot || observation.LatestProposalHash != zeroHash ||
			observation.AdminOperationUsed || observation.AdminNonceUsed {
			return ErrInvalidPresign
		}
		if index == 0 {
			firstTimestamp = observation.BlockTimestamp
		} else {
			difference := int64(observation.BlockTimestamp) - int64(firstTimestamp)
			if difference < 0 {
				difference = -difference
			}
			if time.Duration(difference)*time.Second > maximumLiveBlockTimestampSkew {
				return ErrInvalidPresign
			}
		}
	}
	return nil
}

func validateLiveTarget(target LivePresignTarget) error {
	nonce, ok := new(big.Int).SetString(target.AdminNonce, 10)
	if target.ChainID != BaseSepoliaChainID || !validAddress(target.ContractAddress) || !validHash(target.OrganizationDomain, false) ||
		!validAddress(target.ExpectedPublisher) || target.PublisherEpoch == 0 || target.VersionID == 0 ||
		!validHash(target.PreviousRoot, true) || !validHash(target.AuthorityRole, false) || !validHash(target.AdminOperationID, false) ||
		!ok || !decimalPattern.MatchString(target.AdminNonce) || nonce.Sign() <= 0 || nonce.BitLen() > 256 ||
		target.ValidBefore <= target.ValidAfter || target.ValidBefore-target.ValidAfter != 600 {
		return ErrInvalidPresign
	}
	return nil
}

func rpcCall(ctx context.Context, provider liveRPCProvider, id int, method string, params []any, output any) error {
	payload, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}{"2.0", id, method, params})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Host = request.URL.Host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := provider.client.Do(request)
	if err != nil {
		return ErrInvalidPresign
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.Request.URL.String() != provider.endpoint.String() ||
		(response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return ErrInvalidPresign
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || strings.ToLower(mediaType) != "application/json" {
		return ErrInvalidPresign
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return ErrInvalidPresign
	}
	if rejectDuplicateJSONKeys(raw) != nil {
		return ErrInvalidPresign
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.JSONRPC != "2.0" || envelope.ID != id || len(envelope.Result) == 0 ||
		len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		return ErrInvalidPresign
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidPresign
	}
	if err := json.Unmarshal(envelope.Result, output); err != nil {
		return ErrInvalidPresign
	}
	return nil
}

func parseHexUint64(raw string) (uint64, error) {
	if len(raw) < 3 || !strings.HasPrefix(raw, "0x") || raw != strings.ToLower(raw) || raw == "0x00" {
		return 0, ErrInvalidPresign
	}
	value, ok := new(big.Int).SetString(raw[2:], 16)
	if !ok || value.BitLen() > 64 || (value.Sign() == 0 && raw != "0x0") || (value.Sign() > 0 && raw[2] == '0') {
		return 0, ErrInvalidPresign
	}
	return value.Uint64(), nil
}

func wordUint64(raw []byte) (uint64, error) {
	value := new(big.Int).SetBytes(raw)
	if len(raw) != 32 || value.BitLen() > 64 {
		return 0, ErrInvalidPresign
	}
	return value.Uint64(), nil
}

func wordBool(raw []byte) (bool, error) {
	value := new(big.Int).SetBytes(raw)
	if len(raw) != 32 || value.BitLen() > 1 {
		return false, ErrInvalidPresign
	}
	return value.Sign() == 1, nil
}

func liveTarget(value PresignPackage) LivePresignTarget {
	return LivePresignTarget{ChainID: value.AuthorizationChainID(), ContractAddress: value.Authorization.ContractAddress,
		OrganizationDomain: value.Authorization.OrgDomain, ExpectedPublisher: value.ExpectedSigner,
		PublisherEpoch: value.Authorization.AdminEpoch, VersionID: value.Proposal.VersionID,
		PreviousVersion: value.Proposal.PreviousVersion, PreviousRoot: value.Proposal.PreviousRoot,
		AuthorityRole: value.Authorization.AuthorityRole, AdminOperationID: value.Authorization.AdminOperationID,
		AdminNonce: value.Authorization.AdminNonce, ValidAfter: value.Authorization.ValidAfter, ValidBefore: value.Authorization.ValidBefore}
}

func (value PresignPackage) AuthorizationChainID() uint64 {
	chain, _ := new(big.Int).SetString(value.Authorization.ChainID, 10)
	if chain == nil || chain.BitLen() > 64 {
		return 0
	}
	return chain.Uint64()
}
