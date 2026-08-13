package tmux

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

// recordingTmux writes an executable standing in for tmux that appends every
// argument it was handed to a log before running script.
func recordingTmux(t *testing.T, script string) (bin, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub relies on a POSIX shell")
	}

	dir := t.TempDir()
	bin = filepath.Join(dir, "tmux")
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

func sessions(bin string) *Sessions {
	return &Sessions{Bin: bin, Timeout: 5 * time.Second}
}

// R5: the session is detached, named, started in the given directory, and runs
// the agent. Detached is the load-bearing part — it is what lets the session
// outlive musem.
func TestStartingASessionDetachesItInTheGivenDirectory(t *testing.T) {
	bin, log := recordingTmux(t, "exit 0")

	err := sessions(bin).Start(context.Background(), "musem-abc", "/w/api", []string{"claude", "--session-id", "abc"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := []string{"new-session", "-d", "-s", "musem-abc", "-c", "/w/api", "--", "claude", "--session-id", "abc"}
	got := argv(t, log)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

// R7, scenario "No session substrate": the failure names what is missing rather
// than reporting a generic problem, because a machine without tmux is a machine
// the user can fix in one step once they are told which step.
func TestAMissingTmuxIsReportedAsUnavailable(t *testing.T) {
	s := sessions(filepath.Join(t.TempDir(), "definitely-not-here"))

	err := s.Available(context.Background())
	if err == nil {
		t.Fatal("a missing tmux reported itself as available")
	}
	if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
		t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
	}
	if msg := musem.ErrorMessage(err); !strings.Contains(msg, "tmux") || !strings.Contains(msg, "PATH") {
		t.Errorf("message = %q, want it to name tmux and PATH", msg)
	}

	// The same must hold for a launch that gets as far as starting: it is the
	// path a user reaches without ever calling Available.
	err = s.Start(context.Background(), "musem-abc", "/w/api", []string{"claude"})
	if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
		t.Errorf("Start code = %q, want %q", got, musem.EUNAVAILABLE)
	}
}

func TestAnAvailableTmuxReportsNoError(t *testing.T) {
	bin, log := recordingTmux(t, "echo 'tmux 3.4'")

	if err := sessions(bin).Available(context.Background()); err != nil {
		t.Fatalf("Available: %v", err)
	}
	if got := argv(t, log); len(got) != 1 || got[0] != "-V" {
		t.Errorf("argv = %v, want the version query", got)
	}
}

// R5, R8: existence is asked rather than assumed, and asked exactly — tmux
// matches a prefix otherwise, so a session named for one launch would answer for
// another that merely starts the same way.
func TestExistenceIsAskedExactly(t *testing.T) {
	bin, log := recordingTmux(t, "exit 0")

	ok, err := sessions(bin).Exists(context.Background(), "musem-abc")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("a session tmux acknowledged was reported as absent")
	}

	want := []string{"has-session", "-t", "=musem-abc"}
	got := argv(t, log)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v (the leading = is what makes the match exact)", got, want)
	}
}

// tmux exiting non-zero for has-session is an answer, not a failure: there is no
// server at all until something has been started.
func TestAnAbsentSessionIsAnAnswerNotAFailure(t *testing.T) {
	bin, _ := recordingTmux(t, "echo 'no server running' >&2; exit 1")

	ok, err := sessions(bin).Exists(context.Background(), "musem-abc")
	if err != nil {
		t.Fatalf("err = %v, want nil: tmux answered", err)
	}
	if ok {
		t.Error("a session tmux does not have was reported as present")
	}
}

// A question that could not be asked must not read as "no". The rollback acts on
// this answer, and a false "no" is a session left running with nothing recording
// that it exists.
func TestAQuestionTmuxCouldNotAnswerIsAnError(t *testing.T) {
	s := &Sessions{Bin: filepath.Join(t.TempDir(), "gone"), Timeout: time.Second}

	ok, err := s.Exists(context.Background(), "musem-abc")
	if err == nil {
		t.Fatal("a missing tmux answered the question")
	}
	if ok {
		t.Error("a failed check reported the session as present")
	}
}

// tmux addresses windows and panes with ":" and ".", so a name carrying one
// names something other than what was meant.
func TestSessionNamesTmuxWouldMisreadAreRefused(t *testing.T) {
	bin, log := recordingTmux(t, "exit 0")
	s := sessions(bin)

	for _, name := range []string{"", "  ", "-d", "musem:1", "musem.0", "musem\x1b[2J"} {
		if err := s.Start(context.Background(), name, "/w/api", []string{"claude"}); err == nil {
			t.Errorf("Start accepted the name %q", name)
		}
	}
	if got := argv(t, log); got != nil {
		t.Errorf("tmux ran with %v; none of these names should have reached it", got)
	}
}

// A tmux that refuses says why, rather than the failure being reported as
// musem's own.
func TestARefusedStartCarriesTmuxsReason(t *testing.T) {
	bin, _ := recordingTmux(t, "echo 'duplicate session: musem-abc' >&2; exit 1")

	err := sessions(bin).Start(context.Background(), "musem-abc", "/w/api", []string{"claude"})
	if err == nil {
		t.Fatal("a refused start reported success")
	}
	if !strings.Contains(musem.ErrorMessage(err), "duplicate session") {
		t.Errorf("message = %q, want tmux's own complaint", musem.ErrorMessage(err))
	}
}

// Killing is the rollback for starting, and is addressed as exactly as the
// existence check is: an inexact match would end somebody else's session.
func TestKillingASessionAddressesItExactly(t *testing.T) {
	bin, log := recordingTmux(t, "exit 0")

	if err := sessions(bin).Kill(context.Background(), "musem-abc"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	want := []string{"kill-session", "-t", "=musem-abc"}
	got := argv(t, log)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}
