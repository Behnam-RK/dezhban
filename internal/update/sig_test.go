package update

import (
	"crypto/ed25519"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	data := []byte("abc123  dezhban-darwin-arm64\n")

	// This test signs with a throwaway key, not ReleasePublicKey's matching
	// private half (which does not exist in this repo — see sig.go). It only
	// exercises VerifySignature's plumbing: real end-to-end coverage is the
	// release workflow's smoke test signing with the actual secret and this
	// binary verifying it.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, data)

	if !ed25519.Verify(pub, data, sig) {
		t.Fatal("sanity: stdlib self-check failed")
	}

	orig := ReleasePublicKey
	ReleasePublicKey = pub
	defer func() { ReleasePublicKey = orig }()

	if !VerifySignature(data, sig) {
		t.Error("VerifySignature rejected a valid signature")
	}
	if VerifySignature(append([]byte{}, data...), append([]byte{}, sig[:len(sig)-1]...)) {
		t.Error("VerifySignature accepted a truncated signature")
	}
	tampered := append([]byte{}, data...)
	tampered[0] ^= 0xff
	if VerifySignature(tampered, sig) {
		t.Error("VerifySignature accepted a signature over tampered data")
	}
}

func TestReleasePublicKeyDecodes(t *testing.T) {
	if len(ReleasePublicKey) != ed25519.PublicKeySize {
		t.Fatalf("ReleasePublicKey is %d bytes, want %d", len(ReleasePublicKey), ed25519.PublicKeySize)
	}
}
