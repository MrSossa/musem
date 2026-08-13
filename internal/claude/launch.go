package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/execx"
)

// Agent produces what is needed to start a Claude Code session, and reports
// whether the tool is there to start.
//
// It sits beside Discoverer because it is the same foreign tool: the CLI's name,
// its flags and the shape of the identifier it will accept are all knowledge of
// Claude Code's format, and this package is where that knowledge is kept.
type Agent struct {
	// Bin is the executable to run. Empty means "claude" on PATH.
	Bin string

	// Timeout bounds the availability check.
	Timeout time.Duration
}

// DefaultAgentTimeout bounds the check that the CLI is installed.
const DefaultAgentTimeout = 10 * time.Second

// NewAgent returns an Agent with sensible defaults.
func NewAgent() *Agent { return &Agent{Bin: "claude", Timeout: DefaultAgentTimeout} }

func (a *Agent) bin() string {
	if a.Bin == "" {
		return "claude"
	}
	return a.Bin
}

// Available reports whether the agent tool can be run.
//
// It is asked before a launch creates anything, so a machine without the CLI
// costs a refusal rather than a worktree that has to be rolled back. The
// treatment is the one discovery already gives a missing CLI: EUNAVAILABLE with
// the binary named, because the dashboard's job in that case is to stay up and
// explain itself.
func (a *Agent) Available(ctx context.Context) error {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultAgentTimeout
	}

	_, err := execx.Run(ctx, execx.Cmd{
		Bin:     a.bin(),
		Args:    []string{"--version"},
		Timeout: timeout,
		// Nothing here reads the output; only whether the binary ran at all.
		Answered: func(string) bool { return true },
	})
	if err == nil {
		return nil
	}

	switch execx.KindOf(err) {
	case execx.NotFound:
		return musem.Wrap(err, musem.EUNAVAILABLE,
			"the Claude CLI (%s) was not found on PATH; musem needs it to start a session", a.bin())
	case execx.Timeout:
		return musem.Wrap(err, musem.EUNAVAILABLE,
			"the Claude CLI did not respond within %s", timeout)
	case execx.Exited, execx.Failed:
	}
	return musem.Wrap(err, musem.EUNAVAILABLE, "the Claude CLI could not be run")
}

// Command returns the argv that starts an agent for the given session.
//
// The working directory is not among the arguments, and that is not an
// omission: the CLI takes the directory it is started in, so where the session
// runs is the substrate's business. Putting a directory here would be a
// parameter that changed nothing.
//
// The session identifier is passed rather than left for the CLI to invent. It is
// what makes the session musem starts the same session musem later discovers,
// and therefore what lets the worktree record be keyed by the session it was
// made for instead of guessed at from a path.
func (a *Agent) Command(sessionID string) []string {
	return []string{a.bin(), "--session-id", sessionID}
}

// NewSessionID returns an identifier the CLI will accept.
//
// Claude Code takes a UUID and nothing else, which is a fact about that tool's
// format and therefore this package's to know. It is version 4 — random, not
// derived from anything about the machine — so two musem processes launching at
// the same moment cannot produce the same one.
func (a *Agent) NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", musem.Wrap(err, musem.EINTERNAL, "cannot generate a session identifier")
	}
	// Version 4, variant 1: the two fields a UUID carries about how it was made.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32], nil
}
