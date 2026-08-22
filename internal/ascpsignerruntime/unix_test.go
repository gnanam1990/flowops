package ascpsignerruntime

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpkeeper"
	"golang.org/x/sys/unix"
)

func TestServeUnixExposesDistinctExactHealthIdentitiesAndShutsDown(t *testing.T) {
	directory := shortTempDir(t)
	signerPath := filepath.Join(directory, "signer.sock")
	artifactPath := filepath.Join(directory, "artifact.sock")
	service, _ := testService(t, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- service.ServeUnix(ctx, UnixConfig{SignerSocket: signerPath, ArtifactSocket: artifactPath, RequestTimeout: time.Second})
	}()
	waitForSocket(t, signerPath)
	waitForSocket(t, artifactPath)
	artifactBoundary, err := ascpkeeper.NewAuthenticatedUnixBoundary("artifact", artifactPath, time.Second, testArtifactToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifactBoundary.Check(context.Background()); err != nil {
		t.Fatalf("production keeper client could not authenticate artifact health: %v", err)
	}
	for path, want := range map[string]string{signerPath: `"boundary":"signer"`, artifactPath: `"boundary":"artifact"`} {
		client := unixClient(path)
		request, err := http.NewRequest(http.MethodGet, "http://unix/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		if path == artifactPath {
			request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(testArtifactToken))
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Fatalf("path=%s status=%d body=%s err=%v", path, response.StatusCode, body, readErr)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("path=%s info=%v err=%v", path, info, err)
		}
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Unix signer did not shut down")
	}
}

func TestServeUnixRefusesExistingPathAndInsecureParentWithoutRemovingEither(t *testing.T) {
	service, _ := testService(t, time.Now)
	directory := shortTempDir(t)
	existing := filepath.Join(directory, "existing.sock")
	if err := os.WriteFile(existing, []byte("operator-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := service.ServeUnix(context.Background(), UnixConfig{SignerSocket: existing, ArtifactSocket: filepath.Join(directory, "artifact.sock"), RequestTimeout: time.Second})
	if err == nil {
		t.Fatal("existing socket path was accepted")
	}
	if raw, readErr := os.ReadFile(existing); readErr != nil || string(raw) != "operator-owned" {
		t.Fatalf("existing path changed: raw=%q err=%v", raw, readErr)
	}
	insecure := filepath.Join(shortTempDir(t), "insecure")
	if err := os.Mkdir(insecure, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecure, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := service.ServeUnix(context.Background(), UnixConfig{SignerSocket: filepath.Join(insecure, "one.sock"), ArtifactSocket: filepath.Join(insecure, "two.sock"), RequestTimeout: time.Second}); err == nil {
		t.Fatal("insecure socket parent was accepted")
	}
	traversable := filepath.Join(shortTempDir(t), "traversable")
	if err := os.Mkdir(traversable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := service.ServeUnix(context.Background(), UnixConfig{SignerSocket: filepath.Join(traversable, "one.sock"), ArtifactSocket: filepath.Join(traversable, "two.sock"), RequestTimeout: time.Second}); err == nil {
		t.Fatal("group/other-traversable socket parent was accepted")
	}
	target := shortTempDir(t)
	ancestor := shortTempDir(t)
	linked := filepath.Join(ancestor, "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if err := service.ServeUnix(context.Background(), UnixConfig{SignerSocket: filepath.Join(linked, "one.sock"), ArtifactSocket: filepath.Join(linked, "two.sock"), RequestTimeout: time.Second}); err == nil {
		t.Fatal("symlinked socket ancestor was accepted")
	}
	tooLong := "/" + strings.Repeat("a", len(unix.RawSockaddrUnix{}.Path))
	if cleanAbsoluteSocket(tooLong) {
		t.Fatal("overlong Unix socket path was accepted")
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(base, "asr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s was not created", path)
}

func unixClient(path string) *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	return &http.Client{Transport: transport, Timeout: time.Second}
}
