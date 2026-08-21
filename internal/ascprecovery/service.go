// Package ascprecovery verifies the externally checkpointed ASCP event chain
// and publishes a short-lived signed proof for isolated controlled-effect workers.
package ascprecovery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"reflect"
	"regexp"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpevents"
	"github.com/gnanam1990/flowops/internal/ascprails"
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

var (
	ErrInvalidConfig    = errors.New("invalid event-recovery configuration")
	ErrRecoveryUnproved = errors.New("event recovery is not externally checkpointed")
)

type RecoverySource interface {
	Verify(context.Context) (ascpevents.RecoveryStatus, error)
}

type EventRecoverySource struct {
	store          ascpevents.RecoveryStore
	worm           ascpevents.WORMReader
	remote         ascpevents.RemoteHeadReader
	writerKeys     map[string][]byte
	checkpointKeys map[string]ed25519.PublicKey
}

func NewEventRecoverySource(store ascpevents.RecoveryStore, worm ascpevents.WORMReader, remote ascpevents.RemoteHeadReader,
	writerKeys map[string][]byte, checkpointKeys map[string]ed25519.PublicKey) (*EventRecoverySource, error) {
	if isNil(store) || isNil(worm) || isNil(remote) {
		return nil, ErrInvalidConfig
	}
	writers := cloneWriterKeys(writerKeys)
	checkpoints := cloneCheckpointKeys(checkpointKeys)
	if len(writers) == 0 || len(checkpoints) == 0 {
		return nil, ErrInvalidConfig
	}
	return &EventRecoverySource{store: store, worm: worm, remote: remote, writerKeys: writers, checkpointKeys: checkpoints}, nil
}

func (s *EventRecoverySource) Verify(ctx context.Context) (ascpevents.RecoveryStatus, error) {
	return ascpevents.VerifyRecovery(ctx, s.store, s.worm, s.remote, s.writerKeys, s.checkpointKeys)
}

type Config struct {
	KeyID         string
	PrivateKey    ed25519.PrivateKey
	ProofTTL      time.Duration
	CacheTTL      time.Duration
	VerifyTimeout time.Duration
	Clock         func() time.Time
}

type Service struct {
	source        RecoverySource
	keyID         string
	privateKey    ed25519.PrivateKey
	proofTTL      time.Duration
	cacheTTL      time.Duration
	verifyTimeout time.Duration
	clock         func() time.Time
	gate          chan struct{}
	mu            sync.RWMutex
	cached        ascprails.IntegrityAttestation
	cachedErr     error
	cachedTill    time.Time
}

func NewService(source RecoverySource, config Config) (*Service, error) {
	if isNil(source) || len(config.PrivateKey) != ed25519.PrivateKeySize || config.ProofTTL < time.Second ||
		config.ProofTTL > 5*time.Minute || config.CacheTTL <= 0 || config.CacheTTL > 5*time.Second ||
		config.CacheTTL >= config.ProofTTL || config.VerifyTimeout < time.Second || config.VerifyTimeout > time.Minute || config.Clock == nil {
		return nil, ErrInvalidConfig
	}
	if !keyIDPattern.MatchString(config.KeyID) {
		return nil, ErrInvalidConfig
	}
	derivedKey := ed25519.NewKeyFromSeed(config.PrivateKey[:ed25519.SeedSize])
	if !bytes.Equal(derivedKey, config.PrivateKey) {
		return nil, ErrInvalidConfig
	}
	return &Service{source: source, keyID: config.KeyID, privateKey: append(ed25519.PrivateKey(nil), config.PrivateKey...),
		proofTTL: config.ProofTTL, cacheTTL: config.CacheTTL, verifyTimeout: config.VerifyTimeout, clock: config.Clock, gate: make(chan struct{}, 1)}, nil
}

func (s *Service) Latest(ctx context.Context) (ascprails.IntegrityAttestation, error) {
	if attestation, err, ok := s.cachedResult(s.clock().UTC()); ok {
		return attestation, err
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return ascprails.IntegrityAttestation{}, ctx.Err()
	}
	now := s.clock().UTC()
	if attestation, err, ok := s.cachedResult(now); ok {
		return attestation, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, s.verifyTimeout)
	defer cancel()
	status, err := s.source.Verify(verifyCtx)
	if err != nil {
		// Cache dependency failures so a burst does not serialize repeated full
		// recovery scans. Do not cache cancellation caused by either the caller
		// or this verifier's own budget: a fresh request gets a fresh budget.
		if ctx.Err() == nil && verifyCtx.Err() == nil {
			s.cacheResult(ascprails.IntegrityAttestation{}, err, s.clock().UTC())
		}
		return ascprails.IntegrityAttestation{}, err
	}
	if !status.ExternallyCheckpointed || status.LocalHead.Sequence == 0 ||
		status.LocalHead != status.RemoteHead || status.CheckpointSequence != status.LocalHead.Sequence ||
		!ascprails.ValidRawHash(status.LocalHead.EventHash) {
		s.cacheResult(ascprails.IntegrityAttestation{}, ErrRecoveryUnproved, s.clock().UTC())
		return ascprails.IntegrityAttestation{}, ErrRecoveryUnproved
	}
	now = s.clock().UTC().Truncate(time.Second)
	attestation, err := ascprails.SignIntegrityAttestation(ascprails.IntegrityAttestation{
		SchemaVersion: 1, State: "VERIFIED", LocalSequence: status.LocalHead.Sequence,
		LocalEventHash: status.LocalHead.EventHash, RemoteSequence: status.RemoteHead.Sequence,
		RemoteEventHash: status.RemoteHead.EventHash, CheckpointSequence: status.CheckpointSequence,
		IssuedAtUnix: now.Unix(), ExpiresAtUnix: now.Add(s.proofTTL).Unix(), KeyID: s.keyID,
	}, s.privateKey)
	if err != nil {
		s.cacheResult(ascprails.IntegrityAttestation{}, err, s.clock().UTC())
		return ascprails.IntegrityAttestation{}, err
	}
	s.cacheResult(attestation, nil, now)
	return attestation, nil
}

func (s *Service) cacheResult(attestation ascprails.IntegrityAttestation, err error, now time.Time) {
	s.mu.Lock()
	s.cached = attestation
	s.cachedErr = err
	s.cachedTill = now.Add(s.cacheTTL)
	s.mu.Unlock()
}

func (s *Service) cachedResult(now time.Time) (ascprails.IntegrityAttestation, error, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	valid := now.Before(s.cachedTill) && (s.cached.Signature != "" || s.cachedErr != nil)
	return s.cached, s.cachedErr, valid
}

func cloneWriterKeys(input map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(input))
	for keyID, key := range input {
		if !keyIDPattern.MatchString(keyID) || len(key) != 32 {
			return nil
		}
		result[keyID] = append([]byte(nil), key...)
	}
	return result
}

func cloneCheckpointKeys(input map[string]ed25519.PublicKey) map[string]ed25519.PublicKey {
	result := make(map[string]ed25519.PublicKey, len(input))
	for keyID, key := range input {
		if !keyIDPattern.MatchString(keyID) || len(key) != ed25519.PublicKeySize {
			return nil
		}
		result[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	return result
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ RecoverySource = (*EventRecoverySource)(nil)
var _ ascprails.IntegrityAttestationSource = (*Service)(nil)
