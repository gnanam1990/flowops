package releaseadmission

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// ArtifactSHA256 hashes the exact executable artifact that will be admitted.
// The release approver records this digest in the signed manifest after the
// reviewed build has completed and before the artifact is deployed.
func ArtifactSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open control-plane artifact: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash control-plane artifact: %w", err)
	}
	return fmt.Sprintf("0x%x", digest.Sum(nil)), nil
}

// CurrentExecutableSHA256 hashes the inode that the Linux process was started
// from. The fallback keeps local development and non-Linux tests usable while
// Base mainnet still compares the result with signed release evidence.
func CurrentExecutableSHA256() (string, error) {
	if _, err := os.Stat("/proc/self/exe"); err == nil {
		return ArtifactSHA256("/proc/self/exe")
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate control-plane executable: %w", err)
	}
	return ArtifactSHA256(path)
}
