package referencesigner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrNonceAlreadyClaimed = errors.New("nonce already claimed")

type NonceStore interface {
	// Claim must make the nonce durable before it returns nil. A failed or
	// ambiguous durability operation must fail closed.
	Claim(ctx context.Context, key string, claimedAt time.Time) error
	Close() error
}

// FileNonceStore is a minimal customer-controlled append-only nonce journal.
// It is suitable for the reference signer and deliberately burns a nonce if a
// crash occurs after the durable claim but before a broadcast.
type FileNonceStore struct {
	mu      sync.Mutex
	file    *os.File
	claimed map[string]struct{}
}

func OpenFileNonceStore(path string) (*FileNonceStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("nonce journal path is required")
	}
	file, created, err := openNonceJournalFile(path)
	if err != nil {
		return nil, fmt.Errorf("open nonce journal: %w", err)
	}
	if created {
		if err := syncParentDirectory(path); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("sync nonce journal directory: %w", err)
		}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock nonce journal (another signer may be using it): %w", err)
	}
	store := &FileNonceStore{file: file, claimed: make(map[string]struct{})}
	if err := store.replay(); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("seek nonce journal: %w", err)
	}
	return store, nil
}

func openNonceJournalFile(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
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

func (s *FileNonceStore) replay() error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek nonce journal for replay: %w", err)
	}
	scanner := bufio.NewScanner(s.file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		parts := strings.Split(line, "\t")
		if len(parts) != 2 || parts[0] == "" {
			return fmt.Errorf("nonce journal line %d is malformed", lineNumber)
		}
		if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
			return fmt.Errorf("nonce journal line %d has invalid timestamp", lineNumber)
		}
		if _, exists := s.claimed[parts[0]]; exists {
			return fmt.Errorf("nonce journal line %d duplicates a prior claim", lineNumber)
		}
		s.claimed[parts[0]] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read nonce journal: %w", err)
	}
	return nil
}

func (s *FileNonceStore) Claim(ctx context.Context, key string, claimedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" || strings.ContainsAny(key, "\t\r\n") {
		return errors.New("nonce claim key is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.claimed[key]; exists {
		return ErrNonceAlreadyClaimed
	}
	record := key + "\t" + strconv.FormatInt(claimedAt.Unix(), 10) + "\n"
	n, err := s.file.WriteString(record)
	if err != nil {
		return fmt.Errorf("append nonce claim: %w", err)
	}
	if n != len(record) {
		return errors.New("append nonce claim: short write")
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("sync nonce claim: %w", err)
	}
	s.claimed[key] = struct{}{}
	return nil
}

func (s *FileNonceStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(s.file.Fd()), syscall.LOCK_UN)
	err := s.file.Close()
	s.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return err
}
