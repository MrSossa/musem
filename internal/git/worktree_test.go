package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

// recordingGit writes an executable standing in for git that appends every
// argument it was handed to a log before running script.
//
// The argv is what these tests are mostly about: whether a worktree is created
// on a new branch or an existing one is the difference between two git
// invocations, and the only place that difference is observable from outside is
// in what git was asked to do.
func recordingGit(t *testing.T, script string) (bin, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub relies on a POSIX shell")
	}

	dir := t.TempDir()
	bin = filepath.Join(dir, "git")
	log = filepath.Join(dir, "argv")

	body := "#!/bin/sh\n" +
		`log="` + log + `"` + "\n" +
		`for a in "$@"; do printf '%s\n' "$a" >> "$log"; done` + "\n" +
		script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// argv returns what the stub was called with, or nil when it was never called.
func argv(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func worktrees(bin string) *Worktrees {
	return &Worktrees{Bin: bin, Timeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
}

// unused returns a path inside a fresh temporary directory that does not exist.
func unused(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// R3, R4: a proposed branch is created, and git is asked to create it.
func TestAWorktreeOnANewBranchAsksGitToCreateIt(t *testing.T) {
	bin, log := recordingGit(t, "exit 0")
	dest := unused(t, "musem-feat")

	if err := worktrees(bin).Add(context.Background(), "/repo", dest, "musem/feat", true); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := []string{"-C", "/repo", "worktree", "add", "-b", "musem/feat", dest}
	got := argv(t, log)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

// R3: picking a branch that already exists checks it out instead, which is a
// different invocation — git refuses -b for a branch it already has.
func TestAWorktreeOnAnExistingBranchChecksItOut(t *testing.T) {
	bin, log := recordingGit(t, "exit 0")
	dest := unused(t, "musem-existing")

	if err := worktrees(bin).Add(context.Background(), "/repo", dest, "release", false); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := []string{"-C", "/repo", "worktree", "add", dest, "release"}
	got := argv(t, log)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "-b" {
			t.Error("git was asked to create a branch that already exists")
		}
	}
}

// R2, scenario "A directory that is not a repository": the refusal has to be
// distinguishable from git being missing, because only one of them is the user's
// to fix by changing the directory.
func TestADirectoryOutsideARepositoryIsReportedAsSuch(t *testing.T) {
	bin, _ := recordingGit(t, "echo 'fatal: not a git repository' >&2; exit 128")

	root, err := worktrees(bin).Repository(context.Background(), "/some/dir")
	if err == nil {
		t.Fatalf("Repository = %q with no error; a launch would proceed outside a repository", root)
	}
	if got := musem.ErrorCode(err); got != musem.ENOTFOUND {
		t.Errorf("code = %q, want %q", got, musem.ENOTFOUND)
	}
}

func TestARepositoryRootIsReturned(t *testing.T) {
	bin, _ := recordingGit(t, "echo /home/u/repo")

	root, err := worktrees(bin).Repository(context.Background(), "/home/u/repo/sub")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if root != "/home/u/repo" {
		t.Errorf("root = %q, want %q", root, "/home/u/repo")
	}
}

// R3, scenario "A branch already checked out elsewhere": git permits one
// checkout of a branch at a time, so the form has to be able to see it coming.
func TestABranchCheckedOutElsewhereIsFound(t *testing.T) {
	const listing = `worktree /home/u/repo
HEAD aaa
branch refs/heads/main

worktree /home/u/repo-feat
HEAD bbb
branch refs/heads/feat/x

worktree /home/u/repo-detached
HEAD ccc
detached
`
	bin, _ := recordingGit(t, "cat <<'EOF'\n"+listing+"EOF")
	w := worktrees(bin)

	path, err := w.CheckedOut(context.Background(), "/home/u/repo", "feat/x")
	if err != nil {
		t.Fatalf("CheckedOut: %v", err)
	}
	if path != "/home/u/repo-feat" {
		t.Errorf("path = %q, want the worktree holding the branch", path)
	}

	path, err = w.CheckedOut(context.Background(), "/home/u/repo", "feat/unused")
	if err != nil {
		t.Fatalf("CheckedOut: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty for a branch nothing has checked out", path)
	}
}

// R3, scenario "Reusing an existing branch": the form can only offer what it can
// list.
func TestExistingBranchesAreListed(t *testing.T) {
	bin, _ := recordingGit(t, "printf 'main\\nfeat/a\\nrelease\\n'")

	got, err := worktrees(bin).Branches(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	want := []string{"main", "feat/a", "release"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("branches = %v, want %v", got, want)
	}
}

// A branch whose name musem could not draw is dropped rather than cleaned. The
// list is not display text: whatever is picked goes straight back to git, and a
// cleaned name would name a branch that does not exist.
func TestABranchNameThatCannotBeDrawnIsNotOffered(t *testing.T) {
	bin, _ := recordingGit(t, "printf 'main\\nfeat/\\342\\200\\256niam\\n'")

	got, err := worktrees(bin).Branches(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(got) != 1 || got[0] != "main" {
		t.Errorf("branches = %v, want only the name that can be shown and used", got)
	}
}

// R4, scenario "Occupied destination": what is there is left untouched, and git
// is never asked — an empty directory somebody made on purpose is still theirs,
// and git would happily use it.
func TestAnOccupiedDestinationIsRefusedWithoutRunningGit(t *testing.T) {
	bin, log := recordingGit(t, "exit 0")

	dest := filepath.Join(t.TempDir(), "occupied")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dest, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := worktrees(bin).Add(context.Background(), "/repo", dest, "feat/x", true)
	if err == nil {
		t.Fatal("Add wrote into a destination that already existed")
	}
	if !strings.Contains(musem.ErrorMessage(err), dest) {
		t.Errorf("message = %q, want the occupied path named", musem.ErrorMessage(err))
	}
	if got := argv(t, log); got != nil {
		t.Errorf("git ran with %v; nothing should have been attempted", got)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "mine" {
		t.Errorf("the existing content was disturbed: %q, %v", data, err)
	}
}

// A branch name git would read as an option never reaches git.
func TestABranchNameThatLooksLikeAnOptionIsRefused(t *testing.T) {
	bin, log := recordingGit(t, "exit 0")

	err := worktrees(bin).Add(context.Background(), "/repo", unused(t, "wt"), "--force", true)
	if musem.ErrorCode(err) != musem.EINVALID {
		t.Errorf("err = %v, want an EINVALID about the branch name", err)
	}
	if got := argv(t, log); got != nil {
		t.Errorf("git ran with %v; the name should never have reached it", got)
	}
}

// R8, R9: removal goes through git, and never with --force. Reclamation has
// already decided the worktree is clean; git's own refusal to delete a dirty one
// is the second lock, and passing --force would pick it.
func TestRemovingAWorktreeNeverForces(t *testing.T) {
	bin, log := recordingGit(t, "exit 0")

	if err := worktrees(bin).Remove(context.Background(), "/repo", "/home/u/repo-feat"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	got := argv(t, log)
	want := []string{"-C", "/repo", "worktree", "remove", "--", "/home/u/repo-feat"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "--force" || a == "-f" {
			t.Error("removal forced; git's refusal to delete a dirty worktree is the second lock on this")
		}
	}
}

// A git that refuses to remove says why, rather than the failure being reported
// as musem's own.
func TestARefusedRemovalCarriesGitsReason(t *testing.T) {
	bin, _ := recordingGit(t, "echo 'fatal: contains modified or untracked files' >&2; exit 1")

	err := worktrees(bin).Remove(context.Background(), "/repo", "/home/u/repo-feat")
	if err == nil {
		t.Fatal("a refused removal reported success")
	}
	if !strings.Contains(musem.ErrorMessage(err), "modified or untracked") {
		t.Errorf("message = %q, want git's own complaint", musem.ErrorMessage(err))
	}
}

// cleanState is one answer per condition, as a shell fragment. The default for
// each is "print nothing, exit zero", which is what a clean worktree looks like.
type cleanState struct {
	status    string
	untracked string
	upstream  string
	ahead     string
	stash     string
}

func cleanlinessGit(t *testing.T, s cleanState) *Worktrees {
	t.Helper()

	or := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}
	script := "case \"$*\" in\n" +
		"  *'status --porcelain'*) " + or(s.status, "exit 0") + " ;;\n" +
		"  *'ls-files --others'*) " + or(s.untracked, "exit 0") + " ;;\n" +
		"  *'symbolic-full-name'*) " + or(s.upstream, "echo origin/main") + " ;;\n" +
		"  *'rev-list --count'*) " + or(s.ahead, "echo 0") + " ;;\n" +
		"  *'stash list'*) " + or(s.stash, "exit 0") + " ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac"

	bin, _ := recordingGit(t, script)
	return worktrees(bin)
}

// R9, scenario "Clean worktree": everything committed and pushed.
func TestACleanWorktreeIsClean(t *testing.T) {
	v := cleanlinessGit(t, cleanState{}).Cleanliness(context.Background(), "/wt")

	if !v.Clean() {
		t.Errorf("verdict = %q, want clean", v.Reason())
	}
}

// R9, scenario "Uncommitted work", first half: tracked files that were changed.
func TestUncommittedChangesKeepTheWorktree(t *testing.T) {
	v := cleanlinessGit(t, cleanState{status: "echo ' M main.go'"}).
		Cleanliness(context.Background(), "/wt")

	if v.Clean() {
		t.Fatal("a worktree with uncommitted changes reads as clean")
	}
	if !v.Determined() {
		t.Error("the state was established; the verdict must not claim otherwise")
	}
	if !strings.Contains(v.Reason(), "uncommitted") {
		t.Errorf("reason = %q, want it to name the uncommitted changes", v.Reason())
	}
}

// R9, scenario "Uncommitted work", second half. Asked separately from the above
// so the reason names which of the two was found.
func TestUntrackedFilesKeepTheWorktree(t *testing.T) {
	v := cleanlinessGit(t, cleanState{untracked: "echo scratch.md"}).
		Cleanliness(context.Background(), "/wt")

	if v.Clean() {
		t.Fatal("a worktree with untracked files reads as clean")
	}
	if !strings.Contains(v.Reason(), "untracked") {
		t.Errorf("reason = %q, want it to name the untracked files", v.Reason())
	}
}

// R9, scenario "Committed but never pushed": the case a naive `git status`
// reports as clean, and the one where deleting destroys everything.
func TestCommitsTheRemoteHasNotSeenKeepTheWorktree(t *testing.T) {
	v := cleanlinessGit(t, cleanState{ahead: "echo 3"}).
		Cleanliness(context.Background(), "/wt")

	if v.Clean() {
		t.Fatal("a branch with unpushed commits reads as clean; deleting it would destroy them")
	}
	if !strings.Contains(v.Reason(), "remote") {
		t.Errorf("reason = %q, want it to name the remote", v.Reason())
	}
}

// R9, scenario "No remote at all": there is nowhere the commits could have been
// preserved, so every one of them exists in exactly one place.
func TestABranchWithNoRemoteKeepsTheWorktree(t *testing.T) {
	v := cleanlinessGit(t, cleanState{upstream: "echo 'fatal: no upstream configured' >&2; exit 128"}).
		Cleanliness(context.Background(), "/wt")

	if v.Clean() {
		t.Fatal("a branch tracking no remote reads as clean")
	}
	if !strings.Contains(v.Reason(), "no remote") {
		t.Errorf("reason = %q, want it to say the branch tracks no remote", v.Reason())
	}
}

// R9: stashed entries keep the worktree.
func TestStashedChangesKeepTheWorktree(t *testing.T) {
	v := cleanlinessGit(t, cleanState{stash: "echo 'stash@{0}: WIP on feat/x'"}).
		Cleanliness(context.Background(), "/wt")

	if v.Clean() {
		t.Fatal("a repository with stashed changes reads as clean")
	}
	if !strings.Contains(v.Reason(), "stash") {
		t.Errorf("reason = %q, want it to name the stash", v.Reason())
	}
}

// R9, scenario "Cleanliness cannot be established": the failure is reported
// rather than treated as clean, and it is distinguishable from a dirty verdict
// because only one of the two is something the user has.
func TestAStateGitCannotReportIsNotTreatedAsClean(t *testing.T) {
	v := cleanlinessGit(t, cleanState{status: "echo 'fatal: bad object' >&2; exit 128"}).
		Cleanliness(context.Background(), "/wt")

	if v.Clean() {
		t.Fatal("a worktree whose state git could not report reads as clean")
	}
	if v.Determined() {
		t.Error("the verdict claims to have established a state git refused to report")
	}
	if !strings.Contains(v.Reason(), "bad object") {
		t.Errorf("reason = %q, want git's own complaint", v.Reason())
	}
}

// A git that was killed rather than answered established nothing, and must not
// be read as a clean worktree.
func TestATimedOutCleanlinessCheckIsUndetermined(t *testing.T) {
	bin, _ := recordingGit(t, "exec sleep 5")
	w := &Worktrees{Bin: bin, Timeout: 50 * time.Millisecond, WriteTimeout: time.Second}

	v := w.Cleanliness(context.Background(), "/wt")
	if v.Clean() || v.Determined() {
		t.Fatalf("verdict = %+v, want undetermined for a git that never answered", v)
	}
	if !strings.Contains(v.Reason(), "timed out") {
		t.Errorf("reason = %q, want the timeout named", v.Reason())
	}
}

// A missing git cannot report anything, and the worktree is kept.
func TestAMissingGitLeavesCleanlinessUndetermined(t *testing.T) {
	w := worktrees(filepath.Join(t.TempDir(), "definitely-not-here"))

	v := w.Cleanliness(context.Background(), "/wt")
	if v.Clean() {
		t.Fatal("a missing git reads as a clean worktree")
	}
	if !strings.Contains(v.Reason(), "PATH") {
		t.Errorf("reason = %q, want it to say git was not found", v.Reason())
	}
}
