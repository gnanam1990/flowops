package controlplane

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

type Event struct {
	Version      int             `json:"version"`
	Sequence     uint64          `json:"sequence"`
	At           int64           `json:"at"`
	Kind         string          `json:"kind"`
	RequestID    string          `json:"requestId"`
	PreviousHash string          `json:"previousHash"`
	Payload      json.RawMessage `json:"payload"`
	Hash         string          `json:"hash"`
}

// EventJournal is the durability boundary for intent, approval, issuance, and
// expiry events. Implementations must append atomically and return events in
// strict sequence order. The lifecycle replays the complete stream at startup
// and refuses to start when any hash-chain invariant is broken.
type EventJournal interface {
	Append(ctx context.Context, at time.Time, kind, requestID string, payload any) (Event, error)
	Events() []Event
}

type eventHashInput struct {
	Version      int             `json:"version"`
	Sequence     uint64          `json:"sequence"`
	At           int64           `json:"at"`
	Kind         string          `json:"kind"`
	RequestID    string          `json:"requestId"`
	PreviousHash string          `json:"previousHash"`
	Payload      json.RawMessage `json:"payload"`
}

type Journal struct {
	mu       sync.Mutex
	file     *os.File
	events   []Event
	lastHash string
	fault    error
}

func OpenJournal(path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("journal path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock journal (another control plane may be using it): %w", err)
	}
	j := &Journal{file: file}
	if err := j.replay(); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("seek journal end: %w", err)
	}
	return j, nil
}

func (j *Journal) replay() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek journal for replay: %w", err)
	}
	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("journal line %d is invalid JSON: %w", len(j.events)+1, err)
		}
		if err := validateEvent(event, uint64(len(j.events)+1), j.lastHash); err != nil {
			return fmt.Errorf("journal line %d: %w", len(j.events)+1, err)
		}
		j.events = append(j.events, cloneEvent(event))
		j.lastHash = event.Hash
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read journal: %w", err)
	}
	return nil
}

func validateEvent(event Event, expectedSequence uint64, previousHash string) error {
	if event.Version != journalVersion {
		return fmt.Errorf("version %d is unsupported", event.Version)
	}
	if event.Sequence != expectedSequence {
		return fmt.Errorf("sequence %d, want %d", event.Sequence, expectedSequence)
	}
	if event.At <= 0 || event.Kind == "" || event.RequestID == "" || len(event.Payload) == 0 {
		return errors.New("required event field is empty")
	}
	if event.PreviousHash != previousHash {
		return errors.New("previous hash does not match journal chain")
	}
	want, err := hashEvent(eventHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At, Kind: event.Kind,
		RequestID: event.RequestID, PreviousHash: event.PreviousHash, Payload: event.Payload,
	})
	if err != nil {
		return err
	}
	if event.Hash != want {
		return errors.New("event hash does not match content")
	}
	return nil
}

func hashEvent(input eventHashInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode event hash input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (j *Journal) Append(ctx context.Context, at time.Time, kind, requestID string, payload any) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if kind == "" || requestID == "" || strings.ContainsAny(kind+requestID, "\r\n\t") {
		return Event{}, errors.New("event kind or request ID is invalid")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return Event{}, errors.New("journal is closed")
	}
	if j.fault != nil {
		return Event{}, fmt.Errorf("journal is faulted after a prior durability failure: %w", j.fault)
	}
	event := Event{
		Version: journalVersion, Sequence: uint64(len(j.events) + 1), At: at.UTC().Unix(),
		Kind: kind, RequestID: requestID, PreviousHash: j.lastHash, Payload: raw,
	}
	event.Hash, err = hashEvent(eventHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At, Kind: event.Kind,
		RequestID: event.RequestID, PreviousHash: event.PreviousHash, Payload: event.Payload,
	})
	if err != nil {
		return Event{}, err
	}
	if err := validateEvent(event, event.Sequence, j.lastHash); err != nil {
		return Event{}, fmt.Errorf("validate event before append: %w", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("encode event: %w", err)
	}
	line = append(line, '\n')
	n, err := j.file.Write(line)
	if err != nil {
		j.fault = err
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	if n != len(line) {
		j.fault = errors.New("short write")
		return Event{}, errors.New("append event: short write")
	}
	if err := j.file.Sync(); err != nil {
		j.fault = err
		return Event{}, fmt.Errorf("sync event: %w", err)
	}
	j.events = append(j.events, cloneEvent(event))
	j.lastHash = event.Hash
	return cloneEvent(event), nil
}

func (j *Journal) Events() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]Event, len(j.events))
	for i, event := range j.events {
		result[i] = cloneEvent(event)
	}
	return result
}

func cloneEvent(event Event) Event {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}

func (j *Journal) Close() error {
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
