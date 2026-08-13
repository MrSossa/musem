package launch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/claude"
	"github.com/MrSossa/musem/internal/git"
	"github.com/MrSossa/musem/internal/launch"
	"github.com/MrSossa/musem/internal/sqlite"
	"github.com/MrSossa/musem/internal/tmux"
)

// The end-to-end runs that use the real tmux.
//
// They are opt-in, and the reason is the machine they run on rather than the
// code: these start a tmux server and leave sessions in it for as long as they
// take, which is something to do to a developer only when they asked for it.
// The rest of the end-to-end suite stubs tmux for that reason and covers
// everything below the substrate.
//
// What only these can establish is the part the stub is standing in for: that a
// session musem starts is really detached, really outlives the process that
// started it, and is really addressable afterwards.
//
//	MUSEM_E2E_TMUX=1 go test ./internal/launch/ -run TestRealTmux -v
func requireTmux(t *testing.T) {
	t.Helper()
	if os.Getenv("MUSEM_E2E_TMUX") != "1" {
		t.Skip("set MUSEM_E2E_TMUX=1 to run the end-to-end tests against the real tmux")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
}

// sleepingAgent stands in for the Claude CLI with something that stays alive, so
// the session is still there to be inspected. It answers --version, because that
// is what the launcher asks before it creates anything.
func sleepingAgent(t *testing.T) *claude.Agent {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 1.0.0 ;; *) exec sleep 300 ;; esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &claude.Agent{Bin: path}
}

// realHarness wires every adapter to the real thing except the agent.
func realHarness(t *testing.T) (*launch.Launcher, string, *sqlite.Store) {
	t.Helper()
	requireTmux(t)

	ctx := context.Background()
	repo := repository(t)

	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "musem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessions := tmux.NewSessions()
	l := launch.New(git.NewWorktrees(), sessions, sleepingAgent(t), store)

	return l, repo, store
}

// killSession ends a session this test created, whatever the test did.
//
// Named for the tmux session rather than for everything musem started, so a
// failing test cannot end a session that was not its own.
func killSession(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if name == "" {
			return
		}
		_ = exec.Command("tmux", "kill-session", "-t", "="+name).Run()
	})
}

// tmuxSays runs tmux directly, so the assertion does not go through the same
// adapter it is checking.
func tmuxSays(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// R5, against the real substrate: the session is created detached, in the
// worktree, and is still there once the launch has returned.
func TestRealTmuxLaunchIntoAWorktree(t *testing.T) {
	l, repo, store := realHarness(t)
	ctx := context.Background()

	out, err := l.Launch(ctx, musem.LaunchRequest{Dir: repo, Branch: "musem/session-1"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	killSession(t, out.Substrate)

	// Asked of tmux directly rather than through the adapter that created it.
	if _, ok := tmuxSays(t, "has-session", "-t", "="+out.Substrate); !ok {
		listing, _ := tmuxSays(t, "ls")
		t.Fatalf("tmux does not have the session %q; it has:\n%s", out.Substrate, listing)
	}

	// Detached: it belongs to the tmux server, not to whoever started it.
	//
	// Asked through list-sessions rather than display-message, because only the
	// listing commands accept the "=" exact-match prefix on a target — which is
	// the whole reason to ask by name at all.
	sessions, _ := tmuxSays(t, "list-sessions", "-F", "#{session_name} #{session_attached}")
	if want := out.Substrate + " 0"; !strings.Contains(sessions, want) {
		t.Errorf("no %q among the sessions, so it is attached to its creator and dies with musem:\n%s",
			want, sessions)
	}

	// Started where it was told to start, running the agent rather than an idle
	// shell.
	pane, ok := tmuxSays(t, "list-panes", "-t", "="+out.Substrate, "-F", "#{pane_current_path} #{pane_current_command}")
	if !ok {
		t.Fatalf("tmux would not describe the pane: %s", pane)
	}
	cwd, command, _ := strings.Cut(pane, " ")
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	if cwd != out.Worktree {
		t.Errorf("pane cwd = %q, want the worktree %q", cwd, out.Worktree)
	}
	if command == "" {
		t.Error("the pane is running nothing")
	}

	// And the worktree really is a checkout on the branch that was asked for.
	if _, err := os.Stat(filepath.Join(out.Worktree, "README.md")); err != nil {
		t.Errorf("the worktree has no checkout in it: %v", err)
	}
	if _, ok, _ := store.WorktreeFor(ctx, out.SessionID); !ok {
		t.Error("the worktree was not recorded")
	}
}

// R5, scenario "The session survives musem": the session is hosted by the tmux
// server, so a musem that goes away leaves it running.
//
// Survival is checked from a process that is not this one and did not create it:
// the tmux client here is a fresh subprocess talking to a server that outlived
// the launcher entirely.
func TestRealTmuxSessionOutlivesTheLauncher(t *testing.T) {
	l, repo, _ := realHarness(t)

	out, err := l.Launch(context.Background(), musem.LaunchRequest{Dir: repo, Branch: "musem/session-1"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	killSession(t, out.Substrate)

	// Drop every reference musem had to it.
	l = nil
	_ = l

	time.Sleep(500 * time.Millisecond)

	listing, ok := tmuxSays(t, "ls")
	if !ok {
		t.Fatal("the tmux server went away with the launcher")
	}
	if !strings.Contains(listing, out.Substrate) {
		t.Errorf("tmux ls does not list %q:\n%s", out.Substrate, listing)
	}
}

// R7, against the real substrate: a launch into a directory that is not a
// repository is refused, and no tmux session is left behind by the attempt.
func TestRealTmuxRefusedLaunchStartsNothing(t *testing.T) {
	l, _, _ := realHarness(t)

	before, _ := tmuxSays(t, "ls")

	_, err := l.Launch(context.Background(), musem.LaunchRequest{
		Dir: t.TempDir(), Branch: "musem/session-1",
	})
	if err == nil {
		t.Fatal("a launch outside a repository reported success")
	}

	after, _ := tmuxSays(t, "ls")
	if after != before {
		t.Errorf("the refused launch changed the session list:\nbefore: %s\nafter:  %s", before, after)
	}
}

// R9 and R10 against the real substrate and a real repository: the worktree of a
// session that ended is reclaimed when it is clean and kept when it is not.
func TestRealTmuxReclamation(t *testing.T) {
	l, repo, _ := realHarness(t)
	ctx := context.Background()

	t.Run("clean is reclaimed", func(t *testing.T) {
		out, err := l.Launch(ctx, musem.LaunchRequest{
			Dir: repo, Branch: "feature", ExistingBranch: true,
		})
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		killSession(t, out.Substrate)

		got, acted := l.Reclaim(ctx, endedSession(out.SessionID, out.Worktree))
		if !acted || !got.Removed {
			t.Fatalf("reclamation = %+v, acted = %v; want removed (%s)", got, acted, got.Reason)
		}
		if _, err := os.Stat(out.Worktree); !os.IsNotExist(err) {
			t.Errorf("%s is still on disk", out.Worktree)
		}
	})

	t.Run("work in it is kept", func(t *testing.T) {
		out, err := l.Launch(ctx, musem.LaunchRequest{
			Dir: repo, Branch: "feature", ExistingBranch: true,
		})
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		killSession(t, out.Substrate)

		if err := os.WriteFile(filepath.Join(out.Worktree, "scratch.md"), []byte("mine\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		got, acted := l.Reclaim(ctx, endedSession(out.SessionID, out.Worktree))
		if !acted || got.Removed {
			t.Fatalf("reclamation = %+v; a worktree with work in it was removed", got)
		}
		if !strings.Contains(got.Reason, "untracked") {
			t.Errorf("reason = %q, want it to name the untracked file", got.Reason)
		}
		if _, err := os.Stat(out.Worktree); err != nil {
			t.Errorf("the worktree is gone despite being reported as kept: %v", err)
		}
	})
}
