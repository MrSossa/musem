// Package git answers one question — which branch is checked out in a
// directory — by shelling out to the git binary.
//
// Shelling out rather than linking a git library is deliberate: git is present
// by definition on machines where musem makes sense, and a library is a large
// dependency to carry for a single question.
package git

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/execx"
	"github.com/MrSossa/musem/internal/safetext"
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

	// The directory comes from a discovered session and is genuinely foreign,
	// but it arrives as one element of an argv — there is no shell to
	// reinterpret it — and it is passed after "-C", where git reads it as a path
	// and nothing else. A directory that does not exist, or is not a repository,
	// is the routine case this function already answers with an empty name.
	res, err := execx.Run(ctx, execx.Cmd{
		Bin:     bin,
		Args:    []string{"-C", dir, "rev-parse", "--abbrev-ref", "HEAD"},
		Timeout: timeout,
		// The trailing newline is what separates a complete answer from a pipe
		// cut mid-word. git terminates the name with one, so output without it
		// is a fragment — and a fragment shown as a branch is precisely the
		// confident wrong label this resolver exists to refuse.
		Answered: func(out string) bool { return strings.HasSuffix(out, "\n") },
	})
	if err != nil {
		var xerr *execx.Error
		if errors.As(err, &xerr) {
			switch xerr.Kind {
			// Distinguish "git is not installed" from "this is not a
			// repository". The first is worth telling the user about; the
			// second is routine.
			case execx.NotFound:
				return "", musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")

			// A git that was killed rather than answered says nothing about
			// dir. Reporting that as "no branch" would be a claim this call
			// never established, and the caller would cache it in place of a
			// name it already knew.
			case execx.Timeout:
				return "", musem.Wrap(err, musem.EUNAVAILABLE, "git timed out resolving the branch at %s", dir)

			// git ran and exited non-zero: dir is not a repository, which is a
			// normal state for a session rather than a failure.
			case execx.Exited:
				return "", nil

			case execx.Failed:
			}
		}
		return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot run git in %s", dir)
	}

	// Cleaned rather than refused, unlike a session identifier: a branch is a
	// label musem displays and never matches on, so the readable part of a
	// strange name is worth more than nothing at all. What counts as needing
	// cleaning, and why git's own ref validation does not settle it, is
	// safetext's to explain.
	raw := strings.TrimSpace(res.Stdout)
	if raw == "HEAD" {
		// Detached HEAD: there is no branch, and saying "HEAD" would read as one.
		//
		// Compared before cleaning, not after. git only ever writes this sentinel
		// itself — it refuses to create a ref actually named HEAD — so the raw
		// value is the honest place to look for it. Cleaning first would let a
		// branch named with characters that get stripped, "H<zero-width>EAD",
		// arrive at the comparison as the sentinel and be reported as no branch
		// at all: the defence would have manufactured the very value it is
		// testing for.
		return "", nil
	}
	return safetext.Clean(raw), nil
}
