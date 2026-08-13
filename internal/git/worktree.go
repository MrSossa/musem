package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/execx"
	"github.com/MrSossa/musem/internal/safetext"
)

// Worktrees runs the git worktree commands, and the queries that decide whether
// running them is safe.
//
// It lives beside BranchResolver rather than in an adapter of its own because it
// wraps the same foreign thing: the git binary, and musem's knowledge of how to
// read what it prints. Two packages shelling out to one tool would be two places
// that could disagree about its output.
type Worktrees struct {
	// Bin is the executable to run. Empty means "git" on PATH.
	Bin string

	// Timeout bounds a query. These are the calls the launch form makes while
	// the user types, so they are held to the same budget as the refresh loop:
	// a directory on a stalled mount must not be able to freeze the form.
	Timeout time.Duration

	// WriteTimeout bounds creating and removing a worktree. Checking out a large
	// repository is genuinely slow, and killing it halfway is the failure this
	// package works hardest to avoid, so it gets a budget of its own.
	WriteTimeout time.Duration
}

// Defaults for the worktree commands.
const (
	// DefaultQueryTimeout bounds the questions the form asks while it is open.
	DefaultQueryTimeout = 10 * time.Second

	// DefaultWriteTimeout bounds creating or removing a worktree.
	DefaultWriteTimeout = 5 * time.Minute
)

// NewWorktrees returns a Worktrees with sensible defaults.
func NewWorktrees() *Worktrees {
	return &Worktrees{Bin: "git", Timeout: DefaultQueryTimeout, WriteTimeout: DefaultWriteTimeout}
}

func (w *Worktrees) bin() string {
	if w.Bin == "" {
		return "git"
	}
	return w.Bin
}

func (w *Worktrees) queryTimeout() time.Duration {
	if w.Timeout <= 0 {
		return DefaultQueryTimeout
	}
	return w.Timeout
}

func (w *Worktrees) writeTimeout() time.Duration {
	if w.WriteTimeout <= 0 {
		return DefaultWriteTimeout
	}
	return w.WriteTimeout
}

// run executes git with the given arguments and classifies what came back. The
// returned Kind is meaningful only when the error is non-nil.
//
// answered decides whether output captured before a stranded grandchild closed
// the pipe counts as an answer; see execx.Cmd.Answered. Callers that cannot tell
// a truncated answer from a complete one pass nil, and get an error instead of a
// plausible lie.
func (w *Worktrees) run(ctx context.Context, timeout time.Duration, answered func(string) bool, args ...string) (execx.Result, execx.Kind, error) {
	res, err := execx.Run(ctx, execx.Cmd{
		Bin:      w.bin(),
		Args:     args,
		Timeout:  timeout,
		Answered: answered,
	})
	return res, execx.KindOf(err), err
}

// terminated reports that git wrote a complete line. git terminates the values
// these commands return with a newline, so output without one is a fragment.
func terminated(out string) bool { return strings.HasSuffix(out, "\n") }

// Repository returns the root of the repository containing dir.
//
// A directory outside a repository is reported as ENOTFOUND rather than as an
// empty answer: here, unlike branch resolution, it is the reason a launch cannot
// proceed and the form has to say so.
func (w *Worktrees) Repository(ctx context.Context, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", musem.Errorf(musem.EINVALID, "a working directory is required")
	}

	res, kind, err := w.run(ctx, w.queryTimeout(), terminated,
		"-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		switch kind {
		case execx.NotFound:
			return "", musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		case execx.Exited:
			return "", musem.Wrap(err, musem.ENOTFOUND, "%s is not inside a git repository", dir)
		case execx.Timeout, execx.Failed:
		}
		return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot run git in %s", dir)
	}

	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return "", musem.Errorf(musem.ENOTFOUND, "%s is not inside a git repository", dir)
	}
	return root, nil
}

// Branches lists the branches that already exist in the repository, for the form
// to offer instead of the name it proposes.
//
// A name carrying unprintable characters is dropped rather than cleaned, which
// is the opposite of what a displayed branch gets. This list is not display
// text: whatever the user picks goes straight back to git as the branch to check
// out, so a cleaned name would name a branch that does not exist — and a
// launch would fail for a reason nobody could see on screen.
func (w *Worktrees) Branches(ctx context.Context, dir string) ([]string, error) {
	res, kind, err := w.run(ctx, w.queryTimeout(), terminated,
		"-C", dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		// An empty repository has no refs and for-each-ref still exits zero, so a
		// non-zero exit here means dir is not a repository rather than that it has
		// no branches.
		switch kind {
		case execx.NotFound:
			return nil, musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		case execx.Exited:
			return nil, musem.Wrap(err, musem.ENOTFOUND, "%s is not inside a git repository", dir)
		case execx.Timeout, execx.Failed:
		}
		return nil, musem.Wrap(err, musem.EUNAVAILABLE, "cannot list the branches in %s", dir)
	}

	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || safetext.HasUnprintable(name) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// CheckedOut returns the worktree that already has branch checked out, or an
// empty path when none does.
//
// git permits one checkout of a branch at a time, so this is what turns a launch
// that would fail into a refusal the form can explain before anything happens.
func (w *Worktrees) CheckedOut(ctx context.Context, dir, branch string) (string, error) {
	res, kind, err := w.run(ctx, w.queryTimeout(), nil,
		"-C", dir, "worktree", "list", "--porcelain")
	if err != nil {
		switch kind {
		case execx.NotFound:
			return "", musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		case execx.Exited:
			return "", musem.Wrap(err, musem.ENOTFOUND, "%s is not inside a git repository", dir)
		case execx.Timeout, execx.Failed:
		}
		return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot list the worktrees of %s", dir)
	}

	want := "refs/heads/" + branch
	var path string
	for _, line := range strings.Split(res.Stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.TrimSpace(line) == "":
			path = ""
		case strings.HasPrefix(line, "branch "):
			if strings.TrimPrefix(line, "branch ") == want {
				return path, nil
			}
		}
	}
	return "", nil
}

// Occupied reports whether anything already exists at path.
//
// It is the same question Add asks before it does anything, exposed so the form
// can ask it while it is open: a destination that is already taken is a refusal
// the user can see and fix, and finding out at confirmation time means a round
// trip through an operation that then has to be undone.
func (w *Worktrees) Occupied(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, musem.Wrap(err, musem.EUNAVAILABLE, "cannot check whether %s already exists", path)
	}
	return false, nil
}

// ValidBranch reports whether a name can be used as a branch, without using it.
//
// Exposed so the form can refuse a name while it is still being typed. Add
// applies the same rule, because a check the caller can skip is not a rule; this
// is the same check offered early, not a replacement for it.
func (w *Worktrees) ValidBranch(branch string) error { return checkBranchName(branch) }

// Add creates a worktree at path, on branch, checked out from repo.
//
// The destination is refused rather than written into when it already exists.
// git would refuse a non-empty directory itself, but not an empty one — and an
// empty directory the user made on purpose is still theirs, so the check is made
// here where it can name the path back to them.
func (w *Worktrees) Add(ctx context.Context, repo, path, branch string, create bool) error {
	if err := checkBranchName(branch); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		// An absolute path cannot be read by git as an option, whatever it
		// contains, and everything that reaches here derives its destination
		// rather than typing it.
		return musem.Errorf(musem.EINVALID, "a worktree destination must be an absolute path, got %s", path)
	}
	// Asked again here even though the form already asked: the disk can change
	// between validating a launch and performing it, which is the whole reason
	// the sequence has to be able to undo itself.
	if occupied, err := w.Occupied(path); err != nil {
		return err
	} else if occupied {
		return musem.Errorf(musem.EINVALID, "%s already exists; musem will not write into it", path)
	}

	args := []string{"-C", repo, "worktree", "add"}
	if create {
		args = append(args, "-b", branch, path)
	} else {
		args = append(args, path, branch)
	}

	// A git that exited zero having left a process behind — a checkout hook, an
	// fsmonitor daemon — counts as having done the job. Nothing here reads git's
	// output, so there is no fragment that could be mistaken for an answer, and
	// calling a successful creation a failure is worse than the alternative: the
	// worktree exists, and the launch would walk away without recording it.
	res, kind, err := w.run(ctx, w.writeTimeout(), func(string) bool { return true }, args...)
	if err != nil {
		switch kind {
		case execx.NotFound:
			return musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		case execx.Timeout:
			return musem.Wrap(err, musem.EUNAVAILABLE,
				"git timed out creating the worktree at %s", path)
		case execx.Exited, execx.Failed:
		}
		if detail := gitComplaint(res.Stderr); detail != "" {
			return musem.Wrap(err, musem.EINVALID, "git refused to create the worktree: %s", detail)
		}
		return musem.Wrap(err, musem.EINVALID, "git refused to create the worktree at %s", path)
	}
	return nil
}

// Remove deletes a worktree.
//
// No --force, deliberately. Reclamation has already decided the worktree is
// clean before calling this, and git's own refusal to remove a dirty worktree is
// the second lock on the only destructive thing musem does — one that a bug in
// the first check cannot reach past.
func (w *Worktrees) Remove(ctx context.Context, repo, path string) error {
	// As in Add: nothing reads git's output here, so a removal that succeeded and
	// left a process behind is a removal.
	res, kind, err := w.run(ctx, w.writeTimeout(), func(string) bool { return true },
		"-C", repo, "worktree", "remove", "--", path)
	if err != nil {
		switch kind {
		case execx.NotFound:
			return musem.Wrap(err, musem.EUNAVAILABLE, "git was not found on PATH")
		case execx.Timeout:
			return musem.Wrap(err, musem.EUNAVAILABLE, "git timed out removing the worktree at %s", path)
		case execx.Exited, execx.Failed:
		}
		if detail := gitComplaint(res.Stderr); detail != "" {
			return musem.Wrap(err, musem.EINVALID, "git refused to remove %s: %s", path, detail)
		}
		return musem.Wrap(err, musem.EINVALID, "git refused to remove the worktree at %s", path)
	}
	return nil
}

// Cleanliness answers whether removing the worktree at path would destroy work.
//
// The four conditions of the specification are asked one at a time, because a
// single porcelain call answers only two of them: a branch whose commits the
// remote has never seen looks clean to `git status` and is the case where
// deleting loses everything. Any condition that cannot be answered yields an
// undetermined verdict, which keeps the worktree.
//
// There is no error return by design. A caller handed a verdict and an error
// beside it can drop the error and keep the verdict, and the verdict alone reads
// as clean; folding the failure into the answer removes that move.
func (w *Worktrees) Cleanliness(ctx context.Context, path string) musem.Cleanliness {
	for _, check := range []func(context.Context, string) musem.Cleanliness{
		w.uncommitted,
		w.untracked,
		w.unpushed,
		w.stashed,
	} {
		if v := check(ctx, path); !v.Clean() {
			return v
		}
	}
	return musem.CleanWorktree()
}

// emptyMeansClean runs a command whose empty output is the clean answer.
//
// Answered is nil throughout: these commands say "there is work here" by
// printing something and "there is none" by printing nothing, so output cut
// short by a process git forked and left behind is indistinguishable from a
// clean worktree. That is precisely the confusion that would delete somebody's
// changes, so a cut pipe is reported as a failure and becomes an undetermined
// verdict instead.
func (w *Worktrees) emptyMeansClean(ctx context.Context, dirty string, args ...string) musem.Cleanliness {
	res, kind, err := w.run(ctx, w.queryTimeout(), nil, args...)
	if err != nil {
		return undeterminedFrom(kind, res)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		return musem.DirtyWorktree(dirty)
	}
	return musem.CleanWorktree()
}

// uncommitted reports modifications to files git is already tracking.
func (w *Worktrees) uncommitted(ctx context.Context, path string) musem.Cleanliness {
	return w.emptyMeansClean(ctx, "it has uncommitted changes",
		"-C", path, "status", "--porcelain", "--untracked-files=no")
}

// untracked reports files git has never been told about. Asked separately from
// the above rather than folded into one status call, so the reason given back to
// the user names which of the two it found.
func (w *Worktrees) untracked(ctx context.Context, path string) musem.Cleanliness {
	return w.emptyMeansClean(ctx, "it has untracked files",
		"-C", path, "ls-files", "--others", "--exclude-standard")
}

// stashed reports stash entries.
//
// The stash is stored in the repository rather than in the worktree that made
// it, so this cannot tell a stash pushed from here from one pushed elsewhere in
// the same repository. It keeps the worktree either way. Removing a worktree
// whose branch a stash may be sitting on is not a mistake worth risking to
// reclaim some disk, and the direction to err in is the one that costs disk.
func (w *Worktrees) stashed(ctx context.Context, path string) musem.Cleanliness {
	return w.emptyMeansClean(ctx, "the repository has stashed changes",
		"-C", path, "stash", "list")
}

// unpushed reports commits the branch's remote has never seen.
//
// A branch that tracks nothing is kept rather than treated as up to date: there
// is nowhere its commits could have been preserved, so every commit on it exists
// in exactly one place, which is the definition of work that deleting destroys.
func (w *Worktrees) unpushed(ctx context.Context, path string) musem.Cleanliness {
	res, kind, err := w.run(ctx, w.queryTimeout(), terminated,
		"-C", path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		if kind == execx.Exited {
			return musem.DirtyWorktree("its branch tracks no remote, so its commits exist nowhere else")
		}
		return undeterminedFrom(kind, res)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return musem.DirtyWorktree("its branch tracks no remote, so its commits exist nowhere else")
	}

	res, kind, err = w.run(ctx, w.queryTimeout(), terminated,
		"-C", path, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return undeterminedFrom(kind, res)
	}
	count := strings.TrimSpace(res.Stdout)
	if count == "" {
		return musem.UndeterminedWorktree("git did not report how many commits are unpushed")
	}
	if count != "0" {
		return musem.DirtyWorktree("it has commits the remote does not have")
	}
	return musem.CleanWorktree()
}

// undeterminedFrom turns a failed check into a verdict that says what stopped
// it. The failure is carried into the reason rather than dropped, because "kept,
// nobody knows why" is the one outcome the user cannot act on.
func undeterminedFrom(kind execx.Kind, res execx.Result) musem.Cleanliness {
	switch kind {
	case execx.NotFound:
		return musem.UndeterminedWorktree("git was not found on PATH")
	case execx.Timeout:
		return musem.UndeterminedWorktree("git timed out before it could report the state of the worktree")
	case execx.Exited, execx.Failed:
	}
	if detail := gitComplaint(res.Stderr); detail != "" {
		return musem.UndeterminedWorktree("git could not report the state of the worktree: " + detail)
	}
	return musem.UndeterminedWorktree("git could not report the state of the worktree")
}

// checkBranchName refuses names git would read as options or musem could not
// draw.
//
// The leading dash is the one that matters: the name is passed after -b, and a
// value beginning with a dash is an option to every git subcommand there is. The
// rest are refused because a branch musem creates is a branch musem will later
// have to show, and a name it cannot render honestly is one it should not make.
func checkBranchName(branch string) error {
	if strings.TrimSpace(branch) == "" {
		return musem.Errorf(musem.EINVALID, "a branch name is required")
	}
	if strings.HasPrefix(branch, "-") {
		return musem.Errorf(musem.EINVALID, "a branch name cannot start with %q; git would read it as an option", "-")
	}
	if safetext.HasUnprintable(branch) {
		return musem.Errorf(musem.EINVALID, "a branch name cannot contain unprintable characters")
	}
	if strings.ContainsAny(branch, " \t") || strings.Contains(branch, "..") ||
		strings.HasSuffix(branch, ".lock") || strings.ContainsAny(branch, "~^:?*[\\") {
		return musem.Errorf(musem.EINVALID, "git will not accept %q as a branch name", branch)
	}
	return nil
}

// gitComplaint returns the first useful line git wrote to stderr.
//
// git announces "Preparing worktree" on stderr before it gets round to saying
// what went wrong, so that line is skipped: it is progress, not a reason, and
// showing it in place of the failure would answer a question nobody asked.
func gitComplaint(stderr string) string {
	return safetext.FirstLine(stderr, func(line string) bool {
		return strings.HasPrefix(line, "Preparing worktree")
	})
}
