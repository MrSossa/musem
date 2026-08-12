// Package app joins what the registry knows about sessions with what the
// accountant knows about their cost, and hands the result out as one snapshot.
//
// It is deliberately thin. The dashboard needs a session and its cost in the
// same row, so something has to join them — and if that something were the UI,
// the join would have to be duplicated by any second front end. The moment this
// package grows rules of its own, those rules belong to registry or cost.
package app

import (
	"context"
	"sync"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/cost"
	"github.com/MrSossa/musem/internal/registry"
)

// Row is one session with its accounting attached.
type Row struct {
	Session musem.Session
	Cost    musem.Cost

	// Partial reports that some of this session's usage could not be priced.
	Partial bool

	// Degraded reports that some of this session's usage was never counted,
	// because the records carrying it could not be read. The figure beside it
	// is an understatement, and saying so is the difference between a number
	// the user can audit and one they cannot.
	Degraded bool

	// Priced is the dollars accumulated from this session's usage that did have
	// a rate. It is what is left to show once one unpriceable model has made the
	// cost itself unknown: the figure is a floor rather than a total, and a
	// floor is worth more than nothing at all.
	Priced float64

	// UnknownModels names the models this session used that have no known rate.
	// Carried through to the view because naming them is what turns "the cost
	// is incomplete" into something the user can act on — a gap they can close
	// by adding a rate, rather than one they can only be told about.
	UnknownModels []string
}

// Snapshot is everything the dashboard renders in one pass.
type Snapshot struct {
	Rows  []Row
	Fleet cost.Fleet

	// Stale, ErrCode and ErrMessage carry the registry's honesty about its own
	// data: how old it is and why, if the last refresh failed.
	Stale      bool
	ErrCode    string
	ErrMessage string
}

// Composer assembles snapshots.
type Composer struct {
	registry   *registry.Registry
	accountant *cost.Accountant

	// settled names ended sessions whose final usage has already been folded
	// in. The registry never drops a session, so without this the refresh loop
	// grows without bound and spends every pass re-reading records that stopped
	// changing hours ago.
	//
	// failing records when an ended session's final read first failed, so a
	// transcript that will never be readable stops being chased.
	mu      sync.Mutex
	settled map[string]bool
	failing map[string]time.Time

	now func() time.Time
}

// finalReadGrace bounds how long an ended session's last read keeps being
// retried before it is settled regardless.
//
// A failed read says nothing, so one failure must not settle a session — but a
// transcript that was deleted never becomes readable, and each attempt walks
// every project directory looking for it.
//
// The bound is time, not a count of attempts, because attempts are cheap in a
// way that misleads: the reader remembers a failed search for DefaultRetryLookup
// and answers from that memory without looking, so a budget of three tries would
// be spent in a few seconds on two answers nobody went and fetched. A session
// whose transcript is merely late needs wall-clock time, not passes.
const finalReadGrace = 2 * time.Minute

// Option configures a Composer.
type Option func(*Composer)

// WithClock replaces the clock, so a test can reach the retry grace without
// waiting for it.
func WithClock(now func() time.Time) Option {
	return func(c *Composer) {
		if now != nil {
			c.now = now
		}
	}
}

// New returns a Composer over the given sources.
func New(r *registry.Registry, a *cost.Accountant, opts ...Option) *Composer {
	c := &Composer{
		registry:   r,
		accountant: a,
		settled:    make(map[string]bool),
		failing:    make(map[string]time.Time),
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Snapshot joins the current inventory with current costs, preserving the
// registry's ordering — which already puts sessions awaiting the user first.
func (c *Composer) Snapshot() Snapshot {
	inventory := c.registry.Snapshot()

	rows := make([]Row, 0, len(inventory.Sessions))
	ids := make([]string, 0, len(inventory.Sessions))
	for _, s := range inventory.Sessions {
		// A session the accountant has never recorded is one whose usage could
		// not be read, not one that cost nothing. USD(0) here would render a
		// confident $0.00, with no marker, for a session burning real money.
		// The em dash needs no marker beside it: unlike a starred figure, it is
		// not a number claiming to be short — it is the absence of one.
		row := Row{Session: s, Cost: musem.UnknownCost()}
		if sc, ok := c.accountant.Session(s.ID); ok {
			row.Cost = sc.Cost
			row.Partial = sc.Partial()
			row.Degraded = sc.Degraded()
			row.UnknownModels = sc.UnknownModels
			row.Priced = sc.Priced
		}
		rows = append(rows, row)
		ids = append(ids, s.ID)
	}

	// The total covers exactly the sessions listed beside it. The accountant
	// holds more than that — history outlives the sessions that made it — and a
	// figure summed over all of it under a count of the few on screen would
	// belong to neither.
	return Snapshot{
		Rows:       rows,
		Fleet:      c.accountant.Total(ids),
		Stale:      inventory.Stale,
		ErrCode:    inventory.ErrCode,
		ErrMessage: inventory.ErrMessage,
	}
}

// Refresh updates costs for every session still worth reading. Discovery has
// its own loop in the registry; this keeps the accounting in step with it.
//
// A session whose usage cannot be read is skipped rather than failing the pass:
// one unreadable transcript must not stop the other figures from updating.
//
// An ended session is read once more and then left alone. The extra pass is
// what catches anything written between the last refresh and the session
// disappearing; after it there is nothing left to arrive, and the accumulated
// total stays where it is whether or not the record still exists.
//
// A successful final read settles a session. A read that failed says nothing
// about whether more usage is waiting — the transcript may simply not have been
// written yet — so it is retried, but only for finalReadGrace: a record that was
// deleted will not appear however long it is chased.
func (c *Composer) Refresh(ctx context.Context) {
	for _, s := range c.registry.Snapshot().Sessions {
		if !s.Ended() {
			// A session that reappears is live again, and the registry has
			// already cleared its end. Forgetting that it was settled is what
			// keeps its second ending from skipping the final read.
			c.unsettle(s.ID)
		} else if c.isSettled(s.ID) {
			continue
		}

		err := c.accountant.Update(ctx, s.ID)
		if !s.Ended() {
			continue
		}
		if err == nil || c.exhausted(s.ID) {
			c.settle(s.ID)
		}
	}
}

func (c *Composer) isSettled(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settled[id]
}

func (c *Composer) settle(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled == nil {
		c.settled = make(map[string]bool)
	}
	c.settled[id] = true
	delete(c.failing, id)
}

func (c *Composer) unsettle(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.settled, id)
	delete(c.failing, id)
}

// exhausted notes that an ended session's final read failed and reports whether
// it has been failing long enough to stop chasing.
func (c *Composer) exhausted(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failing == nil {
		c.failing = make(map[string]time.Time)
	}

	now := c.now()
	first, ok := c.failing[id]
	if !ok {
		c.failing[id] = now
		return false
	}
	return now.Sub(first) >= finalReadGrace
}
