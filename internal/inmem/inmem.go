// Package inmem serves fabricated sessions so the dashboard can be developed
// and demonstrated without live agents running.
//
// It exists for a practical reason: the interesting states — a session waiting
// on the user, one that died, one whose data went stale — are awkward to
// reproduce on demand with real agents, and a UI that can only be exercised
// against whatever happens to be running is a UI whose edge cases never get
// looked at.
package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/MrSossa/musem"
)

// Discoverer returns a fixed set of sessions. It satisfies the same port the
// real adapter does, so main can swap one for the other.
type Discoverer struct {
	mu       sync.Mutex
	sessions []musem.Session

	// Err, when set, is returned instead of the sessions, which is how the
	// unavailable-source path gets exercised.
	Err error
}

// NewDiscoverer returns a Discoverer preloaded with a representative spread of
// states, including the ones that are hardest to catch in the wild.
func NewDiscoverer() *Discoverer {
	now := time.Now()
	ended := now.Add(-15 * time.Minute)

	return &Discoverer{sessions: []musem.Session{
		{
			ID: "inmem-0001", Name: "api", Dir: "/home/dev/projects/api",
			Branch: "feat/rate-limit", Status: musem.StatusWaiting,
			PID: 1001, Started: now.Add(-42 * time.Minute), LastSeen: now,
		},
		{
			ID: "inmem-0002", Name: "web", Dir: "/home/dev/projects/web",
			Branch: "main", Status: musem.StatusRunning,
			PID: 1002, Started: now.Add(-8 * time.Minute), LastSeen: now,
		},
		{
			ID: "inmem-0003", Name: "docs", Dir: "/home/dev/notes",
			Branch: "", Status: musem.StatusIdle,
			PID: 1003, Started: now.Add(-3 * time.Hour), LastSeen: now,
		},
		{
			// Stale on purpose: LastSeen is old, so the UI must show it as such
			// rather than presenting it like a current reading.
			ID: "inmem-0004", Name: "worker", Dir: "/home/dev/projects/worker",
			Branch: "fix/retry", Status: musem.StatusIndeterminate,
			PID: 1004, Started: now.Add(-90 * time.Minute), LastSeen: now.Add(-4 * time.Minute),
		},
		{
			ID: "inmem-0005", Name: "spike", Dir: "/home/dev/projects/spike",
			Branch: "spike/cache", Status: musem.StatusDead,
			PID: 1005, Started: now.Add(-2 * time.Hour), LastSeen: ended, EndedAt: &ended,
		},
	}}
}

// Discover returns the configured sessions, or the configured error.
func (d *Discoverer) Discover(_ context.Context) ([]musem.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.Err != nil {
		return nil, d.Err
	}

	out := make([]musem.Session, len(d.sessions))
	copy(out, d.sessions)
	return out, nil
}

// SetSessions replaces the served set, so a test can drive a transition such as
// a session appearing, changing status, or disappearing.
func (d *Discoverer) SetSessions(sessions []musem.Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions = sessions
}

// BranchResolver reports a fixed branch per directory.
type BranchResolver struct {
	Branches map[string]string
}

// Branch returns the configured branch for dir, or empty when there is none —
// which is the same answer a directory outside a repository gets, and is not an
// error.
func (r BranchResolver) Branch(_ context.Context, dir string) (string, error) {
	return r.Branches[dir], nil
}

// UsageReader reports fabricated usage, so the cost column has something to
// show during development.
//
// State is per instance rather than per package: two readers must not share a
// memory of what they have already reported, or a second consumer silently sees
// nothing. That is the same no-package-level-globals rule the rest of the
// project follows, and a test double is not exempt from it.
type UsageReader struct {
	mu   sync.Mutex
	seen map[string]bool
}

// NewUsageReader returns a reader with no prior state.
func NewUsageReader() *UsageReader {
	return &UsageReader{seen: make(map[string]bool)}
}

// ReadUsage returns a fixed sample the first time a session is asked about and
// nothing afterwards, mimicking a transcript that is read incrementally.
func (r *UsageReader) ReadUsage(_ context.Context, sessionID string) ([]musem.ModelUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.seen == nil {
		r.seen = make(map[string]bool)
	}
	if r.seen[sessionID] {
		return nil, nil
	}
	r.seen[sessionID] = true

	return []musem.ModelUsage{{
		Model: "claude-opus-5",
		Usage: musem.Usage{
			InputTokens:        1_200,
			OutputTokens:       3_400,
			CacheWrite1hTokens: 26_177,
			CacheReadTokens:    120_000,
		},
	}}, nil
}
