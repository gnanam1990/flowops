package ascpbearer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testActivationVerifier struct{ activationErr, expiryErr error }

func (v testActivationVerifier) VerifyActivation(context.Context, Handle, ActivationProof) error {
	return v.activationErr
}
func (v testActivationVerifier) ProveUnactivated(context.Context, Handle, time.Time) error {
	return v.expiryErr
}

func TestSignerLedgerReturnsOnlyOpaqueHandleAndReleasesAfterVerifiedActivation(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	store := signerStore(t, now, testActivationVerifier{})
	signature := bytes.Repeat([]byte{0x7a}, 65)
	input := signerInput(now, signature)
	handle, err := store.Prepare(context.Background(), input)
	if err != nil || handle.State != Prepared || handle.ID == "" {
		t.Fatalf("handle=%+v err=%v", handle, err)
	}
	raw, _ := json.Marshal(handle)
	if bytes.Contains(raw, signature) {
		t.Fatal("opaque handle leaked signer artifact")
	}
	if bytes.Equal(store.encryptedForTest(handle.ID), signature) {
		t.Fatal("signer ledger stored plaintext signature")
	}
	if _, _, err := store.Release(context.Background(), handle.ID, input.KeeperID); !errors.Is(err, ErrTransition) {
		t.Fatalf("pre-activation release error=%v", err)
	}
	proof := activationProof(handle, now)
	if _, err := store.Activate(context.Background(), handle.ID, proof); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Release(context.Background(), handle.ID, "wrong-keeper"); !errors.Is(err, ErrKeeper) {
		t.Fatalf("wrong keeper error=%v", err)
	}
	_, first, err := store.Release(context.Background(), handle.ID, input.KeeperID)
	if err != nil || !bytes.Equal(first, signature) {
		t.Fatalf("first=%x err=%v", first, err)
	}
	_, second, err := store.Release(context.Background(), handle.ID, input.KeeperID)
	if err != nil || !bytes.Equal(second, signature) {
		t.Fatalf("second=%x err=%v", second, err)
	}
	terminal, err := store.Finalize(handle.ID, "CONSUMED")
	if err != nil || terminal.State != Terminal || len(store.encryptedForTest(handle.ID)) == 0 {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
}

func TestSignerLedgerExactPrepareReplayReturnsOriginalHandleAndRejectsMutation(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	store := signerStore(t, now, testActivationVerifier{})
	input := signerInput(now, bytes.Repeat([]byte{0x41}, 65))
	first, err := store.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Prepare(context.Background(), input)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	input.Signature = bytes.Repeat([]byte{0x42}, 65)
	if _, err := store.Prepare(context.Background(), input); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mutated signature error=%v", err)
	}
}

func TestSignerLedgerRejectsAlreadyExpiredPrepare(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	store := signerStore(t, now, testActivationVerifier{})
	input := signerInput(now, bytes.Repeat([]byte{0x43}, 65))
	input.ValidUntil = now
	if _, err := store.Prepare(context.Background(), input); !errors.Is(err, ErrTransition) {
		t.Fatalf("expired prepare error=%v", err)
	}
}

func TestPreparedExpiryRequiresAuthoritativeUnactivatedProof(t *testing.T) {
	current := time.Unix(1800000000, 0).UTC()
	clock := func() time.Time { return current }
	cipher, _ := NewAESGCMCipher(bytes.Repeat([]byte{1}, 32), bytes.NewReader(bytes.Repeat([]byte{2}, 128)))
	store, _ := NewSignerStore(cipher, testActivationVerifier{expiryErr: errors.New("control plane says active")}, clock, bytes.NewReader(bytes.Repeat([]byte{3}, 64)))
	handle, err := store.Prepare(context.Background(), signerInput(current, bytes.Repeat([]byte{4}, 65)))
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(11 * time.Minute)
	if _, err := store.Expire(context.Background(), handle.ID); err == nil {
		t.Fatal("expired prepared artifact without unactivated proof")
	}
}

func TestFileSignerLedgerSurvivesRestartWithExactEncryptedArtifact(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	path := filepath.Join(t.TempDir(), "signer-ledger.jsonl")
	key := bytes.Repeat([]byte{1}, 32)
	signature := bytes.Repeat([]byte{0x55}, 65)
	store := openFileSignerStore(t, path, key, now)
	handle, err := store.Prepare(context.Background(), signerInput(now, signature))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := store.encryptedForTest(handle.ID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openFileSignerStore(t, path, key, now)
	defer store.Close()
	if !bytes.Equal(store.encryptedForTest(handle.ID), encrypted) {
		t.Fatal("restart changed encrypted signer artifact")
	}
	if _, err := store.Activate(context.Background(), handle.ID, activationProof(handle, now)); err != nil {
		t.Fatal(err)
	}
	_, released, err := store.Release(context.Background(), handle.ID, "keeper-primary")
	if err != nil || !bytes.Equal(released, signature) {
		t.Fatalf("released=%x err=%v", released, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openFileSignerStore(t, path, key, now)
	defer store.Close()
	_, replayed, err := store.Release(context.Background(), handle.ID, "keeper-primary")
	if err != nil || !bytes.Equal(replayed, signature) {
		t.Fatalf("replayed=%x err=%v", replayed, err)
	}
}

func TestFileSignerLedgerRejectsTamperingAndInsecurePermissions(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	directory := t.TempDir()
	path := filepath.Join(directory, "signer-ledger.jsonl")
	key := bytes.Repeat([]byte{1}, 32)
	store := openFileSignerStore(t, path, key, now)
	if _, err := store.Prepare(context.Background(), signerInput(now, bytes.Repeat([]byte{6}, 65))); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileSignerStore(path, testCipher(t, key), testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128))); err == nil {
		t.Fatal("tampered signer ledger reopened")
	}
	insecure := filepath.Join(directory, "insecure.jsonl")
	if err := os.WriteFile(insecure, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileSignerStore(insecure, testCipher(t, key), testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128))); err == nil {
		t.Fatal("insecure signer ledger reopened")
	}
}

func TestFileSignerLedgerRejectsInsecureParentDirectory(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	path := filepath.Join(directory, "signer-ledger.jsonl")
	if _, err := OpenFileSignerStore(path, testCipher(t, bytes.Repeat([]byte{1}, 32)), testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128))); err == nil {
		t.Fatal("signer ledger opened inside a group/other-writable directory")
	}
}

func TestFileSignerLedgerRejectsSymlinkParentDirectory(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	target := t.TempDir()
	parent := t.TempDir()
	linked := filepath.Join(parent, "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linked, "signer-ledger.jsonl")
	if _, err := OpenFileSignerStore(path, testCipher(t, bytes.Repeat([]byte{1}, 32)), testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128))); err == nil {
		t.Fatal("signer ledger opened through a symlink parent directory")
	}
}

func TestSignerStoreRefusesOpaqueHandleCollisionWithoutOverwriting(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cipher := testCipher(t, bytes.Repeat([]byte{1}, 32))
	store, err := NewSignerStore(cipher, testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 32*4)))
	if err != nil {
		t.Fatal(err)
	}
	first := signerInput(now, bytes.Repeat([]byte{4}, 65))
	handle, err := store.Prepare(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.RequestID, second.AuthorizationID, second.ReservationID = bearerHash(81), bearerHash(82), bearerHash(83)
	second.OperationID, second.ActionID, second.SignerRequestHash = bearerHash(84), "lock-action-2", bearerHash(85)
	if _, err := store.Prepare(context.Background(), second); err == nil {
		t.Fatal("duplicate opaque handle entropy overwrote the first record")
	}
	if replayed, exists, err := store.PreparedFor(first.OperationID, first.ActionID, first.SignerRequestHash); err != nil || !exists || replayed.ID != handle.ID {
		t.Fatalf("first record changed: replayed=%+v exists=%t err=%v", replayed, exists, err)
	}
}

func TestFileSignerLedgerAuthenticatesCiphertextAfterValidHashChainReplay(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	path := filepath.Join(t.TempDir(), "signer-ledger.jsonl")
	key := bytes.Repeat([]byte{1}, 32)
	store := openFileSignerStore(t, path, key, now)
	if _, err := store.Prepare(context.Background(), signerInput(now, bytes.Repeat([]byte{0x6a}, 65))); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event signerLedgerEvent
	if err := json.Unmarshal(bytes.TrimSpace(raw), &event); err != nil {
		t.Fatal(err)
	}
	var record signerRecord
	if err := json.Unmarshal(event.Record, &record); err != nil {
		t.Fatal(err)
	}
	record.Encrypted[len(record.Encrypted)/2] ^= 1
	event.Record, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	event.Hash, err = signerLedgerEventHash(signerLedgerHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At,
		HandleID: event.HandleID, PreviousHash: event.PreviousHash, Record: event.Record,
	})
	if err != nil {
		t.Fatal(err)
	}
	rehashed, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	rehashed = append(rehashed, '\n')
	if err := os.WriteFile(path, rehashed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileSignerStore(path, testCipher(t, key), testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128))); err == nil {
		t.Fatal("ledger with forged valid hash chain and unauthentic ciphertext reopened")
	}
}

func TestFileSignerLedgerRevalidatesForgedActivationAfterValidHashChainReplay(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	path := filepath.Join(t.TempDir(), "signer-ledger.jsonl")
	key := bytes.Repeat([]byte{1}, 32)
	store := openFileSignerStore(t, path, key, now)
	if _, err := store.Prepare(context.Background(), signerInput(now, bytes.Repeat([]byte{0x6b}, 65))); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event signerLedgerEvent
	if err := json.Unmarshal(bytes.TrimSpace(raw), &event); err != nil {
		t.Fatal(err)
	}
	var record signerRecord
	if err := json.Unmarshal(event.Record, &record); err != nil {
		t.Fatal(err)
	}
	preparedEvent := event
	record.Handle.State = Active
	proof := activationProof(record.Handle, now)
	record.Activation = &proof
	event = signerLedgerEvent{
		Version: signerLedgerVersion, Sequence: preparedEvent.Sequence + 1, At: now,
		HandleID: record.Handle.ID, PreviousHash: preparedEvent.Hash,
	}
	event.Record, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	event.Hash, err = signerLedgerEventHash(signerLedgerHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At,
		HandleID: event.HandleID, PreviousHash: event.PreviousHash, Record: event.Record,
	})
	if err != nil {
		t.Fatal(err)
	}
	preparedLine, err := json.Marshal(preparedEvent)
	if err != nil {
		t.Fatal(err)
	}
	forgedLine, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	rehashed := append(append(preparedLine, '\n'), forgedLine...)
	if err := os.WriteFile(path, append(rehashed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := testActivationVerifier{activationErr: errors.New("primary registry has no activation")}
	if _, err := OpenFileSignerStore(path, testCipher(t, key), verifier, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128))); err == nil {
		t.Fatal("ledger with forged activation and recomputed hash chain reopened")
	}
}

func signerStore(t *testing.T, now time.Time, verifier ActivationVerifier) *SignerStore {
	t.Helper()
	cipher, err := NewAESGCMCipher(bytes.Repeat([]byte{1}, 32), bytes.NewReader(bytes.Repeat([]byte{2}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSignerStore(cipher, verifier, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openFileSignerStore(t *testing.T, path string, key []byte, now time.Time) *SignerStore {
	t.Helper()
	store, err := OpenFileSignerStore(path, testCipher(t, key), testActivationVerifier{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{3}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testCipher(t *testing.T, key []byte) *AESGCMCipher {
	t.Helper()
	cipher, err := NewAESGCMCipher(key, bytes.NewReader(bytes.Repeat([]byte{2}, 1024)))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func signerInput(now time.Time, signature []byte) PrepareInput {
	return PrepareInput{
		RequestID: bearerHash(8), AuthorizationID: bearerHash(9), ReservationID: bearerHash(10),
		ActionID: "lock-action-1", OperationID: bearerHash(1), SignerRequestHash: bearerHash(7), CanonicalPayloadHash: bearerHash(2),
		Digest: bearerHash(3), Nonce: bearerHash(4), SignerKeyID: "signer-key-1", KeyEpoch: 1,
		KeeperID: "keeper-primary", ValidAfter: now, ValidUntil: now.Add(9 * time.Minute), Signature: signature,
	}
}

func activationProof(handle Handle, now time.Time) ActivationProof {
	return ActivationProof{RequestID: handle.RequestID, HandleID: handle.ID, OperationID: handle.OperationID,
		Digest: handle.Digest, Nonce: handle.Nonce, PrimaryMirrorDigest: bearerHash(6), ActivationOccurredAt: now}
}

func bearerHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
