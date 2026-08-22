package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRequiresPinnedBoundariesAndTLSDatabase(t *testing.T) {
	validEnvironment(t)
	cfg, err := loadConfig([]string{"run"})
	if err != nil || cfg.mode != "run" || cfg.quorum != 2 || cfg.batch != 20 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	for name, value := range map[string]string{
		"database ssl":      testDatabaseURL("sslmode=disable"),
		"database override": testDatabaseURL("sslmode=verify-full&search_path=evil"),
	} {
		t.Run(name, func(t *testing.T) {
			validEnvironment(t)
			t.Setenv("FLOWOPS_GOVERNANCE_RELAY_DATABASE_URL", value)
			if _, err := loadConfig(nil); err == nil {
				t.Fatal("unsafe database URL was accepted")
			}
		})
	}
	validEnvironment(t)
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_CHAIN_SOCKET", "relative.sock")
	if _, err := loadConfig(nil); err == nil {
		t.Fatal("relative boundary socket was accepted")
	}
}

func TestReadSignaturesRequiresPrivateCanonicalBase64(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "safe-signatures.b64")
	bundle := make([]byte, 130)
	for index := range bundle {
		bundle[index] = byte(index + 1)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(bundle)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readSignatures(path)
	if err != nil || len(decoded) != len(bundle) {
		t.Fatalf("decoded=%d err=%v", len(decoded), err)
	}
	clear(decoded)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSignatures(path); err == nil {
		t.Fatal("world-readable owner signatures were accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(bundle)+" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSignatures(path); err == nil {
		t.Fatal("non-canonical base64 line was accepted")
	}
	link := filepath.Join(directory, "signatures-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSignatures(link); err == nil {
		t.Fatal("signature symlink was accepted")
	}
}

func TestAuthorizeModeRequiresExactInputs(t *testing.T) {
	validEnvironment(t)
	if _, err := loadConfig([]string{"authorize"}); err == nil {
		t.Fatal("incomplete authorize mode was accepted")
	}
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_ORG", "org-a")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_WORKFLOW", "0x"+strings.Repeat("1", 64))
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_KEY", "owner-sign-1")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_SIGNATURE_FILE", "/private/tmp/safe-signatures.b64")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_WORKER_ID", "")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_BROADCAST_SOCKET", "")
	if cfg, err := loadConfig([]string{"authorize"}); err != nil || cfg.mode != "authorize" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestInspectModeRequiresOnlyTheSigningTarget(t *testing.T) {
	validEnvironment(t)
	if _, err := loadConfig([]string{"inspect"}); err == nil {
		t.Fatal("inspect mode without a target was accepted")
	}
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_ORG", "org-a")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_WORKFLOW", "0x"+strings.Repeat("1", 64))
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_WORKER_ID", "")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_VAULT_SOCKET", "")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_BROADCAST_SOCKET", "")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_VAULT_TOKEN_FILE", "")
	cfg, err := loadConfig([]string{"inspect"})
	if err != nil || cfg.mode != "inspect" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func validEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_DATABASE_URL", testDatabaseURL("sslmode=verify-full"))
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_WORKER_ID", "relay-worker")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_DIRECTORY_SOCKET", "/private/tmp/flowops-governance-directory.sock")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_CHAIN_SOCKET", "/private/tmp/flowops-governance-chain.sock")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_VAULT_SOCKET", "/private/tmp/flowops-governance-vault.sock")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_BROADCAST_SOCKET", "/private/tmp/flowops-governance-broadcast.sock")
	t.Setenv("FLOWOPS_GOVERNANCE_RELAY_VAULT_TOKEN_FILE", "/private/tmp/flowops-governance-vault-token")
	for _, name := range []string{"FLOWOPS_GOVERNANCE_RELAY_INTERVAL", "FLOWOPS_GOVERNANCE_RELAY_LEASE_DURATION",
		"FLOWOPS_GOVERNANCE_RELAY_BOUNDARY_TIMEOUT", "FLOWOPS_GOVERNANCE_RELAY_BATCH_SIZE", "FLOWOPS_GOVERNANCE_RELAY_QUORUM",
		"FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_ORG", "FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_WORKFLOW",
		"FLOWOPS_GOVERNANCE_RELAY_AUTHORIZE_KEY", "FLOWOPS_GOVERNANCE_RELAY_SIGNATURE_FILE"} {
		t.Setenv(name, "")
	}
}

func testDatabaseURL(query string) string {
	return "postgres" + "://relay@example.com/flowops?" + query
}
