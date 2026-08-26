// Package directoryrelease compiles an exact, funding-disabled Base Sepolia
// ServiceDirectory v1 manifest into Merkle proofs and governance bindings.
// It never signs or submits a transaction.
package directoryrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/directoryproof"
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

const (
	SchemaVersion          = 1
	BaseSepoliaNetwork     = "base-sepolia"
	BaseSepoliaChainID     = 84532
	BaseSepoliaUSDC        = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	PayoutAuthorityChange  = 2
	MaxAuthorizationWindow = 600
	MaxSellers             = 256
	MaxResources           = 1024
	MaxCanonicalBlobBytes  = 1 << 20
)

var (
	ErrInvalidManifest = errors.New("invalid directory release manifest")
	ErrInvalidArtifact = errors.New("invalid directory release artifact")
	releaseIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`)
	commitPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	decimalPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
)

type AssetBinding struct {
	Address         string `json:"address"`
	Symbol          string `json:"symbol"`
	Decimals        uint8  `json:"decimals"`
	RuntimeCodeHash string `json:"runtimeCodeHash"`
}

type SourceDeployment struct {
	ReleaseID    string `json:"releaseId"`
	SourceCommit string `json:"sourceCommit"`
}

type Seller struct {
	SellerID        string `json:"sellerId"`
	PayoutAddress   string `json:"payoutAddress"`
	AckAuthority    string `json:"ackAuthority"`
	QuoteSigningKey string `json:"quoteSigningKey"`
	KeyEpoch        uint64 `json:"keyEpoch"`
	BaseURLOrigin   string `json:"baseURLOrigin"`
	Status          uint8  `json:"status"`
}

type Resource struct {
	SellerID                  string `json:"sellerId"`
	ResourceID                string `json:"resourceId"`
	Price                     string `json:"price"`
	EscrowSupported           bool   `json:"escrowSupported"`
	VerificationSpecHash      string `json:"verificationSpecHash"`
	DeclaredWorkTime          uint64 `json:"declaredWorkTime"`
	VerificationBudgetSeconds uint64 `json:"verificationBudgetSeconds"`
}

type Manifest struct {
	SchemaVersion           int              `json:"schemaVersion"`
	ReleaseID               string           `json:"releaseId"`
	Network                 string           `json:"network"`
	ChainID                 uint64           `json:"chainId"`
	SourceDeployment        SourceDeployment `json:"sourceDeployment"`
	DirectoryContract       string           `json:"directoryContract"`
	OrganizationDomain      string           `json:"organizationDomain"`
	DirectoryPublisher      string           `json:"directoryPublisher"`
	DirectoryPublisherEpoch uint64           `json:"directoryPublisherEpoch"`
	Asset                   AssetBinding     `json:"asset"`
	VersionID               uint64           `json:"versionId"`
	PreviousVersion         uint64           `json:"previousVersion"`
	PreviousRoot            string           `json:"previousRoot"`
	ChangeClass             uint8            `json:"changeClass"`
	RequestedActivatesAt    uint64           `json:"requestedActivatesAt"`
	WorkflowID              string           `json:"workflowId"`
	ProposerNonce           string           `json:"proposerNonce"`
	Locations               []string         `json:"locations"`
	Sellers                 []Seller         `json:"sellers"`
	Resources               []Resource       `json:"resources"`
	FundingEnabled          bool             `json:"fundingEnabled"`
}

type deploymentRecord struct {
	ReleaseID          string `json:"releaseId"`
	Network            string `json:"network"`
	ChainID            uint64 `json:"chainId"`
	SourceCommit       string `json:"sourceCommit"`
	OrganizationDomain string `json:"organizationDomain"`
	Authorities        struct {
		DirectoryPublisher string `json:"directoryPublisher"`
	} `json:"authorities"`
	Asset     AssetBinding `json:"asset"`
	Contracts []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"contracts"`
}

type CompiledLeaf struct {
	Kind       string   `json:"kind"`
	SellerID   string   `json:"sellerId"`
	ResourceID string   `json:"resourceId,omitempty"`
	Hash       string   `json:"hash"`
	Proof      []string `json:"proof"`
}

type Proposal struct {
	VersionID            uint64 `json:"versionId"`
	PreviousVersion      uint64 `json:"previousVersion"`
	PreviousRoot         string `json:"previousRoot"`
	NewRoot              string `json:"newRoot"`
	BlobContentHash      string `json:"blobContentHash"`
	LocationsHash        string `json:"locationsHash"`
	ChangeClass          uint8  `json:"changeClass"`
	RequestedActivatesAt uint64 `json:"requestedActivatesAt"`
	WorkflowID           string `json:"workflowId"`
	WorkflowPayloadHash  string `json:"workflowPayloadHash"`
	ProposerNonce        string `json:"proposerNonce"`
	ProposalHash         string `json:"proposalHash"`
	ProposePayloadHash   string `json:"proposePayloadHash"`
}

type PublisherAuthorizationBinding struct {
	ExpectedSigner     string `json:"expectedSigner"`
	OrganizationDomain string `json:"organizationDomain"`
	ContractAddress    string `json:"contractAddress"`
	ChainID            uint64 `json:"chainId"`
	AdminEpoch         uint64 `json:"adminEpoch"`
	AuthorityRole      string `json:"authorityRole"`
	FunctionSelector   string `json:"functionSelector"`
	PayloadHash        string `json:"payloadHash"`
	WorkflowID         string `json:"workflowId"`
	MaxWindowSeconds   uint64 `json:"maxWindowSeconds"`
}

type Artifact struct {
	SchemaVersion          int                            `json:"schemaVersion"`
	Manifest               Manifest                       `json:"manifest"`
	CanonicalBlob          json.RawMessage                `json:"canonicalBlob"`
	BlobContentHash        string                         `json:"blobContentHash"`
	LocationsHash          string                         `json:"locationsHash"`
	MerkleRoot             string                         `json:"merkleRoot"`
	Leaves                 []CompiledLeaf                 `json:"leaves"`
	Proposal               Proposal                       `json:"proposal"`
	PublisherAuthorization PublisherAuthorizationBinding  `json:"publisherAuthorization"`
	Approval               governanceworkflow.BoundAction `json:"approval"`
	FundingEnabled         bool                           `json:"fundingEnabled"`
}

type directoryBlob struct {
	SchemaVersion     int          `json:"schemaVersion"`
	ReleaseID         string       `json:"releaseId"`
	Network           string       `json:"network"`
	ChainID           uint64       `json:"chainId"`
	DirectoryContract string       `json:"directoryContract"`
	Asset             AssetBinding `json:"asset"`
	VersionID         uint64       `json:"versionId"`
	PreviousVersion   uint64       `json:"previousVersion"`
	PreviousRoot      string       `json:"previousRoot"`
	Sellers           []Seller     `json:"sellers"`
	Resources         []Resource   `json:"resources"`
}

func DecodeManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	if err := decodeStrict(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	return manifest, nil
}

func DecodeArtifact(raw []byte) (Artifact, error) {
	var artifact Artifact
	if err := decodeStrict(raw, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
	}
	return artifact, nil
}

func Compile(manifest Manifest, deploymentJSON []byte) (Artifact, error) {
	deployment, err := decodeDeployment(deploymentJSON)
	if err != nil || validateManifest(manifest, deployment) != nil {
		return Artifact{}, ErrInvalidManifest
	}

	sellers := append([]Seller(nil), manifest.Sellers...)
	resources := append([]Resource(nil), manifest.Resources...)
	sort.Slice(sellers, func(i, j int) bool { return sellers[i].SellerID < sellers[j].SellerID })
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].SellerID == resources[j].SellerID {
			return resources[i].ResourceID < resources[j].ResourceID
		}
		return resources[i].SellerID < resources[j].SellerID
	})
	manifest.Sellers, manifest.Resources = sellers, resources

	blob := directoryBlob{SchemaVersion: SchemaVersion, ReleaseID: manifest.ReleaseID, Network: manifest.Network,
		ChainID: manifest.ChainID, DirectoryContract: manifest.DirectoryContract, Asset: manifest.Asset,
		VersionID: manifest.VersionID, PreviousVersion: manifest.PreviousVersion, PreviousRoot: manifest.PreviousRoot,
		Sellers: sellers, Resources: resources}
	canonicalBlob, err := json.Marshal(blob)
	if err != nil || len(canonicalBlob) > MaxCanonicalBlobBytes {
		return Artifact{}, ErrInvalidManifest
	}
	blobHash := crypto.Keccak256Hash(canonicalBlob)
	if err := validateLocations(manifest.Locations, canonicalBlob); err != nil {
		return Artifact{}, ErrInvalidManifest
	}
	locationsJSON, _ := json.Marshal(manifest.Locations)
	locationsHash := crypto.Keccak256Hash(locationsJSON)

	leafInputs := make([]leafInput, 0, len(sellers)+len(resources))
	for _, seller := range sellers {
		hash, hashErr := directoryproof.SellerLeafHash(seller.proofLeaf())
		if hashErr != nil {
			return Artifact{}, ErrInvalidManifest
		}
		leafInputs = append(leafInputs, leafInput{kind: "seller", sellerID: seller.SellerID, hash: hash})
	}
	for _, resource := range resources {
		hash, hashErr := directoryproof.ResourceLeafHash(resource.proofLeaf())
		if hashErr != nil {
			return Artifact{}, ErrInvalidManifest
		}
		leafInputs = append(leafInputs, leafInput{kind: "resource", sellerID: resource.SellerID, resourceID: resource.ResourceID, hash: hash})
	}
	sort.Slice(leafInputs, func(i, j int) bool { return bytes.Compare(leafInputs[i].hash[:], leafInputs[j].hash[:]) < 0 })
	root, proofs := merkle(leafInputs)
	compiledLeaves := make([]CompiledLeaf, len(leafInputs))
	for index, leaf := range leafInputs {
		proof := make([]string, len(proofs[index]))
		for proofIndex, item := range proofs[index] {
			proof[proofIndex] = strings.ToLower(item.Hex())
		}
		compiledLeaves[index] = CompiledLeaf{Kind: leaf.kind, SellerID: leaf.sellerID,
			ResourceID: leaf.resourceID, Hash: strings.ToLower(leaf.hash.Hex()), Proof: proof}
	}

	workflowProposal := governanceworkflow.DirectoryProposal{VersionID: manifest.VersionID, PreviousVersion: manifest.PreviousVersion,
		PreviousRoot: manifest.PreviousRoot, NewRoot: strings.ToLower(root.Hex()), BlobContentHash: strings.ToLower(blobHash.Hex()),
		LocationsHash: strings.ToLower(locationsHash.Hex()), ChangeClass: manifest.ChangeClass,
		RequestedActivatesAt: manifest.RequestedActivatesAt}
	bound, err := governanceworkflow.BindAction(manifest.WorkflowID, governanceworkflow.Action{Type: governanceworkflow.ActionDirectoryApprove,
		ChainID: manifest.ChainID, ContractAddress: manifest.DirectoryContract,
		DirectoryApprove: &governanceworkflow.DirectoryApproveAction{Proposal: workflowProposal, ProposerNonce: manifest.ProposerNonce}})
	if err != nil {
		return Artifact{}, ErrInvalidManifest
	}
	proposalHash, err := governanceworkflow.DirectoryProposalHash(manifest.ChainID, manifest.DirectoryContract,
		manifest.WorkflowID, workflowProposal, manifest.ProposerNonce)
	if err != nil {
		return Artifact{}, ErrInvalidManifest
	}
	proposePayloadHash, err := proposalPayloadHash(workflowProposal, manifest.WorkflowID, bound.PayloadHash, manifest.ProposerNonce)
	if err != nil {
		return Artifact{}, ErrInvalidManifest
	}
	proposeSelector := selector("proposeVersion((uint64,uint64,bytes32,bytes32,bytes32,bytes32,uint8,uint64,bytes32,bytes32,uint256),(bytes32,address,uint256,bytes32,bytes4,bytes32,bytes32,uint256,uint64,uint64,uint64,bytes32),bytes)")
	publisherRole := crypto.Keccak256Hash([]byte("ASCP_DIRECTORY_PUBLISHER"))

	return Artifact{SchemaVersion: SchemaVersion, Manifest: manifest, CanonicalBlob: canonicalBlob,
		BlobContentHash: strings.ToLower(blobHash.Hex()), LocationsHash: strings.ToLower(locationsHash.Hex()),
		MerkleRoot: strings.ToLower(root.Hex()), Leaves: compiledLeaves,
		Proposal: Proposal{VersionID: manifest.VersionID, PreviousVersion: manifest.PreviousVersion, PreviousRoot: manifest.PreviousRoot,
			NewRoot: strings.ToLower(root.Hex()), BlobContentHash: strings.ToLower(blobHash.Hex()), LocationsHash: strings.ToLower(locationsHash.Hex()),
			ChangeClass: manifest.ChangeClass, RequestedActivatesAt: manifest.RequestedActivatesAt, WorkflowID: manifest.WorkflowID,
			WorkflowPayloadHash: bound.PayloadHash, ProposerNonce: manifest.ProposerNonce,
			ProposalHash: strings.ToLower(proposalHash.Hex()), ProposePayloadHash: strings.ToLower(proposePayloadHash.Hex())},
		PublisherAuthorization: PublisherAuthorizationBinding{ExpectedSigner: manifest.DirectoryPublisher,
			OrganizationDomain: manifest.OrganizationDomain, ContractAddress: manifest.DirectoryContract,
			ChainID: manifest.ChainID, AdminEpoch: manifest.DirectoryPublisherEpoch, AuthorityRole: strings.ToLower(publisherRole.Hex()),
			FunctionSelector: proposeSelector, PayloadHash: strings.ToLower(proposePayloadHash.Hex()),
			WorkflowID: manifest.WorkflowID, MaxWindowSeconds: MaxAuthorizationWindow},
		Approval: bound, FundingEnabled: false}, nil
}

func Verify(artifact Artifact, deploymentJSON []byte) error {
	expected, err := Compile(artifact.Manifest, deploymentJSON)
	if err != nil || artifact.FundingEnabled || artifact.SchemaVersion != SchemaVersion {
		return ErrInvalidArtifact
	}
	if !canonicalRaw(artifact.CanonicalBlob, expected.CanonicalBlob, &directoryBlob{}) ||
		!canonicalRaw(artifact.Approval.CanonicalAction, expected.Approval.CanonicalAction, &governanceworkflow.Action{}) {
		return ErrInvalidArtifact
	}
	artifact.CanonicalBlob = expected.CanonicalBlob
	artifact.Approval.CanonicalAction = expected.Approval.CanonicalAction
	if !reflect.DeepEqual(artifact, expected) {
		return ErrInvalidArtifact
	}
	for _, leaf := range artifact.Leaves {
		if err := directoryproof.Verify(artifact.MerkleRoot, common.HexToHash(leaf.Hash), leaf.Proof); err != nil {
			return ErrInvalidArtifact
		}
	}
	return nil
}

func validateManifest(manifest Manifest, deployment deploymentRecord) error {
	zeroHash := "0x" + strings.Repeat("0", 64)
	if manifest.SchemaVersion != SchemaVersion || !releaseIDPattern.MatchString(manifest.ReleaseID) ||
		manifest.Network != BaseSepoliaNetwork || manifest.ChainID != BaseSepoliaChainID || manifest.FundingEnabled ||
		!releaseIDPattern.MatchString(manifest.SourceDeployment.ReleaseID) || !commitPattern.MatchString(manifest.SourceDeployment.SourceCommit) ||
		manifest.SourceDeployment.ReleaseID != deployment.ReleaseID || manifest.SourceDeployment.SourceCommit != deployment.SourceCommit ||
		manifest.Network != deployment.Network || manifest.ChainID != deployment.ChainID ||
		manifest.OrganizationDomain != deployment.OrganizationDomain || manifest.DirectoryPublisher != deployment.Authorities.DirectoryPublisher ||
		manifest.DirectoryPublisherEpoch != 1 || manifest.DirectoryContract != deployment.directoryAddress() ||
		manifest.Asset != deployment.Asset || manifest.Asset.Address != BaseSepoliaUSDC || manifest.Asset.Symbol != "USDC" || manifest.Asset.Decimals != 6 ||
		!validHash(manifest.Asset.RuntimeCodeHash, false) ||
		manifest.VersionID != 1 || manifest.PreviousVersion != 0 || manifest.PreviousRoot != zeroHash ||
		manifest.ChangeClass != PayoutAuthorityChange || manifest.RequestedActivatesAt != 0 || !validHash(manifest.WorkflowID, false) ||
		!validAddress(manifest.DirectoryContract) || !validAddress(manifest.DirectoryPublisher) || !validHash(manifest.OrganizationDomain, false) {
		return ErrInvalidManifest
	}
	nonce, ok := new(big.Int).SetString(manifest.ProposerNonce, 10)
	if !ok || !decimalPattern.MatchString(manifest.ProposerNonce) || nonce.Sign() <= 0 || nonce.BitLen() > 256 {
		return ErrInvalidManifest
	}
	if err := validateLocationSyntax(manifest.Locations); err != nil || len(manifest.Sellers) == 0 || len(manifest.Sellers) > MaxSellers ||
		len(manifest.Resources) == 0 || len(manifest.Resources) > MaxResources {
		return ErrInvalidManifest
	}
	sellers := make(map[string]struct{}, len(manifest.Sellers))
	quoteKeys := make(map[string]struct{}, len(manifest.Sellers))
	resourceOwners := make(map[string]bool, len(manifest.Sellers))
	for _, seller := range manifest.Sellers {
		if seller.Status != 1 || seller.KeyEpoch == 0 || !validOrigin(seller.BaseURLOrigin) {
			return ErrInvalidManifest
		}
		if _, err := directoryproof.SellerLeafHash(seller.proofLeaf()); err != nil {
			return ErrInvalidManifest
		}
		if _, duplicate := sellers[seller.SellerID]; duplicate {
			return ErrInvalidManifest
		}
		if _, duplicate := quoteKeys[seller.QuoteSigningKey]; duplicate {
			return ErrInvalidManifest
		}
		sellers[seller.SellerID], quoteKeys[seller.QuoteSigningKey] = struct{}{}, struct{}{}
	}
	resources := make(map[string]struct{}, len(manifest.Resources))
	for _, resource := range manifest.Resources {
		if !resource.EscrowSupported || resource.DeclaredWorkTime == 0 || resource.VerificationBudgetSeconds == 0 {
			return ErrInvalidManifest
		}
		if _, exists := sellers[resource.SellerID]; !exists {
			return ErrInvalidManifest
		}
		if _, err := directoryproof.ResourceLeafHash(resource.proofLeaf()); err != nil {
			return ErrInvalidManifest
		}
		key := resource.SellerID + ":" + resource.ResourceID
		if _, duplicate := resources[key]; duplicate {
			return ErrInvalidManifest
		}
		resources[key] = struct{}{}
		resourceOwners[resource.SellerID] = true
	}
	for sellerID := range sellers {
		if !resourceOwners[sellerID] {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validateLocationSyntax(locations []string) error {
	if len(locations) != 2 || !sort.StringsAreSorted(locations) {
		return ErrInvalidManifest
	}
	schemes := map[string]bool{}
	for index, location := range locations {
		if index > 0 && locations[index-1] == location {
			return ErrInvalidManifest
		}
		var scheme, identifier string
		switch {
		case strings.HasPrefix(location, "ar://"):
			scheme, identifier = "ar", strings.TrimPrefix(location, "ar://")
		case strings.HasPrefix(location, "ipfs://"):
			scheme, identifier = "ipfs", strings.TrimPrefix(location, "ipfs://")
		default:
			return ErrInvalidManifest
		}
		if identifier == "" || strings.ContainsAny(identifier, "/?#@ 	\r\n") {
			return ErrInvalidManifest
		}
		if scheme == "ar" {
			decoded, err := base64.RawURLEncoding.DecodeString(identifier)
			if err != nil || len(identifier) != 43 || len(decoded) != 32 {
				return ErrInvalidManifest
			}
		} else {
			if len(identifier) != 59 || identifier[0] != 'b' || identifier != strings.ToLower(identifier) {
				return ErrInvalidManifest
			}
			decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(identifier[1:]))
			if err != nil || len(decoded) != 36 || !bytes.Equal(decoded[:4], []byte{0x01, 0x55, 0x12, 0x20}) {
				return ErrInvalidManifest
			}
		}
		schemes[scheme] = true
	}
	if len(schemes) < 2 {
		return ErrInvalidManifest
	}
	return nil
}

func validateLocations(locations []string, canonicalBlob []byte) error {
	if err := validateLocationSyntax(locations); err != nil {
		return err
	}
	expected := rawIPFSCID(canonicalBlob)
	for _, location := range locations {
		if strings.HasPrefix(location, "ipfs://") {
			if location != expected {
				return ErrInvalidManifest
			}
			return nil
		}
	}
	return ErrInvalidManifest
}

// CanonicalIPFSLocation returns the raw-block CIDv1 location that must contain
// the exact canonical blob. It performs no upload or network request.
func CanonicalIPFSLocation(manifest Manifest) (string, error) {
	sellers := append([]Seller(nil), manifest.Sellers...)
	resources := append([]Resource(nil), manifest.Resources...)
	sort.Slice(sellers, func(i, j int) bool { return sellers[i].SellerID < sellers[j].SellerID })
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].SellerID == resources[j].SellerID {
			return resources[i].ResourceID < resources[j].ResourceID
		}
		return resources[i].SellerID < resources[j].SellerID
	})
	blob := directoryBlob{SchemaVersion: SchemaVersion, ReleaseID: manifest.ReleaseID, Network: manifest.Network,
		ChainID: manifest.ChainID, DirectoryContract: manifest.DirectoryContract, Asset: manifest.Asset,
		VersionID: manifest.VersionID, PreviousVersion: manifest.PreviousVersion, PreviousRoot: manifest.PreviousRoot,
		Sellers: sellers, Resources: resources}
	canonical, err := json.Marshal(blob)
	if err != nil {
		return "", err
	}
	return rawIPFSCID(canonical), nil
}

func rawIPFSCID(content []byte) string {
	digest := sha256.Sum256(content)
	cid := append([]byte{0x01, 0x55, 0x12, 0x20}, digest[:]...)
	return "ipfs://b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(cid))
}

func (seller Seller) proofLeaf() directoryproof.SellerLeaf {
	originHash := crypto.Keccak256Hash([]byte(seller.BaseURLOrigin))
	return directoryproof.SellerLeaf{SellerID: seller.SellerID, PayoutAddress: seller.PayoutAddress,
		AckAuthority: seller.AckAuthority, QuoteSigningKey: seller.QuoteSigningKey, KeyEpoch: seller.KeyEpoch,
		BaseURLOriginHash: strings.ToLower(originHash.Hex()), Status: seller.Status}
}

func (resource Resource) proofLeaf() directoryproof.ResourceLeaf {
	return directoryproof.ResourceLeaf{SellerID: resource.SellerID, ResourceID: resource.ResourceID,
		Price: resource.Price, EscrowSupported: resource.EscrowSupported, VerificationSpecHash: resource.VerificationSpecHash,
		DeclaredWorkTime: resource.DeclaredWorkTime, VerificationBudgetSeconds: resource.VerificationBudgetSeconds}
}

type leafInput struct {
	kind, sellerID, resourceID string
	hash                       common.Hash
}

func merkle(inputs []leafInput) (common.Hash, map[int][]common.Hash) {
	proofs := make(map[int][]common.Hash, len(inputs))
	type node struct {
		hash    common.Hash
		indexes []int
	}
	layer := make([]node, len(inputs))
	for index, input := range inputs {
		layer[index] = node{hash: input.hash, indexes: []int{index}}
	}
	for len(layer) > 1 {
		next := make([]node, 0, (len(layer)+1)/2)
		for index := 0; index < len(layer); index += 2 {
			if index+1 == len(layer) {
				next = append(next, layer[index])
				continue
			}
			left, right := layer[index], layer[index+1]
			for _, leafIndex := range left.indexes {
				proofs[leafIndex] = append(proofs[leafIndex], right.hash)
			}
			for _, leafIndex := range right.indexes {
				proofs[leafIndex] = append(proofs[leafIndex], left.hash)
			}
			first, second := left.hash[:], right.hash[:]
			if bytes.Compare(first, second) > 0 {
				first, second = second, first
			}
			next = append(next, node{hash: crypto.Keccak256Hash(first, second), indexes: append(append([]int{}, left.indexes...), right.indexes...)})
		}
		layer = next
	}
	return layer[0].hash, proofs
}

func proposalPayloadHash(proposal governanceworkflow.DirectoryProposal, workflowID, workflowPayloadHash, proposerNonce string) (common.Hash, error) {
	nonce, ok := new(big.Int).SetString(proposerNonce, 10)
	if !ok {
		return common.Hash{}, ErrInvalidManifest
	}
	words := [][]byte{uintWord(proposal.VersionID), uintWord(proposal.PreviousVersion), hashWord(proposal.PreviousRoot),
		hashWord(proposal.NewRoot), hashWord(proposal.BlobContentHash), hashWord(proposal.LocationsHash), uintWord(uint64(proposal.ChangeClass)),
		uintWord(proposal.RequestedActivatesAt), hashWord(workflowID), hashWord(workflowPayloadHash), common.LeftPadBytes(nonce.Bytes(), 32)}
	return crypto.Keccak256Hash(bytes.Join(words, nil)), nil
}

func decodeDeployment(raw []byte) (deploymentRecord, error) {
	var record deploymentRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.directoryAddress() == "" {
		return deploymentRecord{}, ErrInvalidManifest
	}
	return record, nil
}

func (record deploymentRecord) directoryAddress() string {
	for _, contract := range record.Contracts {
		if contract.Name == "service_directory" {
			return contract.Address
		}
	}
	return ""
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func canonicalRaw(actual, expected json.RawMessage, target any) bool {
	if decodeStrict(actual, target) != nil {
		return false
	}
	actualCompact := new(bytes.Buffer)
	expectedCompact := new(bytes.Buffer)
	return json.Compact(actualCompact, actual) == nil && json.Compact(expectedCompact, expected) == nil && bytes.Equal(actualCompact.Bytes(), expectedCompact.Bytes())
}

func selector(signature string) string {
	return "0x" + hex.EncodeToString(crypto.Keccak256([]byte(signature))[:4])
}
func uintWord(value uint64) []byte {
	return common.LeftPadBytes(new(big.Int).SetUint64(value).Bytes(), 32)
}
func hashWord(value string) []byte { return common.HexToHash(value).Bytes() }
func validAddress(value string) bool {
	return len(value) == 42 && value == strings.ToLower(value) && common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}
func validHash(value string, allowZero bool) bool {
	if len(value) != 66 || value != strings.ToLower(value) || !strings.HasPrefix(value, "0x") {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32 && (allowZero || common.BytesToHash(decoded) != (common.Hash{}))
}

func validOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host != strings.ToLower(parsed.Host) || parsed.Port() != "" {
		return false
	}
	hostname := parsed.Hostname()
	if net.ParseIP(hostname) != nil || !strings.Contains(hostname, ".") {
		return false
	}
	for _, suffix := range []string{".internal", ".invalid", ".lan", ".local", ".localhost", ".home"} {
		if strings.HasSuffix(hostname, suffix) {
			return false
		}
	}
	return parsed.String() == value
}
