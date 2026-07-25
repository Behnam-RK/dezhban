package token

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tokenPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "control.token")
}

func TestEnrolledTokenVerifies(t *testing.T) {
	p := tokenPath(t)
	tok, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(p, tok); err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(p, tok)
	if err != nil || !ok {
		t.Fatalf("Verify(correct token) = %v, %v; want true, nil", ok, err)
	}
}

func TestWrongTokenIsRefused(t *testing.T) {
	p := tokenPath(t)
	tok, _ := New()
	other, _ := New()
	if err := Save(p, tok); err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(p, other)
	if err != nil {
		t.Fatalf("Verify(wrong token) errored: %v", err)
	}
	if ok {
		t.Error("a different token verified")
	}
}

// The dangerous failure: no enrollment being read as "anything goes". It has to
// be a distinguishable refusal so callers cannot accidentally treat a missing
// file as permission.
func TestMissingEnrollmentRefusesAndSaysSo(t *testing.T) {
	ok, err := Verify(tokenPath(t), "anything")
	if ok {
		t.Error("verification succeeded with no token enrolled")
	}
	if !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("err = %v, want ErrNotEnrolled", err)
	}
}

// A client that just leaves the field out must never match, including against a
// hash file that has somehow been emptied.
func TestEmptyPresentedTokenNeverMatches(t *testing.T) {
	p := tokenPath(t)
	tok, _ := New()
	if err := Save(p, tok); err != nil {
		t.Fatal(err)
	}
	for _, presented := range []string{"", "   ", "\n"} {
		ok, err := Verify(p, presented)
		if ok || err != nil {
			t.Errorf("Verify(%q) = %v, %v; want false, nil", presented, ok, err)
		}
	}
}

func TestEmptyHashFileIsTreatedAsNotEnrolled(t *testing.T) {
	p := tokenPath(t)
	if err := os.WriteFile(p, []byte("   \n"), FileMode); err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(p, "anything")
	if ok {
		t.Error("an empty hash file authorised a request")
	}
	if !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("err = %v, want ErrNotEnrolled", err)
	}
}

// The hash file must not be readable by the users the socket's group gate
// already admits — anything that can read it can forge the proof.
func TestHashFileIsRootOnly(t *testing.T) {
	p := tokenPath(t)
	tok, _ := New()
	if err := Save(p, tok); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != FileMode {
		t.Errorf("hash file mode = %o, want %o", perm, FileMode)
	}
}

// Only the hash is stored. A state directory someone can read must not hand
// over the secret itself.
func TestTokenItselfIsNeverWrittenToDisk(t *testing.T) {
	p := tokenPath(t)
	tok, _ := New()
	if err := Save(p, tok); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tok) {
		t.Error("the token was written to disk in the clear")
	}
}

func TestReEnrollmentReplacesThePreviousToken(t *testing.T) {
	p := tokenPath(t)
	first, _ := New()
	second, _ := New()
	if err := Save(p, first); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, second); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Verify(p, first); ok {
		t.Error("the replaced token still verifies")
	}
	if ok, _ := Verify(p, second); !ok {
		t.Error("the new token does not verify")
	}
}

// A lost keychain item must be recoverable from; leaving a hash nobody can
// satisfy would permanently disable token ops on the host.
func TestRemoveUnenrolls(t *testing.T) {
	p := tokenPath(t)
	tok, _ := New()
	if err := Save(p, tok); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	if Enrolled(p) {
		t.Error("still reported as enrolled after Remove")
	}
	if err := Remove(p); err != nil {
		t.Errorf("removing an absent token errored: %v", err)
	}
}

func TestSaveRefusesAnEmptyToken(t *testing.T) {
	if err := Save(tokenPath(t), "  "); err == nil {
		t.Error("Save accepted an empty token")
	}
}

func TestNewTokensAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatalf("New returned a duplicate token: %s", tok)
		}
		seen[tok] = true
	}
}
