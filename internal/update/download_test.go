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

// TestChecksumForBinaryModeAndSpaces pins the fix for two cases the previous
// len(fields)==2 approach silently rejected: sha256sum's BINARY-mode line
// format ("<hash> *<name>", one space and a leading asterisk, vs. text
// mode's two plain spaces), and a name containing a space (which
// strings.Fields would have split into 3+ fields, never matching).
func TestChecksumForBinaryModeAndSpaces(t *testing.T) {
	sums := []byte("cccc *binary-mode-file\ndddd  a name with spaces.pkg\n")
	if got, err := checksumFor(sums, "binary-mode-file"); err != nil || got != "cccc" {
		t.Errorf("checksumFor(binary-mode) = %q, %v, want \"cccc\", nil", got, err)
	}
	if got, err := checksumFor(sums, "a name with spaces.pkg"); err != nil || got != "dddd" {
		t.Errorf("checksumFor(name with spaces) = %q, %v, want \"dddd\", nil", got, err)
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

// TestChecksumForLeadingAsteriskName pins that exactly ONE byte of separator
// is consumed, never a character of the name. A name legitimately starting
// with "*" must survive a text-mode line ("<hash>  *name", two spaces) — a
// looser TrimLeft/TrimPrefix over the remainder would eat the name's own
// asterisk and silently fail to match.
func TestChecksumForLeadingAsteriskName(t *testing.T) {
	sums := []byte("aaaa  *literal-star.pkg\nbbbb **binary-mode-star.pkg\n")
	if got, err := checksumFor(sums, "*literal-star.pkg"); err != nil || got != "aaaa" {
		t.Errorf("checksumFor(text-mode, *-leading name) = %q, %v, want \"aaaa\", nil", got, err)
	}
	if got, err := checksumFor(sums, "*binary-mode-star.pkg"); err != nil || got != "bbbb" {
		t.Errorf("checksumFor(binary-mode, *-leading name) = %q, %v, want \"bbbb\", nil", got, err)
	}
}

// TestChecksumForNoMatchErrors is the Go half of the invariant
// scripts/install.sh's verify() enforces with its own emptiness check: a name
// with no entry must be a hard ERROR, never a quiet pass. (GNU sha256sum -c
// exits 0 on empty input, which is how the shell path could have verified
// nothing and called it success.)
func TestChecksumForNoMatchErrors(t *testing.T) {
	sums := []byte("aaaa  something-else.pkg\n")
	if got, err := checksumFor(sums, "dezhban-v1.2.3.pkg"); err == nil {
		t.Errorf("checksumFor(absent name) = %q, nil — want an error, not a silent pass", got)
	}
}
