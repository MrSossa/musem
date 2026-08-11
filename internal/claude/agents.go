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
	cmd := exec.CommandContext(ctx, bin, "agents", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return nil, musem.Wrap(err, musem.EUNAVAILABLE,
				"the Claude CLI (%s) was not found on PATH", bin)
		}
		if ctx.Err() != nil {
			return nil, musem.Wrap(ctx.Err(), musem.EUNAVAILABLE,
				"discovery timed out after %s", timeout)
		}
		return nil, musem.Wrap(err, musem.EUNAVAILABLE,
			"the Claude CLI failed to list sessions")
	}

	return parseAgents(stdout.Bytes())
}

// parseAgents maps the CLI payload onto musem sessions. It is separate from the
// process call so the mapping can be tested against fixtures without running
// anything.
func parseAgents(data []byte) ([]musem.Session, error) {
	var records []agentRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, musem.Wrap(err, musem.EUNPARSEABLE,
			"the session list was not in a recognised format")
	}

	now := time.Now()
	sessions := make([]musem.Session, 0, len(records))
	for _, r := range records {
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
