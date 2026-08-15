package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparePinsWalletsWritesPrivateFilesAndRefusesOverwrite(t *testing.T) {
	directory := t.TempDir()
	artifact := filepath.Join(directory, "preparation.json")
	typedData := filepath.Join(directory, "typed-data.json")
	args := []string{
		"--payer", designatedPayer, "--payee", designatedPayee,
		"--app-code", "flowops_evidence", "--service-code", "flowops_client",
		"--artifact", artifact, "--typed-data", typedData,
	}
	if err := prepare(args); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{artifact, typedData} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	if err := prepare(args); err == nil {
		t.Fatal("existing artifacts were overwritten")
	}
	if err := prepare([]string{
		"--payer", designatedPayee, "--payee", designatedPayer,
		"--app-code", "flowops_evidence", "--service-code", "flowops_client",
		"--artifact", filepath.Join(directory, "wrong.json"), "--typed-data", filepath.Join(directory, "wrong-typed.json"),
	}); err == nil {
		t.Fatal("swapped designated wallets accepted")
	}
}

func TestReadSmallTextRejectsOversizedSignatureInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signature.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 1025)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSmallText(path, 1024); err == nil {
		t.Fatal("oversized signature accepted")
	}
}
