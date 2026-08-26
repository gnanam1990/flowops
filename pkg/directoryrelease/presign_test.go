package directoryrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

var presignNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

type blobFixtureFetcher struct {
	responses map[string]FetchedBlob
	err       error
}

type liveFixtureObserver struct {
	mutate func(*LivePresignEvidence)
	err    error
}

func (o liveFixtureObserver) Observe(_ context.Context, target LivePresignTarget) (LivePresignEvidence, error) {
	if o.err != nil {
		return LivePresignEvidence{}, o.err
	}
	observations := []LiveProviderObservation{
		{ProviderID: hash(90), BlockNumber: 100, BlockHash: hash(92), BlockTimestamp: uint64(presignNow.Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: zeroHash()},
		{ProviderID: hash(91), BlockNumber: 101, BlockHash: hash(93), BlockTimestamp: uint64(presignNow.Add(time.Second).Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: zeroHash()},
	}
	value := LivePresignEvidence{SchemaVersion: PresignSchemaVersion, Observations: observations}
	if o.mutate != nil {
		o.mutate(&value)
	}
	return value, nil
}

func (f blobFixtureFetcher) Fetch(_ context.Context, rawURL string, _ int64) (FetchedBlob, error) {
	if f.err != nil {
		return FetchedBlob{}, f.err
	}
	response, ok := f.responses[rawURL]
	if !ok {
		return FetchedBlob{}, errors.New("unexpected URL")
	}
	response.Body = append([]byte(nil), response.Body...)
	return response, nil
}

func TestBuildAndVerifyPresignBindsRemoteBytesAndAuthorization(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	request := PresignRequest{SchemaVersion: PresignSchemaVersion, AdminNonce: "41"}
	value, err := BuildPresign(context.Background(), artifact, deployment, gateways, request, fetcher, liveFixtureObserver{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPresign(value, artifact, deployment, presignNow); err != nil {
		t.Fatal(err)
	}
	if value.ExpectedSigner != artifact.Manifest.DirectoryPublisher || value.FundingEnabled || value.UnsignedCall.BroadcastAuthorized ||
		!value.UnsignedCall.SignatureRequired || value.Authorization.PayloadHash != artifact.Proposal.ProposePayloadHash ||
		value.Authorization.ValidBefore-value.Authorization.ValidAfter != 600 || len(value.RemoteEvidence.Copies) != 2 {
		t.Fatalf("unsafe presign boundary=%+v", value)
	}
	if value.PreparedAt != presignNow || value.RemoteEvidence.VerifiedAt != presignNow.Add(-time.Second) ||
		value.Authorization.ValidAfter != uint64(presignNow.Add(-30*time.Second).Unix()) ||
		value.Authorization.ValidBefore != uint64(presignNow.Add(570*time.Second).Unix()) {
		t.Fatalf("time binding=%+v evidence=%+v", value.Authorization, value.RemoteEvidence)
	}
	for _, copy := range value.RemoteEvidence.Copies {
		if copy.ContentKeccak != artifact.BlobContentHash || copy.ContentLength != int64(len(artifact.CanonicalBlob)) {
			t.Fatalf("remote copy=%+v", copy)
		}
	}
	golden := map[string]string{
		"artifact":  "0x4cb4482f646ba115d08d429a047423d9cd8a036f15089ee9eb2387d485a6e5db",
		"operation": "0x4305b3a7d59ec5cb8e073a531c8596fbdd5b770ca615c44907376b210ee0d4fd",
		"domain":    "0xf86c6231f4b1dd031f448f9eba710e62cdcb8a7ab1fec632893115cebe6dc7ed",
		"struct":    "0x2fd06c1fb43df2f9f7de50185e82726044abb3f100c3ca51387ff972d9691858",
		"digest":    "0x40849e12a6f903e1fbe06ce35bd6221718a22ff50d0e2201f3ece255f7847f58",
	}
	got := map[string]string{"artifact": value.ArtifactHash, "operation": value.Authorization.AdminOperationID,
		"domain": value.DomainSeparator, "struct": value.StructHash, "digest": value.Digest}
	if !reflect.DeepEqual(got, golden) {
		t.Fatalf("golden mismatch got=%v", got)
	}
}

func TestRemoteVerificationRejectsEveryContentAndGatewaySubstitution(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	firstURL, _ := gatewayURL(gateways, artifact.Manifest.Locations[0])
	mutations := map[string]func(*GatewayConfig, *blobFixtureFetcher){
		"same gateway":        func(g *GatewayConfig, _ *blobFixtureFetcher) { g.IPFSGatewayOrigin = g.ArweaveGatewayOrigin },
		"http gateway":        func(g *GatewayConfig, _ *blobFixtureFetcher) { g.IPFSGatewayOrigin = "http://ipfs.example.com" },
		"gateway path":        func(g *GatewayConfig, _ *blobFixtureFetcher) { g.IPFSGatewayOrigin += "/api" },
		"gateway credentials": func(g *GatewayConfig, _ *blobFixtureFetcher) { g.IPFSGatewayOrigin = "https://u:p@ipfs.example.com" },
		"gateway IP":          func(g *GatewayConfig, _ *blobFixtureFetcher) { g.IPFSGatewayOrigin = "https://127.0.0.1" },
		"gateway local":       func(g *GatewayConfig, _ *blobFixtureFetcher) { g.IPFSGatewayOrigin = "https://ipfs.local" },
		"wrong bytes": func(_ *GatewayConfig, f *blobFixtureFetcher) {
			changed := f.responses[firstURL]
			changed.Body = []byte("substituted")
			changed.ContentLength = int64(len(changed.Body))
			f.responses[firstURL] = changed
		},
		"wrong URL": func(_ *GatewayConfig, f *blobFixtureFetcher) {
			changed := f.responses[firstURL]
			changed.URL += "/redirect"
			f.responses[firstURL] = changed
		},
		"redirect status": func(_ *GatewayConfig, f *blobFixtureFetcher) {
			changed := f.responses[firstURL]
			changed.StatusCode = http.StatusFound
			f.responses[firstURL] = changed
		},
		"unsupported type": func(_ *GatewayConfig, f *blobFixtureFetcher) {
			changed := f.responses[firstURL]
			changed.ContentType = "text/html"
			f.responses[firstURL] = changed
		},
		"length mismatch": func(_ *GatewayConfig, f *blobFixtureFetcher) {
			changed := f.responses[firstURL]
			changed.ContentLength++
			f.responses[firstURL] = changed
		},
		"subsecond time": func(_ *GatewayConfig, f *blobFixtureFetcher) {
			changed := f.responses[firstURL]
			changed.FetchedAt = changed.FetchedAt.Add(time.Nanosecond)
			f.responses[firstURL] = changed
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changedGateways := gateways
			changedFetcher := cloneBlobFetcher(fetcher)
			mutate(&changedGateways, &changedFetcher)
			if _, err := VerifyRemote(context.Background(), artifact, deployment, changedGateways, changedFetcher); err == nil {
				t.Fatal("remote substitution accepted")
			}
		})
	}
	failed := cloneBlobFetcher(fetcher)
	failed.err = errors.New("offline")
	if _, err := VerifyRemote(context.Background(), artifact, deployment, gateways, failed); err == nil {
		t.Fatal("fetch failure accepted")
	}
}

func TestVerifyPresignRejectsEveryDerivedMutationAndExpiry(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	base, err := BuildPresign(context.Background(), artifact, deployment, gateways,
		PresignRequest{SchemaVersion: PresignSchemaVersion, AdminNonce: "41"}, fetcher, liveFixtureObserver{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*PresignPackage){
		"schema":        func(v *PresignPackage) { v.SchemaVersion++ },
		"release":       func(v *PresignPackage) { v.ReleaseID = "other-release" },
		"artifact":      func(v *PresignPackage) { v.ArtifactHash = hash(88) },
		"signer":        func(v *PresignPackage) { v.ExpectedSigner = address(88) },
		"prepared time": func(v *PresignPackage) { v.PreparedAt = v.PreparedAt.Add(time.Second) },
		"gateway":       func(v *PresignPackage) { v.Gateways.IPFSGatewayOrigin = "https://other.example.com" },
		"evidence hash": func(v *PresignPackage) { v.RemoteEvidence.Copies[0].ContentSHA256 = hash(88) },
		"evidence time": func(v *PresignPackage) {
			v.RemoteEvidence.Copies[0].FetchedAt = v.PreparedAt.Add(-MaximumRemoteEvidenceAge - time.Second)
		},
		"live provider":       func(v *PresignPackage) { v.LiveReadiness.Observations[0].ProviderID = hash(91) },
		"live nonce used":     func(v *PresignPackage) { v.LiveReadiness.Observations[0].AdminNonceUsed = true },
		"live operation used": func(v *PresignPackage) { v.LiveReadiness.Observations[0].AdminOperationUsed = true },
		"live predecessor":    func(v *PresignPackage) { v.LiveReadiness.Observations[0].CurrentVersion++ },
		"live timestamp":      func(v *PresignPackage) { v.LiveReadiness.Observations[0].BlockTimestamp = v.Authorization.ValidBefore },
		"proposal":            func(v *PresignPackage) { v.Proposal.NewRoot = hash(88) },
		"operation":           func(v *PresignPackage) { v.Authorization.AdminOperationID = hash(88) },
		"nonce":               func(v *PresignPackage) { v.Authorization.AdminNonce = "42" },
		"window":              func(v *PresignPackage) { v.Authorization.ValidBefore-- },
		"domain":              func(v *PresignPackage) { v.DomainSeparator = hash(88) },
		"struct":              func(v *PresignPackage) { v.StructHash = hash(88) },
		"digest":              func(v *PresignPackage) { v.Digest = hash(88) },
		"call":                func(v *PresignPackage) { v.UnsignedCall.BroadcastAuthorized = true },
		"funding":             func(v *PresignPackage) { v.FundingEnabled = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := clonePresign(t, base)
			mutate(&changed)
			if err := VerifyPresign(changed, artifact, deployment, presignNow); err == nil {
				t.Fatal("mutated presign accepted")
			}
		})
	}
	if err := VerifyPresign(base, artifact, deployment, time.Unix(int64(base.Authorization.ValidBefore), 0).UTC()); err == nil {
		t.Fatal("expired presign accepted")
	}
	if err := VerifyPresign(base, artifact, deployment, time.Unix(int64(base.Authorization.ValidAfter)-1, 0).UTC()); err == nil {
		t.Fatal("not-yet-valid presign accepted")
	}
	if err := VerifyPresign(base, artifact, deployment,
		base.RemoteEvidence.VerifiedAt.Add(MaximumRemoteEvidenceAge+time.Second)); err == nil {
		t.Fatal("stale remote evidence accepted")
	}
	staggered := clonePresign(t, base)
	staggered.RemoteEvidence.Copies[0].FetchedAt = staggered.PreparedAt.Add(-MaximumRemoteEvidenceAge)
	if err := VerifyPresign(staggered, artifact, deployment, staggered.PreparedAt.Add(time.Second)); err == nil {
		t.Fatal("stale first remote copy accepted behind a fresh second copy")
	}
}

func TestPresignStrictDecodersAndHTTPFetcherRejectUnsafeInputs(t *testing.T) {
	for name, raw := range map[string][]byte{
		"gateway unknown":   []byte(`{"schemaVersion":1,"ipfsGatewayOrigin":"https://ipfs.example.com","arweaveGatewayOrigin":"https://ar.example.com","unknown":true}`),
		"gateway duplicate": []byte(`{"schemaVersion":1,"ipfsGatewayOrigin":"https://reviewed.example.com","ipfsGatewayOrigin":"https://ipfs.example.com","arweaveGatewayOrigin":"https://ar.example.com"}`),
		"request trailing":  []byte(`{"schemaVersion":1,"adminNonce":"1"} {}`),
		"request duplicate": []byte(`{"schemaVersion":1,"adminNonce":"99","adminNonce":"1"}`),
		"request zero":      []byte(`{"schemaVersion":1,"adminNonce":"0"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(name, "gateway") {
				if _, err := DecodeGatewayConfig(raw); err == nil {
					t.Fatal("invalid gateway decoded")
				}
			} else if _, err := DecodePresignRequest(raw); err == nil {
				t.Fatal("invalid request decoded")
			}
		})
	}
	fetcher, err := NewHTTPBlobFetcher(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, rawURL := range []string{"http://example.com/blob", "https://127.0.0.1/blob", "https://localhost/blob"} {
		if _, err := fetcher.Fetch(context.Background(), rawURL, 10); err == nil {
			t.Fatalf("unsafe URL accepted: %s", rawURL)
		}
	}
	if _, err := NewHTTPBlobFetcher(31 * time.Second); err == nil {
		t.Fatal("unsafe timeout accepted")
	}
}

func TestPresignDecoderRejectsDuplicateNestedSigningFields(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	value, err := BuildPresign(context.Background(), artifact, deployment, gateways,
		PresignRequest{SchemaVersion: 1, AdminNonce: "41"}, fetcher, liveFixtureObserver{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	duplicate := []byte(strings.Replace(string(raw), `"digest":"`+value.Digest+`"`, `"digest":"`+hash(88)+`","digest":"`+value.Digest+`"`, 1))
	if _, err := DecodePresignPackage(duplicate); err == nil {
		t.Fatal("duplicate signing digest accepted")
	}
}

func TestBuildPresignFailsClosedOnLiveObserverErrorAndUsedNonce(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	request := PresignRequest{SchemaVersion: 1, AdminNonce: "41"}
	for name, observer := range map[string]LiveStateObserver{
		"observer error": liveFixtureObserver{err: errors.New("rpc unavailable")},
		"used nonce": liveFixtureObserver{mutate: func(value *LivePresignEvidence) {
			value.Observations[0].AdminNonceUsed = true
		}},
		"used operation": liveFixtureObserver{mutate: func(value *LivePresignEvidence) {
			value.Observations[1].AdminOperationUsed = true
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildPresign(context.Background(), artifact, deployment, gateways, request, fetcher, observer,
				func() time.Time { return presignNow }); err == nil {
				t.Fatal("unsafe live state produced a signing package")
			}
		})
	}
}

func TestBuildPresignRejectsEvidenceThatAgesWhileLiveQuorumRuns(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 1 {
			return presignNow
		}
		return presignNow.Add(MaximumRemoteEvidenceAge + time.Second)
	}
	if _, err := BuildPresign(context.Background(), artifact, deployment, gateways,
		PresignRequest{SchemaVersion: 1, AdminNonce: "41"}, fetcher, liveFixtureObserver{}, clock); err == nil {
		t.Fatal("presign accepted remote evidence that aged while live quorum ran")
	}
}

func TestHTTPBlobFetcherReadsOnlyExactBoundedUnencodedBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ok":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"release":"exact"}`))
		case "/encoded":
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Content-Encoding", "gzip")
			_, _ = writer.Write([]byte("encoded"))
		case "/html":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = writer.Write([]byte("html"))
		case "/large":
			writer.Header().Set("Content-Type", "application/octet-stream")
			_, _ = writer.Write(bytes.Repeat([]byte("x"), 33))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	fetcher := &HTTPBlobFetcher{timeout: time.Second, now: func() time.Time { return presignNow },
		clientFactory: func(raw string, _ time.Duration) (*url.URL, *http.Client, error) {
			parsed, err := url.Parse(raw)
			return parsed, server.Client(), err
		}}
	result, err := fetcher.Fetch(context.Background(), server.URL+"/ok", 32)
	if err != nil || string(result.Body) != `{"release":"exact"}` || result.ContentLength != int64(len(result.Body)) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, path := range []string{"/encoded", "/html", "/large", "/missing"} {
		if _, err := fetcher.Fetch(context.Background(), server.URL+path, 32); err == nil {
			t.Fatalf("unsafe response accepted: %s", path)
		}
	}
}

func presignFixture(t *testing.T) (Artifact, []byte, GatewayConfig, blobFixtureFetcher) {
	t.Helper()
	manifest, deployment := validFixture()
	artifact, err := Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	gateways := GatewayConfig{SchemaVersion: PresignSchemaVersion, IPFSGatewayOrigin: "https://ipfs.example.com", ArweaveGatewayOrigin: "https://arweave.example.com"}
	responses := make(map[string]FetchedBlob, 2)
	for _, location := range artifact.Manifest.Locations {
		gateway, err := gatewayURL(gateways, location)
		if err != nil {
			t.Fatal(err)
		}
		responses[gateway] = FetchedBlob{URL: gateway, FetchedAt: presignNow.Add(-time.Second), StatusCode: http.StatusOK,
			ContentType: "application/json", ContentLength: int64(len(artifact.CanonicalBlob)), Body: append([]byte(nil), artifact.CanonicalBlob...)}
	}
	return artifact, deployment, gateways, blobFixtureFetcher{responses: responses}
}

func cloneBlobFetcher(value blobFixtureFetcher) blobFixtureFetcher {
	cloned := blobFixtureFetcher{err: value.err, responses: make(map[string]FetchedBlob, len(value.responses))}
	for key, response := range value.responses {
		response.Body = append([]byte(nil), response.Body...)
		cloned.responses[key] = response
	}
	return cloned
}

func clonePresign(t *testing.T, value PresignPackage) PresignPackage {
	t.Helper()
	raw, _ := json.Marshal(value)
	decoded, err := DecodePresignPackage(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestRemoteCopiesHaveDifferentLocationsAndIdenticalBytes(t *testing.T) {
	artifact, deployment, gateways, fetcher := presignFixture(t)
	evidence, err := VerifyRemote(context.Background(), artifact, deployment, gateways, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Copies[0].Location == evidence.Copies[1].Location || evidence.Copies[0].ContentSHA256 != evidence.Copies[1].ContentSHA256 {
		t.Fatalf("copy independence=%+v", evidence.Copies)
	}
	if !bytes.Equal(artifact.CanonicalBlob, fetcher.responses[evidence.Copies[0].GatewayURL].Body) {
		t.Fatal("fixture lost exact byte binding")
	}
}
