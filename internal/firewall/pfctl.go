//go:build darwin

package firewall

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// pfctlTimeout bounds any single pfctl invocation; pf operations are local and
// fast, so a hang means something is wrong.
const pfctlTimeout = 10 * time.Second

// pfctl runs `pfctl args...`, feeding stdin if non-empty, and returns stdout.
// On failure it wraps the error with the full command and captured stderr so
// the caller gets a debuggable message instead of a bare "exit status 1".
func pfctl(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pfctlTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pfctl", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("pfctl %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
