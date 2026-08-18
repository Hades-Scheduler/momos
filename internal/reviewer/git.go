package reviewer

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ls1intum/momos/internal/protocol"
)

// git runs a git subcommand in the cloned repo with hardening flags:
//   - safe.directory=* avoids the cross-UID "dubious ownership" error (plan.md §12.4)
//   - diff.external= disables any external diff driver a malicious .gitattributes
//     might configure (arbitrary-exec vector, plan.md §12.4)
func git(ctx context.Context, args ...string) (string, error) {
	full := append([]string{
		"-C", protocol.RepoDir,
		"-c", "safe.directory=*",
		"-c", "diff.external=",
		"-c", "core.hooksPath=/dev/null",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// computeDiff returns the unified diff for the review. It prefers the three-dot
// form (changes introduced by the head relative to the merge-base with base);
// if the merge-base is unreachable (shallow fetch), it falls back to the
// two-dot form (direct base->head comparison).
func computeDiff(ctx context.Context, base, head string) (string, error) {
	if base == "" || head == "" {
		return "", fmt.Errorf("base and head SHAs are required")
	}
	if out, err := git(ctx, "diff", "--no-ext-diff", "--unified=3", base+"..."+head); err == nil {
		return out, nil
	}
	out, err := git(ctx, "diff", "--no-ext-diff", "--unified=3", base+".."+head)
	if err != nil {
		return "", err
	}
	return out, nil
}
