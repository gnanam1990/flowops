//go:build unix

package ascpring6

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalRejectsTamperTruncationAndConcurrentWriter(t *testing.T) {
	directory := ringTempDir(t)
	path := filepath.Join(directory, "ring6.jsonl")
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	if _, _, err := journal.Bind(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkHSMRequested(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(context.Background(), path); err == nil {
		t.Fatal("concurrent journal writer acquired the process lock")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("truncated-final-record", func(t *testing.T) {
		if err := os.WriteFile(path, original[:len(original)-1], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenJournal(context.Background(), path); err == nil {
			t.Fatal("journal without final record terminator was accepted")
		}
	})
	t.Run("hash-tamper", func(t *testing.T) {
		mutated := append([]byte(nil), original...)
		for index, value := range mutated {
			if value == 'a' {
				mutated[index] = 'b'
				break
			}
		}
		if err := os.WriteFile(path, mutated, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenJournal(context.Background(), path); err == nil {
			t.Fatal("journal hash tamper was accepted")
		}
	})
}

func TestJournalRejectsPublicFileSymlinkAndCanceledReplay(t *testing.T) {
	directory := ringTempDir(t)
	public := filepath.Join(directory, "public.jsonl")
	if err := os.WriteFile(public, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(context.Background(), public); err == nil {
		t.Fatal("public journal file was accepted")
	}
	target := filepath.Join(directory, "target.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "linked.jsonl")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(context.Background(), linked); err == nil {
		t.Fatal("symlink journal path was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenJournal(ctx, filepath.Join(directory, "canceled.jsonl")); err == nil {
		t.Fatal("canceled journal startup was accepted")
	}
}

func testBinding() ActionBinding {
	return ActionBinding{
		OperationID: ringHash(1), ActionID: "action-1", InputHash: ringHash(2), Digest: ringHash(3),
		KeyID: "key-1", KeyEpoch: 1, IdempotencyKey: ringHash(4), State: "BOUND",
	}
}
