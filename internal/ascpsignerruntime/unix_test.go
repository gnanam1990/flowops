package ascpsignerruntime

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpkeeper"
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
		body := make([]byte, 256)
		count, _ := response.Body.Read(body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || !contains(string(body[:count]), want) {
			t.Fatalf("path=%s status=%d body=%s", path, response.StatusCode, body[:count])
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
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "asr-")
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

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
