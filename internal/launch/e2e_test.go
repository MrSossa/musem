package launch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/claude"
	"github.com/MrSossa/musem/internal/git"
	"github.com/MrSossa/musem/internal/launch"
	"github.com/MrSossa/musem/internal/registry"
	"github.com/MrSossa/musem/internal/sqlite"
	"github.com/MrSossa/musem/internal/tmux"
)

// This file wires the real adapters together over a real repository, which is
// what the unit tests deliberately do not do: every one of them replaces git
// with a script that prints a fixed answer, so none of them can catch musem
// asking git a question git does not actually answer that way.
//
// The substrate and the agent tool are stubbed, because a test that started
// tmux servers and Claude sessions on the machine running it would be doing
// something to the developer rather than to a fixture. Everything below the
// substrate — the worktree, the branch, the ownership record and every
// cleanliness condition — is genuine.

// stub writes an executable standing in for a binary that is not being
// exercised here.
func stub(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// substrate returns a tmux stub that keeps its sessions as files, so a test can
// see that a session was started and that it is still there afterwards.
func substrate(t *testing.T) (*tmux.Sessions, string) {
	t.Helper()

	dir := t.TempDir()
	state := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}

	stub(t, dir, "tmux", `
state="`+state+`"
case "$1" in
  -V) echo "tmux 3.4"; exit 0 ;;
  new-session)
    shift
    while [ "$1" != "--" ] && [ $# -gt 0 ]; do
      case "$1" in
        -s) name="$2"; shift 2 ;;
        -c) cwd="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    shift
    printf '%s\n%s\n' "$cwd" "$*" > "$state/$name"
    exit 0 ;;
  has-session) test -f "$state/${3#=}" ;;
  kill-session) rm -f "$state/${3#=}" ;;
  *) exit 1 ;;
esac`)
	stub(t, dir, "claude", `case "$1" in --version) echo 1.0.0 ;; *) exit 0 ;; esac`)

	return &tmux.Sessions{Bin: filepath.Join(dir, "tmux")}, state
}

func agent(t *testing.T, substrateDir string) *claude.Agent {
	t.Helper()
	return &claude.Agent{Bin: filepath.Join(filepath.Dir(substrateDir), "claude")}
}

// repository builds a real repository with a real remote, so the cleanliness
// checks have something true to answer about.
func repository(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stubs rely on a POSIX shell")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "api")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=musem", "GIT_AUTHOR_EMAIL=musem@example.invalid",
			"GIT_COMMITTER_NAME=musem", "GIT_COMMITTER_EMAIL=musem@example.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	run(root, "init", "--bare", "-b", "main", origin)
	run(root, "clone", origin, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "README.md")
	run(work, "commit", "-m", "initial")
	run(work, "push", "-u", "origin", "main")

	// A second branch that exists and is fully pushed, for the reclamation that
	// is supposed to succeed.
	run(work, "branch", "feature")
	run(work, "push", "-u", "origin", "feature")

	return work
}

// inRepo runs a git command inside a checkout and fails the test if it does not
// work.
func inRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=musem", "GIT_AUTHOR_EMAIL=musem@example.invalid",
		"GIT_COMMITTER_NAME=musem", "GIT_COMMITTER_EMAIL=musem@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

type harness struct {
	*launch.Launcher
	repo  string
	state string
	store *sqlite.Store
	db    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ctx := context.Background()
	repo := repository(t)
	sessions, state := substrate(t)
	db := filepath.Join(t.TempDir(), "musem.db")

	store, err := sqlite.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return &harness{
		Launcher: launch.New(git.NewWorktrees(), sessions, agent(t, state), store),
		repo:     repo,
		state:    state,
		store:    store,
		db:       db,
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// R5, end to end with a worktree: the worktree is created on the branch that was
// asked for, the session is started in it, and musem has a record saying the
// worktree is its own.
func TestEndToEndLaunchIntoAWorktree(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	out, err := h.Launch(ctx, musem.LaunchRequest{Dir: h.repo, Branch: "musem/session-1"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if !exists(t, out.Worktree) {
		t.Fatalf("the worktree at %s was not created", out.Worktree)
	}
	if !exists(t, filepath.Join(out.Worktree, "README.md")) {
		t.Error("the worktree has no checkout in it")
	}

	// The branch is what was asked for, resolved by the same adapter the
	// dashboard resolves every other session's branch with.
	branch, err := git.NewBranchResolver().Branch(ctx, out.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "musem/session-1" {
		t.Errorf("branch = %q, want the one the launch created", branch)
	}

	// The session was started, in the worktree.
	started, err := os.ReadFile(filepath.Join(h.state, out.Substrate))
	if err != nil {
		t.Fatalf("no session was started: %v", err)
	}
	if !strings.Contains(string(started), out.Worktree) {
		t.Errorf("the session was started in %q, want the worktree", started)
	}
	if !strings.Contains(string(started), out.SessionID) {
		t.Errorf("the agent was not given the session id musem chose: %q", started)
	}

	// R10, scenario "The record survives a restart": the record outlives the
	// process that wrote it, which is what makes reclamation possible at all.
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	w, ok, err := reopened.WorktreeFor(ctx, out.SessionID)
	if err != nil || !ok {
		t.Fatalf("WorktreeFor = %v, %v; the worktree would be unreclaimable", ok, err)
	}
	if w.Path != out.Worktree || w.Repo != h.repo {
		t.Errorf("record = %+v, want the worktree and its repository", w)
	}

	// R5, scenario "The session survives musem": the substrate holds the session,
	// not musem, so closing musem leaves it running.
	if !exists(t, filepath.Join(h.state, out.Substrate)) {
		t.Error("the session did not outlive the musem that started it")
	}
}

// R2, end to end without a worktree: the session starts in the directory as
// given, and nothing is created beside it.
func TestEndToEndLaunchWithoutAWorktree(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	before, err := os.ReadDir(filepath.Dir(h.repo))
	if err != nil {
		t.Fatal(err)
	}

	req := musem.LaunchRequest{Dir: h.repo}
	req.SetWorktree(false)

	out, err := h.Launch(ctx, req)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if out.Worktree != "" {
		t.Errorf("worktree = %q, want none", out.Worktree)
	}
	if out.Dir != h.repo {
		t.Errorf("dir = %q, want the directory as given", out.Dir)
	}

	after, err := os.ReadDir(filepath.Dir(h.repo))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the directory gained entries: %d before, %d after", len(before), len(after))
	}
	if _, ok, _ := h.store.WorktreeFor(ctx, out.SessionID); ok {
		t.Error("musem recorded owning a worktree it did not create")
	}
}

// R9, end to end: a worktree whose branch is fully pushed is taken back when its
// session ends.
func TestEndToEndACleanWorktreeIsReclaimed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	out, err := h.Launch(ctx, musem.LaunchRequest{
		Dir: h.repo, Branch: "feature", ExistingBranch: true,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	got, acted := h.Reclaim(ctx, endedSession(out.SessionID, out.Worktree))
	if !acted || !got.Removed {
		t.Fatalf("reclamation = %+v, acted = %v; want the worktree removed (%s)", got, acted, got.Reason)
	}
	if exists(t, out.Worktree) {
		t.Errorf("%s is still on disk", out.Worktree)
	}
	if _, ok, _ := h.store.WorktreeFor(ctx, out.SessionID); ok {
		t.Error("the record outlived the worktree it granted permission over")
	}
}

// R9, end to end, one case per condition. These are the four questions asked of
// a real git rather than of a script that was told what to say.
func TestEndToEndADirtyWorktreeIsKept(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty func(t *testing.T, worktree string)
		want  string
	}{
		{
			name: "uncommitted changes",
			dirty: func(t *testing.T, w string) {
				if err := os.WriteFile(filepath.Join(w, "README.md"), []byte("edited\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "uncommitted",
		},
		{
			name: "untracked files",
			dirty: func(t *testing.T, w string) {
				if err := os.WriteFile(filepath.Join(w, "scratch.md"), []byte("notes\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "untracked",
		},
		{
			// The case a naive status check calls clean, and the one where
			// deleting the worktree destroys the only copy.
			name: "commits the remote has never seen",
			dirty: func(t *testing.T, w string) {
				if err := os.WriteFile(filepath.Join(w, "new.md"), []byte("work\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				inRepo(t, w, "add", "new.md")
				inRepo(t, w, "commit", "-m", "unpushed work")
			},
			want: "remote",
		},
		{
			name: "stashed changes",
			dirty: func(t *testing.T, w string) {
				if err := os.WriteFile(filepath.Join(w, "README.md"), []byte("edited\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				inRepo(t, w, "stash", "push", "-m", "wip")
			},
			want: "stash",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t)

			out, err := h.Launch(ctx, musem.LaunchRequest{
				Dir: h.repo, Branch: "feature", ExistingBranch: true,
			})
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			tc.dirty(t, out.Worktree)

			got, acted := h.Reclaim(ctx, endedSession(out.SessionID, out.Worktree))
			if !acted {
				t.Fatal("nothing was reported for a worktree that should have been kept")
			}
			if got.Removed {
				t.Fatalf("%s was removed with work in it", out.Worktree)
			}
			if !strings.Contains(got.Reason, tc.want) {
				t.Errorf("reason = %q, want it to name %q", got.Reason, tc.want)
			}
			if !exists(t, out.Worktree) {
				t.Error("the worktree is gone despite being reported as kept")
			}
		})
	}
}

// R9, scenario "No remote at all": a branch musem itself created tracks nothing,
// so every commit on it exists in one place and the worktree stays.
func TestEndToEndABranchWithNoRemoteKeepsItsWorktree(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	out, err := h.Launch(ctx, musem.LaunchRequest{Dir: h.repo, Branch: "musem/session-1"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	got, acted := h.Reclaim(ctx, endedSession(out.SessionID, out.Worktree))
	if !acted || got.Removed {
		t.Fatalf("reclamation = %+v; a branch with nowhere to have been preserved was deleted", got)
	}
	if !strings.Contains(got.Reason, "no remote") {
		t.Errorf("reason = %q, want it to say the branch tracks no remote", got.Reason)
	}
	if !exists(t, out.Worktree) {
		t.Error("the worktree is gone")
	}
}

// R10, R11 end to end: a repository musem never launched into is untouched, even
// when it is spotless.
func TestEndToEndSomebodyElsesRepositoryIsUntouched(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	got, acted := h.Reclaim(ctx, endedSession("never-launched", h.repo))
	if acted {
		t.Fatalf("musem acted on a repository it never launched into: %+v", got)
	}
	if !exists(t, filepath.Join(h.repo, "README.md")) {
		t.Fatal("the repository was disturbed")
	}
}

// R4 end to end: a destination that is already taken is refused, and what is
// there is left alone.
func TestEndToEndAnOccupiedDestinationIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	dest := launch.Destination(h.repo, "musem/session-1")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dest, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := h.Launch(ctx, musem.LaunchRequest{Dir: h.repo, Branch: "musem/session-1"})
	if err == nil {
		t.Fatal("the launch wrote into a destination that was already taken")
	}
	if !strings.Contains(musem.ErrorMessage(err), dest) {
		t.Errorf("message = %q, want the path named", musem.ErrorMessage(err))
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "mine" {
		t.Errorf("what was there was disturbed: %q, %v", data, err)
	}
}

func endedSession(id, dir string) musem.Session {
	at := musem.Session{ID: id, Dir: dir, Status: musem.StatusEnded}
	now := at.LastSeen
	at.EndedAt = &now
	return at
}

// launchedDiscoverer reports exactly what a discovery pass would report for a
// session musem launched: the identifier musem chose and the directory it chose,
// which are the two things the launch controls.
//
// It stands in for the Claude CLI because the CLI is the one part of this path
// that cannot be exercised here. What it must not do is invent either value —
// both are taken from the launch outcome, so a launch that passed the wrong
// identifier or started in the wrong place would fail this test rather than be
// papered over by a fixture that agrees with itself.
type launchedDiscoverer struct{ out musem.LaunchOutcome }

func (d launchedDiscoverer) Discover(context.Context) (musem.Discovery, error) {
	return musem.Discovery{Sessions: []musem.Session{{
		ID:     d.out.SessionID,
		Name:   "api",
		Dir:    d.out.Dir,
		Status: musem.StatusRunning,
	}}}, nil
}

// R5, scenario "The session joins the inventory": the session a launch created
// is discoverable by the same inventory that observes every other session, and
// it arrives with its working directory and its branch.
//
// The branch is the part worth testing end to end. Nothing about the launch
// tells the registry what branch the session is on: the registry resolves it
// from the working directory with the same git adapter it uses for every other
// session, so this asserts that the worktree the launch created is one that
// ordinary discovery can read a branch out of.
func TestEndToEndALaunchedSessionJoinsTheInventory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	out, err := h.Launch(ctx, musem.LaunchRequest{Dir: h.repo, Branch: "musem/session-1"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	reg := registry.New(launchedDiscoverer{out: out}, git.NewBranchResolver())
	reg.Refresh(ctx)

	snap := reg.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("the inventory holds %d sessions, want the one just launched", len(snap.Sessions))
	}

	got := snap.Sessions[0]
	if got.ID != out.SessionID {
		t.Errorf("id = %q, want the identifier musem launched with (%q)", got.ID, out.SessionID)
	}
	if got.Dir != out.Worktree {
		t.Errorf("dir = %q, want the worktree the launch created (%q)", got.Dir, out.Worktree)
	}
	if got.Branch != "musem/session-1" {
		t.Errorf("branch = %q, want the branch the launch created", got.Branch)
	}
	if got.Ended() {
		t.Error("a session that has just been launched is reported as ended")
	}
}

// R5, scenario "The session joins the inventory", without a worktree: the
// session appears under the directory it was given, and the branch is whatever
// was already checked out there.
func TestEndToEndALaunchWithoutAWorktreeJoinsTheInventory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	req := musem.LaunchRequest{Dir: h.repo}
	req.SetWorktree(false)

	out, err := h.Launch(ctx, req)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	reg := registry.New(launchedDiscoverer{out: out}, git.NewBranchResolver())
	reg.Refresh(ctx)

	got := reg.Snapshot().Sessions[0]
	if got.Dir != h.repo {
		t.Errorf("dir = %q, want the directory as given", got.Dir)
	}
	if got.Branch != "main" {
		t.Errorf("branch = %q, want the branch already checked out there", got.Branch)
	}
}
