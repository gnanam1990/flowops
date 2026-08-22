//go:build unix

package securefile

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestReadCanonicalBase64SecretRejectsSymlinkedAncestor(t *testing.T) {
	base := canonicalTempDir(t)
	target := filepath.Join(base, "target")
	secure := filepath.Join(target, "secure")
	if err := os.MkdirAll(secure, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(filepath.Join(secure, "token"), []byte(base64.StdEncoding.EncodeToString(secret)), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCanonicalBase64Secret(filepath.Join(linked, "secure", "token")); err == nil {
		t.Fatal("secret through a symlinked ancestor was accepted")
	}
	loaded, err := ReadCanonicalBase64Secret(filepath.Join(secure, "token"))
	defer clear(loaded)
	if err != nil || !bytes.Equal(loaded, secret) {
		t.Fatalf("loaded=%x err=%v", loaded, err)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(base, "flowops-securefile-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
