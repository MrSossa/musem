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

// fakeGit writes an executable standing in for the git binary, so a lookup can
// be made to hang or to refuse without needing a real repository.
func fakeGit(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub relies on a POSIX shell")
	}

	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A git that exits non-zero has answered: the directory is not a repository,
// which is a normal state for a session rather than a failure.
func TestNonRepositoryIsNotAnError(t *testing.T) {
	r := &BranchResolver{Bin: fakeGit(t, "exit 128"), Timeout: 5 * time.Second}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err != nil {
		t.Errorf("err = %v, want nil: a directory outside a repository is routine", err)
	}
	if branch != "" {
		t.Errorf("branch = %q, want empty", branch)
	}
}

// A git that was killed rather than answered has established nothing. Reporting
// that as "no branch" is a claim the call never made — and the caller caches it
// over a name it already knew.
func TestTimedOutLookupIsAnError(t *testing.T) {
	// exec, so the shell is replaced rather than left as a parent: a stub that
	// forks leaves the grandchild holding the stdout pipe, and the call would
	// then be bounded by the sleep rather than by the timeout under test.
	r := &BranchResolver{Bin: fakeGit(t, "exec sleep 5"), Timeout: 50 * time.Millisecond}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err == nil {
		t.Fatal("a timed-out lookup reported success; the caller will cache the empty name")
	}
	if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
		t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
	}
	if branch != "" {
		t.Errorf("branch = %q, want empty", branch)
	}
}

// A missing binary is worth telling the user about, unlike the two above.
func TestMissingGitIsReported(t *testing.T) {
	r := &BranchResolver{Bin: filepath.Join(t.TempDir(), "definitely-not-here"), Timeout: time.Second}

	if _, err := r.Branch(context.Background(), "/some/dir"); musem.ErrorCode(err) != musem.EUNAVAILABLE {
		t.Errorf("err = %v, want an EUNAVAILABLE about git not being found", err)
	}
}

// The detached-HEAD case must not read as a branch called HEAD.
func TestDetachedHeadHasNoBranch(t *testing.T) {
	r := &BranchResolver{Bin: fakeGit(t, "echo HEAD"), Timeout: time.Second}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if branch != "" {
		t.Errorf("branch = %q, want empty for a detached HEAD", branch)
	}
}

// Killing the process is not the same as getting the call back: Run waits for
// the goroutine copying stdout, and that finishes only when every process
// holding the pipe has exited. A grandchild the context never signalled — a
// credential helper, a hook — would otherwise hold this call open past the
// timeout and freeze the refresh loop the timeout exists to protect.
func TestATimedOutLookupReturnsEvenWhenAChildHoldsThePipe(t *testing.T) {
	// The stub forks and returns, leaving the grandchild holding stdout open.
	r := &BranchResolver{
		Bin:     fakeGit(t, "sleep 30 & exit 0"),
		Timeout: 50 * time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Branch(context.Background(), "/some/dir")
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Branch never returned: a wedged grandchild holds the refresh loop open indefinitely")
	}
}

// The delay that keeps a stranded grandchild from wedging the refresh loop also
// reports a git that did its job. git answered and exited zero; only a process
// it forked and detached — an fsmonitor daemon, a background maintenance run —
// still held the pipe. Calling that a failure throws away the name already
// captured and blanks the BRANCH column of every such repository, once a
// refresh, for as long as it keeps doing it.
func TestABranchSurvivesAChildThatOutlivesGit(t *testing.T) {
	// Answers, then leaves a grandchild holding stdout open. The timeout is long
	// enough that nothing here is a timeout: what ends the call is WaitDelay.
	r := &BranchResolver{Bin: fakeGit(t, "echo main; sleep 30 & exit 0"), Timeout: 30 * time.Second}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err != nil {
		t.Fatalf("err = %v, want nil: git answered, and only its child outlived it", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want %q", branch, "main")
	}
}

// The same path must not promote a fragment. A pipe closed mid-word leaves
// output git never finished writing, and a half-written name shown as a branch
// is the confident wrong label this resolver exists to refuse — worse than
// admitting it does not know.
func TestATruncatedAnswerIsNotReportedAsABranch(t *testing.T) {
	// No newline: git terminates the name with one, so this is what a cut looks
	// like rather than what an answer looks like.
	r := &BranchResolver{Bin: fakeGit(t, "printf part; sleep 30 & exit 0"), Timeout: 30 * time.Second}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err == nil {
		t.Fatalf("branch = %q with no error; a fragment was promoted to a name", branch)
	}
	if branch != "" {
		t.Errorf("branch = %q, want empty", branch)
	}
}

// A branch name is chosen by whoever created the branch, and git's own ref
// validation does not make it safe to draw — see safetext. What this asserts is
// that the adapter routes the name through that defence at all.
func TestAHostileBranchNameCannotDriveTheTerminal(t *testing.T) {
	r := &BranchResolver{
		Bin:     fakeGit(t, "printf 'feat/\\342\\200\\256niam\\n'"),
		Timeout: 5 * time.Second,
	}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(branch, '\u202e') {
		t.Errorf("branch = %q still carries the direction override", branch)
	}
	// Cleaned, not refused: a branch is a label, and the readable part of an odd
	// name is worth more than an empty column.
	if !strings.Contains(branch, "feat/") {
		t.Errorf("branch = %q lost the name it was meant to keep", branch)
	}
}

// Cleaning must not be able to produce the detached-HEAD sentinel. A branch
// named with characters the cleaner strips would otherwise arrive at the
// comparison as "HEAD" and be reported as no branch at all — the defence
// manufacturing the value it tests for, and blanking a column instead of
// showing a name.
func TestACleanedNameCannotImpersonateDetachedHEAD(t *testing.T) {
	r := &BranchResolver{
		Bin:     fakeGit(t, "printf 'H\\342\\200\\213EAD\\n'"),
		Timeout: 5 * time.Second,
	}

	branch, err := r.Branch(context.Background(), "/some/dir")
	if err != nil {
		t.Fatal(err)
	}
	if branch == "" {
		t.Error("a branch whose name merely cleans to HEAD was reported as a detached head")
	}
	if branch != "HEAD" {
		t.Errorf("branch = %q, want the cleaned name", branch)
	}
}
