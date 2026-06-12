//go:build linux

package firewall

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// nftTimeout bounds any single nft invocation; nft operations are local netlink
// calls and fast, so a hang means something is wrong.
const nftTimeout = 10 * time.Second

// nft runs `nft args...`, feeding stdin if non-empty, and returns stdout. On
// failure it wraps the error with the full command and captured stderr so the
// caller gets a debuggable message instead of a bare "exit status 1".
//
// This is the Linux analogue of pfctl() (macOS): we drive the kernel firewall
// by shelling to the OS tool with a self-contained ruleset on stdin, rather than
// linking google/nftables. The seam (FirewallBackend) is identical either way;
// shelling keeps the binary dependency-light and cross-compiles with no cgo.
func nft(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nftTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nft", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("nft %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
