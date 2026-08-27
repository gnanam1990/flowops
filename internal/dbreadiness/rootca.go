package dbreadiness

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/securefile"
	"golang.org/x/sys/unix"
)

const DatabaseRootCAPath = "/etc/ssl/certs/flowops-database-root-ca.pem"

// InstallRootCA decodes one canonical base64 PEM CA certificate and installs
// it without following the destination or any ancestor symlink. The caller is
// expected to run during privileged container startup before dropping to the
// unprivileged application user.
func InstallRootCA(path, encoded string, now time.Time) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return "", errors.New("database root CA path must be a clean absolute file path")
	}
	encoded = strings.TrimSpace(encoded)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || encoded == "" || base64.StdEncoding.EncodeToString(decoded) != encoded || len(decoded) > 64*1024 {
		return "", errors.New("FLOWOPS_DATABASE_ROOT_CA_B64 must be canonical base64 for one bounded PEM certificate")
	}
	block, rest := pem.Decode(decoded)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return "", errors.New("database root CA must contain exactly one PEM CERTIFICATE block")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.BasicConstraintsValid || !certificate.IsCA ||
		certificate.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return "", errors.New("database root CA certificate is invalid, expired, or not authorized to sign certificates")
	}
	canonical := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if !bytes.Equal(decoded, canonical) {
		return "", errors.New("database root CA PEM is not canonical")
	}
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("open database root CA directory: %w", err)
	}
	defer func() { _ = parent.Close() }()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o444)
	if err != nil {
		return "", errors.New("database root CA destination is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 ||
		(info.Uid != uint32(os.Geteuid()) && info.Uid != 0) {
		_ = file.Close()
		return "", errors.New("database root CA destination is not a single owner-controlled regular file")
	}
	if err := unix.Ftruncate(fd, 0); err != nil {
		_ = file.Close()
		return "", errors.New("truncate database root CA")
	}
	if err := file.Chmod(0o444); err != nil {
		_ = file.Close()
		return "", errors.New("secure database root CA permissions")
	}
	if _, err := file.Write(canonical); err != nil {
		_ = file.Close()
		return "", errors.New("write database root CA")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", errors.New("sync database root CA")
	}
	if err := file.Close(); err != nil {
		return "", errors.New("close database root CA")
	}
	digest := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(digest[:]), nil
}
