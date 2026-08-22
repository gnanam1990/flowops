//go:build unix

// Package securefile opens security-sensitive files without following any
// symlink in their absolute path.
package securefile

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// OpenDirectory walks an absolute directory one descriptor at a time. Every
// component is opened with O_NOFOLLOW so an ancestor cannot redirect the
// caller outside the intended secured volume.
func OpenDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("directory path must be a clean absolute non-root path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(fd)
			return nil, errors.New("directory path contains an invalid component")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

// OwnerAllowed reports whether the file belongs to this runtime or root.
func OwnerAllowed(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == uint32(os.Geteuid()) || stat.Uid == 0)
}

// ReadCanonicalBase64Secret reads exactly 32 nonzero bytes from a private,
// owner-controlled file. The parent and file are both held by no-follow
// descriptors during validation and reading.
func ReadCanonicalBase64Secret(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return nil, errors.New("secret path must be a clean absolute file path")
	}
	parent, err := OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open secret parent: %w", err)
	}
	defer parent.Close()
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o022 != 0 || !OwnerAllowed(parentInfo) {
		return nil, errors.New("secret parent must be a secured owner-controlled directory")
	}
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("secret file is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1024 || !OwnerAllowed(info) {
		return nil, errors.New("secret must be a private owner-controlled regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1025))
	defer clear(raw)
	if err != nil || len(raw) > 1024 {
		return nil, errors.New("read secret file")
	}
	encoded := bytes.TrimSpace(raw)
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(decoded, encoded)
	decoded = decoded[:n]
	canonical := make([]byte, base64.StdEncoding.EncodedLen(len(decoded)))
	base64.StdEncoding.Encode(canonical, decoded)
	defer clear(canonical)
	nonzero := byte(0)
	for _, value := range decoded {
		nonzero |= value
	}
	if err != nil || len(decoded) != 32 || !bytes.Equal(encoded, canonical) || nonzero == 0 {
		clear(decoded)
		return nil, errors.New("secret must be canonical base64 for 32 nonzero bytes")
	}
	return decoded, nil
}
