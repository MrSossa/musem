// Package registry owns the inventory of agent sessions: it runs the discovery
// loop, holds what is currently known, resolves branches and decides when a
// session has ended or when the data has gone stale.
//
// It is orchestration, not a use case: it has state and a lifecycle. It also
// belongs to no adapter — the loop composes discovery with branch resolution
// and neither adapter should be calling the other.
package registry

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/MrSossa/musem"
)

// Discoverer lists the sessions currently alive on the machine.
//
// Declared here, by the consumer, rather than in the package that implements
// it: registry states what it needs and stays ignorant of who provides it.
type Discoverer interface {
	Discover(ctx context.Context) (musem.Discovery, error)
}

// BranchResolver reports the git branch checked out in a directory. An empty
// name with a nil error means "not a repository", which is normal.
type BranchResolver interface {
	Branch(ctx context.Context, dir string) (string, error)
}

// Defaults for the refresh loop.
const (
	DefaultInterval  = 2 * time.Second
	DefaultStaleness = 15 * time.Second

	// DefaultBranchTTL is how long a resolved branch is trusted before it is
	// looked up again. Branches change under musem's feet — switching branch is
	// among the most ordinary things to do in a working directory — so a cached
	// name has to expire. It is well above the refresh interval because
	// resolution shells out to git and the answer is a label, not a figure
	// anyone acts on within the second.
	DefaultBranchTTL = 10 * time.Second

	// branchStaleGrace is how many expired lifetimes a branch may go on being
	// shown while re-resolution keeps failing.
	//
	// It buys the difference between a git that is busy and a git that is gone.
	// Within the grace the last known name survives a failure, because blanking
	// a column over one slow call is noise; past it there is no longer reason to
	// believe the name, and showing a stale value as though it were current is
	// the one thing musem is built not to do.
	branchStaleGrace = 10
)

// Snapshot is the inventory as of one moment, together with everything the UI
// needs in order to be honest about it.
type Snapshot struct {
	Sessions  []musem.Session
	UpdatedAt time.Time

	// Stale reports that UpdatedAt is older than the staleness window, which
	// happens when refreshes stop succeeding. The data is still served — it is
	// the best that is known — but it must never look current.
	Stale bool

	// Age is how long ago the data was refreshed, and is only meaningful beside
	// Stale. It is set when Stale is true and a refresh has completed, and is
	// zero otherwise — both for fresh data, where the age is nobody's business
	// because the figures are current, and before the first refresh, where there
	// is no age to report and inventing one from a zero timestamp would claim
	// the data is decades old rather than absent. Read it only where Stale is,
	// and treat a zero on a stale snapshot as "never refreshed".
	//
	// It is derived here rather than left to the reader to compute from
	// UpdatedAt: Stale is decided by this same clock in this same call, and a
	// caller subtracting its own now from UpdatedAt could report an age that
	// contradicts the flag beside it. Saying how old the data is, and not only
	// that it is old, is what the interface needs to be honest about it.
	Age time.Duration

	// ErrCode and ErrMessage describe why the last refresh failed, empty when
	// it succeeded. Carried as an application code so the UI decides the
	// presentation and the adapter never has to.
	ErrCode    string
	ErrMessage string

	// Skipped is how many records the last successful pass could not read.
	//
	// It is distinct from ErrCode on purpose: the call worked, so the inventory
	// beside it is real — but it is missing however many sessions this counts,
	// and every one of them will look to the rest of the registry like a
	// session that has ended. A refresh that reads nothing is not a refresh
	// that found nothing, and this is what lets the interface tell them apart.
	Skipped int
}

// Registry keeps the known sessions up to date.
type Registry struct {
	discoverer Discoverer
	branches   BranchResolver

	interval  time.Duration
	staleness time.Duration
	branchTTL time.Duration
	now       func() time.Time

	mu sync.RWMutex
	// sessions is keyed by the session's own stable identifier. Titles and
	// paths are user-editable and would collide or churn as keys.
	sessions    map[string]musem.Session
	branchByDir map[string]branchEntry
	updatedAt   time.Time
	lastErr     error
	skipped     int
}

// branchEntry is a resolved branch and when it was resolved, so it can be
// retired rather than believed forever.
type branchEntry struct {
	branch     string
	resolvedAt time.Time
}

// Option configures a Registry.
type Option func(*Registry)

// WithInterval sets how long to wait between refreshes.
func WithInterval(d time.Duration) Option {
	return func(r *Registry) {
		if d > 0 {
			r.interval = d
		}
	}
}

// WithStaleness sets how old data may get before it is flagged as stale.
func WithStaleness(d time.Duration) Option {
	return func(r *Registry) {
		if d > 0 {
			r.staleness = d
		}
	}
}

// WithBranchTTL sets how long a resolved branch is trusted before it is looked
// up again.
func WithBranchTTL(d time.Duration) Option {
	return func(r *Registry) {
		if d > 0 {
			r.branchTTL = d
		}
	}
}

// WithClock replaces the time source, so tests can drive staleness without
// sleeping.
func WithClock(now func() time.Time) Option {
	return func(r *Registry) {
		if now != nil {
			r.now = now
		}
	}
}

// New returns a Registry. Dependencies are passed explicitly rather than pulled
// from a shared container, so this signature states exactly what the package
// needs.
func New(d Discoverer, b BranchResolver, opts ...Option) *Registry {
	r := &Registry{
		discoverer:  d,
		branches:    b,
		interval:    DefaultInterval,
		staleness:   DefaultStaleness,
		branchTTL:   DefaultBranchTTL,
		now:         time.Now,
		sessions:    make(map[string]musem.Session),
		branchByDir: make(map[string]branchEntry),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run refreshes on an interval until ctx is cancelled.
//
// It publishes nothing: readers pull with Snapshot, which is what the composer
// does on every frame. A push channel used to live here and had exactly one
// consumer, whose whole job was to discard what it received — and because Run
// blocked on the send, that discard was load-bearing for a value nobody wanted.
//
// The next wait starts only after a refresh returns, so two discovery calls can
// never overlap — the property is structural rather than something a guard has
// to enforce. A slow source therefore stretches the interval instead of piling
// up work behind it.
func (r *Registry) Run(ctx context.Context) {
	for {
		r.Refresh(ctx)

		select {
		case <-ctx.Done():
			return
		case <-time.After(r.interval):
		}
	}
}

// Refresh performs one discovery pass and folds the result into what is known.
//
// A failure is recorded but does not clear the inventory: the last known data
// is more useful than an empty screen, provided the UI is told it is old.
func (r *Registry) Refresh(ctx context.Context) {
	discovery, err := r.discoverer.Discover(ctx)
	if err != nil {
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		return
	}
	found := discovery.Sessions

	// Branches are resolved before the lock is taken. Resolution shells out to
	// git, and holding the write lock across that would block every reader for
	// as long as the slowest directory takes to answer — which the UI would
	// experience as a freeze, since it reads a snapshot every frame.
	branches := r.resolveBranches(ctx, found)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.lastErr = nil
	r.skipped = discovery.Skipped

	now := r.now()
	seen := make(map[string]bool, len(found))

	for _, s := range found {
		if s.Validate() != nil {
			// A record musem cannot make sense of is dropped rather than stored
			// half-formed, where it would later read as fact.
			continue
		}
		seen[s.ID] = true
		s.LastSeen = now
		s.Branch = branches[s.Dir]
		// Stamped here rather than by the adapter: how long a session has held a
		// status is a fact about musem's observation of it, and the adapter
		// reports one instant with no memory of the last.
		s.StatusSince = now

		if prev, ok := r.sessions[s.ID]; ok {
			// A session that reappears keeps the identity it had — a rename
			// changes the label, not the session. Its end is whatever discovery
			// just said, which for a session that came back is nothing: the
			// mark below is this registry's inference, and being seen again is
			// what withdraws it. An end the adapter reports itself survives,
			// because that is discovery stating a fact rather than inferring
			// one.
			if s.Started.IsZero() {
				s.Started = prev.Started
			}
			// A status that did not change keeps the moment it began. Restamping
			// it every pass is what LastSeen already does, and it would report
			// the age of the refresh in place of the age of the state — which is
			// the whole distinction "waiting since when" rests on.
			if prev.Status == s.Status && !prev.StatusSince.IsZero() {
				s.StatusSince = prev.StatusSince
			}
		}
		r.sessions[s.ID] = s
	}

	// Anything that stopped appearing has ended. It is marked and kept, never
	// dropped: its accumulated cost has to remain reachable.
	//
	// Only a pass that read everything it found may draw that conclusion. Ending
	// a session is an inference from absence, and a pass that admits it could not
	// read some of its records has not established that anything is absent — the
	// session may well have been sitting in one of them. Acting on it anyway is
	// how a field changing type turns a whole fleet of running sessions into a
	// wall of finished ones, confidently and with nothing on screen tying the two
	// together.
	//
	// The cost of waiting is that a session which genuinely ended during a
	// degraded pass keeps showing as live until a clean one arrives. That is the
	// direction to err in: a finished session shown as live for one cycle is
	// noise, a live session shown as finished is a session the user stops
	// looking at.
	if discovery.Skipped == 0 {
		for id, s := range r.sessions {
			if seen[id] || s.Ended() {
				continue
			}
			endedAt := s.LastSeen
			if endedAt.IsZero() {
				endedAt = now
			}
			s.EndedAt = &endedAt
			// Ended, not dead. Disappearing from discovery is how a session
			// finishes normally; dead is a verdict the source has to pronounce,
			// and inferring it here would label every cleanly closed session a
			// failure.
			if s.Status != musem.StatusDead {
				s.Status = musem.StatusEnded
				s.StatusSince = now
			}
			r.sessions[id] = s
		}
	}

	r.updatedAt = now
}

// resolveBranches returns the branch for every distinct directory in sessions,
// consulting the cache first and looking up whatever is missing or expired.
//
// The cache exists to keep musem from shelling out to git once per session per
// refresh; it is not a record of anything. Entries therefore expire: a user who
// switches branch must see the new name, and an entry that is never retired
// would show the old one for as long as musem runs.
//
// It must be called without holding the lock: it performs I/O. Newly resolved
// entries are written back to the cache under a short-lived write lock at the
// end.
func (r *Registry) resolveBranches(ctx context.Context, sessions []musem.Session) map[string]string {
	out := make(map[string]string, len(sessions))
	var missing []string

	now := r.now()

	r.mu.RLock()
	for _, s := range sessions {
		if s.Dir == "" {
			continue
		}
		if _, done := out[s.Dir]; done {
			continue
		}
		e, cached := r.branchByDir[s.Dir]
		if cached && now.Sub(e.resolvedAt) < r.branchTTL {
			out[s.Dir] = e.branch
			continue
		}
		// An expired entry still seeds the result with the name it held, so a
		// git that was busy for a moment does not blank a column the user
		// reads. That grace is bounded: past it the name is old enough that
		// showing it as though it were current would be the worse lie, and an
		// empty branch at least admits ignorance.
		if now.Sub(e.resolvedAt) < r.branchTTL*branchStaleGrace {
			out[s.Dir] = e.branch
		} else {
			out[s.Dir] = ""
		}
		missing = append(missing, s.Dir)
	}
	r.mu.RUnlock()

	if len(missing) == 0 {
		return out
	}

	resolved := make(map[string]branchEntry, len(missing))
	for _, dir := range missing {
		branch, err := r.branches.Branch(ctx, dir)
		if err != nil {
			// A branch that cannot be resolved is shown as absent, and is not
			// cached so a transient failure can be retried. It is a label, not
			// a fact the user acts on, and failing the whole refresh over it
			// would be wildly disproportionate.
			continue
		}
		resolved[dir] = branchEntry{branch: branch, resolvedAt: now}
		out[dir] = branch
	}

	r.mu.Lock()
	for dir, e := range resolved {
		r.branchByDir[dir] = e
	}
	r.mu.Unlock()

	return out
}

// Snapshot returns the current inventory, ordered with the sessions that demand
// attention first and ties broken by name so the list does not jitter between
// refreshes.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sessions := make([]musem.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		// A session that has ended sorts below every live one, whatever its
		// status says.
		//
		// StatusEnded already ranks last on its own, so this guard is not about
		// the ordinary case. It is about the session the source itself called
		// dead before it disappeared: that verdict is preserved rather than
		// softened to ended, and StatusDead outranks running — so without this,
		// an afternoon of sessions that failed and went away buries the one
		// session actually working behind a wall of finished ones. Nothing that
		// has stopped is competing for attention with something that has not.
		if a.Ended() != b.Ended() {
			return b.Ended()
		}
		if a.Status.Urgency() != b.Status.Urgency() {
			return a.Status.Urgency() < b.Status.Urgency()
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})

	snap := Snapshot{Sessions: sessions, UpdatedAt: r.updatedAt, Skipped: r.skipped}

	if r.lastErr != nil {
		snap.ErrCode = musem.ErrorCode(r.lastErr)
		snap.ErrMessage = musem.ErrorMessage(r.lastErr)
	}
	// One reading of the clock decides both the flag and the age, so the two can
	// never disagree about the same snapshot.
	if r.updatedAt.IsZero() {
		snap.Stale = true
	} else if age := r.now().Sub(r.updatedAt); age > r.staleness {
		snap.Stale, snap.Age = true, age
	}

	return snap
}
