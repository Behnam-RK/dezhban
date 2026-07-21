package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Asset names published by release.yml for macOS.
const (
	pkgAssetPattern = "dezhban-%s.pkg" // %s = bare version, no leading v
	sumsAsset       = "SHA256SUMS"
	sigAsset        = "SHA256SUMS.sig"
)

// downloadBaseURL is a var, not a const, purely so tests can point it at an
// httptest server instead of real GitHub release asset URLs.
var downloadBaseURL = "https://github.com/" + Repo + "/releases/download"

// Download fetches the .pkg for the given version (bare, no "v") plus
// SHA256SUMS and SHA256SUMS.sig into dir, verifies the ed25519 signature over
// SHA256SUMS, then verifies the .pkg's own checksum against it. Returns the
// path to the verified .pkg.
//
// This is the control that makes an updater safe to have at all: it replaces
// a root-owned LaunchDaemon binary, so "download whatever the CDN served" is
// not acceptable — see internal/update/sig.go. A signature or checksum
// mismatch returns an error and applies nothing; the caller owns cleaning up
// dir, this function never deletes anything on its own.
func Download(dir, version string, httpClient *http.Client) (pkgPath string, err error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	tag := "v" + version
	base := downloadBaseURL + "/" + tag + "/"
	pkgName := fmt.Sprintf(pkgAssetPattern, version)

	if err := fetch(httpClient, base+pkgName, filepath.Join(dir, pkgName)); err != nil {
		return "", err
	}
	if err := fetch(httpClient, base+sumsAsset, filepath.Join(dir, sumsAsset)); err != nil {
		return "", err
	}
	if err := fetch(httpClient, base+sigAsset, filepath.Join(dir, sigAsset)); err != nil {
		return "", err
	}

	sums, err := os.ReadFile(filepath.Join(dir, sumsAsset))
	if err != nil {
		return "", err
	}
	sig, err := os.ReadFile(filepath.Join(dir, sigAsset))
	if err != nil {
		return "", err
	}
	if !VerifySignature(sums, sig) {
		return "", fmt.Errorf("SHA256SUMS signature verification failed for %s — refusing to apply an unverified update", tag)
	}

	expected, err := checksumFor(sums, pkgName)
	if err != nil {
		return "", err
	}
	pkgPath = filepath.Join(dir, pkgName)
	actual, err := sha256File(pkgPath)
	if err != nil {
		return "", err
	}
	if actual != expected {
		return "", fmt.Errorf("checksum mismatch for %s (expected %s, got %s) — refusing to apply", pkgName, expected, actual)
	}

	return pkgPath, nil
}

func fetch(client *http.Client, url, dst string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	return nil
}

// checksumFor finds name's expected hex digest in a SHA256SUMS-format byte
// slice (lines of "<hex>  <name>").
func checksumFor(sums []byte, name string) (string, error) {
	for line := range strings.SplitSeq(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s in SHA256SUMS", name)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
