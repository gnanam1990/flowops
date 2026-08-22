package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/flowops/internal/ascpcapacity"
)

func TestDecodeFileRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, encoded := range []string{
		`{"version":1,"unknown":true}`,
		`{"version":1} {"version":1}`,
	} {
		path := filepath.Join(t.TempDir(), "evidence.json")
		if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
			t.Fatal(err)
		}
		var evidence ascpcapacity.Evidence
		if err := decodeFile(path, &evidence); err == nil {
			t.Fatalf("malformed evidence accepted: %s", encoded)
		}
	}
}
