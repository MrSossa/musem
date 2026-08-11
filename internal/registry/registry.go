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
	Discover(ctx context.Context) ([]musem.Session, error)
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

	// ErrCode and ErrMessage describe why the last refresh failed, empty when
	// it succeeded. Carried as an application code so the UI decides the
	// presentation and the adapter never has to.
	ErrCode    string
	ErrMessage string
}

// Registry keeps the known sessions up to date.
type Registry struct {
	discoverer Discoverer
	branches   BranchResolver

	interval  time.Duration
	staleness time.Duration
	now       func() time.Time

	mu sync.RWMutex
	// sessions is keyed by the session's own stable identifier. Titles and
	// paths are user-editable and would collide or churn as keys.
	sessions    map[string]musem.Session
	branchByDir map[string]string
	updatedAt   time.Time
	lastErr     error
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
		now:         time.Now,
		sessions:    make(map[string]musem.Session),
		branchByDir: make(map[string]string),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run refreshes on an interval until ctx is cancelled, sending a Snapshot after
// every attempt.
//
// The next wait starts only after a refresh returns, so two discovery calls can
// never overlap — the property is structural rather than something a guard has
// to enforce. A slow source therefore stretches the interval instead of piling
// up work behind it.
func (r *Registry) Run(ctx context.Context, out chan<- Snapshot) {
	for {
		r.Refresh(ctx)

		select {
		case out <- r.Snapshot():
		case <-ctx.Done():
			return
		}

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
	found, err := r.discoverer.Discover(ctx)
	if err != nil {
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		return
	}

	// Branches are resolved before the lock is taken. Resolution shells out to
	// git, and holding the write lock across that would block every reader for
	// as long as the slowest directory takes to answer — which the UI would
	// experience as a freeze, since it reads a snapshot every frame.
	branches := r.resolveBranches(ctx, found)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.lastErr = nil

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

		if prev, ok := r.sessions[s.ID]; ok {
			// A session that reappears is no longer ended, and it keeps the
			// identity it had — a rename changes the label, not the session.
			s.EndedAt = nil
			if s.Started.IsZero() {
				s.Started = prev.Started
			}
		}
		r.sessions[s.ID] = s
	}

	// Anything that stopped appearing has ended. It is marked and kept, never
	// dropped: its accumulated cost has to remain reachable.
	for id, s := range r.sessions {
		if seen[id] || s.Ended() {
			continue
		}
		endedAt := s.LastSeen
		if endedAt.IsZero() {
			endedAt = now
		}
		s.EndedAt = &endedAt
		s.Status = musem.StatusDead
		r.sessions[id] = s
	}

	r.updatedAt = now
}

// resolveBranches returns the branch for every distinct directory in sessions,
// consulting the cache first and looking up only what is missing.
//
// It must be called without holding the lock: it performs I/O. Newly resolved
// entries are written back to the cache under a short-lived write lock at the
// end.
func (r *Registry) resolveBranches(ctx context.Context, sessions []musem.Session) map[string]string {
	out := make(map[string]string, len(sessions))
	var missing []string

	r.mu.RLock()
	for _, s := range sessions {
		if s.Dir == "" {
			continue
		}
		if _, done := out[s.Dir]; done {
			continue
		}
		if branch, ok := r.branchByDir[s.Dir]; ok {
			out[s.Dir] = branch
			continue
		}
		out[s.Dir] = ""
		missing = append(missing, s.Dir)
	}
	r.mu.RUnlock()

	if len(missing) == 0 {
		return out
	}

	resolved := make(map[string]string, len(missing))
	for _, dir := range missing {
		branch, err := r.branches.Branch(ctx, dir)
		if err != nil {
			// A branch that cannot be resolved is shown as absent, and is not
			// cached so a transient failure can be retried. It is a label, not
			// a fact the user acts on, and failing the whole refresh over it
			// would be wildly disproportionate.
			continue
		}
		resolved[dir] = branch
		out[dir] = branch
	}

	r.mu.Lock()
	for dir, branch := range resolved {
		r.branchByDir[dir] = branch
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
		if a.Status.Urgency() != b.Status.Urgency() {
			return a.Status.Urgency() < b.Status.Urgency()
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})

	snap := Snapshot{Sessions: sessions, UpdatedAt: r.updatedAt}

	if r.lastErr != nil {
		snap.ErrCode = musem.ErrorCode(r.lastErr)
		snap.ErrMessage = musem.ErrorMessage(r.lastErr)
	}
	if r.updatedAt.IsZero() || r.now().Sub(r.updatedAt) > r.staleness {
		snap.Stale = true
	}

	return snap
}
