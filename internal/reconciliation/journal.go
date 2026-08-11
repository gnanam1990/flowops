package reconciliation

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const journalVersion = 1

type journalEvent struct {
	Version      int             `json:"version"`
	Sequence     uint64          `json:"sequence"`
	At           time.Time       `json:"at"`
	Kind         string          `json:"kind"`
	Key          string          `json:"key"`
	PreviousHash string          `json:"previousHash"`
	Payload      json.RawMessage `json:"payload"`
	Hash         string          `json:"hash"`
}

type journalHashInput struct {
	Version      int             `json:"version"`
	Sequence     uint64          `json:"sequence"`
	At           time.Time       `json:"at"`
	Kind         string          `json:"kind"`
	Key          string          `json:"key"`
	PreviousHash string          `json:"previousHash"`
	Payload      json.RawMessage `json:"payload"`
}

type journal struct {
	mu       sync.Mutex
	file     *os.File
	events   []journalEvent
	lastHash string
	fault    error
}

func openJournal(path string) (*journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("reconciliation journal path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open reconciliation journal: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock reconciliation journal: %w", err)
	}
	j := &journal{file: file}
	if err := j.replay(); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("seek reconciliation journal: %w", err)
	}
	return j, nil
}

func (j *journal) replay() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var event journalEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("reconciliation journal line %d: %w", len(j.events)+1, err)
		}
		if err := validateJournalEvent(event, uint64(len(j.events)+1), j.lastHash); err != nil {
			return fmt.Errorf("reconciliation journal line %d: %w", len(j.events)+1, err)
		}
		j.events = append(j.events, cloneJournalEvent(event))
		j.lastHash = event.Hash
	}
	return scanner.Err()
}

func validateJournalEvent(event journalEvent, sequence uint64, previousHash string) error {
	if event.Version != journalVersion || event.Sequence != sequence || event.At.IsZero() || event.Kind == "" || event.Key == "" || len(event.Payload) == 0 {
		return errors.New("journal event fields are invalid")
	}
	if event.PreviousHash != previousHash {
		return errors.New("journal hash chain is broken")
	}
	want, err := journalEventHash(journalHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At, Kind: event.Kind,
		Key: event.Key, PreviousHash: event.PreviousHash, Payload: event.Payload,
	})
	if err != nil {
		return err
	}
	if event.Hash != want {
		return errors.New("journal event hash does not match content")
	}
	return nil
}

func journalEventHash(input journalHashInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func (j *journal) append(ctx context.Context, at time.Time, kind, key string, payload any) (journalEvent, error) {
	if err := ctx.Err(); err != nil {
		return journalEvent{}, err
	}
	if kind == "" || key == "" || strings.ContainsAny(kind+key, "\r\n\t") {
		return journalEvent{}, errors.New("journal kind or key is invalid")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return journalEvent{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return journalEvent{}, errors.New("reconciliation journal is closed")
	}
	if j.fault != nil {
		return journalEvent{}, fmt.Errorf("reconciliation journal is faulted: %w", j.fault)
	}
	event := journalEvent{Version: journalVersion, Sequence: uint64(len(j.events) + 1), At: at.UTC(), Kind: kind, Key: key, PreviousHash: j.lastHash, Payload: raw}
	event.Hash, err = journalEventHash(journalHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At, Kind: event.Kind,
		Key: event.Key, PreviousHash: event.PreviousHash, Payload: event.Payload,
	})
	if err != nil {
		return journalEvent{}, err
	}
	if err := validateJournalEvent(event, event.Sequence, j.lastHash); err != nil {
		return journalEvent{}, fmt.Errorf("validate reconciliation event before append: %w", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return journalEvent{}, err
	}
	line = append(line, '\n')
	if written, err := j.file.Write(line); err != nil || written != len(line) {
		if err == nil {
			err = errors.New("short write")
		}
		j.fault = err
		return journalEvent{}, fmt.Errorf("append reconciliation event: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		j.fault = err
		return journalEvent{}, fmt.Errorf("sync reconciliation event: %w", err)
	}
	j.events = append(j.events, cloneJournalEvent(event))
	j.lastHash = event.Hash
	return cloneJournalEvent(event), nil
}

func (j *journal) Events() []journalEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]journalEvent, len(j.events))
	for index, event := range j.events {
		result[index] = cloneJournalEvent(event)
	}
	return result
}

func cloneJournalEvent(event journalEvent) journalEvent {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}

func (j *journal) Close() error {
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
