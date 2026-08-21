package ascpevents

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignedCheckpointWORMRemoteHeadAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	writer, _ := NewWriter("writer-key-a", []byte(strings.Repeat("a", 32)))
	store := &memoryStore{}
	store.append(t, testInput("event_0001", now, map[string]any{"state": "created"}), writer)
	store.append(t, testInput("event_0002", now.Add(time.Second), map[string]any{"state": "authorized"}), writer)
	publicKey, privateKey, _ := ed25519.GenerateKey(nil)
	worm := &memoryWORM{objects: map[string][]byte{}}
	remote := &memoryRemote{}
	publisher, err := NewPublisher(store, worm, remote, "checkpoint-key-a", privateKey, map[string][]byte{writer.KeyID: writer.Key}, func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	trialHash := strings.Repeat("c", 64)
	checkpoint, replayed, err := publisher.Publish(context.Background(), trialHash)
	if err != nil || replayed || checkpoint.LastSequence != 2 || remote.head.Sequence != 2 {
		t.Fatalf("checkpoint=%+v replay=%v err=%v", checkpoint, replayed, err)
	}
	second, replayed, err := publisher.Publish(context.Background(), trialHash)
	if err != nil || !replayed || !sameCheckpoint(checkpoint, second) {
		t.Fatalf("replay=%v err=%v", replayed, err)
	}
	status, err := VerifyRecovery(context.Background(), store, worm, remote,
		map[string][]byte{writer.KeyID: writer.Key}, map[string]ed25519.PublicKey{"checkpoint-key-a": publicKey})
	if err != nil || !status.ExternallyCheckpointed || status.UncheckpointedEventCount != 0 {
		t.Fatal(err)
	}
	store.append(t, testInput("event_0003", now.Add(3*time.Second), map[string]any{"state": "submitted"}), writer)
	status, err = VerifyRecovery(context.Background(), store, worm, remote,
		map[string][]byte{writer.KeyID: writer.Key}, map[string]ed25519.PublicKey{"checkpoint-key-a": publicKey})
	if err != nil || status.ExternallyCheckpointed || status.UncheckpointedEventCount != 1 {
		t.Fatalf("tail status=%+v err=%v", status, err)
	}
}

func TestRecoveryRejectsTruncationWORMAndSignatureMutation(t *testing.T) {
	fixture := checkpointFixture(t)
	cases := []struct {
		name   string
		mutate func(*checkpointFixtureState)
	}{
		{"local truncation", func(state *checkpointFixtureState) { state.store.events = state.store.events[:1] }},
		{"remote conflict", func(state *checkpointFixtureState) { state.remote.head.EventHash = strings.Repeat("f", 64) }},
		{"WORM substitution", func(state *checkpointFixtureState) { state.worm.objects[state.checkpoint.WORMRef][0] ^= 1 }},
		{"signature mutation", func(state *checkpointFixtureState) { state.store.checkpoints[0].Signature[0] ^= 1 }},
		{"checkpoint key missing", func(state *checkpointFixtureState) { state.checkpointKeys = map[string]ed25519.PublicKey{} }},
		{"writer key missing", func(state *checkpointFixtureState) { state.writerKeys = map[string][]byte{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := fixture.clone()
			testCase.mutate(state)
			if _, err := VerifyRecovery(context.Background(), state.store, state.worm, state.remote, state.writerKeys, state.checkpointKeys); err == nil {
				t.Fatal("mutation was accepted")
			}
		})
	}
}

func TestPublisherStopsBeforeLocalCommitWhenWORMOrRemoteFails(t *testing.T) {
	state := checkpointFixture(t).clone()
	state.store.checkpoints = nil
	state.worm.failure = errors.New("WORM unavailable")
	publisher, _ := NewPublisher(state.store, state.worm, state.remote, "checkpoint-key-a", state.privateKey, state.writerKeys, func() time.Time { return time.Unix(100, 0) })
	if _, _, err := publisher.Publish(context.Background(), strings.Repeat("d", 64)); err == nil || len(state.store.checkpoints) != 0 {
		t.Fatalf("WORM failure err=%v", err)
	}
	state.worm.failure = nil
	state.remote.failure = errors.New("remote unavailable")
	if _, _, err := publisher.Publish(context.Background(), strings.Repeat("d", 64)); err == nil || len(state.store.checkpoints) != 0 {
		t.Fatalf("remote failure err=%v", err)
	}
}

func TestPublisherVerifiesFullChainBeforeExternalEffects(t *testing.T) {
	state := checkpointFixture(t).clone()
	state.store.checkpoints = nil
	state.worm.objects = map[string][]byte{}
	state.remote.head = Head{}
	state.store.events[1].Payload = []byte(`{"n":999}`)
	publisher, _ := NewPublisher(state.store, state.worm, state.remote, "checkpoint-key-a", state.privateKey, state.writerKeys)
	if _, _, err := publisher.Publish(context.Background(), strings.Repeat("d", 64)); err == nil || len(state.worm.objects) != 0 || state.remote.head.Sequence != 0 {
		t.Fatalf("corrupt chain publish err=%v WORM=%d remote=%+v", err, len(state.worm.objects), state.remote.head)
	}
}

func TestRecoveryRejectsRemoteHeadWithoutMatchingCheckpoint(t *testing.T) {
	state := checkpointFixture(t).clone()
	writer, _ := NewWriter("writer-key-a", state.writerKeys["writer-key-a"])
	state.store.append(t, testInput("event_0003", time.Unix(103, 0), map[string]any{"n": uint64(3)}), writer)
	third := state.store.events[2]
	state.remote.head = Head{Sequence: 3, EventHash: third.EventHash}
	if _, err := VerifyRecovery(context.Background(), state.store, state.worm, state.remote, state.writerKeys, state.checkpointKeys); err == nil {
		t.Fatal("remote head without matching local checkpoint was accepted")
	}
}

type memoryStore struct {
	events      []Event
	checkpoints []Checkpoint
}

func (s *memoryStore) append(t *testing.T, input Input, writer Writer) {
	t.Helper()
	previous := zeroHash
	if len(s.events) > 0 {
		previous = s.events[len(s.events)-1].EventHash
	}
	event, err := buildEvent(input, uint64(len(s.events)+1), previous, writer)
	if err != nil {
		t.Fatal(err)
	}
	s.events = append(s.events, event)
}
func (s *memoryStore) Head(context.Context) (Head, error) {
	if len(s.events) == 0 {
		return Head{EventHash: zeroHash}, nil
	}
	e := s.events[len(s.events)-1]
	return Head{uint64(len(s.events)), e.EventHash}, nil
}
func (s *memoryStore) EventAt(_ context.Context, seq uint64) (Event, error) {
	if seq == 0 || seq > uint64(len(s.events)) {
		return Event{}, sql.ErrNoRows
	}
	return cloneEvent(s.events[seq-1]), nil
}
func (s *memoryStore) Verify(_ context.Context, keys map[string][]byte) (Head, error) {
	p := zeroHash
	for i, e := range s.events {
		if err := VerifyEvent(e, uint64(i+1), p, keys); err != nil {
			return Head{}, err
		}
		p = e.EventHash
	}
	return Head{uint64(len(s.events)), p}, nil
}
func (s *memoryStore) SaveCheckpoint(_ context.Context, c Checkpoint) (Checkpoint, bool, error) {
	for _, v := range s.checkpoints {
		if v.CheckpointID == c.CheckpointID {
			if !sameCheckpoint(v, c) {
				return Checkpoint{}, false, ErrCheckpointConflict
			}
			return cloneCheckpoint(v), true, nil
		}
	}
	s.checkpoints = append(s.checkpoints, cloneCheckpoint(c))
	return c, false, nil
}
func (s *memoryStore) LatestCheckpoint(context.Context) (Checkpoint, error) {
	if len(s.checkpoints) == 0 {
		return Checkpoint{}, sql.ErrNoRows
	}
	return cloneCheckpoint(s.checkpoints[len(s.checkpoints)-1]), nil
}

type memoryWORM struct {
	objects map[string][]byte
	failure error
}

func (w *memoryWORM) Put(_ context.Context, ref string, data []byte) error {
	if w.failure != nil {
		return w.failure
	}
	if old, ok := w.objects[ref]; ok && !bytes.Equal(old, data) {
		return ErrCheckpointConflict
	}
	w.objects[ref] = append([]byte(nil), data...)
	return nil
}
func (w *memoryWORM) Get(_ context.Context, ref string) ([]byte, error) {
	v, ok := w.objects[ref]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return append([]byte(nil), v...), nil
}

type memoryRemote struct {
	head    Head
	failure error
}

func (r *memoryRemote) Advance(_ context.Context, h Head) error {
	if r.failure != nil {
		return r.failure
	}
	if h.Sequence < r.head.Sequence || h.Sequence == r.head.Sequence && r.head.Sequence > 0 && h.EventHash != r.head.EventHash {
		return ErrCheckpointConflict
	}
	r.head = h
	return nil
}
func (r *memoryRemote) Current(context.Context) (Head, error) {
	if r.failure != nil {
		return Head{}, r.failure
	}
	return r.head, nil
}

type checkpointFixtureState struct {
	store          *memoryStore
	worm           *memoryWORM
	remote         *memoryRemote
	checkpoint     Checkpoint
	writerKeys     map[string][]byte
	checkpointKeys map[string]ed25519.PublicKey
	privateKey     ed25519.PrivateKey
}

func checkpointFixture(t *testing.T) *checkpointFixtureState {
	t.Helper()
	now := time.Unix(100, 0)
	writer, _ := NewWriter("writer-key-a", []byte(strings.Repeat("a", 32)))
	s := &memoryStore{}
	s.append(t, testInput("event_0001", now, map[string]any{"n": uint64(1)}), writer)
	s.append(t, testInput("event_0002", now.Add(time.Second), map[string]any{"n": uint64(2)}), writer)
	pub, priv, _ := ed25519.GenerateKey(nil)
	w := &memoryWORM{objects: map[string][]byte{}}
	r := &memoryRemote{}
	p, _ := NewPublisher(s, w, r, "checkpoint-key-a", priv, map[string][]byte{writer.KeyID: writer.Key}, func() time.Time { return now.Add(2 * time.Second) })
	cp, _, err := p.Publish(context.Background(), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	return &checkpointFixtureState{s, w, r, cp, map[string][]byte{writer.KeyID: writer.Key}, map[string]ed25519.PublicKey{"checkpoint-key-a": pub}, priv}
}
func (s *checkpointFixtureState) clone() *checkpointFixtureState {
	events := make([]Event, len(s.store.events))
	for i, e := range s.store.events {
		events[i] = cloneEvent(e)
	}
	checkpoints := make([]Checkpoint, len(s.store.checkpoints))
	for i, c := range s.store.checkpoints {
		checkpoints[i] = cloneCheckpoint(c)
	}
	objects := map[string][]byte{}
	for k, v := range s.worm.objects {
		objects[k] = append([]byte(nil), v...)
	}
	wk := map[string][]byte{}
	for k, v := range s.writerKeys {
		wk[k] = append([]byte(nil), v...)
	}
	ck := map[string]ed25519.PublicKey{}
	for k, v := range s.checkpointKeys {
		ck[k] = append(ed25519.PublicKey(nil), v...)
	}
	return &checkpointFixtureState{&memoryStore{events, checkpoints}, &memoryWORM{objects, nil}, &memoryRemote{s.remote.head, nil}, cloneCheckpoint(s.checkpoint), wk, ck, append(ed25519.PrivateKey(nil), s.privateKey...)}
}
