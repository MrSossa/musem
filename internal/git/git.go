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

	if err := cmd.Run(); err != nil {
		// Distinguish "git is not installed" from "this is not a repository".
		// The first is worth telling the user about; the second is routine.
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return "", musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		}
		return "", nil
	}

	branch := strings.TrimSpace(stdout.String())
	if branch == "HEAD" {
		// Detached HEAD: there is no branch, and saying "HEAD" would read as one.
		return "", nil
	}
	return branch, nil
}
