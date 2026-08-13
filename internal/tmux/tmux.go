// Package tmux adapts tmux to musem's needs as the substrate a session runs in.
//
// tmux rather than a child process of musem, because a session has to outlive
// the observatory watching it: a user who closes musem has not asked their agent
// to stop. tmux already solves the PTY, the resize, the scrollback and the
// persistence, and every one of those is a project on its own.
//
// It is a hard dependency of launching and of nothing else. A machine without
// tmux can still observe every session on it; it just cannot start one, and
// saying which is missing is the whole of this package's error handling.
package tmux

import (
	"context"
	"strings"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/execx"
	"github.com/MrSossa/musem/internal/safetext"
)

// Sessions starts and inspects tmux sessions.
type Sessions struct {
	// Bin is the executable to run. Empty means "tmux" on PATH.
	Bin string

	// Timeout bounds one call. tmux answers in milliseconds when it answers at
	// all; the budget is here so a wedged server cannot hold the launch open.
	Timeout time.Duration
}

// DefaultTimeout bounds one tmux call.
const DefaultTimeout = 10 * time.Second

// NewSessions returns a Sessions with sensible defaults.
func NewSessions() *Sessions { return &Sessions{Bin: "tmux", Timeout: DefaultTimeout} }

func (s *Sessions) bin() string {
	if s.Bin == "" {
		return "tmux"
	}
	return s.Bin
}

func (s *Sessions) timeout() time.Duration {
	if s.Timeout <= 0 {
		return DefaultTimeout
	}
	return s.Timeout
}

// run executes tmux and classifies what came back. The returned Kind is
// meaningful only when the error is non-nil.
func (s *Sessions) run(ctx context.Context, answered func(string) bool, args ...string) (execx.Result, execx.Kind, error) {
	res, err := execx.Run(ctx, execx.Cmd{
		Bin:      s.bin(),
		Args:     args,
		Timeout:  s.timeout(),
		Answered: answered,
	})
	return res, execx.KindOf(err), err
}

// Available reports whether tmux can be run at all.
//
// It exists so a launch can fail before it has created anything, naming what is
// missing. Discovering that tmux is absent after a worktree has been checked out
// means a rollback that need never have happened.
func (s *Sessions) Available(ctx context.Context) error {
	_, kind, err := s.run(ctx, func(string) bool { return true }, "-V")
	if err == nil {
		return nil
	}
	switch kind {
	case execx.NotFound:
		return musem.Wrap(err, musem.EUNAVAILABLE,
			"tmux (%s) was not found on PATH; musem needs it to host a session", s.bin())
	case execx.Timeout:
		return musem.Wrap(err, musem.EUNAVAILABLE, "tmux did not respond within %s", s.timeout())
	case execx.Exited, execx.Failed:
	}
	return musem.Wrap(err, musem.EUNAVAILABLE, "tmux could not be run")
}

// Start creates a detached session named name, running command in dir.
//
// Detached is the point: the session belongs to the tmux server rather than to
// musem, so it survives musem exiting and is still there on the next run. The
// command goes as an argv with no shell between it and tmux.
func (s *Sessions) Start(ctx context.Context, name, dir string, command []string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return musem.Errorf(musem.EINVALID, "a session needs a directory to start in")
	}
	if len(command) == 0 {
		return musem.Errorf(musem.EINVALID, "a session needs a command to run")
	}

	args := append([]string{"new-session", "-d", "-s", name, "-c", dir, "--"}, command...)

	// Nothing here reads tmux's output, so a tmux that did its job and left the
	// server process behind — which is exactly what starting a server looks
	// like — counts as success.
	res, kind, err := s.run(ctx, func(string) bool { return true }, args...)
	if err == nil {
		return nil
	}
	switch kind {
	case execx.NotFound:
		return musem.Wrap(err, musem.EUNAVAILABLE,
			"tmux (%s) was not found on PATH; musem needs it to host a session", s.bin())
	case execx.Timeout:
		return musem.Wrap(err, musem.EUNAVAILABLE, "tmux timed out starting the session %s", name)
	case execx.Exited, execx.Failed:
	}
	if detail := complaint(res.Stderr); detail != "" {
		return musem.Wrap(err, musem.EUNAVAILABLE, "tmux refused to start the session: %s", detail)
	}
	return musem.Wrap(err, musem.EUNAVAILABLE, "tmux refused to start the session %s", name)
}

// Exists reports whether a session by that name is running.
//
// A launch uses it to tell that starting worked rather than assuming it from an
// exit code, and the rollback uses it to know whether there is anything to undo.
// An error is not "no": a question that could not be asked is returned as one,
// so a caller never reads a failed check as an absent session.
func (s *Sessions) Exists(ctx context.Context, name string) (bool, error) {
	if err := checkName(name); err != nil {
		return false, err
	}

	// "=" is an exact match. Without it tmux matches a prefix, and a session
	// named for one launch would answer for another that merely starts the same
	// way — which is the difference between rolling back the launch that failed
	// and leaving it in place because something else looked like it.
	_, kind, err := s.run(ctx, func(string) bool { return true }, "has-session", "-t", "="+name)
	if err == nil {
		return true, nil
	}
	switch kind {
	case execx.NotFound:
		return false, musem.Wrap(err, musem.EUNAVAILABLE,
			"tmux (%s) was not found on PATH", s.bin())
	case execx.Timeout:
		return false, musem.Wrap(err, musem.EUNAVAILABLE, "tmux timed out looking for the session %s", name)
	case execx.Exited:
		// tmux ran and said no. That is an answer, not a failure: the session
		// does not exist, and there is no tmux server at all when nothing has
		// been started yet.
		return false, nil
	case execx.Failed:
	}
	return false, musem.Wrap(err, musem.EUNAVAILABLE, "cannot ask tmux about the session %s", name)
}

// Kill ends a session. It is the rollback for Start and has no other caller:
// stopping a session the user asked for is not something this change does.
func (s *Sessions) Kill(ctx context.Context, name string) error {
	if err := checkName(name); err != nil {
		return err
	}

	res, kind, err := s.run(ctx, func(string) bool { return true }, "kill-session", "-t", "="+name)
	if err == nil {
		return nil
	}
	switch kind {
	case execx.NotFound:
		return musem.Wrap(err, musem.EUNAVAILABLE, "tmux (%s) was not found on PATH", s.bin())
	case execx.Timeout:
		return musem.Wrap(err, musem.EUNAVAILABLE, "tmux timed out ending the session %s", name)
	case execx.Exited, execx.Failed:
	}
	if detail := complaint(res.Stderr); detail != "" {
		return musem.Wrap(err, musem.EUNAVAILABLE, "tmux could not end the session %s: %s", name, detail)
	}
	return musem.Wrap(err, musem.EUNAVAILABLE, "tmux could not end the session %s", name)
}

// checkName refuses names tmux would misread.
//
// A colon or a full stop is how tmux addresses a window and a pane inside a
// session, so a name carrying one names something other than what was meant. A
// leading dash is read as an option. The name musem generates contains none of
// these; the check is here because the value crosses a boundary, and a rule that
// only holds while the caller behaves is not a rule.
func checkName(name string) error {
	if strings.TrimSpace(name) == "" {
		return musem.Errorf(musem.EINVALID, "a tmux session needs a name")
	}
	if strings.HasPrefix(name, "-") {
		return musem.Errorf(musem.EINVALID, "a session name cannot start with %q; tmux would read it as an option", "-")
	}
	if strings.ContainsAny(name, ":.") {
		return musem.Errorf(musem.EINVALID, "a session name cannot contain %q or %q; tmux reads them as a window or pane", ":", ".")
	}
	if safetext.HasUnprintable(name) {
		return musem.Errorf(musem.EINVALID, "a session name cannot contain unprintable characters")
	}
	return nil
}

// complaint returns the first useful line tmux wrote to stderr.
func complaint(stderr string) string { return safetext.FirstLine(stderr, nil) }
