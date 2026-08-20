package ascpbearer

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPreparedActiveReleaseReplayAndErase(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := NewStore()
	h, err := s.Prepare(Handle{ID: "h", OperationID: "o", PayloadHash: "p", Digest: "d", Nonce: "n", EncryptedArtifact: "cipher", ValidUntil: now.Add(time.Minute)})
	if err != nil || h.State != Prepared {
		t.Fatal(h, err)
	}
	if _, _, err := s.Release("h", now); !errors.Is(err, ErrTransition) {
		t.Fatal(err)
	}
	_, err = s.Activate("h", now)
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := s.Release("h", now)
	if err != nil || string(first) != "cipher" {
		t.Fatal(err)
	}
	_, second, err := s.Release("h", now)
	if err != nil || string(second) != "cipher" {
		t.Fatal(err)
	}
	h, err = s.Finalize("h")
	if err != nil || h.EncryptedArtifact != "" || h.State != Terminal {
		t.Fatal(h, err)
	}
}
func TestConcurrentActivateHasOneWinner(t *testing.T) {
	now := time.Now()
	s := NewStore()
	_, _ = s.Prepare(Handle{ID: "h", OperationID: "o", PayloadHash: "p", Digest: "d", Nonce: "n", EncryptedArtifact: "x", ValidUntil: now.Add(time.Minute)})
	var wg sync.WaitGroup
	wins := 0
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Activate("h", now); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatal(wins)
	}
}
