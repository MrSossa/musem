// Package claude adapts the Claude Code CLI to musem's own types.
//
// Everything musem knows about a foreign format lives in this package: the
// shape of `claude agents --json`, the layout of the JSONL transcripts, the
// names of the usage fields. The rest of the codebase works in musem types, so
// when one of those formats changes — and it will — there is one place to fix
// and one place to test.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/MrSossa/musem"
)

// agentRecord mirrors one entry of `claude agents --json`. Unknown fields are
// ignored by encoding/json, which is exactly the desired behaviour: a foreign
// tool adding a field must not break musem.
type agentRecord struct {
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
	StartedAt int64  `json:"startedAt"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

// Discoverer lists the live agent sessions by shelling out to the Claude CLI.
type Discoverer struct {
	// Bin is the executable to run. Empty means "claude" on PATH.
	Bin string
	// Timeout bounds one discovery call so a hung CLI cannot stall the loop.
	Timeout time.Duration
}

// NewDiscoverer returns a Discoverer with sensible defaults.
func NewDiscoverer() *Discoverer {
	return &Discoverer{Bin: "claude", Timeout: 15 * time.Second}
}

// Discover returns the sessions currently alive on this machine.
//
// A missing CLI is reported as EUNAVAILABLE rather than as a generic failure,
// because the dashboard's job in that case is to stay up and explain itself.
func (d *Discoverer) Discover(ctx context.Context) ([]musem.Session, error) {
	bin := d.Bin
	if bin == "" {
		bin = "claude"
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// The binary is a variable, which is what gosec objects to, but nothing an
	// attacker reaches: it is a field on this struct, defaulted to "claude" and
	// set only by the code that wires the adapter up. The arguments beside it are
	// constants, and there is no shell — exec.CommandContext passes an argv, so a
	// binary name carrying spaces or metacharacters is a filename that will not
	// be found, not a second command. Resolving it via PATH is the intent: musem
	// runs the CLI the user's own shell would.
	// #nosec G204 -- bin is a configured field, never external input; argv, no shell
	cmd := exec.CommandContext(ctx, bin, "agents", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Killing the process is not the same as getting the call back. Run waits
	// for the goroutine copying into these buffers, and that goroutine finishes
	// only when every process holding the pipe's write end has exited — the
	// context kills the direct child, not a grandchild it left behind. Without a
	// delay a credential helper, a hook, or anything wedged on a stalled mount
	// holds this call open past the timeout and freezes the refresh loop the
	// timeout exists to protect.
	cmd.WaitDelay = time.Second

	if err := cmd.Run(); err != nil && !answeredBeforeItsChildLetGo(cmd, err) {
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return nil, musem.Wrap(err, musem.EUNAVAILABLE,
				"the Claude CLI (%s) was not found on PATH", bin)
		}
		if ctx.Err() != nil {
			return nil, musem.Wrap(ctx.Err(), musem.EUNAVAILABLE,
				"discovery timed out after %s", timeout)
		}
		// The CLI's own explanation is worth more than a generic failure: an
		// unsupported flag after an upgrade, a config problem, a pending auth
		// prompt. It was already captured; throwing it away leaves the user an
		// error they cannot act on.
		if detail := firstLine(stderr.String()); detail != "" {
			return nil, musem.Wrap(err, musem.EUNAVAILABLE,
				"the Claude CLI failed to list sessions: %s", detail)
		}
		return nil, musem.Wrap(err, musem.EUNAVAILABLE,
			"the Claude CLI failed to list sessions")
	}

	return parseAgents(stdout.Bytes())
}

// answeredBeforeItsChildLetGo reports that the CLI did the job and only the
// plumbing outlived it.
//
// WaitDelay closes the pipes when the process holding their write end is not the
// one that was waited on — a hook, a helper, anything the CLI forks and detaches
// — and Run then reports ErrWaitDelay for a command that exited zero. Taking
// that as a failure throws away a session list that was captured in full and
// puts an error banner over stale rows, every refresh, for a call that worked.
//
// Nothing is assumed about the payload. Output the closing pipe cut short is not
// valid JSON, so it fails in the parser as unparseable — which is the accurate
// complaint, and a different one from the CLI having failed.
func answeredBeforeItsChildLetGo(cmd *exec.Cmd, err error) bool {
	return errors.Is(err, exec.ErrWaitDelay) &&
		cmd.ProcessState != nil && cmd.ProcessState.Success()
}

// firstLine returns the first non-empty line of s, bounded so a CLI that writes
// a stack trace to stderr cannot push the rest of the interface off the screen.
func firstLine(s string) string {
	const maxDetail = 200

	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Cut on a rune boundary, not a byte one. This text reaches the
		// dashboard header, where half a character is both unreadable and
		// mis-measured by everything that lays the line out.
		if runes := []rune(line); len(runes) > maxDetail {
			line = string(runes[:maxDetail]) + "…"
		}
		return line
	}
	return ""
}

// parseAgents maps the CLI payload onto musem sessions. It is separate from the
// process call so the mapping can be tested against fixtures without running
// anything.
func parseAgents(data []byte) ([]musem.Session, error) {
	// Decoded one record at a time. The payload belongs to another tool and can
	// change shape without warning; a single record musem cannot read must cost
	// the user that session, not every session — the same degradation the
	// transcript reader applies line by line.
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, musem.Wrap(err, musem.EUNPARSEABLE,
			"the session list was not in a recognised format")
	}

	now := time.Now()
	sessions := make([]musem.Session, 0, len(raw))
	for _, item := range raw {
		var r agentRecord
		if err := json.Unmarshal(item, &r); err != nil {
			continue
		}
		if r.SessionID == "" {
			// Without a stable identifier there is nothing to key on, and
			// inventing one would silently create a duplicate on every refresh.
			continue
		}
		s := musem.Session{
			ID:       r.SessionID,
			Name:     r.Name,
			Dir:      r.CWD,
			Status:   mapStatus(r.Status),
			PID:      r.PID,
			LastSeen: now,
		}
		if r.StartedAt > 0 {
			s.Started = time.UnixMilli(r.StartedAt)
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// mapStatus translates the CLI's status vocabulary into musem's.
//
// An unrecognised value becomes StatusIndeterminate rather than being guessed
// at. Guessing "idle" would be actively harmful: idle is the status a user
// reads as "nothing to do here", so a wrong idle hides a session that may be
// waiting for them.
func mapStatus(s string) musem.Status {
	switch s {
	case "busy", "running":
		return musem.StatusRunning
	case "waiting":
		return musem.StatusWaiting
	case "idle":
		return musem.StatusIdle
	case "error", "dead", "failed":
		return musem.StatusDead
	default:
		return musem.StatusIndeterminate
	}
}
