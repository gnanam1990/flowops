// Package ascpbearer controls the lifecycle of a prepared signer artifact.
package ascpbearer

import (
	"errors"
	"sync"
	"time"
)

type State string

const (
	Prepared State = "PREPARED"
	Active   State = "ACTIVE"
	Released State = "RELEASED"
	Expired  State = "EXPIRED"
	Terminal State = "TERMINAL"
)

var (
	ErrTransition = errors.New("invalid bearer handle transition")
	ErrMismatch   = errors.New("prepared handle binding mismatch")
)

type Handle struct {
	ID, OperationID, PayloadHash, Digest, Nonce, EncryptedArtifact string
	ValidUntil                                                     time.Time
	State                                                          State
	released                                                       []byte
}
type Store struct {
	mu   sync.Mutex
	byID map[string]Handle
}

func NewStore() *Store { return &Store{byID: map[string]Handle{}} }
func (s *Store) Prepare(h Handle) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h.ID == "" || h.OperationID == "" || h.PayloadHash == "" || h.Digest == "" || h.Nonce == "" || len(h.EncryptedArtifact) == 0 || h.ValidUntil.IsZero() {
		return Handle{}, ErrTransition
	}
	if old, ok := s.byID[h.ID]; ok {
		if old.OperationID != h.OperationID || old.PayloadHash != h.PayloadHash || old.Digest != h.Digest || old.Nonce != h.Nonce {
			return Handle{}, ErrMismatch
		}
		return old, nil
	}
	h.State = Prepared
	s.byID[h.ID] = h
	return h, nil
}
func (s *Store) Activate(id string, now time.Time) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.byID[id]
	if !ok || h.State != Prepared || !now.Before(h.ValidUntil) {
		return h, ErrTransition
	}
	h.State = Active
	s.byID[id] = h
	return h, nil
}
func (s *Store) Release(id string, now time.Time) (Handle, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.byID[id]
	if !ok {
		return h, nil, ErrTransition
	}
	if h.State == Released {
		return h, append([]byte(nil), h.released...), nil
	}
	if h.State != Active || !now.Before(h.ValidUntil) {
		return h, nil, ErrTransition
	}
	h.State = Released
	h.released = []byte(h.EncryptedArtifact)
	s.byID[id] = h
	return h, append([]byte(nil), h.released...), nil
}
func (s *Store) Expire(id string, now time.Time, activationProven bool) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.byID[id]
	if !ok || h.State != Prepared || activationProven || now.Before(h.ValidUntil) {
		return h, ErrTransition
	}
	h.State = Expired
	s.byID[id] = h
	return h, nil
}
func (s *Store) Finalize(id string) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.byID[id]
	if !ok || h.State != Released {
		return h, ErrTransition
	}
	h.EncryptedArtifact = ""
	h.released = nil
	h.State = Terminal
	s.byID[id] = h
	return h, nil
}
