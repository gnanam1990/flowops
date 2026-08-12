package referencesigner

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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const attemptJournalVersion = 1

type attemptEvent struct {
	Version         int             `json:"version"`
	Sequence        uint64          `json:"sequence"`
	At              time.Time       `json:"at"`
	AuthorizationID string          `json:"authorizationId"`
	PreviousHash    string          `json:"previousHash"`
	Payload         json.RawMessage `json:"payload"`
	Hash            string          `json:"hash"`
}

type attemptHashInput struct {
	Version         int             `json:"version"`
	Sequence        uint64          `json:"sequence"`
	At              time.Time       `json:"at"`
	AuthorizationID string          `json:"authorizationId"`
	PreviousHash    string          `json:"previousHash"`
	Payload         json.RawMessage `json:"payload"`
}

// AttemptJournal is a process-locked, append-only, hash-chained customer-side
// state store. Every append is synchronized before the executor may cross the
// next network boundary.
type AttemptJournal struct {
	mu       sync.Mutex
	file     *os.File
	sequence uint64
	attempts map[string]Attempt
	lastHash string
	fault    error
}

func OpenAttemptJournal(path string) (*AttemptJournal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("signer attempt journal path is required")
	}
	file, created, err := openAttemptJournalFile(path)
	if err != nil {
		return nil, fmt.Errorf("open signer attempt journal: %w", err)
	}
	if created {
		if err := syncParentDirectory(path); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("sync signer attempt journal directory: %w", err)
		}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock signer attempt journal (another executor may be active): %w", err)
	}
	journal := &AttemptJournal{file: file, attempts: make(map[string]Attempt)}
	if err := journal.replay(); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("seek signer attempt journal: %w", err)
	}
	return journal, nil
}

func openAttemptJournalFile(path string) (*os.File, bool, error) {
	flags := os.O_CREATE | os.O_EXCL | os.O_RDWR | syscall.O_NOFOLLOW
	file, err := os.OpenFile(path, flags, 0o600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, false, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, false, errors.New("journal must be a regular file inaccessible to group and other users")
	}
	return file, created, nil
}

func syncParentDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (j *AttemptJournal) replay() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek signer attempt journal for replay: %w", err)
	}
	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		if j.sequence == math.MaxUint64 {
			return errors.New("signer attempt journal sequence exhausted")
		}
		lineNumber := j.sequence + 1
		var event attemptEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("signer attempt journal line %d: %w", lineNumber, err)
		}
		if err := validateAttemptEvent(event, lineNumber, j.lastHash); err != nil {
			return fmt.Errorf("signer attempt journal line %d: %w", lineNumber, err)
		}
		var attempt Attempt
		if err := json.Unmarshal(event.Payload, &attempt); err != nil {
			return fmt.Errorf("signer attempt journal line %d payload: %w", lineNumber, err)
		}
		if err := j.apply(event.AuthorizationID, attempt); err != nil {
			return fmt.Errorf("signer attempt journal line %d transition: %w", lineNumber, err)
		}
		j.sequence = event.Sequence
		j.lastHash = event.Hash
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read signer attempt journal: %w", err)
	}
	return nil
}

func validateAttemptEvent(event attemptEvent, sequence uint64, previousHash string) error {
	if event.Version != attemptJournalVersion || event.Sequence != sequence || event.At.IsZero() || !envelopeIdentifier(event.AuthorizationID) || len(event.Payload) == 0 {
		return errors.New("attempt journal event fields are invalid")
	}
	if event.PreviousHash != previousHash {
		return errors.New("attempt journal hash chain is broken")
	}
	want, err := attemptEventHash(attemptHashInput{Version: event.Version, Sequence: event.Sequence, At: event.At, AuthorizationID: event.AuthorizationID, PreviousHash: event.PreviousHash, Payload: event.Payload})
	if err != nil {
		return err
	}
	if event.Hash != want {
		return errors.New("attempt journal event hash does not match content")
	}
	return nil
}

func attemptEventHash(input attemptHashInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func envelopeIdentifier(value string) bool {
	return envelope.ValidIdentifier(value)
}

func (j *AttemptJournal) Append(ctx context.Context, at time.Time, attempt Attempt) (Attempt, error) {
	if err := ctx.Err(); err != nil {
		return Attempt{}, err
	}
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	authorizationID := attempt.Authorized.Authorization.AuthorizationID
	raw, err := json.Marshal(attempt)
	if err != nil {
		return Attempt{}, err
	}
	if len(raw) > 1<<20 {
		return Attempt{}, errors.New("signer attempt event exceeds 1 MiB")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return Attempt{}, errors.New("signer attempt journal is closed")
	}
	if j.fault != nil {
		return Attempt{}, fmt.Errorf("signer attempt journal is faulted: %w", j.fault)
	}
	if j.sequence == math.MaxUint64 {
		return Attempt{}, errors.New("signer attempt journal sequence exhausted")
	}
	if err := j.validateTransition(authorizationID, attempt); err != nil {
		return Attempt{}, err
	}
	event := attemptEvent{Version: attemptJournalVersion, Sequence: j.sequence + 1, At: at.UTC(), AuthorizationID: authorizationID, PreviousHash: j.lastHash, Payload: raw}
	event.Hash, err = attemptEventHash(attemptHashInput{Version: event.Version, Sequence: event.Sequence, At: event.At, AuthorizationID: event.AuthorizationID, PreviousHash: event.PreviousHash, Payload: event.Payload})
	if err != nil {
		return Attempt{}, err
	}
	if err := validateAttemptEvent(event, event.Sequence, j.lastHash); err != nil {
		return Attempt{}, fmt.Errorf("validate signer attempt event: %w", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return Attempt{}, err
	}
	line = append(line, '\n')
	written, err := j.file.Write(line)
	if err != nil || written != len(line) {
		if err == nil {
			err = errors.New("short write")
		}
		j.fault = err
		return Attempt{}, fmt.Errorf("append signer attempt event: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		j.fault = err
		return Attempt{}, fmt.Errorf("sync signer attempt event: %w", err)
	}
	if err := j.apply(authorizationID, attempt); err != nil {
		j.fault = err
		return Attempt{}, fmt.Errorf("apply durable signer attempt event: %w", err)
	}
	j.sequence = event.Sequence
	j.lastHash = event.Hash
	return cloneAttempt(attempt), nil
}

func (j *AttemptJournal) validateTransition(authorizationID string, next Attempt) error {
	current, exists := j.attempts[authorizationID]
	if !exists {
		if next.State != AttemptPrepared {
			return errors.New("first signer attempt state must be PREPARED")
		}
		return nil
	}
	if !sameAttemptIdentity(current, next) {
		return ErrAttemptConflict
	}
	valid := current.State == AttemptPrepared && next.State == AttemptBroadcasting ||
		current.State == AttemptBroadcasting && (next.State == AttemptSubmitted || next.State == AttemptAmbiguous) ||
		(current.State == AttemptSubmitted || current.State == AttemptAmbiguous) && next.State == AttemptRegistered
	if !valid {
		return fmt.Errorf("invalid signer attempt transition %s -> %s", current.State, next.State)
	}
	return nil
}

func sameAttemptIdentity(current, next Attempt) bool {
	if current.Authorization != next.Authorization || current.Authorized != next.Authorized || current.PreparedAt != next.PreparedAt ||
		current.Prepared.TransactionHash != next.Prepared.TransactionHash || current.Prepared.Sender != next.Prepared.Sender ||
		!bytes.Equal(current.Prepared.RawTransaction, next.Prepared.RawTransaction) {
		return false
	}
	if current.BroadcastAt != 0 && current.BroadcastAt != next.BroadcastAt {
		return false
	}
	if current.Receipt != nil && (next.Receipt == nil || *current.Receipt != *next.Receipt || current.ReceiptPublicKeyB64 != next.ReceiptPublicKeyB64) {
		return false
	}
	return true
}

func (j *AttemptJournal) apply(authorizationID string, attempt Attempt) error {
	if err := attempt.validate(); err != nil {
		return err
	}
	if attempt.Authorized.Authorization.AuthorizationID != authorizationID {
		return errors.New("attempt authorization ID does not match journal key")
	}
	if err := j.validateTransition(authorizationID, attempt); err != nil {
		return err
	}
	j.attempts[authorizationID] = cloneAttempt(attempt)
	return nil
}

func (j *AttemptJournal) Get(authorizationID string) (Attempt, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	attempt, ok := j.attempts[authorizationID]
	return cloneAttempt(attempt), ok
}

func (j *AttemptJournal) Attempts() []Attempt {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]Attempt, 0, len(j.attempts))
	for _, attempt := range j.attempts {
		result = append(result, cloneAttempt(attempt))
	}
	sort.Slice(result, func(i, k int) bool {
		return result[i].Authorized.Authorization.AuthorizationID < result[k].Authorized.Authorization.AuthorizationID
	})
	return result
}

func (j *AttemptJournal) Close() error {
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
