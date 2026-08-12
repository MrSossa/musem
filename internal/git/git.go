// Package git answers one question — which branch is checked out in a
// directory — by shelling out to the git binary.
//
// Shelling out rather than linking a git library is deliberate: git is present
// by definition on machines where musem makes sense, and a library is a large
// dependency to carry for a single question.
package git

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/MrSossa/musem"
)

// BranchResolver reports the branch checked out in a directory.
type BranchResolver struct {
	// Bin is the executable to run. Empty means "git" on PATH.
	Bin string
	// Timeout bounds one lookup. A directory on a stalled network mount must
	// not be able to hold up the refresh loop.
	Timeout time.Duration
}

// NewBranchResolver returns a resolver with sensible defaults.
func NewBranchResolver() *BranchResolver {
	return &BranchResolver{Bin: "git", Timeout: 5 * time.Second}
}

// Branch returns the branch name checked out at dir.
//
// A directory outside a repository yields an empty name and no error: that is a
// normal state for a session, not a failure. A detached HEAD likewise yields an
// empty name, since there is no branch to report.
func (r *BranchResolver) Branch(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		return "", nil
	}

	bin := r.Bin
	if bin == "" {
		bin = "git"
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	// Killing the process is not the same as getting the call back. Run waits
	// for the goroutine copying into these buffers, and that goroutine finishes
	// only when every process holding the pipe's write end has exited — the
	// context kills the direct child, not a grandchild it left behind. Without a
	// delay a credential helper, a hook, or anything wedged on a stalled mount
	// holds this call open past the timeout and freezes the refresh loop the
	// timeout exists to protect.
	cmd.WaitDelay = time.Second

	if err := cmd.Run(); err != nil && !answeredBeforeItsChildLetGo(cmd, err, stdout.String()) {
		// Distinguish "git is not installed" from "this is not a repository".
		// The first is worth telling the user about; the second is routine.
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return "", musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		}

		// A git that was killed rather than answered says nothing about dir.
		// Reporting that as "no branch" would be a claim this call never
		// established, and the caller would cache it in place of a name it
		// already knew.
		if ctx.Err() != nil {
			return "", musem.Wrap(err, musem.EUNAVAILABLE, "git timed out resolving the branch at %s", dir)
		}

		// git ran and exited non-zero: dir is not a repository, which is a
		// normal state for a session rather than a failure.
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return "", nil
		}

		return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot run git in %s", dir)
	}

	branch := strings.TrimSpace(stdout.String())
	if branch == "HEAD" {
		// Detached HEAD: there is no branch, and saying "HEAD" would read as one.
		return "", nil
	}
	return branch, nil
}

// answeredBeforeItsChildLetGo reports that git did the job and only the plumbing
// outlived it.
//
// WaitDelay closes the pipes when the process that holds their write end is not
// the one that was waited on — an fsmonitor daemon, a background maintenance
// run, anything git forks and detaches — and Run then reports ErrWaitDelay for a
// git that exited zero. Taking that as a failure discards the name already
// sitting in stdout and blanks the BRANCH column of every repository whose git
// leaves a process behind, once per refresh and for as long as it does.
//
// The trailing newline is what separates a complete answer from a pipe cut
// mid-word. git terminates the name with one, so output without it is a
// fragment — and a fragment shown as a branch is precisely the confident wrong
// label this resolver exists to refuse.
func answeredBeforeItsChildLetGo(cmd *exec.Cmd, err error, out string) bool {
	return errors.Is(err, exec.ErrWaitDelay) &&
		cmd.ProcessState != nil && cmd.ProcessState.Success() &&
		strings.HasSuffix(out, "\n")
}
