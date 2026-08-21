package main

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLoadConfigAcceptsIsolatedRecoveryRuntime(t *testing.T) {
	setValidEnvironment(t)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.listenAddress != "0.0.0.0:8082" || len(config.writerKeys) != 1 || len(config.checkpointKeys) != 1 ||
		config.attestationKeyID != "recovery-key-1" || len(config.attestationKey) != ed25519.PrivateKeySize ||
		len(config.attestationPublic) != ed25519.PublicKeySize || config.wormReader == nil || config.remoteHeadReader == nil {
		t.Fatalf("config=%+v", config)
	}
}

func TestLoadConfigRejectsUnsafeBoundaries(t *testing.T) {
	tests := []struct{ name, env, value string }{
		{"database TLS downgrade", "FLOWOPS_RECOVERY_DATABASE_URL", testDatabaseURL("sslmode=require")},
		{"database schema override", "FLOWOPS_RECOVERY_DATABASE_URL", testDatabaseURL("sslmode=verify-full&options=-csearch_path%3Dtenant")},
		{"public listen hostname", "FLOWOPS_RECOVERY_LISTEN_ADDRESS", "example.com:8082"},
		{"private WORM", "FLOWOPS_RECOVERY_WORM_URL", "https://127.0.0.1/worm"},
		{"remote head query secret", "FLOWOPS_RECOVERY_REMOTE_HEAD_URL", "https://head.example/v1/head?token=secret"},
		{"duplicate writer key", "FLOWOPS_RECOVERY_WRITER_KEYS_JSON", `{"writer-key-1":"` + keyB64(32, "w") + `","writer-key-1":"` + keyB64(32, "w") + `"}`},
		{"short checkpoint key", "FLOWOPS_RECOVERY_CHECKPOINT_KEYS_JSON", `{"checkpoint-key-1":"` + keyB64(31, "c") + `"}`},
		{"wrong attestation public key", "FLOWOPS_RECOVERY_ATTESTATION_PUBLIC_KEY_B64", keyB64(ed25519.PublicKeySize, "x")},
		{"long cache", "FLOWOPS_RECOVERY_CACHE_TTL", "6s"},
		{"short verification budget", "FLOWOPS_RECOVERY_VERIFICATION_TIMEOUT", "10s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.env, test.value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("unsafe %s accepted", test.env)
			}
		})
	}
}

func TestPrivateKeyFileRequiresCanonicalBase64AndOwnerOnlyMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "key")
	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", ed25519.SeedSize)))
	encodedKey := base64.StdEncoding.EncodeToString(privateKey)
	if err := os.WriteFile(path, []byte(encodedKey), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKeyFile(path); err == nil {
		t.Fatal("group-readable key accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(encodedKey+"=="), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKeyFile(path); err == nil {
		t.Fatal("non-canonical base64 key accepted")
	}
	malformedKey := append(ed25519.PrivateKey(nil), privateKey...)
	malformedKey[len(malformedKey)-1] ^= 1
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(malformedKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKeyFile(path); err == nil {
		t.Fatal("private key with a mismatched public half accepted")
	}
	if err := os.WriteFile(path, []byte(encodedKey), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "key-link")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKeyFile(symlink); err == nil {
		t.Fatal("symlinked private key accepted")
	}
}

func TestRequirePublicSchemaFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, schema string
		queryErr     error
		wantErr      bool
	}{
		{name: "public", schema: "public"},
		{name: "alternate", schema: "tenant", wantErr: true},
		{name: "query failure", queryErr: errors.New("lost"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			expectation := mock.ExpectQuery(`SELECT current_schema()`)
			if test.queryErr != nil {
				expectation.WillReturnError(test.queryErr)
			} else {
				expectation.WillReturnRows(sqlmock.NewRows([]string{"current_schema"}).AddRow(test.schema))
			}
			if err := requirePublicSchema(t.Context(), db); (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, test.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := requirePublicSchema(t.Context(), (*sql.DB)(nil)); err == nil {
		t.Fatal("nil database accepted")
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	seed := []byte(strings.Repeat("a", ed25519.SeedSize))
	privateKey := ed25519.NewKeyFromSeed(seed)
	keyFile := filepath.Join(t.TempDir(), "attestation-key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOWOPS_RECOVERY_DATABASE_URL", testDatabaseURL("sslmode=verify-full"))
	t.Setenv("FLOWOPS_RECOVERY_LISTEN_ADDRESS", "0.0.0.0:8082")
	t.Setenv("FLOWOPS_RECOVERY_WORM_URL", "https://worm.example/v1/object")
	t.Setenv("FLOWOPS_RECOVERY_REMOTE_HEAD_URL", "https://head.example/v1/head")
	t.Setenv("FLOWOPS_RECOVERY_WRITER_KEYS_JSON", `{"writer-key-1":"`+keyB64(32, "w")+`"}`)
	t.Setenv("FLOWOPS_RECOVERY_CHECKPOINT_KEYS_JSON", `{"checkpoint-key-1":"`+keyB64(ed25519.PublicKeySize, "c")+`"}`)
	t.Setenv("FLOWOPS_RECOVERY_ATTESTATION_KEY_ID", "recovery-key-1")
	t.Setenv("FLOWOPS_RECOVERY_ATTESTATION_KEY_FILE", keyFile)
	t.Setenv("FLOWOPS_RECOVERY_ATTESTATION_PUBLIC_KEY_B64", base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)))
	for _, name := range []string{"FLOWOPS_RECOVERY_EXTERNAL_TIMEOUT", "FLOWOPS_RECOVERY_VERIFICATION_TIMEOUT", "FLOWOPS_RECOVERY_PROOF_TTL", "FLOWOPS_RECOVERY_CACHE_TTL"} {
		t.Setenv(name, "")
	}
}

func keyB64(size int, value string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(value, size)))
}

func testDatabaseURL(query string) string {
	return strings.Join([]string{"postgres", "ql://recovery@db.example/flowops?", query}, "")
}
