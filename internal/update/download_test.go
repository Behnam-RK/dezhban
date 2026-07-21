package update

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// testKeypair swaps ReleasePublicKey for a throwaway one for the duration of
// the test, returning the matching private key to sign fixtures with. Same
// pattern as sig_test.go: the real private key does not exist in this repo.
func testKeypair(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	orig := ReleasePublicKey
	ReleasePublicKey = pub
	t.Cleanup(func() { ReleasePublicKey = orig })
	return priv
}

func serveAssets(t *testing.T, pkgName string, pkgContent []byte, sums []byte, sig []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.5.0/"+pkgName, func(w http.ResponseWriter, r *http.Request) { w.Write(pkgContent) })
	mux.HandleFunc("/v0.5.0/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	mux.HandleFunc("/v0.5.0/SHA256SUMS.sig", func(w http.ResponseWriter, r *http.Request) { w.Write(sig) })
	return httptest.NewServer(mux)
}

func TestDownloadSuccess(t *testing.T) {
	priv := testKeypair(t)
	pkgName := "dezhban-0.5.0.pkg"
	pkgContent := []byte("fake pkg bytes")
	digest, err := shaHex(pkgContent)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte(digest + "  " + pkgName + "\n")
	sig := ed25519.Sign(priv, sums)

	srv := serveAssets(t, pkgName, pkgContent, sums, sig)
	defer srv.Close()

	orig := downloadBaseURL
	downloadBaseURL = srv.URL
	defer func() { downloadBaseURL = orig }()

	dir := t.TempDir()
	path, err := Download(dir, "0.5.0", srv.Client())
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(pkgContent) {
		t.Error("downloaded pkg content does not match")
	}
}

func TestDownloadBadSignature(t *testing.T) {
	testKeypair(t) // real key installed, but we sign with a DIFFERENT one below
	_, wrongPriv, _ := ed25519.GenerateKey(nil)

	pkgName := "dezhban-0.5.0.pkg"
	pkgContent := []byte("fake pkg bytes")
	digest, _ := shaHex(pkgContent)
	sums := []byte(digest + "  " + pkgName + "\n")
	sig := ed25519.Sign(wrongPriv, sums) // signed with the WRONG key

	srv := serveAssets(t, pkgName, pkgContent, sums, sig)
	defer srv.Close()
	orig := downloadBaseURL
	downloadBaseURL = srv.URL
	defer func() { downloadBaseURL = orig }()

	if _, err := Download(t.TempDir(), "0.5.0", srv.Client()); err == nil {
		t.Fatal("expected a signature verification failure, got nil error")
	} else if !strings.Contains(err.Error(), "signature verification failed") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestDownloadTamperedChecksum(t *testing.T) {
	priv := testKeypair(t)
	pkgName := "dezhban-0.5.0.pkg"
	pkgContent := []byte("fake pkg bytes")
	// Checksum entry deliberately does not match pkgContent's real digest —
	// the signature over THIS (wrong) SHA256SUMS is still valid, exercising
	// the second, independent check: a validly-signed SHA256SUMS whose
	// checksum doesn't match the actual downloaded bytes must still fail.
	sums := []byte("0000000000000000000000000000000000000000000000000000000000000000  " + pkgName + "\n")
	sig := ed25519.Sign(priv, sums)

	srv := serveAssets(t, pkgName, pkgContent, sums, sig)
	defer srv.Close()
	orig := downloadBaseURL
	downloadBaseURL = srv.URL
	defer func() { downloadBaseURL = orig }()

	if _, err := Download(t.TempDir(), "0.5.0", srv.Client()); err == nil {
		t.Fatal("expected a checksum mismatch, got nil error")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("aaaa  dezhban-0.5.0.pkg\nbbbb  other-file\n")
	got, err := checksumFor(sums, "dezhban-0.5.0.pkg")
	if err != nil || got != "aaaa" {
		t.Errorf("checksumFor() = %q, %v", got, err)
	}
	if _, err := checksumFor(sums, "missing"); err == nil {
		t.Error("expected an error for a name with no checksum entry")
	}
}

// shaHex is a tiny test helper mirroring sha256File but over an in-memory
// byte slice, so fixtures don't need to round-trip through a temp file just
// to compute the digest they're about to serve.
func shaHex(b []byte) (string, error) {
	f, err := os.CreateTemp("", "dezhban-test-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		return "", err
	}
	return sha256File(f.Name())
}
