package ascpkeeper

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

type signerClientFixture struct {
	handle   ascpbearer.Handle
	artifact []byte
	err      error
}

func (f *signerClientFixture) Release(context.Context, string, string) (ascpbearer.Handle, []byte, error) {
	return f.handle, append([]byte(nil), f.artifact...), f.err
}

func TestSignerArtifactSourceRechecksReturnedHandleBinding(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	handle := ascpbearer.Handle{ID: "signer_handle_1234567890", KeeperID: "keeper-primary", State: ascpbearer.Released, ValidAfter: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	client := &signerClientFixture{handle: handle, artifact: bytes.Repeat([]byte{0x44}, 65)}
	source, err := NewSignerArtifactSource(client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := source.Release(context.Background(), handle.ID, handle.KeeperID)
	if err != nil || !bytes.Equal(artifact, client.artifact) {
		t.Fatalf("artifact=%x err=%v", artifact, err)
	}
	client.handle.KeeperID = "attacker-keeper"
	if _, err := source.Release(context.Background(), handle.ID, handle.KeeperID); !errors.Is(err, ErrSignatureUnavailable) {
		t.Fatalf("error=%v", err)
	}
	client.handle = handle
	client.handle.State = ascpbearer.Active
	if _, err := source.Release(context.Background(), handle.ID, handle.KeeperID); !errors.Is(err, ErrSignatureUnavailable) {
		t.Fatalf("unreleased error=%v", err)
	}
}
