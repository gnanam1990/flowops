package ascprails

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpevents"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

const (
	integrityAttestationVersion = 1
	maxIntegrityResponseBytes   = 64 << 10
)

var rawHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SnapshotSource is the read-only Base observer boundary used by the worker.
type SnapshotSource interface {
	Snapshot(context.Context) reconciliation.SnapshotResult
}

// QuorumChainClock derives one confirmed timestamp only from an exact Base
// anchor observed by the configured independent-provider quorum.
type QuorumChainClock struct {
	source      SnapshotSource
	chainID     uint64
	quorum      int
	maxChainLag time.Duration
	clock       func() time.Time
}

func NewQuorumChainClock(source SnapshotSource, chainID uint64, quorum int, maxChainLag time.Duration, clocks ...func() time.Time) (*QuorumChainClock, error) {
	if source == nil || chainID != 8453 && chainID != 84532 || quorum < 2 || quorum > 5 ||
		maxChainLag < time.Second || maxChainLag > 10*time.Minute || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfig
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &QuorumChainClock{source: source, chainID: chainID, quorum: quorum, maxChainLag: maxChainLag, clock: clock}, nil
}

func (c *QuorumChainClock) Confirmed(ctx context.Context, chainID uint64) (ChainObservation, error) {
	if chainID != c.chainID {
		return ChainObservation{}, ErrInvalidJob
	}
	result := c.source.Snapshot(ctx)
	type anchorGroup struct {
		number      uint64
		hash        string
		timestamp   time.Time
		observedAt  time.Time
		providers   []string
		observation []reconciliation.Observation
	}
	groups := make(map[string]*anchorGroup)
	seenProviders := make(map[string]struct{})
	for _, observation := range result.Observations {
		if observation.ChainID != chainID || !identifierPattern.MatchString(observation.Provider) ||
			observation.AnchorNumber == 0 || observation.HeadNumber < observation.AnchorNumber ||
			!nonZeroHash(observation.AnchorHash) || !nonZeroHash(observation.HeadHash) ||
			observation.AnchorTime.Unix() <= 0 || observation.HeadTime.Before(observation.AnchorTime) || observation.ObservedAt.IsZero() {
			continue
		}
		if _, duplicate := seenProviders[observation.Provider]; duplicate {
			return ChainObservation{}, errors.New("Base observer returned a duplicate provider")
		}
		seenProviders[observation.Provider] = struct{}{}
		key := fmt.Sprintf("%d\x00%s\x00%d", observation.AnchorNumber, observation.AnchorHash, observation.AnchorTime.UTC().Unix())
		group := groups[key]
		if group == nil {
			group = &anchorGroup{number: observation.AnchorNumber, hash: observation.AnchorHash,
				timestamp: observation.AnchorTime.UTC(), observedAt: observation.ObservedAt.UTC()}
			groups[key] = group
		}
		if observation.ObservedAt.Before(group.observedAt) {
			group.observedAt = observation.ObservedAt.UTC()
		}
		group.providers = append(group.providers, observation.Provider)
		group.observation = append(group.observation, observation)
	}
	var agreed *anchorGroup
	for _, group := range groups {
		if len(group.providers) < c.quorum {
			continue
		}
		if agreed != nil {
			return ChainObservation{}, errors.New("Base observer quorum is ambiguous")
		}
		agreed = group
	}
	if agreed == nil {
		return ChainObservation{}, errors.New("Base observer quorum is unavailable")
	}
	now := c.clock().UTC()
	if agreed.timestamp.After(now.Add(5*time.Second)) || now.Sub(agreed.timestamp) > c.maxChainLag {
		return ChainObservation{}, errors.New("Base observer quorum anchor is stale or in the future")
	}
	sort.Strings(agreed.providers)
	evidence := fmt.Sprintf("ASCP_CHAIN_CLOCK_V1\x00%d\x00%d\x00%s\x00%d\x00%s",
		chainID, agreed.number, agreed.hash, agreed.timestamp.Unix(), strings.Join(agreed.providers, ","))
	digest := sha256.Sum256([]byte(evidence))
	return ChainObservation{Timestamp: uint64(agreed.timestamp.Unix()),
		EvidenceDigest: "0x" + hex.EncodeToString(digest[:]), ObservedAt: agreed.observedAt}, nil
}

// IntegrityAttestation is signed by the isolated event-recovery verifier after
// it verifies the local event chain, signed checkpoint, WORM object, and remote
// monotonic head. The seller worker independently compares it to its live DB.
type IntegrityAttestation struct {
	SchemaVersion      int    `json:"schemaVersion"`
	State              string `json:"state"`
	LocalSequence      uint64 `json:"localSequence"`
	LocalEventHash     string `json:"localEventHash"`
	RemoteSequence     uint64 `json:"remoteSequence"`
	RemoteEventHash    string `json:"remoteEventHash"`
	CheckpointSequence uint64 `json:"checkpointSequence"`
	IssuedAtUnix       int64  `json:"issuedAtUnix"`
	ExpiresAtUnix      int64  `json:"expiresAtUnix"`
	KeyID              string `json:"keyId"`
	Signature          string `json:"signature"`
}

type integrityPayload struct {
	SchemaVersion      int    `json:"schemaVersion"`
	State              string `json:"state"`
	LocalSequence      uint64 `json:"localSequence"`
	LocalEventHash     string `json:"localEventHash"`
	RemoteSequence     uint64 `json:"remoteSequence"`
	RemoteEventHash    string `json:"remoteEventHash"`
	CheckpointSequence uint64 `json:"checkpointSequence"`
	IssuedAtUnix       int64  `json:"issuedAtUnix"`
	ExpiresAtUnix      int64  `json:"expiresAtUnix"`
	KeyID              string `json:"keyId"`
}

// SignIntegrityAttestation produces the exact detached signature accepted by
// AttestedIntegrityGate. The private key belongs only in the recovery verifier.
func SignIntegrityAttestation(attestation IntegrityAttestation, privateKey ed25519.PrivateKey) (IntegrityAttestation, error) {
	if len(privateKey) != ed25519.PrivateKeySize || attestation.Signature != "" {
		return IntegrityAttestation{}, ErrInvalidConfig
	}
	payload, err := integrityAttestationPayload(attestation)
	if err != nil {
		return IntegrityAttestation{}, err
	}
	attestation.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return attestation, nil
}

type IntegrityAttestationSource interface {
	Latest(context.Context) (IntegrityAttestation, error)
}

type EventHeadReader interface {
	Head(context.Context) (ascpevents.Head, error)
}

// PostgresEventHeadReader uses the migration-owned security-definer function
// that exposes only sequence and hash, not organization event payloads, to the
// least-privilege rails role.
type PostgresEventHeadReader struct{ db *sql.DB }

func NewPostgresEventHeadReader(db *sql.DB) (*PostgresEventHeadReader, error) {
	if db == nil {
		return nil, ErrInvalidConfig
	}
	return &PostgresEventHeadReader{db: db}, nil
}

func (r *PostgresEventHeadReader) Head(ctx context.Context) (ascpevents.Head, error) {
	var head ascpevents.Head
	if err := r.db.QueryRowContext(ctx, `SELECT sequence,event_hash FROM public.ascp_current_event_head()`).Scan(&head.Sequence, &head.EventHash); err != nil {
		return ascpevents.Head{}, fmt.Errorf("read restricted event head: %w", err)
	}
	return head, nil
}

// AttestedIntegrityGate fails closed unless the external recovery proof is
// current, fully checkpointed, correctly signed, and equal to the live DB head.
type AttestedIntegrityGate struct {
	head   EventHeadReader
	source IntegrityAttestationSource
	keys   map[string]ed25519.PublicKey
	maxTTL time.Duration
	clock  func() time.Time
}

func NewAttestedIntegrityGate(head EventHeadReader, source IntegrityAttestationSource, keys map[string]ed25519.PublicKey, maxTTL time.Duration, clocks ...func() time.Time) (*AttestedIntegrityGate, error) {
	if head == nil || source == nil || len(keys) == 0 || maxTTL < time.Second || maxTTL > 5*time.Minute ||
		len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfig
	}
	cloned := make(map[string]ed25519.PublicKey, len(keys))
	for keyID, key := range keys {
		if !identifierPattern.MatchString(keyID) || len(key) != ed25519.PublicKeySize {
			return nil, ErrInvalidConfig
		}
		cloned[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &AttestedIntegrityGate{head: head, source: source, keys: cloned, maxTTL: maxTTL, clock: clock}, nil
}

func (g *AttestedIntegrityGate) Check(ctx context.Context) error {
	attestation, err := g.source.Latest(ctx)
	if err != nil {
		return fmt.Errorf("read event-integrity attestation: %w", err)
	}
	payload, err := integrityAttestationPayload(attestation)
	if err != nil {
		return err
	}
	now := g.clock().UTC()
	issuedAt := time.Unix(attestation.IssuedAtUnix, 0).UTC()
	expiresAt := time.Unix(attestation.ExpiresAtUnix, 0).UTC()
	key := g.keys[attestation.KeyID]
	signature, decodeErr := base64.StdEncoding.DecodeString(attestation.Signature)
	if attestation.State != "VERIFIED" || attestation.LocalSequence == 0 ||
		attestation.LocalSequence != attestation.RemoteSequence || attestation.LocalSequence != attestation.CheckpointSequence ||
		attestation.LocalEventHash != attestation.RemoteEventHash || !nonZeroRawHash(attestation.LocalEventHash) ||
		issuedAt.After(now.Add(5*time.Second)) || !expiresAt.After(now) || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > g.maxTTL ||
		decodeErr != nil || base64.StdEncoding.EncodeToString(signature) != attestation.Signature || len(key) != ed25519.PublicKeySize ||
		len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, payload, signature) {
		return errors.New("event-integrity attestation is invalid or expired")
	}
	head, err := g.head.Head(ctx)
	if err != nil {
		return fmt.Errorf("read live event head: %w", err)
	}
	if head.Sequence != attestation.LocalSequence || head.EventHash != attestation.LocalEventHash {
		return errors.New("live event head differs from the verified recovery head")
	}
	return nil
}

func integrityAttestationPayload(attestation IntegrityAttestation) ([]byte, error) {
	if attestation.SchemaVersion != integrityAttestationVersion || !identifierPattern.MatchString(attestation.KeyID) {
		return nil, errors.New("event-integrity attestation contract is invalid")
	}
	payload, err := json.Marshal(integrityPayload{SchemaVersion: attestation.SchemaVersion, State: attestation.State,
		LocalSequence: attestation.LocalSequence, LocalEventHash: attestation.LocalEventHash,
		RemoteSequence: attestation.RemoteSequence, RemoteEventHash: attestation.RemoteEventHash,
		CheckpointSequence: attestation.CheckpointSequence, IssuedAtUnix: attestation.IssuedAtUnix,
		ExpiresAtUnix: attestation.ExpiresAtUnix, KeyID: attestation.KeyID})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(append([]byte("ASCP_EVENT_RECOVERY_ATTESTATION_V1\x00"), payload...))
	return digest[:], nil
}

func nonZeroRawHash(value string) bool {
	return rawHashPattern.MatchString(value) && value != strings.Repeat("0", 64)
}

// HTTPSIntegritySource reads a bounded strict JSON attestation without proxy or
// redirects. The signed payload, not TLS alone, is the authorization proof.
type HTTPSIntegritySource struct {
	url    string
	client *http.Client
}

func NewHTTPSIntegritySource(rawURL string, timeout time.Duration) (*HTTPSIntegritySource, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.RawQuery != "" || parsed.Port() != "" && parsed.Port() != "443" || timeout < time.Second || timeout > 30*time.Second {
		return nil, ErrInvalidConfig
	}
	if err := ValidateRestrictedURLShape(parsed.String()); err != nil {
		return nil, ErrInvalidConfig
	}
	transport, err := NewRestrictedTransport()
	if err != nil {
		return nil, fmt.Errorf("create restricted event-integrity transport: %w", err)
	}
	return &HTTPSIntegritySource{url: parsed.String(), client: &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *HTTPSIntegritySource) Latest(ctx context.Context) (IntegrityAttestation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return IntegrityAttestation{}, err
	}
	request.Host = request.URL.Host
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return IntegrityAttestation{}, errors.New("event-integrity endpoint request failed")
	}
	defer func() { _ = response.Body.Close() }()
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || strings.ToLower(mediaType) != "application/json" {
		return IntegrityAttestation{}, errors.New("event-integrity endpoint returned an invalid response")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxIntegrityResponseBytes+1))
	if err != nil || len(raw) > maxIntegrityResponseBytes {
		return IntegrityAttestation{}, errors.New("event-integrity response could not be read safely")
	}
	if err := rejectDuplicateAttestationKeys(raw); err != nil {
		return IntegrityAttestation{}, errors.New("event-integrity response contains duplicate or invalid JSON fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var attestation IntegrityAttestation
	if err := decoder.Decode(&attestation); err != nil {
		return IntegrityAttestation{}, errors.New("event-integrity response is not strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return IntegrityAttestation{}, errors.New("event-integrity response must contain one JSON value")
	}
	return attestation, nil
}

func rejectDuplicateAttestationKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("event-integrity response must be an object")
	}
	seen := make(map[string]struct{})
	allowed := map[string]struct{}{
		"schemaVersion": {}, "state": {}, "localSequence": {}, "localEventHash": {},
		"remoteSequence": {}, "remoteEventHash": {}, "checkpointSequence": {},
		"issuedAtUnix": {}, "expiresAtUnix": {}, "keyId": {}, "signature": {},
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("event-integrity response key is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("event-integrity response key is duplicated")
		}
		if _, canonical := allowed[key]; !canonical {
			return errors.New("event-integrity response key is not canonical")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

var _ ChainClock = (*QuorumChainClock)(nil)
var _ IntegrityGate = (*AttestedIntegrityGate)(nil)
