// Package musem holds the domain types for musem, a read-only observatory for
// AI coding agent sessions running on this machine.
//
// This package imports nothing outside the standard library. Every other
// package depends on it and it depends on none of them, which is what keeps the
// adapters genuinely replaceable. The rule is asserted by a test, not left to
// discipline; see internal/archtest.
package musem

import (
	"fmt"
	"strings"
	"time"
)

// Status is the observed state of a session. The set is closed: any value
// outside these constants is invalid, which is what makes it safe to switch on
// exhaustively.
type Status string

const (
	// StatusRunning means the agent is actively working.
	StatusRunning Status = "running"
	// StatusWaiting means the session needs an action from the user. This is
	// the status that matters most: a session in this state is blocked on a
	// human and every second counts against them.
	StatusWaiting Status = "waiting"
	// StatusIdle means the session is ready for instructions.
	StatusIdle Status = "idle"
	// StatusDead means the session terminated abnormally.
	StatusDead Status = "dead"
	// StatusIndeterminate means the available signals do not allow deciding.
	//
	// This exists so musem never has to guess. A false "idle" is worse than an
	// honest "I don't know", because it makes the user ignore a session that is
	// waiting for them.
	StatusIndeterminate Status = "indeterminate"
)

// Valid reports whether s is one of the known statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusRunning, StatusWaiting, StatusIdle, StatusDead, StatusIndeterminate:
		return true
	}
	return false
}

// Urgency orders statuses by how much they demand the user's attention, lowest
// first. It is a domain fact — which state is more urgent than which — and not
// a presentation choice; how that ordering is displayed belongs to the UI.
func (s Status) Urgency() int {
	switch s {
	case StatusWaiting:
		return 0
	case StatusDead:
		return 1
	case StatusRunning:
		return 2
	case StatusIndeterminate:
		return 3
	case StatusIdle:
		return 4
	}
	return 5
}

// Session is an agent session observed on this machine.
type Session struct {
	// ID is the session's own stable identifier. Titles and paths are editable
	// by the user or by the tool itself and must never be used as a key.
	ID string

	// Name is a human-readable label. It can change at any time.
	Name string

	// Dir is the session's working directory.
	Dir string

	// Branch is the git branch checked out at Dir, empty when Dir is not inside
	// a repository. That absence is normal and not an error.
	Branch string

	Status  Status
	PID     int
	Started time.Time

	// LastSeen is when discovery last observed this session.
	LastSeen time.Time

	// EndedAt is set once the session stops appearing in discovery. A session is
	// never dropped from the registry silently; it is marked and kept.
	EndedAt *time.Time
}

// Ended reports whether the session has stopped appearing in discovery.
func (s *Session) Ended() bool { return s.EndedAt != nil }

// Validate returns an error if the session is missing what every session must
// have. Adapters call this after mapping foreign data; they do not reimplement
// it.
func (s *Session) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return Errorf(EINVALID, "session id is required")
	}
	if !s.Status.Valid() {
		return Errorf(EINVALID, "unknown session status %q", string(s.Status))
	}
	return nil
}

// StaleAfter reports whether the session's data is older than d as of now. Data
// freshness is a fact about the data, decided here; how staleness is signalled
// on screen is a separate, presentational decision.
func (s *Session) StaleAfter(now time.Time, d time.Duration) bool {
	if s.LastSeen.IsZero() {
		return true
	}
	return now.Sub(s.LastSeen) > d
}

// Usage is the token consumption recorded for a session, taken verbatim from
// what the agent tool wrote.
//
// Cache tokens are kept apart from ordinary input because they are priced
// differently — and cache writes are split by time-to-live, because a 5-minute
// write and a 1-hour write are billed at different multiples of the input rate.
// Collapsing them would make the cheaper case look like the dearer one.
type Usage struct {
	InputTokens  int64
	OutputTokens int64

	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	CacheReadTokens    int64
}

// CacheWriteTokens returns every cache-creation token, both time-to-live kinds.
func (u Usage) CacheWriteTokens() int64 {
	return u.CacheWrite5mTokens + u.CacheWrite1hTokens
}

// Add accumulates other into u.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheWrite5mTokens += other.CacheWrite5mTokens
	u.CacheWrite1hTokens += other.CacheWrite1hTokens
	u.CacheReadTokens += other.CacheReadTokens
}

// Total returns every token counted, regardless of kind.
func (u Usage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheWriteTokens() + u.CacheReadTokens
}

// IsZero reports whether nothing at all was consumed. This is distinct from a
// Cost being unknown: a session that has produced no response has zero usage,
// which is a fact, not an absence of information.
func (u Usage) IsZero() bool { return u.Total() == 0 }

// Cost is an amount in US dollars that may be unknown.
//
// The zero value is a known zero cost. Unknown is reached only through
// UnknownCost, so a cost that could not be computed can never be silently read
// as $0.00 — which would look plausible and be wrong, the worst combination.
type Cost struct {
	usd     float64
	unknown bool
}

// USD returns a known cost of amount dollars.
func USD(amount float64) Cost { return Cost{usd: amount} }

// UnknownCost returns a cost that could not be determined, typically because no
// rate is known for the model that produced the usage.
func UnknownCost() Cost { return Cost{unknown: true} }

// Known reports whether the amount is available.
func (c Cost) Known() bool { return !c.unknown }

// Amount returns the cost in dollars and whether it is known. Callers must
// check the boolean; there is deliberately no accessor that hands back a bare
// float.
func (c Cost) Amount() (float64, bool) {
	if c.unknown {
		return 0, false
	}
	return c.usd, true
}

// Add returns the sum of two costs. Unknown is contagious: any unknown part
// makes the total unknown, because a total that silently omits a component is
// indistinguishable from a complete one.
func (c Cost) Add(other Cost) Cost {
	if c.unknown || other.unknown {
		return UnknownCost()
	}
	return USD(c.usd + other.usd)
}

// String renders the cost for display, using an em dash for unknown.
func (c Cost) String() string {
	if c.unknown {
		return "—"
	}
	return fmt.Sprintf("$%.2f", c.usd)
}

// ModelUsage is usage attributed to one model.
//
// It lives in the domain rather than in the package that prices it, so an
// adapter can report usage without depending on anything that consumes it.
type ModelUsage struct {
	Model string
	Usage Usage
}

// UsageReading is what one pass over a session's usage record yielded.
//
// It lives in the domain because both the adapter that produces it and the
// package that consumes it need to name it, and neither may depend on the
// other.
type UsageReading struct {
	// Entries is the usage recorded since the cursor the reader was given.
	Entries []ModelUsage

	// Cursor is where the next read should resume. It is opaque to everyone
	// except the reader that issued it: only that reader knows whether it
	// encodes a byte offset, a record number, or nothing at all.
	//
	// It is returned rather than remembered inside the reader so it can be
	// persisted in the same write as the totals it corresponds to. A cursor
	// held only in memory is lost on restart, and re-reading a record that has
	// already been counted inflates the total it is added to.
	Cursor string

	// Skipped counts records that were passed over because they could not be
	// understood. The usage they carried is gone, so a non-zero count means the
	// figures beside it understate the truth and must say so.
	Skipped int
}

// SessionCost is the accounting for one session.
type SessionCost struct {
	SessionID string
	Usage     Usage
	Cost      Cost

	// UnknownModels lists models encountered with no known rate. When this is
	// non-empty the tokens were still counted but Cost is unknown, and naming
	// the models is what makes the gap actionable.
	UnknownModels []string

	// Cursor is how far the source record has been counted, persisted alongside
	// the totals it produced so a restart resumes instead of recounting.
	Cursor string

	// Skipped is how many records have been passed over as unreadable across
	// every pass. Tokens they carried were never counted.
	Skipped int
}

// Partial reports whether some part of this cost could not be priced.
func (sc SessionCost) Partial() bool { return len(sc.UnknownModels) > 0 || !sc.Cost.Known() }

// Degraded reports whether some usage was lost to records that could not be
// read. Distinct from Partial: partial means counted but unpriceable, degraded
// means never counted at all.
func (sc SessionCost) Degraded() bool { return sc.Skipped > 0 }
