// Package claude adapts the Claude Code CLI to musem's own types.
//
// Everything musem knows about a foreign format lives in this package: the
// shape of `claude agents --json`, the layout of the JSONL transcripts, the
// names of the usage fields. The rest of the codebase works in musem types, so
// when one of those formats changes — and it will — there is one place to fix
// and one place to test.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/execx"
	"github.com/MrSossa/musem/internal/safetext"
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
func (d *Discoverer) Discover(ctx context.Context) (musem.Discovery, error) {
	bin := d.Bin
	if bin == "" {
		bin = "claude"
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	res, err := execx.Run(ctx, execx.Cmd{
		Bin:     bin,
		Args:    []string{"agents", "--json"},
		Timeout: timeout,
		// Nothing is assumed about the payload: output the closing pipe cut
		// short is not valid JSON, so it fails in the parser as unparseable —
		// the accurate complaint, and a different one from the CLI failing.
		Answered: func(string) bool { return true },
	})
	if err != nil {
		var xerr *execx.Error
		if errors.As(err, &xerr) {
			switch xerr.Kind {
			case execx.NotFound:
				return musem.Discovery{}, musem.Wrap(err, musem.EUNAVAILABLE,
					"the Claude CLI (%s) was not found on PATH", bin)
			case execx.Timeout:
				return musem.Discovery{}, musem.Wrap(err, musem.EUNAVAILABLE,
					"discovery timed out after %s", timeout)
			case execx.Exited, execx.Failed:
			}
		}
		// The CLI's own explanation is worth more than a generic failure: an
		// unsupported flag after an upgrade, a config problem, a pending auth
		// prompt. It was already captured; throwing it away leaves the user an
		// error they cannot act on.
		if detail := firstLine(res.Stderr); detail != "" {
			return musem.Discovery{}, musem.Wrap(err, musem.EUNAVAILABLE,
				"the Claude CLI failed to list sessions: %s", detail)
		}
		return musem.Discovery{}, musem.Wrap(err, musem.EUNAVAILABLE,
			"the Claude CLI failed to list sessions")
	}

	return parseAgents([]byte(res.Stdout))
}

// firstLine returns the first non-empty line of s, bounded so a CLI that writes
// a stack trace to stderr cannot push the rest of the interface off the screen.
//
// The text is sanitised for the same reason session names and directories are:
// it is written by another program and it reaches the terminal, where an escape
// sequence is an instruction rather than a glyph. It is the likelier vector of
// the two, not the rarer one — a CLI reporting a failure tends to quote the
// input that caused it, and the inputs here are working directories that may
// come from a repository nobody vetted. It is also drawn on every frame for as
// long as discovery keeps failing.
func firstLine(s string) string {
	const maxDetail = 200

	for _, line := range strings.Split(s, "\n") {
		line = safetext.Clean(strings.TrimSpace(line))
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
func parseAgents(data []byte) (musem.Discovery, error) {
	// Decoded one record at a time. The payload belongs to another tool and can
	// change shape without warning; a single record musem cannot read must cost
	// the user that session, not every session — the same degradation the
	// transcript reader applies line by line.
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return musem.Discovery{}, musem.Wrap(err, musem.EUNPARSEABLE,
			"the session list was not in a recognised format")
	}

	now := time.Now()
	out := musem.Discovery{Sessions: make([]musem.Session, 0, len(raw))}
	for _, item := range raw {
		var r agentRecord
		if err := json.Unmarshal(item, &r); err != nil {
			// Counted, not merely skipped. A pass that could read none of what
			// it found looks exactly like a pass that found nothing, and the
			// registry treats an empty inventory as every session having ended
			// — so a field changing type across the board would mark the whole
			// fleet dead, confidently and with no marker. The count is what
			// keeps that from being silent.
			out.Skipped++
			continue
		}
		if r.SessionID == "" {
			// Without a stable identifier there is nothing to key on, and
			// inventing one would silently create a duplicate on every refresh.
			out.Skipped++
			continue
		}
		if safetext.HasUnprintable(r.SessionID) {
			// Dropped rather than stripped, which is the opposite of what every
			// other foreign string in this function gets — because an identifier
			// is not display text. This value is the key the registry indexes
			// by and the name the transcript is looked up under
			// (`<sessionId>.jsonl`), so stripping it would point musem at a file
			// that does not exist and quietly give the session a different
			// identity from the one its source uses. Two ids differing only in
			// control bytes would collapse into one session, which is precisely
			// what keying on a stable identifier exists to prevent.
			//
			// So it is counted with the rest of what could not be read. A record
			// whose identifier is unusable is a record musem cannot place, and
			// saying so is better than placing it wrongly.
			out.Skipped++
			continue
		}
		s := musem.Session{
			ID:       r.SessionID,
			Name:     safetext.Clean(r.Name),
			Dir:      safetext.Clean(r.CWD),
			Status:   mapStatus(r.Status),
			PID:      r.PID,
			LastSeen: now,
		}
		if r.StartedAt > 0 {
			s.Started = time.UnixMilli(r.StartedAt)
		}
		out.Sessions = append(out.Sessions, s)
	}
	return out, nil
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
