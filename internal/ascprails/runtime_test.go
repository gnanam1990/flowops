package ascprails

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpevents"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

func TestQuorumChainClockRequiresExactIndependentAnchor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	anchorTime := now.Add(-2 * time.Second)
	base := func(provider string) reconciliation.Observation {
		return reconciliation.Observation{Provider: provider, ChainID: 84532, HeadNumber: 102,
			HeadHash: testHash("81"), HeadTime: now.Add(-time.Second), AnchorNumber: 100,
			AnchorHash: testHash("82"), AnchorTime: anchorTime, ObservedAt: now}
	}
	source := staticSnapshotSource{result: reconciliation.SnapshotResult{Observations: []reconciliation.Observation{base("rpc-a"), base("rpc-b")}}}
	clock, err := NewQuorumChainClock(source, 84532, 2, 30*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	observation, err := clock.Confirmed(t.Context(), 84532)
	if err != nil || observation.Timestamp != uint64(anchorTime.Unix()) || !nonZeroHash(observation.EvidenceDigest) || !observation.ObservedAt.Equal(now) {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}

	changed := base("rpc-b")
	changed.AnchorHash = testHash("83")
	clock.source = staticSnapshotSource{result: reconciliation.SnapshotResult{Observations: []reconciliation.Observation{base("rpc-a"), changed}}}
	if _, err := clock.Confirmed(t.Context(), 84532); err == nil {
		t.Fatal("disagreeing providers produced confirmed chain time")
	}
	clock.source = staticSnapshotSource{result: reconciliation.SnapshotResult{Observations: []reconciliation.Observation{base("rpc-a"), base("rpc-a")}}}
	if _, err := clock.Confirmed(t.Context(), 84532); err == nil {
		t.Fatal("duplicate provider satisfied quorum")
	}
	if _, err := clock.Confirmed(t.Context(), 8453); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("wrong chain error=%v", err)
	}
	stale := base("rpc-a")
	stale.AnchorTime = now.Add(-time.Minute)
	otherStale := stale
	otherStale.Provider = "rpc-b"
	clock.source = staticSnapshotSource{result: reconciliation.SnapshotResult{Observations: []reconciliation.Observation{stale, otherStale}}}
	if _, err := clock.Confirmed(t.Context(), 84532); err == nil {
		t.Fatal("stale exact quorum produced chain time")
	}
	futureHead := base("rpc-a")
	futureHead.HeadTime = now.Add(6 * time.Second)
	otherFutureHead := futureHead
	otherFutureHead.Provider = "rpc-b"
	clock.source = staticSnapshotSource{result: reconciliation.SnapshotResult{Observations: []reconciliation.Observation{futureHead, otherFutureHead}}}
	if _, err := clock.Confirmed(t.Context(), 84532); err == nil {
		t.Fatal("future head exact quorum produced chain time")
	}
}

func TestAttestedIntegrityGateBindsSignatureExpiryAndLiveHead(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("k", ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	attestation, err := SignIntegrityAttestation(IntegrityAttestation{SchemaVersion: 1, State: "VERIFIED",
		LocalSequence: 9, LocalEventHash: hash, RemoteSequence: 9, RemoteEventHash: hash,
		CheckpointSequence: 9, IssuedAtUnix: now.Add(-time.Second).Unix(), ExpiresAtUnix: now.Add(time.Minute).Unix(), KeyID: "recovery-key-1"}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	head := staticEventHead{head: ascpevents.Head{Sequence: 9, EventHash: hash}}
	source := &staticIntegritySource{attestation: attestation}
	gate, err := NewAttestedIntegrityGate(head, source, map[string]ed25519.PublicKey{"recovery-key-1": publicKey}, 2*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	source.attestation = attestation
	source.attestation.KeyID = "recovery-key-2"
	if err := gate.Check(t.Context()); err == nil {
		t.Fatal("unknown key ID passed integrity gate")
	}
	source.attestation = attestation
	source.attestation.Signature += "=="
	if err := gate.Check(t.Context()); err == nil {
		t.Fatal("non-canonical signature encoding passed integrity gate")
	}
	source.attestation = attestation
	source.attestation.State = "PENDING"
	if err := gate.Check(t.Context()); err == nil {
		t.Fatal("unverified state passed integrity gate")
	}

	source.attestation = attestation
	source.attestation.LocalSequence++
	if err := gate.Check(t.Context()); err == nil {
		t.Fatal("substituted sequence passed signature verification")
	}
	source.attestation = attestation
	head.head.Sequence++
	gate.head = head
	if err := gate.Check(t.Context()); err == nil {
		t.Fatal("live-head drift passed integrity gate")
	}
	head.head.Sequence = 9
	gate.head = head
	gate.clock = func() time.Time { return now.Add(2 * time.Minute) }
	if err := gate.Check(t.Context()); err == nil {
		t.Fatal("expired attestation passed integrity gate")
	}
}

func TestHTTPSIntegritySourceRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"schemaVersion":1,"unknown":true}`, `{} {}`, `{"schemaVersion":1,"schemaVersion":1}`, `{"SchemaVersion":1}`} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(body))
		}))
		source := &HTTPSIntegritySource{url: server.URL, client: server.Client()}
		if _, err := source.Latest(t.Context()); err == nil {
			server.Close()
			t.Fatalf("invalid response accepted: %s", body)
		}
		server.Close()
	}
}

func TestHTTPSIntegritySourceRefusesPrivateNetworkDial(t *testing.T) {
	source, err := NewHTTPSIntegritySource("https://127.0.0.1/latest", time.Second)
	if err == nil || source != nil {
		t.Fatalf("private integrity endpoint source=%v error=%v", source, err)
	}
}

func TestHTTPSIntegritySourceRedactsRequestURLFromErrors(t *testing.T) {
	source := &HTTPSIntegritySource{url: "https://integrity.example/secret-path", client: &http.Client{Transport: failingIntegrityTransport{}}}
	_, err := source.Latest(t.Context())
	if err == nil || strings.Contains(err.Error(), "secret-path") || err.Error() != "event-integrity endpoint request failed" {
		t.Fatalf("request error was not redacted: %v", err)
	}
}

func TestHTTPSIntegritySourceRoundTripsSignedContract(t *testing.T) {
	attestation := IntegrityAttestation{SchemaVersion: 1, State: "VERIFIED", LocalSequence: 1,
		LocalEventHash: strings.Repeat("a", 64), RemoteSequence: 1, RemoteEventHash: strings.Repeat("a", 64),
		CheckpointSequence: 1, IssuedAtUnix: 1, ExpiresAtUnix: 2, KeyID: "recovery-key-1", Signature: "signature"}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "application/json" {
			t.Error("missing JSON accept header")
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(attestation)
	}))
	defer server.Close()
	source := &HTTPSIntegritySource{url: server.URL, client: server.Client()}
	got, err := source.Latest(context.Background())
	if err != nil || got != attestation {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

type staticSnapshotSource struct{ result reconciliation.SnapshotResult }

func (s staticSnapshotSource) Snapshot(context.Context) reconciliation.SnapshotResult {
	return s.result
}

type staticIntegritySource struct {
	attestation IntegrityAttestation
	err         error
}

func (s *staticIntegritySource) Latest(context.Context) (IntegrityAttestation, error) {
	return s.attestation, s.err
}

type staticEventHead struct {
	head ascpevents.Head
	err  error
}

func (s staticEventHead) Head(context.Context) (ascpevents.Head, error) { return s.head, s.err }

type failingIntegrityTransport struct{}

func (failingIntegrityTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed for secret-path")
}
