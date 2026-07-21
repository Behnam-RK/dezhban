// Command relsign signs a file with the dezhban release ed25519 private key.
// Used only by the release workflow (.github/workflows/release.yml, "Sign
// checksums") to produce SHA256SUMS.sig; the matching public key is committed
// at internal/update/sig.go and is what `dezhban upgrade` verifies against.
//
// Plain stdlib crypto/ed25519 on both ends (this signer, that verifier) rather
// than a PEM/openssl round-trip: no format wrangling, no extra CI tooling, and
// signing and verifying share the exact same primitive.
//
// usage: relsign <file-to-sign> <output-signature-file>
// reads the base64-encoded private key from $RELEASE_SIGNING_KEY.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: relsign <file-to-sign> <output-signature-file>")
		os.Exit(2)
	}

	keyB64 := os.Getenv("RELEASE_SIGNING_KEY")
	if keyB64 == "" {
		fmt.Fprintln(os.Stderr, "relsign: RELEASE_SIGNING_KEY is empty — set the repo secret (see internal/update/sig.go)")
		os.Exit(1)
	}
	priv, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		fmt.Fprintf(os.Stderr, "relsign: RELEASE_SIGNING_KEY does not decode to a %d-byte ed25519 private key\n", ed25519.PrivateKeySize)
		os.Exit(1)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}

	sig := ed25519.Sign(ed25519.PrivateKey(priv), data)
	if err := os.WriteFile(os.Args[2], sig, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}
