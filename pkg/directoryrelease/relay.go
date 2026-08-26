package directoryrelease

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/adminauthorization"
)

const (
	RelaySchemaVersion          = 1
	MaximumRelayBlockAge        = 2 * time.Minute
	MaximumRelayFutureBlockSkew = 30 * time.Second
)

var ErrInvalidRelaySimulation = errors.New("invalid directory relay simulation")

const serviceDirectoryRelayABI = `[
  {"type":"function","name":"proposeVersion","inputs":[
    {"name":"proposal","type":"tuple","components":[
      {"name":"versionId","type":"uint64"},{"name":"previousVersion","type":"uint64"},
      {"name":"previousRoot","type":"bytes32"},{"name":"newRoot","type":"bytes32"},
      {"name":"blobContentHash","type":"bytes32"},{"name":"locationsHash","type":"bytes32"},
      {"name":"changeClass","type":"uint8"},{"name":"requestedActivatesAt","type":"uint64"},
      {"name":"workflowId","type":"bytes32"},{"name":"workflowPayloadHash","type":"bytes32"},
      {"name":"proposerNonce","type":"uint256"}]},
    {"name":"authorization","type":"tuple","components":[
      {"name":"orgDomain","type":"bytes32"},{"name":"contractAddress","type":"address"},
      {"name":"chainId","type":"uint256"},{"name":"authorityRole","type":"bytes32"},
      {"name":"functionSelector","type":"bytes4"},{"name":"payloadHash","type":"bytes32"},
      {"name":"adminOperationId","type":"bytes32"},{"name":"adminNonce","type":"uint256"},
      {"name":"adminEpoch","type":"uint64"},{"name":"validAfter","type":"uint64"},
      {"name":"validBefore","type":"uint64"},{"name":"workflowId","type":"bytes32"}]},
    {"name":"signature","type":"bytes"}],"outputs":[{"name":"proposalHash","type":"bytes32"}]},
  {"type":"function","name":"orgDomain","inputs":[],"outputs":[{"type":"bytes32"}]},
  {"type":"function","name":"directoryPublisher","inputs":[],"outputs":[{"type":"address"}]},
  {"type":"function","name":"directoryPublisherEpoch","inputs":[],"outputs":[{"type":"uint64"}]},
  {"type":"function","name":"currentVersion","inputs":[],"outputs":[{"type":"uint64"}]},
  {"type":"function","name":"currentRoot","inputs":[],"outputs":[{"type":"bytes32"}]},
  {"type":"function","name":"latestProposalHash","inputs":[{"type":"uint64"}],"outputs":[{"type":"bytes32"}]},
  {"type":"function","name":"usedAdminOperationIds","inputs":[{"type":"bytes32"}],"outputs":[{"type":"bool"}]},
  {"type":"function","name":"usedAdminNonces","inputs":[{"type":"bytes32"},{"type":"uint256"}],"outputs":[{"type":"bool"}]},
  {"type":"function","name":"usedProposerNonces","inputs":[{"type":"uint256"}],"outputs":[{"type":"bool"}]},
  {"type":"function","name":"adminAuthorizationDigest","inputs":[{"name":"authorization","type":"tuple","components":[
    {"name":"orgDomain","type":"bytes32"},{"name":"contractAddress","type":"address"},
    {"name":"chainId","type":"uint256"},{"name":"authorityRole","type":"bytes32"},
    {"name":"functionSelector","type":"bytes4"},{"name":"payloadHash","type":"bytes32"},
    {"name":"adminOperationId","type":"bytes32"},{"name":"adminNonce","type":"uint256"},
    {"name":"adminEpoch","type":"uint64"},{"name":"validAfter","type":"uint64"},
    {"name":"validBefore","type":"uint64"},{"name":"workflowId","type":"bytes32"}]}],"outputs":[{"type":"bytes32"}]},
  {"type":"function","name":"hashProposal","inputs":[{"name":"proposal","type":"tuple","components":[
    {"name":"versionId","type":"uint64"},{"name":"previousVersion","type":"uint64"},
    {"name":"previousRoot","type":"bytes32"},{"name":"newRoot","type":"bytes32"},
    {"name":"blobContentHash","type":"bytes32"},{"name":"locationsHash","type":"bytes32"},
    {"name":"changeClass","type":"uint8"},{"name":"requestedActivatesAt","type":"uint64"},
    {"name":"workflowId","type":"bytes32"},{"name":"workflowPayloadHash","type":"bytes32"},
    {"name":"proposerNonce","type":"uint256"}]}],"outputs":[{"type":"bytes32"}]},
  {"type":"function","name":"directoryProposalWorkflowPayloadHash","inputs":[{"name":"proposal","type":"tuple","components":[
    {"name":"versionId","type":"uint64"},{"name":"previousVersion","type":"uint64"},
    {"name":"previousRoot","type":"bytes32"},{"name":"newRoot","type":"bytes32"},
    {"name":"blobContentHash","type":"bytes32"},{"name":"locationsHash","type":"bytes32"},
    {"name":"changeClass","type":"uint8"},{"name":"requestedActivatesAt","type":"uint64"},
    {"name":"workflowId","type":"bytes32"},{"name":"workflowPayloadHash","type":"bytes32"},
    {"name":"proposerNonce","type":"uint256"}]}],"outputs":[{"type":"bytes32"}]}
]`

type PublisherSignature struct {
	SchemaVersion int    `json:"schemaVersion"`
	Digest        string `json:"digest"`
	Signature     string `json:"signature"`
}

type RelaySimulationRequest struct {
	SchemaVersion  int    `json:"schemaVersion"`
	RelayerAddress string `json:"relayerAddress"`
}

type RelayProviderObservation struct {
	ProviderID                 string `json:"providerId"`
	BlockNumber                uint64 `json:"blockNumber"`
	BlockHash                  string `json:"blockHash"`
	BlockTimestamp             uint64 `json:"blockTimestamp"`
	ChainID                    uint64 `json:"chainId"`
	ContractAddress            string `json:"contractAddress"`
	OrganizationDomain         string `json:"organizationDomain"`
	DirectoryPublisher         string `json:"directoryPublisher"`
	PublisherEpoch             uint64 `json:"publisherEpoch"`
	CurrentVersion             uint64 `json:"currentVersion"`
	CurrentRoot                string `json:"currentRoot"`
	LatestProposalHash         string `json:"latestProposalHash"`
	AdminOperationUsed         bool   `json:"adminOperationUsed"`
	AdminNonceUsed             bool   `json:"adminNonceUsed"`
	ProposerNonceUsed          bool   `json:"proposerNonceUsed"`
	AuthorizationDigest        string `json:"authorizationDigest"`
	ProposalHash               string `json:"proposalHash"`
	WorkflowPayloadHash        string `json:"workflowPayloadHash"`
	ContractSemanticSimulation bool   `json:"contractSemanticSimulation"`
}

type RelaySimulationEvidence struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	ReleaseID            string                     `json:"releaseId"`
	ArtifactHash         string                     `json:"artifactHash"`
	SigningDigest        string                     `json:"signingDigest"`
	SignatureHash        string                     `json:"signatureHash"`
	RecoveredSigner      string                     `json:"recoveredSigner"`
	RelayerAddress       string                     `json:"relayerAddress"`
	CalldataHash         string                     `json:"calldataHash"`
	CalldataLength       int                        `json:"calldataLength"`
	ExpectedProposalHash string                     `json:"expectedProposalHash"`
	SimulatedAt          time.Time                  `json:"simulatedAt"`
	Observations         []RelayProviderObservation `json:"observations"`
	SignatureVerified    bool                       `json:"signatureVerified"`
	CalldataDisclosed    bool                       `json:"calldataDisclosed"`
	BroadcastAuthorized  bool                       `json:"broadcastAuthorized"`
	FundingEnabled       bool                       `json:"fundingEnabled"`
}

type RelaySimulationTarget struct {
	ChainID              uint64
	ContractAddress      string
	OrganizationDomain   string
	ExpectedPublisher    string
	PublisherEpoch       uint64
	VersionID            uint64
	PreviousVersion      uint64
	PreviousRoot         string
	AuthorityRole        string
	AdminOperationID     string
	AdminNonce           string
	ProposerNonce        string
	ValidAfter           uint64
	ValidBefore          uint64
	AuthorizationDigest  string
	ProposalHash         string
	WorkflowPayloadHash  string
	Proposal             ContractProposal
	Authorization        adminauthorization.Authorization
	PreviousObservations []LiveProviderObservation
}

type RelayStateSimulator interface {
	Simulate(context.Context, RelaySimulationTarget) ([]RelayProviderObservation, error)
}

type BaseSepoliaRelaySimulator struct {
	providers []liveRPCProvider
}

type abiDirectoryProposal struct {
	VersionID            uint64   `abi:"versionId"`
	PreviousVersion      uint64   `abi:"previousVersion"`
	PreviousRoot         [32]byte `abi:"previousRoot"`
	NewRoot              [32]byte `abi:"newRoot"`
	BlobContentHash      [32]byte `abi:"blobContentHash"`
	LocationsHash        [32]byte `abi:"locationsHash"`
	ChangeClass          uint8    `abi:"changeClass"`
	RequestedActivatesAt uint64   `abi:"requestedActivatesAt"`
	WorkflowID           [32]byte `abi:"workflowId"`
	WorkflowPayloadHash  [32]byte `abi:"workflowPayloadHash"`
	ProposerNonce        *big.Int `abi:"proposerNonce"`
}

type abiAdminAuthorization struct {
	OrgDomain        [32]byte       `abi:"orgDomain"`
	ContractAddress  common.Address `abi:"contractAddress"`
	ChainID          *big.Int       `abi:"chainId"`
	AuthorityRole    [32]byte       `abi:"authorityRole"`
	FunctionSelector [4]byte        `abi:"functionSelector"`
	PayloadHash      [32]byte       `abi:"payloadHash"`
	AdminOperationID [32]byte       `abi:"adminOperationId"`
	AdminNonce       *big.Int       `abi:"adminNonce"`
	AdminEpoch       uint64         `abi:"adminEpoch"`
	ValidAfter       uint64         `abi:"validAfter"`
	ValidBefore      uint64         `abi:"validBefore"`
	WorkflowID       [32]byte       `abi:"workflowId"`
}

func DecodePublisherSignature(raw []byte) (PublisherSignature, error) {
	var value PublisherSignature
	if err := decodeStrict(raw, &value); err != nil || validatePublisherSignatureShape(value) != nil {
		return PublisherSignature{}, ErrInvalidRelaySimulation
	}
	return value, nil
}

func DecodeRelaySimulationRequest(raw []byte) (RelaySimulationRequest, error) {
	var value RelaySimulationRequest
	if err := decodeStrict(raw, &value); err != nil || validateRelayRequest(value) != nil {
		return RelaySimulationRequest{}, ErrInvalidRelaySimulation
	}
	return value, nil
}

func DecodeRelaySimulationEvidence(raw []byte) (RelaySimulationEvidence, error) {
	var value RelaySimulationEvidence
	if err := decodeStrict(raw, &value); err != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	return value, nil
}

func NewBaseSepoliaRelaySimulator(primaryRPC, secondaryRPC string, timeout time.Duration) (*BaseSepoliaRelaySimulator, error) {
	observer, err := NewBaseSepoliaLiveObserver(primaryRPC, secondaryRPC, timeout)
	if err != nil {
		return nil, ErrInvalidRelaySimulation
	}
	return &BaseSepoliaRelaySimulator{providers: observer.providers}, nil
}

func PrepareRelaySimulation(ctx context.Context, presign PresignPackage, artifact Artifact, deploymentJSON []byte,
	publisherSignature PublisherSignature, request RelaySimulationRequest, simulator RelayStateSimulator, clock func() time.Time,
) (RelaySimulationEvidence, error) {
	if simulator == nil || clock == nil || validateRelayRequest(request) != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	startedAt := clock().UTC().Truncate(time.Second)
	if VerifyPresign(presign, artifact, deploymentJSON, startedAt) != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	signature, signer, err := verifyPublisherSignature(presign, publisherSignature)
	if err != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	defer clear(signature)
	calldata, err := encodeProposeVersionCalldata(presign, signature)
	if err != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	defer clear(calldata)
	target := relayTarget(presign, artifact.Proposal.ProposalHash)
	observations, err := simulator.Simulate(ctx, target)
	if err != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	simulatedAt := clock().UTC().Truncate(time.Second)
	evidence := relayEvidence(presign, artifact.Proposal.ProposalHash, request, signer, signature, calldata, observations, simulatedAt)
	if VerifyRelaySimulation(evidence, presign, artifact, deploymentJSON, publisherSignature, request, simulatedAt) != nil {
		return RelaySimulationEvidence{}, ErrInvalidRelaySimulation
	}
	return evidence, nil
}

func VerifyRelaySimulation(evidence RelaySimulationEvidence, presign PresignPackage, artifact Artifact, deploymentJSON []byte,
	publisherSignature PublisherSignature, request RelaySimulationRequest, at time.Time,
) error {
	at = at.UTC().Truncate(time.Second)
	if VerifyPresign(presign, artifact, deploymentJSON, at) != nil || validateRelayRequest(request) != nil {
		return ErrInvalidRelaySimulation
	}
	signature, signer, err := verifyPublisherSignature(presign, publisherSignature)
	if err != nil {
		return ErrInvalidRelaySimulation
	}
	defer clear(signature)
	calldata, err := encodeProposeVersionCalldata(presign, signature)
	if err != nil {
		return ErrInvalidRelaySimulation
	}
	defer clear(calldata)
	expected := relayEvidence(presign, artifact.Proposal.ProposalHash, request, signer, signature, calldata, evidence.Observations, evidence.SimulatedAt)
	if !reflect.DeepEqual(evidence, expected) || !canonicalSecond(evidence.SimulatedAt) || evidence.SimulatedAt.After(at) ||
		at.Sub(evidence.SimulatedAt) > MaximumRelayBlockAge ||
		verifyRelayObservations(relayTarget(presign, artifact.Proposal.ProposalHash), evidence.Observations, at) != nil {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func (s *BaseSepoliaRelaySimulator) Simulate(ctx context.Context, target RelaySimulationTarget) ([]RelayProviderObservation, error) {
	if s == nil || len(s.providers) != 2 || validateRelayTarget(target) != nil {
		return nil, ErrInvalidRelaySimulation
	}
	parsedABI, err := abi.JSON(strings.NewReader(serviceDirectoryRelayABI))
	if err != nil {
		return nil, ErrInvalidRelaySimulation
	}
	proposal, authorization, err := relayABIValues(target.Proposal, target.Authorization)
	if err != nil {
		return nil, ErrInvalidRelaySimulation
	}
	observations := make([]RelayProviderObservation, 0, 2)
	for _, provider := range s.providers {
		observation, observeErr := simulateRelayProvider(ctx, provider, target, parsedABI, proposal, authorization)
		if observeErr != nil {
			return nil, ErrInvalidRelaySimulation
		}
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ProviderID < observations[j].ProviderID })
	return observations, nil
}

func simulateRelayProvider(ctx context.Context, provider liveRPCProvider, target RelaySimulationTarget, parsedABI abi.ABI,
	proposal abiDirectoryProposal, authorization abiAdminAuthorization,
) (RelayProviderObservation, error) {
	var chainHex string
	if err := rpcCall(ctx, provider, 101, "eth_chainId", []any{}, &chainHex); err != nil {
		return RelayProviderObservation{}, err
	}
	chainID, err := parseHexUint64(chainHex)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	var block struct {
		Number    string `json:"number"`
		Hash      string `json:"hash"`
		Timestamp string `json:"timestamp"`
	}
	if err := rpcCall(ctx, provider, 102, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
		return RelayProviderObservation{}, err
	}
	blockNumber, numberErr := parseHexUint64(block.Number)
	blockTimestamp, timestampErr := parseHexUint64(block.Timestamp)
	if numberErr != nil || timestampErr != nil || blockNumber == 0 || !validHash(block.Hash, false) || block.Hash != strings.ToLower(block.Hash) {
		return RelayProviderObservation{}, ErrInvalidRelaySimulation
	}
	callWord := func(id int, method string, values ...any) ([]byte, error) {
		data, packErr := parsedABI.Pack(method, values...)
		if packErr != nil {
			return nil, ErrInvalidRelaySimulation
		}
		var result string
		if rpcErr := rpcCall(ctx, provider, id, "eth_call", []any{map[string]string{
			"to": target.ContractAddress, "data": "0x" + hex.EncodeToString(data),
		}, block.Number}, &result); rpcErr != nil {
			return nil, rpcErr
		}
		decoded, decodeErr := hex.DecodeString(strings.TrimPrefix(result, "0x"))
		if decodeErr != nil || len(decoded) != 32 || result != strings.ToLower(result) {
			return nil, ErrInvalidRelaySimulation
		}
		return decoded, nil
	}
	orgWord, err := callWord(103, "orgDomain")
	if err != nil {
		return RelayProviderObservation{}, err
	}
	publisherWord, err := callWord(104, "directoryPublisher")
	if err != nil {
		return RelayProviderObservation{}, err
	}
	epochWord, err := callWord(105, "directoryPublisherEpoch")
	if err != nil {
		return RelayProviderObservation{}, err
	}
	versionWord, err := callWord(106, "currentVersion")
	if err != nil {
		return RelayProviderObservation{}, err
	}
	rootWord, err := callWord(107, "currentRoot")
	if err != nil {
		return RelayProviderObservation{}, err
	}
	latestWord, err := callWord(108, "latestProposalHash", target.VersionID)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	operationWord, err := callWord(109, "usedAdminOperationIds", common.HexToHash(target.AdminOperationID))
	if err != nil {
		return RelayProviderObservation{}, err
	}
	adminNonce, _ := new(big.Int).SetString(target.AdminNonce, 10)
	nonceWord, err := callWord(110, "usedAdminNonces", common.HexToHash(target.AuthorityRole), adminNonce)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	proposerNonce, _ := new(big.Int).SetString(target.ProposerNonce, 10)
	proposerNonceWord, err := callWord(111, "usedProposerNonces", proposerNonce)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	digestWord, err := callWord(112, "adminAuthorizationDigest", authorization)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	proposalWord, err := callWord(113, "hashProposal", proposal)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	workflowWord, err := callWord(114, "directoryProposalWorkflowPayloadHash", proposal)
	if err != nil {
		return RelayProviderObservation{}, err
	}
	var confirmedBlock struct {
		Number    string `json:"number"`
		Hash      string `json:"hash"`
		Timestamp string `json:"timestamp"`
	}
	if err := rpcCall(ctx, provider, 115, "eth_getBlockByNumber", []any{block.Number, false}, &confirmedBlock); err != nil ||
		confirmedBlock.Number != block.Number || confirmedBlock.Hash != block.Hash || confirmedBlock.Timestamp != block.Timestamp ||
		!allZero(publisherWord[:12]) {
		return RelayProviderObservation{}, ErrInvalidRelaySimulation
	}
	epoch, epochErr := wordUint64(epochWord)
	version, versionErr := wordUint64(versionWord)
	operationUsed, operationErr := wordBool(operationWord)
	adminNonceUsed, nonceErr := wordBool(nonceWord)
	proposerNonceUsed, proposerErr := wordBool(proposerNonceWord)
	if epochErr != nil || versionErr != nil || operationErr != nil || nonceErr != nil || proposerErr != nil {
		return RelayProviderObservation{}, ErrInvalidRelaySimulation
	}
	return RelayProviderObservation{ProviderID: provider.id, BlockNumber: blockNumber, BlockHash: block.Hash,
		BlockTimestamp: blockTimestamp, ChainID: chainID, ContractAddress: target.ContractAddress,
		OrganizationDomain: strings.ToLower(common.BytesToHash(orgWord).Hex()),
		DirectoryPublisher: strings.ToLower(common.BytesToAddress(publisherWord[12:]).Hex()), PublisherEpoch: epoch,
		CurrentVersion: version, CurrentRoot: strings.ToLower(common.BytesToHash(rootWord).Hex()),
		LatestProposalHash: strings.ToLower(common.BytesToHash(latestWord).Hex()), AdminOperationUsed: operationUsed,
		AdminNonceUsed: adminNonceUsed, ProposerNonceUsed: proposerNonceUsed,
		AuthorizationDigest: strings.ToLower(common.BytesToHash(digestWord).Hex()),
		ProposalHash:        strings.ToLower(common.BytesToHash(proposalWord).Hex()),
		WorkflowPayloadHash: strings.ToLower(common.BytesToHash(workflowWord).Hex()), ContractSemanticSimulation: true}, nil
}

func relayEvidence(presign PresignPackage, proposalHash string, request RelaySimulationRequest, signer common.Address, signature, calldata []byte,
	observations []RelayProviderObservation, simulatedAt time.Time,
) RelaySimulationEvidence {
	return RelaySimulationEvidence{SchemaVersion: RelaySchemaVersion, ReleaseID: presign.ReleaseID,
		ArtifactHash: presign.ArtifactHash, SigningDigest: presign.Digest,
		SignatureHash: strings.ToLower(crypto.Keccak256Hash(signature).Hex()), RecoveredSigner: strings.ToLower(signer.Hex()),
		RelayerAddress: request.RelayerAddress, CalldataHash: strings.ToLower(crypto.Keccak256Hash(calldata).Hex()),
		CalldataLength: len(calldata), ExpectedProposalHash: proposalHash, SimulatedAt: simulatedAt,
		Observations: observations, SignatureVerified: true, CalldataDisclosed: false,
		BroadcastAuthorized: false, FundingEnabled: false}
}

func relayTarget(presign PresignPackage, proposalHash string) RelaySimulationTarget {
	return RelaySimulationTarget{ChainID: presign.AuthorizationChainID(), ContractAddress: presign.Authorization.ContractAddress,
		OrganizationDomain: presign.Authorization.OrgDomain, ExpectedPublisher: presign.ExpectedSigner,
		PublisherEpoch: presign.Authorization.AdminEpoch, VersionID: presign.Proposal.VersionID,
		PreviousVersion: presign.Proposal.PreviousVersion, PreviousRoot: presign.Proposal.PreviousRoot,
		AuthorityRole: presign.Authorization.AuthorityRole, AdminOperationID: presign.Authorization.AdminOperationID,
		AdminNonce: presign.Authorization.AdminNonce, ProposerNonce: presign.Proposal.ProposerNonce,
		ValidAfter: presign.Authorization.ValidAfter, ValidBefore: presign.Authorization.ValidBefore,
		AuthorizationDigest: presign.Digest, ProposalHash: proposalHash,
		WorkflowPayloadHash: presign.Proposal.WorkflowPayloadHash, Proposal: presign.Proposal,
		Authorization: presign.Authorization, PreviousObservations: presign.LiveReadiness.Observations}
}

func verifyRelayObservations(target RelaySimulationTarget, observations []RelayProviderObservation, at time.Time) error {
	zero := relayZeroHash()
	if validateRelayTarget(target) != nil || len(observations) != 2 || observations[0].ProviderID >= observations[1].ProviderID {
		return ErrInvalidRelaySimulation
	}
	previous := make(map[string]LiveProviderObservation, len(target.PreviousObservations))
	for _, item := range target.PreviousObservations {
		previous[item.ProviderID] = item
	}
	var firstTimestamp uint64
	now := uint64(at.Unix())
	for index, observation := range observations {
		prior, ok := previous[observation.ProviderID]
		if !ok || observation.BlockNumber < prior.BlockNumber || !validHash(observation.ProviderID, false) ||
			!validHash(observation.BlockHash, false) || observation.BlockTimestamp < target.ValidAfter ||
			observation.BlockTimestamp >= target.ValidBefore || target.ValidBefore-observation.BlockTimestamp < uint64(MinimumPresignRemaining/time.Second) ||
			observation.BlockTimestamp > now+uint64(MaximumRelayFutureBlockSkew/time.Second) ||
			(now >= observation.BlockTimestamp && now-observation.BlockTimestamp > uint64(MaximumRelayBlockAge/time.Second)) ||
			observation.ChainID != target.ChainID || observation.ContractAddress != target.ContractAddress ||
			observation.OrganizationDomain != target.OrganizationDomain || observation.DirectoryPublisher != target.ExpectedPublisher ||
			observation.PublisherEpoch != target.PublisherEpoch || observation.CurrentVersion != target.PreviousVersion ||
			observation.CurrentRoot != target.PreviousRoot || observation.LatestProposalHash != zero ||
			observation.AdminOperationUsed || observation.AdminNonceUsed || observation.ProposerNonceUsed ||
			observation.AuthorizationDigest != target.AuthorizationDigest || observation.ProposalHash != target.ProposalHash ||
			observation.WorkflowPayloadHash != target.WorkflowPayloadHash || !observation.ContractSemanticSimulation {
			return ErrInvalidRelaySimulation
		}
		if index == 0 {
			firstTimestamp = observation.BlockTimestamp
		} else {
			difference := int64(observation.BlockTimestamp) - int64(firstTimestamp)
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

func validateRelayTarget(target RelaySimulationTarget) error {
	if target.ChainID != BaseSepoliaChainID || !validAddress(target.ContractAddress) || !validHash(target.OrganizationDomain, false) ||
		!validAddress(target.ExpectedPublisher) || target.PublisherEpoch == 0 || target.VersionID != 1 || target.PreviousVersion != 0 ||
		target.PreviousRoot != relayZeroHash() || !validHash(target.AuthorityRole, false) || !validHash(target.AdminOperationID, false) ||
		!validHash(target.AuthorizationDigest, false) || !validHash(target.ProposalHash, false) ||
		!validHash(target.WorkflowPayloadHash, false) || len(target.PreviousObservations) != 2 {
		return ErrInvalidRelaySimulation
	}
	if _, ok := new(big.Int).SetString(target.AdminNonce, 10); !ok {
		return ErrInvalidRelaySimulation
	}
	if _, ok := new(big.Int).SetString(target.ProposerNonce, 10); !ok {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func verifyPublisherSignature(presign PresignPackage, value PublisherSignature) ([]byte, common.Address, error) {
	if validatePublisherSignatureShape(value) != nil || value.Digest != presign.Digest {
		return nil, common.Address{}, ErrInvalidRelaySimulation
	}
	signature, recovery, err := publisherSignatureBytes(value.Signature)
	if err != nil {
		return nil, common.Address{}, ErrInvalidRelaySimulation
	}
	defer clear(recovery)
	publicKey, err := crypto.SigToPub(common.HexToHash(presign.Digest).Bytes(), recovery)
	if err != nil {
		clear(signature)
		return nil, common.Address{}, ErrInvalidRelaySimulation
	}
	signer := crypto.PubkeyToAddress(*publicKey)
	if strings.ToLower(signer.Hex()) != presign.ExpectedSigner {
		clear(signature)
		return nil, common.Address{}, ErrInvalidRelaySimulation
	}
	return signature, signer, nil
}

func validatePublisherSignatureShape(value PublisherSignature) error {
	if value.SchemaVersion != RelaySchemaVersion || !validHash(value.Digest, false) || len(value.Signature) != 132 ||
		!strings.HasPrefix(value.Signature, "0x") || value.Signature != strings.ToLower(value.Signature) {
		return ErrInvalidRelaySimulation
	}
	signature, recovery, err := publisherSignatureBytes(value.Signature)
	clear(signature)
	clear(recovery)
	if err != nil {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func publisherSignatureBytes(value string) ([]byte, []byte, error) {
	signature, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || len(signature) != 65 || (signature[64] != 27 && signature[64] != 28) {
		clear(signature)
		return nil, nil, ErrInvalidRelaySimulation
	}
	recovery := append([]byte(nil), signature...)
	recovery[64] -= 27
	if !crypto.ValidateSignatureValues(recovery[64], new(big.Int).SetBytes(recovery[:32]), new(big.Int).SetBytes(recovery[32:64]), true) {
		clear(signature)
		clear(recovery)
		return nil, nil, ErrInvalidRelaySimulation
	}
	return signature, recovery, nil
}

func validateRelayRequest(value RelaySimulationRequest) error {
	if value.SchemaVersion != RelaySchemaVersion || !validAddress(value.RelayerAddress) {
		return ErrInvalidRelaySimulation
	}
	return nil
}

func encodeProposeVersionCalldata(presign PresignPackage, signature []byte) ([]byte, error) {
	parsedABI, err := abi.JSON(strings.NewReader(serviceDirectoryRelayABI))
	if err != nil {
		return nil, err
	}
	proposal, authorization, err := relayABIValues(presign.Proposal, presign.Authorization)
	if err != nil {
		return nil, err
	}
	calldata, err := parsedABI.Pack("proposeVersion", proposal, authorization, signature)
	if err != nil || len(calldata) < 4 || "0x"+hex.EncodeToString(calldata[:4]) != presign.UnsignedCall.FunctionSelector {
		return nil, ErrInvalidRelaySimulation
	}
	return calldata, nil
}

func relayABIValues(proposal ContractProposal, authorization adminauthorization.Authorization) (abiDirectoryProposal, abiAdminAuthorization, error) {
	proposerNonce, proposerOK := new(big.Int).SetString(proposal.ProposerNonce, 10)
	chainID, chainOK := new(big.Int).SetString(authorization.ChainID, 10)
	adminNonce, adminOK := new(big.Int).SetString(authorization.AdminNonce, 10)
	selectorBytes, selectorErr := hex.DecodeString(strings.TrimPrefix(authorization.FunctionSelector, "0x"))
	if !proposerOK || !chainOK || !adminOK || selectorErr != nil || len(selectorBytes) != 4 {
		return abiDirectoryProposal{}, abiAdminAuthorization{}, ErrInvalidRelaySimulation
	}
	var selectorValue [4]byte
	copy(selectorValue[:], selectorBytes)
	return abiDirectoryProposal{VersionID: proposal.VersionID, PreviousVersion: proposal.PreviousVersion,
			PreviousRoot: common.HexToHash(proposal.PreviousRoot), NewRoot: common.HexToHash(proposal.NewRoot),
			BlobContentHash: common.HexToHash(proposal.BlobContentHash), LocationsHash: common.HexToHash(proposal.LocationsHash),
			ChangeClass: proposal.ChangeClass, RequestedActivatesAt: proposal.RequestedActivatesAt,
			WorkflowID: common.HexToHash(proposal.WorkflowID), WorkflowPayloadHash: common.HexToHash(proposal.WorkflowPayloadHash),
			ProposerNonce: proposerNonce}, abiAdminAuthorization{OrgDomain: common.HexToHash(authorization.OrgDomain),
			ContractAddress: common.HexToAddress(authorization.ContractAddress), ChainID: chainID,
			AuthorityRole: common.HexToHash(authorization.AuthorityRole), FunctionSelector: selectorValue,
			PayloadHash: common.HexToHash(authorization.PayloadHash), AdminOperationID: common.HexToHash(authorization.AdminOperationID),
			AdminNonce: adminNonce, AdminEpoch: authorization.AdminEpoch, ValidAfter: authorization.ValidAfter,
			ValidBefore: authorization.ValidBefore, WorkflowID: common.HexToHash(authorization.WorkflowID)}, nil
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}

func relayZeroHash() string { return "0x" + strings.Repeat("0", 64) }
