package ascpsignerruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/securefile"
	"golang.org/x/sys/unix"
)

type UnixConfig struct {
	SignerSocket   string
	ArtifactSocket string
	RequestTimeout time.Duration
}

// ServeUnix serves both signer trust-boundary protocols on distinct Unix
// sockets. Existing paths are never removed or overwritten; a stale socket is
// an operator-visible startup failure rather than an alias opportunity.
func (s *Service) ServeUnix(ctx context.Context, config UnixConfig) error {
	if err := validateUnixConfig(config); err != nil {
		return err
	}
	signer, err := listenPrivateUnix(config.SignerSocket)
	if err != nil {
		return fmt.Errorf("listen on signer socket: %w", err)
	}
	defer signer.Close()
	artifact, err := listenPrivateUnix(config.ArtifactSocket)
	if err != nil {
		return fmt.Errorf("listen on artifact socket: %w", err)
	}
	defer artifact.Close()
	if err := requireDistinctListeners(signer, artifact); err != nil {
		return err
	}

	servers := []*http.Server{
		newUnixHTTPServer(s.SignerHandler(), config.RequestTimeout),
		newUnixHTTPServer(s.ArtifactHandler(), config.RequestTimeout),
	}
	listeners := []net.Listener{signer, artifact}
	results := make(chan error, len(servers))
	for index := range servers {
		server, listener := servers[index], listeners[index]
		go func() { results <- server.Serve(listener) }()
	}
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var shutdownErr error
		shutdownResults := make(chan error, len(servers))
		for _, server := range servers {
			server := server
			go func() { shutdownResults <- server.Shutdown(shutdownCtx) }()
		}
		timedOut := false
		for range servers {
			select {
			case err := <-shutdownResults:
				shutdownErr = errors.Join(shutdownErr, err)
			case <-shutdownCtx.Done():
				shutdownErr = errors.Join(shutdownErr, shutdownCtx.Err())
				timedOut = true
			}
			if timedOut {
				break
			}
		}
		for _, server := range servers {
			_ = server.Close()
		}
		for range servers {
			select {
			case err := <-results:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					shutdownErr = errors.Join(shutdownErr, err)
				}
			case <-time.After(time.Second):
				shutdownErr = errors.Join(shutdownErr, errors.New("signer Unix server did not stop"))
			}
		}
		return shutdownErr
	case err := <-results:
		for _, server := range servers {
			_ = server.Close()
		}
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return errors.New("signer Unix server stopped unexpectedly")
		}
		return fmt.Errorf("serve signer Unix boundary: %w", err)
	}
}

func validateUnixConfig(config UnixConfig) error {
	if config.RequestTimeout < time.Second || config.RequestTimeout > 10*time.Second ||
		!cleanAbsoluteSocket(config.SignerSocket) || !cleanAbsoluteSocket(config.ArtifactSocket) ||
		config.SignerSocket == config.ArtifactSocket {
		return errors.New("signer Unix runtime configuration is invalid")
	}
	for _, path := range []string{config.SignerSocket, config.ArtifactSocket} {
		if err := validateSocketParent(filepath.Dir(path)); err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("signer socket path must not already exist")
		}
	}
	return nil
}

func cleanAbsoluteSocket(path string) bool {
	return strings.TrimSpace(path) == path && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" &&
		len(path) <= len(unix.RawSockaddrUnix{}.Path)-1
}

func validateSocketParent(path string) error {
	directory, err := securefile.OpenDirectory(path)
	if err != nil {
		return errors.New("signer socket parent path must contain no symlinks")
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("signer socket parent must be an owner-only non-symlink directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		return errors.New("signer socket parent must be owned by the runtime user or root")
	}
	return nil
}

func listenPrivateUnix(path string) (*net.UnixListener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func requireDistinctListeners(left, right *net.UnixListener) error {
	leftInfo, leftErr := os.Lstat(left.Addr().String())
	rightInfo, rightErr := os.Lstat(right.Addr().String())
	if leftErr != nil || rightErr != nil {
		return errors.New("inspect signer Unix sockets")
	}
	leftStat, leftOK := leftInfo.Sys().(*syscall.Stat_t)
	rightStat, rightOK := rightInfo.Sys().(*syscall.Stat_t)
	if !leftOK || !rightOK || leftStat.Dev == rightStat.Dev && leftStat.Ino == rightStat.Ino {
		return errors.New("signer and artifact boundaries must use distinct Unix sockets")
	}
	return nil
}

func newUnixHTTPServer(handler http.Handler, timeout time.Duration) *http.Server {
	return &http.Server{
		Handler: handler, ReadHeaderTimeout: timeout, ReadTimeout: timeout,
		WriteTimeout: timeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
}
