package main

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestLoadConfigPinsLoopbackChainEscrowAndSigner(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "verifier.key")
	keyHex := strings.Repeat("11", 32)
	if err := os.WriteFile(keyPath, []byte(keyHex), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.HexToECDSA(keyHex)
	databaseURL := (&url.URL{Scheme: "postgres", User: url.User("verifier"), Host: "db.example", Path: "/flowops", RawQuery: "sslmode=verify-full"}).String()
	t.Setenv("FLOWOPS_VERIFIER_DATABASE_URL", databaseURL)
	t.Setenv("FLOWOPS_VERIFIER_LISTEN_ADDRESS", "127.0.0.1:8083")
	t.Setenv("FLOWOPS_VERIFIER_CHAIN_ID", "84532")
	t.Setenv("FLOWOPS_VERIFIER_ESCROW_CONTRACT", "0x1111111111111111111111111111111111111111")
	t.Setenv("FLOWOPS_VERIFIER_EPOCH", "7")
	t.Setenv("FLOWOPS_VERIFIER_SOFTWARE_HASH", "0x"+strings.Repeat("ab", 32))
	t.Setenv("FLOWOPS_VERIFIER_INTAKE_KEYS_JSON", `{"delivery-key-1":"`+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+`"}`)
	t.Setenv("FLOWOPS_VERIFIER_SIGNER_KEY_FILE", keyPath)
	t.Setenv("FLOWOPS_VERIFIER_SIGNER_ADDRESS", crypto.PubkeyToAddress(key.PublicKey).Hex())
	config, err := loadConfig()
	if err != nil || config.listenAddress != "127.0.0.1:8083" || config.chainID != "84532" ||
		config.escrowContract != "0x1111111111111111111111111111111111111111" || config.verifierEpoch != 7 ||
		config.softwareHash != "0x"+strings.Repeat("ab", 32) || !bytes.Equal(config.intakeKeys["delivery-key-1"], []byte(strings.Repeat("k", 32))) ||
		config.signer.Address() != crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	t.Setenv("FLOWOPS_VERIFIER_SIGNER_ADDRESS", "0x2222222222222222222222222222222222222222")
	if _, err := loadConfig(); err == nil {
		t.Fatal("signer address substitution was accepted")
	}
}

func TestVerifierConfigRejectsRemoteListenDuplicateKeysAndUnsafeKeyFile(t *testing.T) {
	if validateListenAddress("0.0.0.0:8083") == nil || validateListenAddress("127.0.0.1:80") == nil {
		t.Fatal("unsafe verifier listener accepted")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if _, err := parseKeyMap(`{"delivery-key-1":"` + encoded + `","delivery-key-1":"` + encoded + `"}`); err == nil {
		t.Fatal("duplicate intake key accepted")
	}
	keyPath := filepath.Join(t.TempDir(), "public.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("11", 32)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSignerKey(keyPath); err == nil {
		t.Fatal("world-readable signer key accepted")
	}
	privatePath := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(privatePath, []byte(strings.Repeat("11", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "signer-link")
	if err := os.Symlink(privatePath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSignerKey(symlink); err == nil {
		t.Fatal("symlink signer key accepted")
	}
	databaseURL := url.URL{Scheme: "postgres", User: url.User("verifier"), Host: "db.example", Path: "/flowops"}
	if validateDatabaseURL(databaseURL.String()) == nil {
		t.Fatal("database URL without verify-full TLS accepted")
	}
	for _, override := range []string{"search_path", "options"} {
		query := url.Values{"sslmode": {"verify-full"}, override: {"unsafe"}}
		databaseURL.RawQuery = query.Encode()
		if validateDatabaseURL(databaseURL.String()) == nil {
			t.Fatalf("database URL %s override accepted", override)
		}
	}
}
