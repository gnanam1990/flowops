package ascpevents

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type Head struct {
	Sequence  uint64 `json:"lastSeq"`
	EventHash string `json:"lastEventHash"`
}

type RecoveryStatus struct {
	LocalHead                Head   `json:"localHead"`
	RemoteHead               Head   `json:"remoteHead"`
	CheckpointSequence       uint64 `json:"checkpointSequence"`
	UncheckpointedEventCount uint64 `json:"uncheckpointedEventCount"`
	ExternallyCheckpointed   bool   `json:"externallyCheckpointed"`
}

type Checkpoint struct {
	CheckpointID            string `json:"checkpointId"`
	LastSequence            uint64 `json:"lastSeq"`
	LastEventHash           string `json:"lastEventHash"`
	JournalTrialBalanceHash string `json:"journalTrialBalanceHash"`
	CreatedAtUnixMic        int64  `json:"createdAtUnixMicros"`
	SigningKeyID            string `json:"signingKeyId"`
	CanonicalDocument       []byte `json:"canonicalDocument"`
	Signature               []byte `json:"signature"`
	WORMRef                 string `json:"wormRef"`
}

type checkpointDocument struct {
	SchemaVersion           int    `json:"schemaVersion"`
	LastSequence            uint64 `json:"lastSeq"`
	LastEventHash           string `json:"lastEventHash"`
	JournalTrialBalanceHash string `json:"journalTrialBalanceHash"`
	SigningKeyID            string `json:"signingKeyId"`
}

type CheckpointStore interface {
	RecoveryStore
	Head(context.Context) (Head, error)
	SaveCheckpoint(context.Context, Checkpoint) (Checkpoint, bool, error)
}

// RecoveryStore is the recovery verifier's read-only database boundary.
type RecoveryStore interface {
	EventAt(context.Context, uint64) (Event, error)
	Verify(context.Context, map[string][]byte) (Head, error)
	LatestCheckpoint(context.Context) (Checkpoint, error)
}

// WORMStore must make Put idempotent for the same ref and exact bytes and
// reject any attempt to replace existing bytes.
type WORMStore interface {
	WORMReader
	Put(context.Context, string, []byte) error
}

// WORMReader is the recovery verifier's read-only immutable-object boundary.
type WORMReader interface {
	Get(context.Context, string) ([]byte, error)
}

// RemoteHead is a monotonic external truncation sentinel. Advance accepts an
// identical replay and rejects lower or conflicting heads.
type RemoteHead interface {
	RemoteHeadReader
	Advance(context.Context, Head) error
}

// RemoteHeadReader is the recovery verifier's read-only monotonic-head boundary.
type RemoteHeadReader interface {
	Current(context.Context) (Head, error)
}

type Publisher struct {
	store      CheckpointStore
	worm       WORMStore
	remote     RemoteHead
	keyID      string
	privateKey ed25519.PrivateKey
	writerKeys map[string][]byte
	clock      func() time.Time
}

func NewPublisher(store CheckpointStore, worm WORMStore, remote RemoteHead, keyID string, privateKey ed25519.PrivateKey, writerKeys map[string][]byte, clocks ...func() time.Time) (*Publisher, error) {
	if store == nil || worm == nil || remote == nil || !identifier(keyID, 8, 128) || len(privateKey) != ed25519.PrivateKeySize || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidEvent
	}
	keys := make(map[string][]byte, len(writerKeys))
	for writerKeyID, key := range writerKeys {
		if !identifier(writerKeyID, 8, 128) || len(key) != 32 {
			return nil, ErrInvalidEvent
		}
		keys[writerKeyID] = append([]byte(nil), key...)
	}
	if len(keys) == 0 {
		return nil, ErrInvalidEvent
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &Publisher{store: store, worm: worm, remote: remote, keyID: keyID,
		privateKey: append(ed25519.PrivateKey(nil), privateKey...), writerKeys: keys, clock: clock}, nil
}

func (p *Publisher) Publish(ctx context.Context, trialBalanceHash string) (Checkpoint, bool, error) {
	if !nonzeroHash(trialBalanceHash) {
		return Checkpoint{}, false, ErrInvalidEvent
	}
	head, err := p.store.Verify(ctx, p.writerKeys)
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("verify event chain before checkpoint: %w", err)
	}
	if head.Sequence == 0 || !nonzeroHash(head.EventHash) {
		return Checkpoint{}, false, errors.New("cannot checkpoint an empty event chain")
	}
	latest, latestErr := p.store.LatestCheckpoint(ctx)
	if latestErr == nil && latest.LastSequence == head.Sequence {
		if latest.LastEventHash != head.EventHash || latest.JournalTrialBalanceHash != trialBalanceHash || latest.SigningKeyID != p.keyID ||
			verifyCheckpoint(latest, map[string]ed25519.PublicKey{p.keyID: p.privateKey.Public().(ed25519.PublicKey)}) != nil {
			return Checkpoint{}, false, ErrCheckpointConflict
		}
		if err := p.worm.Put(ctx, latest.WORMRef, checkpointBlob(latest)); err != nil {
			return Checkpoint{}, false, fmt.Errorf("write checkpoint WORM object: %w", err)
		}
		if err := p.remote.Advance(ctx, head); err != nil {
			return Checkpoint{}, false, fmt.Errorf("advance remote event head: %w", err)
		}
		return cloneCheckpoint(latest), true, nil
	}
	if latestErr == nil && latest.LastSequence > head.Sequence {
		return Checkpoint{}, false, ErrIntegrity
	}
	if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
		return Checkpoint{}, false, fmt.Errorf("read latest checkpoint: %w", latestErr)
	}
	checkpoint, err := signCheckpoint(head, trialBalanceHash, p.keyID, p.privateKey, p.clock().UTC())
	if err != nil {
		return Checkpoint{}, false, err
	}
	blob := checkpointBlob(checkpoint)
	if err := p.worm.Put(ctx, checkpoint.WORMRef, blob); err != nil {
		return Checkpoint{}, false, fmt.Errorf("write checkpoint WORM object: %w", err)
	}
	if err := p.remote.Advance(ctx, head); err != nil {
		return Checkpoint{}, false, fmt.Errorf("advance remote event head: %w", err)
	}
	stored, replayed, err := p.store.SaveCheckpoint(ctx, checkpoint)
	if err != nil {
		return Checkpoint{}, false, err
	}
	return stored, replayed, nil
}

func signCheckpoint(head Head, trialBalanceHash, keyID string, privateKey ed25519.PrivateKey, at time.Time) (Checkpoint, error) {
	if head.Sequence == 0 || !nonzeroHash(head.EventHash) || !nonzeroHash(trialBalanceHash) || !identifier(keyID, 8, 128) || len(privateKey) != ed25519.PrivateKeySize || at.UnixMicro() <= 0 {
		return Checkpoint{}, ErrInvalidEvent
	}
	document, err := canonicalJSON(checkpointDocument{SchemaVersion: SchemaVersion, LastSequence: head.Sequence,
		LastEventHash: head.EventHash, JournalTrialBalanceHash: trialBalanceHash,
		SigningKeyID: keyID})
	if err != nil {
		return Checkpoint{}, err
	}
	digest := checkpointDigest(document)
	signature := ed25519.Sign(privateKey, digest[:])
	idDigest := sha256.Sum256(append([]byte("ASCP_CHECKPOINT_ID_V1\x00"), document...))
	id := "checkpoint_" + hex.EncodeToString(idDigest[:])
	return Checkpoint{CheckpointID: id, LastSequence: head.Sequence, LastEventHash: head.EventHash,
		JournalTrialBalanceHash: trialBalanceHash, CreatedAtUnixMic: at.UnixMicro(), SigningKeyID: keyID,
		CanonicalDocument: document, Signature: signature, WORMRef: "ascp/checkpoints/" + id + ".json"}, nil
}

func VerifyRecovery(ctx context.Context, store RecoveryStore, worm WORMReader, remote RemoteHeadReader, writerKeys map[string][]byte, checkpointKeys map[string]ed25519.PublicKey) (RecoveryStatus, error) {
	if store == nil || worm == nil || remote == nil {
		return RecoveryStatus{}, ErrIntegrity
	}
	local, err := store.Verify(ctx, writerKeys)
	if err != nil {
		return RecoveryStatus{}, err
	}
	remoteHead, err := remote.Current(ctx)
	if err != nil {
		return RecoveryStatus{}, fmt.Errorf("read remote event head: %w", err)
	}
	status := RecoveryStatus{LocalHead: local, RemoteHead: remoteHead, UncheckpointedEventCount: local.Sequence}
	if remoteHead.Sequence > local.Sequence {
		return status, fmt.Errorf("%w: local event tail trails remote head", ErrIntegrity)
	}
	if remoteHead.Sequence > 0 {
		event, err := store.EventAt(ctx, remoteHead.Sequence)
		if err != nil || event.EventHash != remoteHead.EventHash {
			return status, fmt.Errorf("%w: remote head conflicts with local chain", ErrIntegrity)
		}
	}
	checkpoint, err := store.LatestCheckpoint(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if remoteHead.Sequence > 0 {
			return status, fmt.Errorf("%w: remote head exists without a local checkpoint", ErrIntegrity)
		}
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("read latest checkpoint: %w", err)
	}
	status.CheckpointSequence = checkpoint.LastSequence
	if err := verifyCheckpoint(checkpoint, checkpointKeys); err != nil {
		return status, err
	}
	if checkpoint.LastSequence > local.Sequence {
		return status, fmt.Errorf("%w: checkpoint exceeds local head", ErrIntegrity)
	}
	status.UncheckpointedEventCount = local.Sequence - checkpoint.LastSequence
	event, err := store.EventAt(ctx, checkpoint.LastSequence)
	if err != nil || event.EventHash != checkpoint.LastEventHash {
		return status, fmt.Errorf("%w: checkpoint event is unavailable", ErrIntegrity)
	}
	blob, err := worm.Get(ctx, checkpoint.WORMRef)
	if err != nil {
		return status, fmt.Errorf("read checkpoint WORM object: %w", err)
	}
	if !bytes.Equal(blob, checkpointBlob(checkpoint)) {
		return status, fmt.Errorf("%w: WORM checkpoint bytes differ", ErrIntegrity)
	}
	if remoteHead.Sequence < checkpoint.LastSequence {
		return status, fmt.Errorf("%w: remote head trails durable checkpoint", ErrIntegrity)
	}
	if remoteHead.Sequence != checkpoint.LastSequence || remoteHead.EventHash != checkpoint.LastEventHash {
		return status, fmt.Errorf("%w: remote head has no matching local checkpoint", ErrIntegrity)
	}
	status.ExternallyCheckpointed = status.UncheckpointedEventCount == 0 && remoteHead.Sequence == local.Sequence
	return status, nil
}

func verifyCheckpoint(checkpoint Checkpoint, keys map[string]ed25519.PublicKey) error {
	if !identifier(checkpoint.CheckpointID, 8, 200) || checkpoint.LastSequence == 0 || !nonzeroHash(checkpoint.LastEventHash) ||
		!nonzeroHash(checkpoint.JournalTrialBalanceHash) || checkpoint.CreatedAtUnixMic <= 0 ||
		!identifier(checkpoint.SigningKeyID, 8, 128) || len(checkpoint.Signature) != ed25519.SignatureSize ||
		!identifier(checkpoint.WORMRef, 8, 300) {
		return ErrIntegrity
	}
	wantDocument, err := canonicalJSON(checkpointDocument{SchemaVersion: SchemaVersion, LastSequence: checkpoint.LastSequence,
		LastEventHash: checkpoint.LastEventHash, JournalTrialBalanceHash: checkpoint.JournalTrialBalanceHash,
		SigningKeyID: checkpoint.SigningKeyID})
	if err != nil || !bytes.Equal(wantDocument, checkpoint.CanonicalDocument) {
		return ErrIntegrity
	}
	idDigest := sha256.Sum256(append([]byte("ASCP_CHECKPOINT_ID_V1\x00"), wantDocument...))
	if checkpoint.CheckpointID != "checkpoint_"+hex.EncodeToString(idDigest[:]) || checkpoint.WORMRef != "ascp/checkpoints/"+checkpoint.CheckpointID+".json" {
		return ErrIntegrity
	}
	key, ok := keys[checkpoint.SigningKeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return ErrIntegrity
	}
	digest := checkpointDigest(checkpoint.CanonicalDocument)
	if !ed25519.Verify(key, digest[:], checkpoint.Signature) {
		return ErrIntegrity
	}
	return nil
}

func checkpointDigest(document []byte) [32]byte {
	return sha256.Sum256(append([]byte("ASCP_CHECKPOINT_V1\x00"), document...))
}

func checkpointBlob(checkpoint Checkpoint) []byte {
	result := make([]byte, 0, len(checkpoint.CanonicalDocument)+1+hex.EncodedLen(len(checkpoint.Signature)))
	result = append(result, checkpoint.CanonicalDocument...)
	result = append(result, '\n')
	encoded := make([]byte, hex.EncodedLen(len(checkpoint.Signature)))
	hex.Encode(encoded, checkpoint.Signature)
	return append(result, encoded...)
}

func sameCheckpoint(left, right Checkpoint) bool {
	return left.CheckpointID == right.CheckpointID && left.LastSequence == right.LastSequence &&
		left.LastEventHash == right.LastEventHash && left.JournalTrialBalanceHash == right.JournalTrialBalanceHash &&
		left.SigningKeyID == right.SigningKeyID &&
		bytes.Equal(left.CanonicalDocument, right.CanonicalDocument) && bytes.Equal(left.Signature, right.Signature) && left.WORMRef == right.WORMRef
}

func cloneCheckpoint(checkpoint Checkpoint) Checkpoint {
	checkpoint.CanonicalDocument = append([]byte(nil), checkpoint.CanonicalDocument...)
	checkpoint.Signature = append([]byte(nil), checkpoint.Signature...)
	return checkpoint
}

const checkpointSelect = `SELECT checkpoint_id,last_sequence,last_event_hash,journal_trial_balance_hash,
	created_at_unix_micro,signing_key_id,canonical_document,signature,worm_ref FROM ascp_event_checkpoints`

func checkpointByID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Checkpoint, error) {
	return scanCheckpoint(q.QueryRowContext(ctx, checkpointSelect+` WHERE checkpoint_id=$1`, id))
}

func scanCheckpoint(row rowScanner) (Checkpoint, error) {
	var checkpoint Checkpoint
	err := row.Scan(&checkpoint.CheckpointID, &checkpoint.LastSequence, &checkpoint.LastEventHash,
		&checkpoint.JournalTrialBalanceHash, &checkpoint.CreatedAtUnixMic, &checkpoint.SigningKeyID,
		&checkpoint.CanonicalDocument, &checkpoint.Signature, &checkpoint.WORMRef)
	return checkpoint, err
}
