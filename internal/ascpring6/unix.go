//go:build unix

package ascpring6

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/internal/securefile"
	"golang.org/x/sys/unix"
)

type UnixConfig struct {
	Socket         string
	RequestTimeout time.Duration
}

type privateUnixListener struct {
	*net.UnixListener
	path     string
	identity socketIdentity
	once     sync.Once
	closeErr error
}

func (l *privateUnixListener) Close() error {
	l.once.Do(func() {
		l.closeErr = errors.Join(l.UnixListener.Close(), unlinkOwnedSocket(l.path, l.identity))
	})
	return l.closeErr
}

func (s *Service) ServeUnix(ctx context.Context, config UnixConfig) error {
	if !cleanAbsoluteSocket(config.Socket) || config.RequestTimeout < time.Second || config.RequestTimeout > 10*time.Second {
		return errors.New("Ring 6 Unix runtime configuration is invalid")
	}
	listener, err := listenPrivateUnix(config.Socket)
	if err != nil {
		return fmt.Errorf("listen on Ring 6 socket: %w", err)
	}
	defer func() { _ = listener.Close() }()
	server := &http.Server{
		Handler: s.Handler(), ReadHeaderTimeout: config.RequestTimeout, ReadTimeout: config.RequestTimeout,
		// A signing request performs sequential verifier and HSM calls plus
		// durable journal fsyncs. Each dependency may consume its full budget.
		WriteTimeout: 3 * config.RequestTimeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		_ = server.Close()
		serveErr := <-result
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, serveErr)
		}
		return shutdownErr
	case err := <-result:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return errors.New("Ring 6 Unix server stopped unexpectedly")
		}
		return fmt.Errorf("serve Ring 6 Unix boundary: %w", err)
	}
}

func ValidSocketPath(path string) bool {
	return strings.TrimSpace(path) == path && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" &&
		len(path) <= len(unix.RawSockaddrUnix{}.Path)-1
}

func cleanAbsoluteSocket(path string) bool { return ValidSocketPath(path) }

func listenPrivateUnix(path string) (*privateUnixListener, error) {
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open secure Ring 6 socket parent: %w", err)
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !securefile.OwnerAllowed(info) {
		return nil, errors.New("Ring 6 socket parent must be private and owner controlled")
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("Ring 6 socket path must not already exist")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	created, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	initialIdentity, ok := identityFromFileInfo(created)
	if created.Mode()&os.ModeSocket == 0 || !ok || !securefile.OwnerAllowed(created) {
		_ = listener.Close()
		if ok {
			_ = unlinkOwnedSocket(path, initialIdentity)
		}
		return nil, errors.New("Ring 6 socket identity changed during bind")
	}
	cleanupIdentity := initialIdentity
	cleanup := func() {
		_ = listener.Close()
		_ = unlinkOwnedSocket(path, cleanupIdentity)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	created, err = os.Lstat(path)
	if err != nil {
		cleanup()
		return nil, err
	}
	identity, ok := identityFromFileInfo(created)
	if created.Mode()&os.ModeSocket != 0 && ok && sameSocketObject(identity, initialIdentity) && securefile.OwnerAllowed(created) {
		cleanupIdentity = identity
	}
	if created.Mode()&os.ModeSocket == 0 || created.Mode().Perm() != 0o600 || !ok ||
		!sameSocketObject(identity, initialIdentity) || !securefile.OwnerAllowed(created) {
		cleanup()
		return nil, errors.New("Ring 6 socket identity changed during bind")
	}
	return &privateUnixListener{UnixListener: listener, path: path, identity: identity}, nil
}

func unlinkOwnedSocket(path string, identity socketIdentity) error {
	parent, err := securefile.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	currentIdentity, ok := identityFromFileInfo(info)
	if !ok || info.Mode()&os.ModeSocket == 0 || currentIdentity != identity {
		return errors.New("Ring 6 socket path no longer identifies this listener")
	}
	if err := unix.Unlinkat(int(parent.Fd()), filepath.Base(path), 0); err != nil {
		return err
	}
	return parent.Sync()
}
