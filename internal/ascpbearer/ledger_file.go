package ascpbearer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/securefile"
	"golang.org/x/sys/unix"
)

const signerLedgerVersion = 2

type signerLedgerEvent struct {
	Version      int             `json:"version"`
	Sequence     uint64          `json:"sequence"`
	At           time.Time       `json:"at"`
	HandleID     string          `json:"handleId"`
	PreviousHash string          `json:"previousHash"`
	Record       json.RawMessage `json:"record"`
	Hash         string          `json:"hash"`
}

type signerLedgerHashInput struct {
	Version      int             `json:"version"`
	Sequence     uint64          `json:"sequence"`
	At           time.Time       `json:"at"`
	HandleID     string          `json:"handleId"`
	PreviousHash string          `json:"previousHash"`
	Record       json.RawMessage `json:"record"`
}

type signerJournal struct {
	mu       sync.Mutex
	file     *os.File
	sequence uint64
	lastHash string
	records  map[string]signerRecord
	fault    error
}

// OpenFileSignerStore opens a process-locked, 0600, append-only, hash-chained
// signer ledger. Each transition is fsynced before the method can return.
func OpenFileSignerStore(path string, artifactCipher ArtifactCipher, verifier ActivationVerifier, clock func() time.Time, random io.Reader) (*SignerStore, error) {
	return OpenFileSignerStoreContext(context.Background(), path, artifactCipher, verifier, clock, random)
}

// OpenFileSignerStoreContext additionally bounds ledger replay and persisted
// artifact revalidation by the caller's startup/shutdown context.
func OpenFileSignerStoreContext(ctx context.Context, path string, artifactCipher ArtifactCipher, verifier ActivationVerifier, clock func() time.Time, random io.Reader) (*SignerStore, error) {
	if ctx == nil {
		return nil, errors.New("signer ledger startup context is required")
	}
	if artifactCipher == nil || verifier == nil {
		return nil, errors.New("artifact cipher and activation verifier are required")
	}
	journal, err := openSignerJournal(ctx, path)
	if err != nil {
		return nil, err
	}
	store, err := NewSignerStore(artifactCipher, verifier, clock, random)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	store.journal = journal
	for id, record := range journal.records {
		if err := ctx.Err(); err != nil {
			_ = journal.Close()
			return nil, fmt.Errorf("revalidate signer ledger: %w", err)
		}
		if id != record.Handle.ID {
			_ = journal.Close()
			return nil, fmt.Errorf("authenticate signer ledger artifact %s: event and record handle differ", id)
		}
		artifact, err := artifactCipher.Decrypt(ctx, artifactAAD(record.Handle), record.Encrypted)
		if err != nil {
			_ = journal.Close()
			return nil, fmt.Errorf("authenticate signer ledger artifact %s: %w", id, err)
		}
		if len(artifact) < 64 || len(artifact) > 16*1024 {
			clear(artifact)
			_ = journal.Close()
			return nil, fmt.Errorf("authenticate signer ledger artifact %s: plaintext length is invalid", id)
		}
		clear(artifact)
		switch record.Handle.State {
		case Active, Released, Terminal:
			if err := verifier.VerifyActivation(ctx, record.Handle, *record.Activation); err != nil {
				_ = journal.Close()
				return nil, fmt.Errorf("revalidate signer ledger activation %s: %w", id, err)
			}
		case Expired:
			if store.clock().UTC().Before(record.Handle.ValidUntil) {
				_ = journal.Close()
				return nil, fmt.Errorf("revalidate signer ledger expiry %s: validity window has not elapsed", id)
			}
			if err := verifier.ProveUnactivated(ctx, record.Handle, store.clock().UTC()); err != nil {
				_ = journal.Close()
				return nil, fmt.Errorf("revalidate signer ledger expiry %s: %w", id, err)
			}
		}
		store.byID[id] = cloneSignerRecord(record)
		store.byAction[signerActionKey(record.Handle.OperationID, record.Handle.ActionID)] = id
	}
	return store, nil
}

func openSignerJournal(ctx context.Context, path string) (*signerJournal, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("signer ledger path must be a clean absolute file path")
	}
	directory, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open signer ledger directory without following symlinks: %w", err)
	}
	defer directory.Close()
	directoryInfo, err := directory.Stat()
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("signer ledger directory must be a non-symlink directory not writable by group or other users")
	}
	if !securefile.OwnerAllowed(directoryInfo) {
		return nil, errors.New("signer ledger directory must be owned by the runtime user or root")
	}
	file, created, err := openSignerLedgerFile(directory, filepath.Base(path), path)
	if err != nil {
		return nil, fmt.Errorf("open signer ledger: %w", err)
	}
	if created {
		if err := directory.Sync(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("sync signer ledger directory: %w", err)
		}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock signer ledger (another signer may be active): %w", err)
	}
	journal := &signerJournal{file: file, records: make(map[string]signerRecord)}
	if err := journal.replay(ctx); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = journal.Close()
		return nil, fmt.Errorf("seek signer ledger: %w", err)
	}
	return journal, nil
}

func openSignerLedgerFile(directory *os.File, name, displayPath string) (*os.File, bool, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), displayPath)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, false, errors.New("signer ledger must be a regular file inaccessible to group and other users")
	}
	if !securefile.OwnerAllowed(info) {
		_ = file.Close()
		return nil, false, errors.New("signer ledger must be owned by the runtime user or root")
	}
	return file, created, nil
}

func (j *signerJournal) replay(ctx context.Context) error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek signer ledger for replay: %w", err)
	}
	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("replay signer ledger: %w", err)
		}
		if j.sequence == math.MaxUint64 {
			return errors.New("signer ledger sequence exhausted")
		}
		var event signerLedgerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("signer ledger line %d: %w", j.sequence+1, err)
		}
		if err := validateSignerLedgerEvent(event, j.sequence+1, j.lastHash); err != nil {
			return fmt.Errorf("signer ledger line %d: %w", j.sequence+1, err)
		}
		var record signerRecord
		if err := json.Unmarshal(event.Record, &record); err != nil {
			return fmt.Errorf("signer ledger line %d record: %w", j.sequence+1, err)
		}
		if err := j.validateTransition(record); err != nil {
			return fmt.Errorf("signer ledger line %d transition: %w", j.sequence+1, err)
		}
		if event.HandleID != record.Handle.ID {
			return fmt.Errorf("signer ledger line %d: event and record handle differ", j.sequence+1)
		}
		j.records[event.HandleID] = cloneSignerRecord(record)
		j.sequence, j.lastHash = event.Sequence, event.Hash
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read signer ledger: %w", err)
	}
	return nil
}

func (j *signerJournal) Append(ctx context.Context, at time.Time, record signerRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return errors.New("signer ledger is closed")
	}
	if j.fault != nil {
		return fmt.Errorf("signer ledger is faulted: %w", j.fault)
	}
	if j.sequence == math.MaxUint64 {
		return errors.New("signer ledger sequence exhausted")
	}
	if err := j.validateTransition(record); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(raw) > 512<<10 {
		return errors.New("signer ledger record exceeds 512 KiB")
	}
	event := signerLedgerEvent{Version: signerLedgerVersion, Sequence: j.sequence + 1, At: at.UTC(), HandleID: record.Handle.ID, PreviousHash: j.lastHash, Record: raw}
	event.Hash, err = signerLedgerEventHash(signerLedgerHashInput{Version: event.Version, Sequence: event.Sequence, At: event.At, HandleID: event.HandleID, PreviousHash: event.PreviousHash, Record: event.Record})
	if err != nil {
		return err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	written, err := j.file.Write(line)
	if err != nil || written != len(line) {
		if err == nil {
			err = errors.New("short write")
		}
		j.fault = err
		return fmt.Errorf("append signer ledger: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		j.fault = err
		return fmt.Errorf("sync signer ledger: %w", err)
	}
	j.records[event.HandleID] = cloneSignerRecord(record)
	j.sequence, j.lastHash = event.Sequence, event.Hash
	return nil
}

func (j *signerJournal) validateTransition(next signerRecord) error {
	if err := validateSignerRecord(next); err != nil {
		return err
	}
	current, exists := j.records[next.Handle.ID]
	if !exists {
		if next.Handle.State != Prepared {
			return errors.New("first signer ledger state must be PREPARED")
		}
		for _, record := range j.records {
			if record.Handle.OperationID == next.Handle.OperationID && record.Handle.ActionID == next.Handle.ActionID {
				return ErrMismatch
			}
		}
		return nil
	}
	if !sameHandleIdentity(current.Handle, next.Handle) || !bytes.Equal(current.Encrypted, next.Encrypted) {
		return ErrMismatch
	}
	if current.Activation != nil && (next.Activation == nil || !sameActivationProof(*current.Activation, *next.Activation)) {
		return ErrMismatch
	}
	valid := current.Handle.State == Prepared && (next.Handle.State == Active || next.Handle.State == Expired) ||
		current.Handle.State == Active && next.Handle.State == Released ||
		current.Handle.State == Released && next.Handle.State == Terminal
	if !valid {
		return fmt.Errorf("invalid signer ledger transition %s -> %s", current.Handle.State, next.Handle.State)
	}
	return nil
}

func validateSignerRecord(record signerRecord) error {
	handle := record.Handle
	if !opaqueHandle(handle.ID) || !hash(handle.RequestID) || !hash(handle.AuthorizationID) || !hash(handle.ReservationID) ||
		!identifier(handle.ActionID) || !hash(handle.OperationID) || !hash(handle.SignerRequestHash) ||
		!hash(handle.CanonicalPayloadHash) || !hash(handle.Digest) || !hash(handle.Nonce) ||
		!identifier(handle.SignerKeyID) || handle.KeyEpoch == 0 || !identifier(handle.KeeperID) ||
		handle.ValidAfter.IsZero() || !handle.ValidAfter.Before(handle.ValidUntil) ||
		handle.ValidUntil.Sub(handle.ValidAfter) > maximumAuthorizationWindow || len(record.Encrypted) == 0 {
		return ErrTransition
	}
	if (handle.State == Prepared || handle.State == Active || handle.State == Released) && handle.Outcome != "" {
		return ErrTransition
	}
	if handle.State == Expired && handle.Outcome != "EXPIRED_UNACTIVATED" || handle.State == Terminal && handle.Outcome == "" {
		return ErrTransition
	}
	if (handle.State == Active || handle.State == Released || handle.State == Terminal) && (record.Activation == nil || !exactActivationProof(handle, *record.Activation)) {
		return ErrTransition
	}
	if (handle.State == Prepared || handle.State == Expired) && record.Activation != nil {
		return ErrTransition
	}
	return nil
}

func sameHandleIdentity(current, next Handle) bool {
	return current.ID == next.ID && current.RequestID == next.RequestID && current.AuthorizationID == next.AuthorizationID &&
		current.ReservationID == next.ReservationID && current.ActionID == next.ActionID && current.OperationID == next.OperationID &&
		current.SignerRequestHash == next.SignerRequestHash && current.CanonicalPayloadHash == next.CanonicalPayloadHash && current.Digest == next.Digest &&
		current.Nonce == next.Nonce && current.SignerKeyID == next.SignerKeyID && current.KeyEpoch == next.KeyEpoch &&
		current.KeeperID == next.KeeperID && current.ValidAfter.Equal(next.ValidAfter) && current.ValidUntil.Equal(next.ValidUntil)
}

func validateSignerLedgerEvent(event signerLedgerEvent, sequence uint64, previousHash string) error {
	if event.Version != signerLedgerVersion || event.Sequence != sequence || event.At.IsZero() || !opaqueHandle(event.HandleID) || len(event.Record) == 0 || event.PreviousHash != previousHash {
		return errors.New("signer ledger event fields are invalid")
	}
	want, err := signerLedgerEventHash(signerLedgerHashInput{Version: event.Version, Sequence: event.Sequence, At: event.At, HandleID: event.HandleID, PreviousHash: event.PreviousHash, Record: event.Record})
	if err != nil {
		return err
	}
	if event.Hash != want {
		return errors.New("signer ledger hash does not match content")
	}
	return nil
}

func signerLedgerEventHash(input signerLedgerHashInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneSignerRecord(record signerRecord) signerRecord {
	record.Encrypted = append([]byte(nil), record.Encrypted...)
	if record.Activation != nil {
		proof := *record.Activation
		record.Activation = &proof
	}
	return record
}

func (j *signerJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(j.file.Fd()), syscall.LOCK_UN)
	closeErr := j.file.Close()
	j.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
