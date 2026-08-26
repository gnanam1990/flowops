package directoryrelease

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascprails"
	"github.com/gnanam1990/flowops/pkg/adminauthorization"
)

const (
	PresignSchemaVersion     = 1
	MaximumRemoteEvidenceAge = 2 * time.Minute
	MinimumPresignRemaining  = 2 * time.Minute
	defaultFetchTimeout      = 20 * time.Second
	validityBackdate         = 30 * time.Second
)

var (
	ErrRemoteContent  = errors.New("directory release remote content verification failed")
	ErrInvalidPresign = errors.New("invalid directory release presign package")
)

type GatewayConfig struct {
	SchemaVersion        int    `json:"schemaVersion"`
	IPFSGatewayOrigin    string `json:"ipfsGatewayOrigin"`
	ArweaveGatewayOrigin string `json:"arweaveGatewayOrigin"`
}

type PresignRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	AdminNonce    string `json:"adminNonce"`
}

type FetchedBlob struct {
	URL           string
	FetchedAt     time.Time
	StatusCode    int
	ContentType   string
	ContentLength int64
	Body          []byte
}

type BlobFetcher interface {
	Fetch(context.Context, string, int64) (FetchedBlob, error)
}

type RemoteCopyEvidence struct {
	Location      string    `json:"location"`
	GatewayURL    string    `json:"gatewayUrl"`
	FetchedAt     time.Time `json:"fetchedAt"`
	ContentType   string    `json:"contentType"`
	ContentLength int64     `json:"contentLength"`
	ContentSHA256 string    `json:"contentSha256"`
	ContentKeccak string    `json:"contentKeccak256"`
}

type RemoteEvidence struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	ReleaseID       string               `json:"releaseId"`
	ArtifactHash    string               `json:"artifactHash"`
	BlobContentHash string               `json:"blobContentHash"`
	VerifiedAt      time.Time            `json:"verifiedAt"`
	Copies          []RemoteCopyEvidence `json:"copies"`
}

type ContractProposal struct {
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
}

type UnsignedPublisherCall struct {
	ContractAddress     string `json:"contractAddress"`
	FunctionSelector    string `json:"functionSelector"`
	Method              string `json:"method"`
	Value               string `json:"value"`
	SignatureRequired   bool   `json:"signatureRequired"`
	BroadcastAuthorized bool   `json:"broadcastAuthorized"`
}

type PresignPackage struct {
	SchemaVersion   int                              `json:"schemaVersion"`
	ReleaseID       string                           `json:"releaseId"`
	ArtifactHash    string                           `json:"artifactHash"`
	ExpectedSigner  string                           `json:"expectedSigner"`
	PreparedAt      time.Time                        `json:"preparedAt"`
	Gateways        GatewayConfig                    `json:"gateways"`
	RemoteEvidence  RemoteEvidence                   `json:"remoteEvidence"`
	LiveReadiness   LivePresignEvidence              `json:"liveReadiness"`
	Proposal        ContractProposal                 `json:"proposal"`
	Authorization   adminauthorization.Authorization `json:"authorization"`
	DomainSeparator string                           `json:"domainSeparator"`
	StructHash      string                           `json:"structHash"`
	Digest          string                           `json:"digest"`
	UnsignedCall    UnsignedPublisherCall            `json:"unsignedCall"`
	FundingEnabled  bool                             `json:"fundingEnabled"`
}

type HTTPBlobFetcher struct {
	timeout       time.Duration
	now           func() time.Time
	clientFactory func(string, time.Duration) (*url.URL, *http.Client, error)
}

func NewHTTPBlobFetcher(timeout time.Duration) (*HTTPBlobFetcher, error) {
	if timeout == 0 {
		timeout = defaultFetchTimeout
	}
	if timeout < time.Second || timeout > 30*time.Second {
		return nil, ErrRemoteContent
	}
	return &HTTPBlobFetcher{timeout: timeout, now: time.Now, clientFactory: ascprails.NewRestrictedHTTPSClient}, nil
}

func (f *HTTPBlobFetcher) Fetch(ctx context.Context, rawURL string, maximumBytes int64) (FetchedBlob, error) {
	if f == nil || f.now == nil || f.clientFactory == nil || maximumBytes < 1 || maximumBytes > MaxCanonicalBlobBytes {
		return FetchedBlob{}, ErrRemoteContent
	}
	endpoint, client, err := f.clientFactory(rawURL, f.timeout)
	if err != nil {
		return FetchedBlob{}, ErrRemoteContent
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return FetchedBlob{}, ErrRemoteContent
	}
	request.Host = request.URL.Host
	request.Header.Set("Accept", "application/octet-stream, application/json, text/plain")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "FlowOps-Directory-Release/1.0")
	response, err := client.Do(request)
	if err != nil {
		return FetchedBlob{}, ErrRemoteContent
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.Request.URL.String() != endpoint.String() ||
		(response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") ||
		response.ContentLength > maximumBytes {
		return FetchedBlob{}, ErrRemoteContent
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !allowedBlobMediaType(strings.ToLower(mediaType)) {
		return FetchedBlob{}, ErrRemoteContent
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBytes+1))
	if err != nil || len(body) == 0 || int64(len(body)) > maximumBytes ||
		(response.ContentLength >= 0 && response.ContentLength != int64(len(body))) {
		return FetchedBlob{}, ErrRemoteContent
	}
	return FetchedBlob{URL: endpoint.String(), FetchedAt: f.now().UTC().Truncate(time.Second), StatusCode: response.StatusCode,
		ContentType: strings.ToLower(mediaType), ContentLength: int64(len(body)), Body: body}, nil
}

func DecodeGatewayConfig(raw []byte) (GatewayConfig, error) {
	var config GatewayConfig
	if err := decodeStrict(raw, &config); err != nil || validateGateways(config) != nil {
		return GatewayConfig{}, ErrRemoteContent
	}
	return config, nil
}

func DecodePresignRequest(raw []byte) (PresignRequest, error) {
	var request PresignRequest
	if err := decodeStrict(raw, &request); err != nil || validatePresignRequest(request) != nil {
		return PresignRequest{}, ErrInvalidPresign
	}
	return request, nil
}

func DecodePresignPackage(raw []byte) (PresignPackage, error) {
	var value PresignPackage
	if err := decodeStrict(raw, &value); err != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	return value, nil
}

func VerifyRemote(ctx context.Context, artifact Artifact, deploymentJSON []byte, gateways GatewayConfig, fetcher BlobFetcher) (RemoteEvidence, error) {
	if fetcher == nil || Verify(artifact, deploymentJSON) != nil || validateGateways(gateways) != nil {
		return RemoteEvidence{}, ErrRemoteContent
	}
	artifactHash, err := hashArtifact(artifact)
	if err != nil {
		return RemoteEvidence{}, ErrRemoteContent
	}
	copies := make([]RemoteCopyEvidence, 0, len(artifact.Manifest.Locations))
	for _, location := range artifact.Manifest.Locations {
		gatewayURL, buildErr := gatewayURL(gateways, location)
		if buildErr != nil {
			return RemoteEvidence{}, ErrRemoteContent
		}
		fetched, fetchErr := fetcher.Fetch(ctx, gatewayURL, int64(len(artifact.CanonicalBlob)))
		if fetchErr != nil || fetched.URL != gatewayURL || fetched.StatusCode != http.StatusOK ||
			fetched.ContentLength != int64(len(fetched.Body)) || !bytes.Equal(fetched.Body, artifact.CanonicalBlob) ||
			!canonicalSecond(fetched.FetchedAt) || !allowedBlobMediaType(fetched.ContentType) {
			return RemoteEvidence{}, ErrRemoteContent
		}
		sha := sha256.Sum256(fetched.Body)
		copies = append(copies, RemoteCopyEvidence{Location: location, GatewayURL: gatewayURL, FetchedAt: fetched.FetchedAt,
			ContentType: fetched.ContentType, ContentLength: fetched.ContentLength,
			ContentSHA256: "0x" + hex.EncodeToString(sha[:]), ContentKeccak: strings.ToLower(crypto.Keccak256Hash(fetched.Body).Hex())})
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i].Location < copies[j].Location })
	verifiedAt := copies[0].FetchedAt
	for _, copy := range copies[1:] {
		if copy.FetchedAt.After(verifiedAt) {
			verifiedAt = copy.FetchedAt
		}
	}
	return RemoteEvidence{SchemaVersion: PresignSchemaVersion, ReleaseID: artifact.Manifest.ReleaseID,
		ArtifactHash: artifactHash, BlobContentHash: artifact.BlobContentHash, VerifiedAt: verifiedAt, Copies: copies}, nil
}

func BuildPresign(ctx context.Context, artifact Artifact, deploymentJSON []byte, gateways GatewayConfig, request PresignRequest,
	fetcher BlobFetcher, observer LiveStateObserver, clock func() time.Time,
) (PresignPackage, error) {
	if observer == nil || clock == nil || validatePresignRequest(request) != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	evidence, err := VerifyRemote(ctx, artifact, deploymentJSON, gateways, fetcher)
	if err != nil {
		return PresignPackage{}, err
	}
	preparedAt := clock().UTC().Truncate(time.Second)
	value, err := buildPresignFromEvidence(artifact, deploymentJSON, gateways, evidence, request, preparedAt)
	if err != nil {
		return PresignPackage{}, err
	}
	live, err := observer.Observe(ctx, liveTarget(value))
	if err != nil || verifyLiveEvidence(liveTarget(value), live) != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	value.LiveReadiness = live
	if VerifyPresign(value, artifact, deploymentJSON, clock().UTC().Truncate(time.Second)) != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	return value, nil
}

func VerifyPresign(value PresignPackage, artifact Artifact, deploymentJSON []byte, at time.Time) error {
	if !canonicalSecond(at.UTC().Truncate(time.Second)) {
		return ErrInvalidPresign
	}
	request := PresignRequest{SchemaVersion: PresignSchemaVersion, AdminNonce: value.Authorization.AdminNonce}
	expected, err := buildPresignFromEvidence(artifact, deploymentJSON, value.Gateways, value.RemoteEvidence, request, value.PreparedAt)
	if err != nil || verifyLiveEvidence(liveTarget(expected), value.LiveReadiness) != nil {
		return ErrInvalidPresign
	}
	expected.LiveReadiness = value.LiveReadiness
	if !reflect.DeepEqual(value, expected) {
		return ErrInvalidPresign
	}
	now := uint64(at.UTC().Unix())
	if at.Before(value.RemoteEvidence.VerifiedAt) || at.Sub(value.RemoteEvidence.VerifiedAt) > MaximumRemoteEvidenceAge ||
		now < value.Authorization.ValidAfter || now >= value.Authorization.ValidBefore ||
		value.Authorization.ValidBefore-now < uint64(MinimumPresignRemaining/time.Second) {
		return ErrInvalidPresign
	}
	for _, copy := range value.RemoteEvidence.Copies {
		if at.Before(copy.FetchedAt) || at.Sub(copy.FetchedAt) > MaximumRemoteEvidenceAge {
			return ErrInvalidPresign
		}
	}
	return nil
}

func buildPresignFromEvidence(artifact Artifact, deploymentJSON []byte, gateways GatewayConfig, evidence RemoteEvidence,
	request PresignRequest, preparedAt time.Time,
) (PresignPackage, error) {
	if Verify(artifact, deploymentJSON) != nil || validateGateways(gateways) != nil || validatePresignRequest(request) != nil ||
		!canonicalSecond(preparedAt) || verifyRemoteEvidence(artifact, gateways, evidence, preparedAt) != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	artifactHash, err := hashArtifact(artifact)
	if err != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	nonce, _ := new(big.Int).SetString(request.AdminNonce, 10)
	operationID := crypto.Keccak256Hash([]byte("ASCP_DIRECTORY_PRESIGN_OPERATION_V1"), common.HexToHash(artifactHash).Bytes(), common.LeftPadBytes(nonce.Bytes(), 32))
	validAfter := preparedAt.Add(-validityBackdate)
	authorization := adminauthorization.Authorization{OrgDomain: artifact.PublisherAuthorization.OrganizationDomain,
		ContractAddress: artifact.PublisherAuthorization.ContractAddress, ChainID: fmt.Sprint(artifact.PublisherAuthorization.ChainID),
		AuthorityRole: artifact.PublisherAuthorization.AuthorityRole, FunctionSelector: artifact.PublisherAuthorization.FunctionSelector,
		PayloadHash: artifact.PublisherAuthorization.PayloadHash, AdminOperationID: strings.ToLower(operationID.Hex()),
		AdminNonce: request.AdminNonce, AdminEpoch: artifact.PublisherAuthorization.AdminEpoch,
		ValidAfter: uint64(validAfter.Unix()), ValidBefore: uint64(validAfter.Add(time.Duration(adminauthorization.MaximumWindowSeconds) * time.Second).Unix()),
		WorkflowID: artifact.PublisherAuthorization.WorkflowID}
	domain, domainErr := adminauthorization.DomainSeparator(authorization.ChainID, authorization.ContractAddress)
	structHash, structErr := authorization.StructHash()
	digest, digestErr := authorization.Digest()
	if domainErr != nil || structErr != nil || digestErr != nil {
		return PresignPackage{}, ErrInvalidPresign
	}
	proposal := ContractProposal{VersionID: artifact.Proposal.VersionID, PreviousVersion: artifact.Proposal.PreviousVersion,
		PreviousRoot: artifact.Proposal.PreviousRoot, NewRoot: artifact.Proposal.NewRoot,
		BlobContentHash: artifact.Proposal.BlobContentHash, LocationsHash: artifact.Proposal.LocationsHash,
		ChangeClass: artifact.Proposal.ChangeClass, RequestedActivatesAt: artifact.Proposal.RequestedActivatesAt,
		WorkflowID: artifact.Proposal.WorkflowID, WorkflowPayloadHash: artifact.Proposal.WorkflowPayloadHash,
		ProposerNonce: artifact.Proposal.ProposerNonce}
	return PresignPackage{SchemaVersion: PresignSchemaVersion, ReleaseID: artifact.Manifest.ReleaseID, ArtifactHash: artifactHash,
		ExpectedSigner: artifact.PublisherAuthorization.ExpectedSigner, PreparedAt: preparedAt, Gateways: gateways,
		RemoteEvidence: evidence, Proposal: proposal, Authorization: authorization,
		DomainSeparator: strings.ToLower(domain.Hex()), StructHash: strings.ToLower(structHash.Hex()), Digest: strings.ToLower(digest.Hex()),
		UnsignedCall: UnsignedPublisherCall{ContractAddress: artifact.Manifest.DirectoryContract,
			FunctionSelector: artifact.PublisherAuthorization.FunctionSelector,
			Method:           "proposeVersion(DirectoryProposal,AdminActionAuthorization,bytes)", Value: "0",
			SignatureRequired: true, BroadcastAuthorized: false}, FundingEnabled: false}, nil
}

func verifyRemoteEvidence(artifact Artifact, gateways GatewayConfig, evidence RemoteEvidence, preparedAt time.Time) error {
	artifactHash, err := hashArtifact(artifact)
	if err != nil || evidence.SchemaVersion != PresignSchemaVersion || evidence.ReleaseID != artifact.Manifest.ReleaseID ||
		evidence.ArtifactHash != artifactHash || evidence.BlobContentHash != artifact.BlobContentHash ||
		len(evidence.Copies) != len(artifact.Manifest.Locations) || !canonicalSecond(evidence.VerifiedAt) ||
		evidence.VerifiedAt.After(preparedAt) || preparedAt.Sub(evidence.VerifiedAt) > MaximumRemoteEvidenceAge {
		return ErrInvalidPresign
	}
	sha := sha256.Sum256(artifact.CanonicalBlob)
	wantSHA := "0x" + hex.EncodeToString(sha[:])
	wantKeccak := strings.ToLower(crypto.Keccak256Hash(artifact.CanonicalBlob).Hex())
	latest := time.Time{}
	for index, copy := range evidence.Copies {
		location := artifact.Manifest.Locations[index]
		wantURL, buildErr := gatewayURL(gateways, location)
		if buildErr != nil || copy.Location != location || copy.GatewayURL != wantURL || !canonicalSecond(copy.FetchedAt) ||
			copy.FetchedAt.After(preparedAt) || preparedAt.Sub(copy.FetchedAt) > MaximumRemoteEvidenceAge ||
			copy.ContentLength != int64(len(artifact.CanonicalBlob)) || copy.ContentSHA256 != wantSHA ||
			copy.ContentKeccak != wantKeccak || !allowedBlobMediaType(copy.ContentType) {
			return ErrInvalidPresign
		}
		if copy.FetchedAt.After(latest) {
			latest = copy.FetchedAt
		}
	}
	if !latest.Equal(evidence.VerifiedAt) {
		return ErrInvalidPresign
	}
	return nil
}

func hashArtifact(artifact Artifact) (string, error) {
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return "", err
	}
	return strings.ToLower(crypto.Keccak256Hash(encoded).Hex()), nil
}

func validateGateways(config GatewayConfig) error {
	if config.SchemaVersion != PresignSchemaVersion || !validGatewayOrigin(config.IPFSGatewayOrigin) ||
		!validGatewayOrigin(config.ArweaveGatewayOrigin) || config.IPFSGatewayOrigin == config.ArweaveGatewayOrigin {
		return ErrRemoteContent
	}
	return nil
}

func validGatewayOrigin(raw string) bool {
	return validOrigin(raw)
}

func gatewayURL(config GatewayConfig, location string) (string, error) {
	var origin, path string
	switch {
	case strings.HasPrefix(location, "ipfs://"):
		origin, path = config.IPFSGatewayOrigin, "/ipfs/"+strings.TrimPrefix(location, "ipfs://")
	case strings.HasPrefix(location, "ar://"):
		origin, path = config.ArweaveGatewayOrigin, "/"+strings.TrimPrefix(location, "ar://")
	default:
		return "", ErrRemoteContent
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", ErrRemoteContent
	}
	parsed.Path = path
	return parsed.String(), nil
}

func validatePresignRequest(request PresignRequest) error {
	value, ok := new(big.Int).SetString(request.AdminNonce, 10)
	if request.SchemaVersion != PresignSchemaVersion || !ok || !decimalPattern.MatchString(request.AdminNonce) || value.Sign() <= 0 || value.BitLen() > 256 {
		return ErrInvalidPresign
	}
	return nil
}

func allowedBlobMediaType(value string) bool {
	return value == "application/octet-stream" || value == "application/json" || value == "text/plain"
}

func canonicalSecond(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond() == 0 && value.Unix() >= 0
}
