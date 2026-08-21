package ascprecovery

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpevents"
	"github.com/gnanam1990/flowops/internal/ascprails"
)

func TestServiceSignsOnlyFullyCheckpointedRecovery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("r", ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	source := &staticRecoverySource{status: verifiedStatus(7, hash)}
	service, err := NewService(source, Config{KeyID: "recovery-key-1", PrivateKey: privateKey,
		ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	attestation, err := service.Latest(t.Context())
	if err != nil || attestation.State != "VERIFIED" || attestation.LocalSequence != 7 ||
		attestation.RemoteSequence != 7 || attestation.CheckpointSequence != 7 || attestation.ExpiresAtUnix-attestation.IssuedAtUnix != 60 {
		t.Fatalf("attestation=%+v err=%v", attestation, err)
	}
	gate, err := ascprails.NewAttestedIntegrityGate(staticHead{head: ascpevents.Head{Sequence: 7, EventHash: hash}}, service,
		map[string]ed25519.PublicKey{"recovery-key-1": publicKey}, 2*time.Minute, func() time.Time { return now })
	if err != nil || gate.Check(t.Context()) != nil {
		t.Fatalf("seller gate rejected recovery proof: %v", err)
	}

	for _, status := range []ascpevents.RecoveryStatus{
		{LocalHead: ascpevents.Head{Sequence: 7, EventHash: hash}, RemoteHead: ascpevents.Head{Sequence: 6, EventHash: hash}, CheckpointSequence: 6},
		{LocalHead: ascpevents.Head{Sequence: 7, EventHash: hash}, RemoteHead: ascpevents.Head{Sequence: 7, EventHash: hash}, CheckpointSequence: 7},
		{ExternallyCheckpointed: true, LocalHead: ascpevents.Head{Sequence: 7, EventHash: hash}, RemoteHead: ascpevents.Head{Sequence: 7, EventHash: strings.Repeat("b", 64)}, CheckpointSequence: 7},
		{ExternallyCheckpointed: true, LocalHead: ascpevents.Head{Sequence: 7, EventHash: strings.Repeat("0", 64)}, RemoteHead: ascpevents.Head{Sequence: 7, EventHash: strings.Repeat("0", 64)}, CheckpointSequence: 7},
	} {
		unproved, _ := NewService(&staticRecoverySource{status: status}, Config{KeyID: "recovery-key-1", PrivateKey: privateKey,
			ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second, Clock: func() time.Time { return now }})
		if _, err := unproved.Latest(t.Context()); !errors.Is(err, ErrRecoveryUnproved) {
			t.Fatalf("status=%+v err=%v", status, err)
		}
	}
}

func TestServiceCoalescesConcurrentVerificationAndExpiresCache(t *testing.T) {
	var unix atomic.Int64
	unix.Store(1_800_000_000)
	_, privateKey, _ := ed25519.GenerateKey(strings.NewReader(strings.Repeat("c", ed25519.SeedSize)))
	source := &staticRecoverySource{status: verifiedStatus(3, strings.Repeat("b", 64)), delay: 20 * time.Millisecond}
	service, _ := NewService(source, Config{KeyID: "recovery-key-1", PrivateKey: privateKey,
		ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second, Clock: func() time.Time { return time.Unix(unix.Load(), 0) }})
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := service.Latest(context.Background()); err != nil {
				t.Errorf("latest: %v", err)
			}
		}()
	}
	wait.Wait()
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("verification calls=%d", calls)
	}
	unix.Add(2)
	if _, err := service.Latest(t.Context()); err != nil || source.calls.Load() != 2 {
		t.Fatalf("expired cache calls=%d err=%v", source.calls.Load(), err)
	}
}

func TestServiceCoalescesVerificationFailuresAndExpiresNegativeCache(t *testing.T) {
	var unix atomic.Int64
	unix.Store(1_800_000_000)
	_, privateKey, _ := ed25519.GenerateKey(nil)
	outage := errors.New("remote head unavailable")
	source := &staticRecoverySource{err: outage, delay: 20 * time.Millisecond}
	service, err := NewService(source, Config{KeyID: "recovery-key-1", PrivateKey: privateKey,
		ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second,
		Clock: func() time.Time { return time.Unix(unix.Load(), 0) }})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := service.Latest(context.Background()); !errors.Is(err, outage) {
				t.Errorf("failure=%v", err)
			}
		}()
	}
	wait.Wait()
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("failure verification calls=%d", calls)
	}
	unix.Add(2)
	if _, err := service.Latest(t.Context()); !errors.Is(err, outage) || source.calls.Load() != 2 {
		t.Fatalf("expired negative cache calls=%d err=%v", source.calls.Load(), err)
	}
}

func TestServiceWaitHonorsContextCancellation(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(nil)
	source := &blockingRecoverySource{started: make(chan struct{}), release: make(chan struct{})}
	service, _ := NewService(source, Config{KeyID: "recovery-key-1", PrivateKey: privateKey,
		ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second, Clock: time.Now})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = service.Latest(context.Background())
	}()
	<-source.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Latest(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting request error=%v", err)
	}
	close(source.release)
	<-firstDone
}

func TestNewServiceRejectsUnsafeConfigurationAndTypedNil(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(nil)
	validSource := &staticRecoverySource{status: verifiedStatus(1, strings.Repeat("a", 64))}
	for _, config := range []Config{
		{KeyID: "short", PrivateKey: privateKey, ProofTTL: time.Minute, CacheTTL: time.Second, Clock: time.Now},
		{KeyID: "recovery-key-1", PrivateKey: privateKey, ProofTTL: time.Second, CacheTTL: time.Second, Clock: time.Now},
		{KeyID: "recovery-key-1", PrivateKey: privateKey, ProofTTL: 6 * time.Minute, CacheTTL: time.Second, Clock: time.Now},
		{KeyID: "recovery-key-1", PrivateKey: privateKey, ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 61 * time.Second, Clock: time.Now},
	} {
		if config.VerifyTimeout == 0 {
			config.VerifyTimeout = 5 * time.Second
		}
		if _, err := NewService(validSource, config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config=%+v error=%v", config, err)
		}
	}
	var typedNil *staticRecoverySource
	if _, err := NewService(typedNil, Config{KeyID: "recovery-key-1", PrivateKey: privateKey,
		ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second, Clock: time.Now}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("typed nil source error=%v", err)
	}
	malformedKey := append(ed25519.PrivateKey(nil), privateKey...)
	malformedKey[len(malformedKey)-1] ^= 1
	if _, err := NewService(validSource, Config{KeyID: "recovery-key-1", PrivateKey: malformedKey,
		ProofTTL: time.Minute, CacheTTL: time.Second, VerifyTimeout: 5 * time.Second, Clock: time.Now}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("non-canonical private key error=%v", err)
	}
}

func TestServiceBoundsEachVerification(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(nil)
	service, err := NewService(deadlineRecoverySource{max: 1100 * time.Millisecond},
		Config{KeyID: "recovery-key-1", PrivateKey: privateKey, ProofTTL: time.Minute,
			CacheTTL: time.Second, VerifyTimeout: time.Second, Clock: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Latest(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded verification error=%v", err)
	}
}

func TestNewEventRecoverySourceValidatesDependenciesAndKeyEpochs(t *testing.T) {
	store := stubCheckpointStore{}
	worm := &stubWORMReader{}
	remote := stubRemoteHeadReader{}
	writers := map[string][]byte{"writer-key-1": []byte(strings.Repeat("w", 32))}
	checkpoints := map[string]ed25519.PublicKey{"checkpoint-key-1": ed25519.PublicKey([]byte(strings.Repeat("c", 32)))}
	source, err := NewEventRecoverySource(store, worm, remote, writers, checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	writers["writer-key-1"][0] = 'x'
	checkpoints["checkpoint-key-1"][0] = 'x'
	if source.writerKeys["writer-key-1"][0] != 'w' || source.checkpointKeys["checkpoint-key-1"][0] != 'c' {
		t.Fatal("recovery source retained mutable caller key bytes")
	}
	invalidWriters := map[string][]byte{"short": []byte(strings.Repeat("w", 32))}
	if _, err := NewEventRecoverySource(store, worm, remote, invalidWriters, checkpoints); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid writer epoch error=%v", err)
	}
	var typedNil *stubWORMReader
	if _, err := NewEventRecoverySource(store, typedNil, remote, writers, checkpoints); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("typed nil WORM reader error=%v", err)
	}
}

func verifiedStatus(sequence uint64, hash string) ascpevents.RecoveryStatus {
	head := ascpevents.Head{Sequence: sequence, EventHash: hash}
	return ascpevents.RecoveryStatus{LocalHead: head, RemoteHead: head, CheckpointSequence: sequence, ExternallyCheckpointed: true}
}

type staticRecoverySource struct {
	status ascpevents.RecoveryStatus
	err    error
	delay  time.Duration
	calls  atomic.Int64
}

func (s *staticRecoverySource) Verify(ctx context.Context) (ascpevents.RecoveryStatus, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ascpevents.RecoveryStatus{}, ctx.Err()
		case <-timer.C:
		}
	}
	return s.status, s.err
}

type blockingRecoverySource struct{ started, release chan struct{} }

func (s *blockingRecoverySource) Verify(context.Context) (ascpevents.RecoveryStatus, error) {
	close(s.started)
	<-s.release
	return ascpevents.RecoveryStatus{}, errors.New("stopped")
}

type deadlineRecoverySource struct{ max time.Duration }

func (s deadlineRecoverySource) Verify(ctx context.Context) (ascpevents.RecoveryStatus, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > s.max {
		return ascpevents.RecoveryStatus{}, errors.New("verification deadline exceeds configured bound")
	}
	<-ctx.Done()
	return ascpevents.RecoveryStatus{}, ctx.Err()
}

type staticHead struct{ head ascpevents.Head }

func (s staticHead) Head(context.Context) (ascpevents.Head, error) { return s.head, nil }

type stubCheckpointStore struct{}

func (stubCheckpointStore) EventAt(context.Context, uint64) (ascpevents.Event, error) {
	return ascpevents.Event{}, nil
}
func (stubCheckpointStore) Verify(context.Context, map[string][]byte) (ascpevents.Head, error) {
	return ascpevents.Head{}, nil
}
func (stubCheckpointStore) LatestCheckpoint(context.Context) (ascpevents.Checkpoint, error) {
	return ascpevents.Checkpoint{}, nil
}

type stubWORMReader struct{}

func (*stubWORMReader) Get(context.Context, string) ([]byte, error) { return nil, nil }

type stubRemoteHeadReader struct{}

func (stubRemoteHeadReader) Current(context.Context) (ascpevents.Head, error) {
	return ascpevents.Head{}, nil
}
