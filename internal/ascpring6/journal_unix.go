//go:build unix

package ascpring6

import (
	"bufio"
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
	"sync"
	"syscall"

	"github.com/gnanam1990/flowops/internal/securefile"
	"golang.org/x/sys/unix"
)

type journalEvent struct {
	Version  uint64        `json:"version"`
	Sequence uint64        `json:"sequence"`
	Previous string        `json:"previous"`
	Binding  ActionBinding `json:"binding"`
	Hash     string        `json:"hash"`
}

type Journal struct {
	mu       sync.Mutex
	file     *os.File
	fault    error
	sequence uint64
	lastHash string
	records  map[string]ActionBinding
}

func OpenJournal(ctx context.Context, path string) (*Journal, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("Ring 6 journal path must be a clean absolute file path")
	}
	directory, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open Ring 6 journal directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || !securefile.OwnerAllowed(info) {
		return nil, errors.New("Ring 6 journal directory must be owner controlled")
	}
	flags := unix.O_CREAT | unix.O_EXCL | unix.O_RDWR | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(int(directory.Fd()), filepath.Base(path), flags, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(directory.Fd()), filepath.Base(path), unix.O_RDWR|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open Ring 6 journal: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 || !securefile.OwnerAllowed(fileInfo) {
		_ = file.Close()
		return nil, errors.New("Ring 6 journal must be a private owner-controlled regular file")
	}
	if created {
		if err := directory.Sync(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("sync Ring 6 journal directory: %w", err)
		}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock Ring 6 journal: %w", err)
	}
	journal := &Journal{file: file, records: make(map[string]ActionBinding)}
	if err := journal.replay(ctx); err != nil {
		_ = journal.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = journal.Close()
		return nil, err
	}
	return journal, nil
}

func (j *Journal) Bind(ctx context.Context, binding ActionBinding) (ActionBinding, bool, error) {
	binding.State, binding.OperationHandle, binding.RefusalCode = "BOUND", "", ""
	if err := validateBinding(binding); err != nil {
		return ActionBinding{}, false, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ActionBinding{}, false, err
	}
	key := actionKey(binding.OperationID, binding.ActionID)
	if existing, ok := j.records[key]; ok {
		if !sameBinding(existing, binding) {
			return ActionBinding{}, false, ErrBinding
		}
		return existing, true, nil
	}
	if err := j.append(binding); err != nil {
		return ActionBinding{}, false, err
	}
	return binding, false, nil
}

func (j *Journal) MarkSigned(ctx context.Context, binding ActionBinding, handle string) (ActionBinding, error) {
	return j.transition(ctx, binding, "SIGNED", handle)
}

func (j *Journal) MarkHSMRequested(ctx context.Context, binding ActionBinding) (ActionBinding, error) {
	return j.transition(ctx, binding, "HSM_REQUESTED", "")
}

func (j *Journal) MarkRefused(ctx context.Context, binding ActionBinding, code string) (ActionBinding, error) {
	return j.transition(ctx, binding, "REFUSED", code)
}

func (j *Journal) transition(ctx context.Context, binding ActionBinding, state, value string) (ActionBinding, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ActionBinding{}, err
	}
	current, ok := j.records[actionKey(binding.OperationID, binding.ActionID)]
	if !ok || !sameBinding(current, binding) {
		return ActionBinding{}, ErrBinding
	}
	next := current
	next.State = state
	if state == "SIGNED" {
		next.OperationHandle = value
	} else {
		next.RefusalCode = value
	}
	if err := validateBinding(next); err != nil {
		return ActionBinding{}, err
	}
	if current.State == state {
		if current == next {
			return current, nil
		}
		return ActionBinding{}, ErrBinding
	}
	validTransition := current.State == "BOUND" && (state == "HSM_REQUESTED" || state == "REFUSED") ||
		current.State == "HSM_REQUESTED" && state == "SIGNED"
	if !validTransition {
		return ActionBinding{}, ErrBinding
	}
	if err := j.append(next); err != nil {
		return ActionBinding{}, err
	}
	return next, nil
}

func (j *Journal) append(binding ActionBinding) error {
	if j.file == nil {
		return errors.New("Ring 6 journal is closed")
	}
	if j.fault != nil {
		return fmt.Errorf("Ring 6 journal is faulted: %w", j.fault)
	}
	if j.sequence == math.MaxUint64 {
		return errors.New("Ring 6 journal sequence exhausted")
	}
	event := journalEvent{Version: 1, Sequence: j.sequence + 1, Previous: j.lastHash, Binding: binding}
	event.Hash = eventHash(event)
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	written, err := j.file.Write(encoded)
	if err != nil || written != len(encoded) {
		j.fault = errors.Join(err, io.ErrShortWrite)
		return fmt.Errorf("append Ring 6 journal: %w", j.fault)
	}
	if err := j.file.Sync(); err != nil {
		j.fault = err
		return fmt.Errorf("sync Ring 6 journal: %w", err)
	}
	j.sequence, j.lastHash = event.Sequence, event.Hash
	j.records[actionKey(binding.OperationID, binding.ActionID)] = binding
	return nil
}

func (j *Journal) replay(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := j.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		var terminator [1]byte
		if _, err := j.file.ReadAt(terminator[:], info.Size()-1); err != nil || terminator[0] != '\n' {
			return errors.New("Ring 6 journal has a truncated final record")
		}
	}
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event journalEvent
		if err := decodeStrict(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode Ring 6 journal line %d: %w", j.sequence+1, err)
		}
		if event.Version != 1 || event.Sequence != j.sequence+1 || event.Previous != j.lastHash || event.Hash != eventHash(event) || validateBinding(event.Binding) != nil {
			return fmt.Errorf("Ring 6 journal line %d is invalid", j.sequence+1)
		}
		key := actionKey(event.Binding.OperationID, event.Binding.ActionID)
		previous, exists := j.records[key]
		validTransition := !exists && event.Binding.State == "BOUND" || exists && sameBinding(previous, event.Binding) &&
			(previous.State == "BOUND" && (event.Binding.State == "HSM_REQUESTED" || event.Binding.State == "REFUSED") ||
				previous.State == "HSM_REQUESTED" && event.Binding.State == "SIGNED")
		if !validTransition {
			return fmt.Errorf("Ring 6 journal line %d has an invalid transition", j.sequence+1)
		}
		j.records[key] = event.Binding
		j.sequence, j.lastHash = event.Sequence, event.Hash
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	file := j.file
	j.file = nil
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return file.Close()
}

func eventHash(event journalEvent) string {
	event.Hash = ""
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(append([]byte("ASCP_RING6_JOURNAL_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:])
}

func actionKey(operationID, actionID string) string { return operationID + "\x00" + actionID }
