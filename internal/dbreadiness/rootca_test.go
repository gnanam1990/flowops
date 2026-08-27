//go:build unix

package dbreadiness

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallRootCAWritesOneImmutableTrustFile(t *testing.T) {
	now := time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC)
	encoded := testRootCA(t, now, true)
	path := filepath.Join(rootCATempDir(t), "database-root-ca.pem")
	digest, err := InstallRootCA(path, encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("unexpected certificate digest %q", digest)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base64.StdEncoding.DecodeString(encoded)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(want) || info.Mode().Perm() != 0o444 {
		t.Fatalf("root CA contents or mode drifted: mode=%o", info.Mode().Perm())
	}
}

func TestInstallRootCAAcceptsCAWithoutOptionalKeyUsageExtension(t *testing.T) {
	now := time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC)
	encoded := testRootCAWithKeyUsage(t, now, true, 0)
	if _, err := InstallRootCA(filepath.Join(rootCATempDir(t), "database-root-ca.pem"), encoded, now); err != nil {
		t.Fatalf("CA without an optional key-usage extension was rejected: %v", err)
	}
}

func TestInstallRootCARejectsInvalidEncodingCertificateAndLifetime(t *testing.T) {
	now := time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC)
	valid := testRootCA(t, now, true)
	validPEM, _ := base64.StdEncoding.DecodeString(valid)
	tests := map[string]string{
		"noncanonical base64": valid[:20] + "\n" + valid[20:],
		"not PEM":             base64.StdEncoding.EncodeToString([]byte("not a certificate")),
		"extra PEM":           base64.StdEncoding.EncodeToString(append(append([]byte(nil), validPEM...), validPEM...)),
		"not a CA":            testRootCA(t, now, false),
		"expired":             testRootCA(t, now.Add(-48*time.Hour), true),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := InstallRootCA(filepath.Join(rootCATempDir(t), "ca.pem"), encoded, now); err == nil {
				t.Fatal("invalid root CA was installed")
			}
		})
	}
}

func TestInstallRootCARejectsSymlinkDestination(t *testing.T) {
	now := time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC)
	directory := rootCATempDir(t)
	target := filepath.Join(directory, "target.pem")
	if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "linked.pem")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRootCA(linked, testRootCA(t, now, true), now); err == nil {
		t.Fatal("symlink destination was followed")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve" {
		t.Fatal("symlink target was modified")
	}
}

func TestInstallRootCARejectsHardLinkedDestinationBeforeTruncation(t *testing.T) {
	now := time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC)
	directory := rootCATempDir(t)
	target := filepath.Join(directory, "target.pem")
	if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "hard-linked.pem")
	if err := os.Link(target, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRootCA(linked, testRootCA(t, now, true), now); err == nil {
		t.Fatal("hard-linked destination was accepted")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve" {
		t.Fatal("hard-link target was truncated")
	}
}

func testRootCA(t *testing.T, validityAnchor time.Time, isCA bool) string {
	usage := x509.KeyUsageDigitalSignature
	if isCA {
		usage |= x509.KeyUsageCertSign
	}
	return testRootCAWithKeyUsage(t, validityAnchor, isCA, usage)
}

func testRootCAWithKeyUsage(t *testing.T, validityAnchor time.Time, isCA bool, usage x509.KeyUsage) string {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "FlowOps test root"},
		NotBefore: validityAnchor.Add(-time.Hour), NotAfter: validityAnchor.Add(24 * time.Hour),
		BasicConstraintsValid: true, IsCA: isCA, KeyUsage: usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	canonical := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return base64.StdEncoding.EncodeToString(canonical)
}

func rootCATempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(base, "flowops-db-root-ca-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
