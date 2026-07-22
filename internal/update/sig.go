// Package update implements `dezhban upgrade`: checking GitHub for a newer
// release, downloading and verifying it, and — on macOS only — applying the
// signed .pkg and restarting into it. See docs/usage/upgrade.md for the full design;
// the short version:
//
//   - The check runs in the GUI, in user context, never in the root daemon.
//     The daemon's egress stays geo-providers-only; the updater never gets its
//     own firewall pass (see CLAUDE.md's invariants).
//   - Applying the .pkg is a zero-gap operation — the running daemon keeps
//     enforcing from its old inode while the new files land. Only the restart
//     that activates the new binary opens a window, and that window is
//     disclosed and gated, never silently taken.
//   - Signing protects that restart: an update replaces a root-owned
//     LaunchDaemon binary, so "download whatever the CDN served" is not
//     acceptable. See ReleasePublicKey below.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

// releasePublicKeyB64 is the base64 standard-encoding of the ed25519 public key
// whose matching private key signs SHA256SUMS in the release workflow (see
// .github/workflows/release.yml, "Sign checksums"). This is a public key: it is
// meant to be embedded in the binary. The private half lives only as a GitHub
// Actions secret (RELEASE_SIGNING_KEY) and is never in this repository.
//
// Deliberately NOT cosign/sigstore: verifying a keyless Sigstore bundle needs
// sigstore-go, which drags in go-containerregistry, protobuf, and friends —
// tens of extra modules in a binary that runs as a root daemon, against the
// "dependency-light standalone binary" convention in CLAUDE.md. ed25519
// verification is ~20 lines of stdlib crypto/ed25519 and adds nothing.
const releasePublicKeyB64 = "LlX8CHyqgaUM0nR3ePzQ1t8RBVWOjyocg20X1s1YN0o="

// ReleasePublicKey is the parsed form of releasePublicKeyB64, computed once.
var ReleasePublicKey = mustDecodePublicKey(releasePublicKeyB64)

func mustDecodePublicKey(b64 string) ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic("update: releasePublicKeyB64 does not decode: " + err.Error())
	}
	if len(raw) != ed25519.PublicKeySize {
		panic(fmt.Sprintf("update: releasePublicKeyB64 decodes to %d bytes, want %d", len(raw), ed25519.PublicKeySize))
	}
	return ed25519.PublicKey(raw)
}

// VerifySignature reports whether sig is a valid ed25519 signature over data
// under ReleasePublicKey. data is normally the raw bytes of a release's
// SHA256SUMS file; sig is the raw (not base64) signature bytes from
// SHA256SUMS.sig.
func VerifySignature(data, sig []byte) bool {
	return ed25519.Verify(ReleasePublicKey, data, sig)
}
