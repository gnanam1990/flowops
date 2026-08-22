//go:build unix

package ascpring6

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const (
	ring6DependencyResponseStages = 2 // verifier and HSM
	ring6DurableResponseStages    = 3 // BOUND, HSM_REQUESTED, and SIGNED
)

func ring6ResponseWriteTimeout(stageBudget time.Duration) time.Duration {
	return time.Duration(ring6DependencyResponseStages+ring6DurableResponseStages) * stageBudget
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

func (s *Service) ServeUnix(ctx context.Context, config UnixConfig) (returnErr error) {
	if !cleanAbsoluteSocket(config.Socket) || config.RequestTimeout < time.Second || config.RequestTimeout > 10*time.Second {
		return errors.New("Ring 6 Unix runtime configuration is invalid")
	}
	listener, err := listenPrivateUnix(config.Socket)
	if err != nil {
		return fmt.Errorf("listen on Ring 6 socket: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, listener.Close()) }()
	server := &http.Server{
		Handler: s.Handler(), ReadHeaderTimeout: config.RequestTimeout, ReadTimeout: config.RequestTimeout,
		// A fresh signing request performs two sequential dependency calls and
		// three durable append+fsync transitions. Reserve one configured stage
		// budget for each before the HTTP server may terminate the response.
		WriteTimeout: ring6ResponseWriteTimeout(config.RequestTimeout), IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
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

func ValidRuntimeSocketPath(path string) bool {
	if !ValidSocketPath(path) {
		return false
	}
	return ValidSocketPath(privateBindPath(path))
}

func cleanAbsoluteSocket(path string) bool { return ValidSocketPath(path) }

func privateBindPath(path string) string {
	return privateSiblingPath(path, "runtime-bind")
}

func privateComponentPinPath(path, name string) string {
	return privateSiblingPath(path, "component-pin-"+name)
}

func privateSiblingPath(path, domain string) string {
	digest := sha256.Sum256([]byte(domain + "\n" + path))
	base := filepath.Base(path)
	encoded := hex.EncodeToString(digest[:])
	encoded = strings.Repeat(encoded, (len(base)/len(encoded))+1)
	bindBase := encoded[:len(base)]
	if len(base) > 1 {
		bindBase = "." + encoded[:len(base)-1]
	}
	if bindBase == base {
		prefix := "_"
		if base[0] == '_' {
			prefix = "."
		}
		bindBase = prefix + bindBase[1:]
	}
	return filepath.Join(filepath.Dir(path), bindBase)
}

func listenPrivateUnix(path string) (*privateUnixListener, error) {
	if !ValidRuntimeSocketPath(path) {
		return nil, errors.New("Ring 6 runtime socket path cannot be published atomically")
	}
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
	bindPath := privateBindPath(path)
	if _, err := os.Lstat(bindPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("Ring 6 private bind path must not already exist")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: bindPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	created, err := os.Lstat(bindPath)
	if err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	bindIdentity, ok := identityFromFileInfo(created)
	if created.Mode()&os.ModeSocket == 0 || !ok || !securefile.OwnerAllowed(created) {
		return nil, errors.Join(errors.New("Ring 6 socket identity changed during bind"), listener.Close(), unlinkOwnedSocket(bindPath, bindIdentity))
	}
	if err := os.Chmod(bindPath, 0o600); err != nil {
		return nil, errors.Join(err, listener.Close(), unlinkOwnedSocket(bindPath, bindIdentity))
	}
	created, err = os.Lstat(bindPath)
	if err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	bindIdentity, ok = identityFromFileInfo(created)
	if created.Mode()&os.ModeSocket == 0 || created.Mode().Perm() != 0o600 || !ok || !securefile.OwnerAllowed(created) {
		return nil, errors.Join(errors.New("Ring 6 private bind identity changed"), listener.Close(), unlinkOwnedSocket(bindPath, bindIdentity))
	}
	if err := os.Link(bindPath, path); err != nil {
		return nil, errors.Join(err, listener.Close(), unlinkOwnedSocket(bindPath, bindIdentity))
	}
	bindInfo, err := os.Lstat(bindPath)
	if err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	finalInfo, err := os.Lstat(path)
	if err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	bindIdentity, bindOK := identityFromFileInfo(bindInfo)
	finalIdentity, finalOK := identityFromFileInfo(finalInfo)
	if !bindOK || !finalOK || bindIdentity != finalIdentity || finalInfo.Mode()&os.ModeSocket == 0 || finalInfo.Mode().Perm() != 0o600 {
		return nil, errors.Join(errors.New("Ring 6 atomically published socket identity changed"), listener.Close(),
			unlinkOwnedSocket(bindPath, bindIdentity), unlinkOwnedSocket(path, finalIdentity))
	}
	if err := unlinkOwnedSocket(bindPath, bindIdentity); err != nil {
		return nil, errors.Join(err, listener.Close(), unlinkOwnedSocket(path, finalIdentity))
	}
	finalInfo, err = os.Lstat(path)
	if err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	finalIdentity, finalOK = identityFromFileInfo(finalInfo)
	if !finalOK || finalInfo.Mode()&os.ModeSocket == 0 || finalInfo.Mode().Perm() != 0o600 || !securefile.OwnerAllowed(finalInfo) {
		return nil, errors.Join(errors.New("Ring 6 final socket identity changed after publication"), listener.Close())
	}
	if err := parent.Sync(); err != nil {
		return nil, errors.Join(err, listener.Close(), unlinkOwnedSocket(path, finalIdentity))
	}
	return &privateUnixListener{UnixListener: listener, path: path, identity: finalIdentity}, nil
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

func unlinkPinnedSocket(path string, identity socketIdentity) error {
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
	current, ok := identityFromFileInfo(info)
	if !ok || info.Mode()&os.ModeSocket == 0 || current.device != identity.device || current.inode != identity.inode {
		return errors.New("Ring 6 component pin no longer identifies the pinned socket")
	}
	if err := unix.Unlinkat(int(parent.Fd()), filepath.Base(path), 0); err != nil {
		return err
	}
	return parent.Sync()
}
