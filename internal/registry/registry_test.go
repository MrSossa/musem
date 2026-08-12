package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

// Doubles are three lines each because the ports are declared by the consumer
// and stay small. No mocking framework is involved anywhere in this file.

type fakeDiscoverer struct {
	sessions []musem.Session
	err      error
	calls    int
}

func (f *fakeDiscoverer) Discover(context.Context) ([]musem.Session, error) {
	f.calls++
	return f.sessions, f.err
}

type fakeBranches struct{ byDir map[string]string }

func (f fakeBranches) Branch(_ context.Context, dir string) (string, error) {
	return f.byDir[dir], nil
}

func session(id, name, dir string, st musem.Status) musem.Session {
	return musem.Session{ID: id, Name: name, Dir: dir, Status: st}
}

func TestRefreshBuildsInventory(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "alpha", "/p/alpha", musem.StatusIdle),
		session("b", "beta", "/p/beta", musem.StatusRunning),
	}}
	r := New(d, fakeBranches{byDir: map[string]string{"/p/alpha": "main"}})

	r.Refresh(context.Background())
	snap := r.Snapshot()

	if len(snap.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(snap.Sessions))
	}
	if snap.Stale {
		t.Error("a fresh refresh must not be stale")
	}
	if snap.ErrCode != "" {
		t.Errorf("unexpected error code %q", snap.ErrCode)
	}

	var alpha musem.Session
	for _, s := range snap.Sessions {
		if s.ID == "a" {
			alpha = s
		}
	}
	if alpha.Branch != "main" {
		t.Errorf("branch = %q, want main", alpha.Branch)
	}
}

// A directory outside a repository is normal, not an error.
func TestSessionWithoutRepository(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "alpha", "/tmp/scratch", musem.StatusIdle)}}
	r := New(d, fakeBranches{byDir: map[string]string{}})

	r.Refresh(context.Background())
	snap := r.Snapshot()

	if snap.Sessions[0].Branch != "" {
		t.Errorf("branch = %q, want empty", snap.Sessions[0].Branch)
	}
	if snap.ErrCode != "" {
		t.Errorf("a directory without a repository must not raise an error, got %q", snap.ErrCode)
	}
}

// Renaming a session must not create a second one: identity is the ID.
func TestRenameKeepsIdentity(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "old-name", "/p/alpha", musem.StatusIdle)}}
	r := New(d, fakeBranches{})

	r.Refresh(context.Background())
	d.sessions = []musem.Session{session("a", "new-name", "/p/alpha", musem.StatusIdle)}
	r.Refresh(context.Background())

	snap := r.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("got %d sessions after a rename, want 1", len(snap.Sessions))
	}
	if snap.Sessions[0].Name != "new-name" {
		t.Errorf("name = %q, want new-name", snap.Sessions[0].Name)
	}
}

// Two sessions in one directory are two sessions.
func TestSessionsSharingADirectory(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "one", "/p/shared", musem.StatusIdle),
		session("b", "two", "/p/shared", musem.StatusRunning),
	}}
	r := New(d, fakeBranches{})

	r.Refresh(context.Background())

	if got := len(r.Snapshot().Sessions); got != 2 {
		t.Fatalf("got %d sessions, want 2", got)
	}
}

// A session that stops appearing is marked ended and kept, never dropped.
func TestDisappearedSessionIsMarkedNotDropped(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "alpha", "/p/alpha", musem.StatusIdle),
		session("b", "beta", "/p/beta", musem.StatusRunning),
	}}
	r := New(d, fakeBranches{})
	r.Refresh(context.Background())

	d.sessions = []musem.Session{session("a", "alpha", "/p/alpha", musem.StatusIdle)}
	r.Refresh(context.Background())

	snap := r.Snapshot()
	if len(snap.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2 (the ended one must be kept)", len(snap.Sessions))
	}

	var beta musem.Session
	for _, s := range snap.Sessions {
		if s.ID == "b" {
			beta = s
		}
	}
	if !beta.Ended() {
		t.Error("the disappeared session must be marked as ended")
	}
	if beta.EndedAt.IsZero() {
		t.Error("EndedAt must record when it was last seen")
	}
}

// A session that comes back is no longer ended.
func TestReappearedSessionIsRevived(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "alpha", "/p/alpha", musem.StatusIdle)}}
	r := New(d, fakeBranches{})
	r.Refresh(context.Background())

	d.sessions = nil
	r.Refresh(context.Background())

	d.sessions = []musem.Session{session("a", "alpha", "/p/alpha", musem.StatusRunning)}
	r.Refresh(context.Background())

	snap := r.Snapshot()
	if snap.Sessions[0].Ended() {
		t.Error("a session that reappeared must not still be marked as ended")
	}
}

// A failing source must not empty the screen; it must degrade with a reason.
func TestUnavailableSourceKeepsLastKnownData(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "alpha", "/p/alpha", musem.StatusIdle)}}
	r := New(d, fakeBranches{})
	r.Refresh(context.Background())

	d.err = musem.Errorf(musem.EUNAVAILABLE, "the Claude CLI was not found on PATH")
	r.Refresh(context.Background())

	snap := r.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("last known data must survive a failed refresh, got %d sessions", len(snap.Sessions))
	}
	if snap.ErrCode != musem.EUNAVAILABLE {
		t.Errorf("ErrCode = %q, want %q", snap.ErrCode, musem.EUNAVAILABLE)
	}
	if snap.ErrMessage == "" {
		t.Error("the reason must be actionable, not empty")
	}
}

// An empty inventory from a missing tool is not a crash.
func TestMissingToolFromTheStart(t *testing.T) {
	d := &fakeDiscoverer{err: musem.Errorf(musem.EUNAVAILABLE, "not installed")}
	r := New(d, fakeBranches{})

	r.Refresh(context.Background())
	snap := r.Snapshot()

	if len(snap.Sessions) != 0 {
		t.Errorf("got %d sessions, want none", len(snap.Sessions))
	}
	if snap.ErrCode != musem.EUNAVAILABLE {
		t.Errorf("ErrCode = %q", snap.ErrCode)
	}
	if !snap.Stale {
		t.Error("data that was never fetched must not look current")
	}
}

// Data older than the staleness window must say so.
func TestSnapshotGoesStale(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "alpha", "/p/alpha", musem.StatusIdle)}}
	r := New(d, fakeBranches{}, WithStaleness(10*time.Second), WithClock(func() time.Time { return clock() }))

	r.Refresh(context.Background())
	if r.Snapshot().Stale {
		t.Fatal("data must not be stale immediately after a refresh")
	}

	now = now.Add(30 * time.Second)
	if !r.Snapshot().Stale {
		t.Error("data past the staleness window must be flagged")
	}
}

// A stale snapshot says how stale. Marking data old without saying how old
// leaves the user unable to tell a dashboard a moment behind from one that
// stopped refreshing an hour ago, and only one of those is worth acting on.
func TestStaleSnapshotReportsItsAge(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "alpha", "/p/alpha", musem.StatusIdle)}}
	r := New(d, fakeBranches{}, WithStaleness(10*time.Second), WithClock(func() time.Time { return clock() }))

	r.Refresh(context.Background())
	if got := r.Snapshot().Age; got != 0 {
		t.Errorf("fresh data reports no age, got %s", got)
	}

	now = now.Add(90 * time.Second)
	snap := r.Snapshot()
	if !snap.Stale {
		t.Fatal("data past the staleness window must be flagged")
	}
	if got, want := snap.Age, 90*time.Second; got != want {
		t.Errorf("Age = %s, want %s", got, want)
	}
}

// Before any refresh has completed there is no age to report, and inventing one
// from a zero timestamp would claim the data is decades old rather than absent.
func TestSnapshotBeforeAnyRefreshIsStaleWithNoAge(t *testing.T) {
	r := New(&fakeDiscoverer{}, fakeBranches{})

	snap := r.Snapshot()
	if !snap.Stale {
		t.Error("data that has never been refreshed is stale")
	}
	if snap.Age != 0 {
		t.Errorf("Age = %s, want zero when nothing has ever refreshed", snap.Age)
	}
}

// Waiting sessions come first: they are the ones blocked on a human.
func TestWaitingSessionsSortFirst(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "aaa", "/p/a", musem.StatusIdle),
		session("b", "bbb", "/p/b", musem.StatusRunning),
		session("c", "ccc", "/p/c", musem.StatusWaiting),
	}}
	r := New(d, fakeBranches{})
	r.Refresh(context.Background())

	if got := r.Snapshot().Sessions[0].ID; got != "c" {
		t.Errorf("first session = %q, want the waiting one", got)
	}
}

// Records that fail domain validation are dropped rather than stored malformed.
func TestInvalidRecordsAreDropped(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "alpha", "/p/alpha", musem.StatusIdle),
		{ID: "", Name: "no-id", Status: musem.StatusIdle},
		{ID: "bad-status", Name: "nope", Status: musem.Status("nonsense")},
	}}
	r := New(d, fakeBranches{})

	r.Refresh(context.Background())

	if got := len(r.Snapshot().Sessions); got != 1 {
		t.Errorf("got %d sessions, want 1 valid one", got)
	}
}

// The loop must not start a discovery call while one is in flight.
func TestRunDoesNotOverlapQueries(t *testing.T) {
	inFlight := make(chan struct{}, 1)
	overlapped := false

	d := &slowDiscoverer{onCall: func() {
		select {
		case inFlight <- struct{}{}:
		default:
			overlapped = true
		}
		time.Sleep(20 * time.Millisecond)
		<-inFlight
	}}

	r := New(d, fakeBranches{}, WithInterval(time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	out := make(chan Snapshot, 64)
	done := make(chan struct{})
	go func() { r.Run(ctx, out); close(done) }()

	// Drain so Run is never blocked on the channel.
	go func() {
		for range out {
		}
	}()

	<-done
	if overlapped {
		t.Error("two discovery calls were in flight at once")
	}
	if d.calls < 2 {
		t.Errorf("the loop ran %d times; it should have refreshed repeatedly", d.calls)
	}
}

// Branch resolution shells out and can be slow. A refresh must not hold the
// lock across it, or the UI — which reads a snapshot every frame — freezes for
// as long as the slowest directory takes to answer.
func TestSnapshotIsNotBlockedByBranchResolution(t *testing.T) {
	release := make(chan struct{})
	d := &fakeDiscoverer{sessions: []musem.Session{session("a", "alpha", "/p/slow", musem.StatusIdle)}}
	r := New(d, blockingBranches{release: release})

	refreshDone := make(chan struct{})
	go func() { r.Refresh(context.Background()); close(refreshDone) }()

	// While the refresh is stuck inside branch resolution, a reader must still
	// be served promptly.
	read := make(chan struct{})
	go func() { _ = r.Snapshot(); close(read) }()

	select {
	case <-read:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Snapshot blocked while a branch was being resolved")
	}

	close(release)
	<-refreshDone
}

type blockingBranches struct{ release chan struct{} }

func (b blockingBranches) Branch(context.Context, string) (string, error) {
	<-b.release
	return "main", nil
}

type slowDiscoverer struct {
	onCall func()
	calls  int
}

func (s *slowDiscoverer) Discover(context.Context) ([]musem.Session, error) {
	s.calls++
	s.onCall()
	return nil, errors.New("no sessions")
}

// countingBranches records how often it is asked, so the cache can be shown to
// be doing its job as well as expiring.
type countingBranches struct {
	byDir map[string]string
	calls int
}

func (c *countingBranches) Branch(_ context.Context, dir string) (string, error) {
	c.calls++
	return c.byDir[dir], nil
}

// Switching branch is among the most ordinary things to do in a working
// directory. A cached name that never expires would show the old branch for as
// long as musem runs.
func TestBranchCacheExpires(t *testing.T) {
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "alpha", "/p/alpha", musem.StatusIdle),
	}}
	b := &countingBranches{byDir: map[string]string{"/p/alpha": "feat/rate-limit"}}

	now := time.Now()
	r := New(d, b,
		WithBranchTTL(10*time.Second),
		WithClock(func() time.Time { return now }),
	)
	ctx := context.Background()

	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "feat/rate-limit" {
		t.Fatalf("branch = %q, want feat/rate-limit", got)
	}

	// Within the window the cache answers and git is left alone.
	r.Refresh(ctx)
	if b.calls != 1 {
		t.Errorf("resolver called %d times within the TTL, want 1", b.calls)
	}

	b.byDir["/p/alpha"] = "main"
	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "feat/rate-limit" {
		t.Errorf("branch = %q before the TTL elapsed, want the cached feat/rate-limit", got)
	}

	now = now.Add(11 * time.Second)
	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "main" {
		t.Errorf("branch = %q after the TTL elapsed, want main", got)
	}
}

// flakyBranches resolves a branch until it is told to fail, so a transient git
// failure can be aimed at exactly the moment the cache expires.
type flakyBranches struct {
	branch string
	fail   bool
}

func (f *flakyBranches) Branch(_ context.Context, _ string) (string, error) {
	if f.fail {
		return "", musem.Errorf(musem.EUNAVAILABLE, "git is busy")
	}
	return f.branch, nil
}

// The branch cache expires so a switched branch is noticed. When re-resolution
// fails, the last known name has to survive: a git that was busy for a moment
// must not blank a column the user reads.
func TestExpiredBranchSurvivesAFailedRefresh(t *testing.T) {
	ctx := context.Background()
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "alpha", "/p/alpha", musem.StatusIdle),
	}}
	branches := &flakyBranches{branch: "main"}

	now := time.Now()
	clock := func() time.Time { return now }
	r := New(d, branches, WithBranchTTL(10*time.Second), WithClock(clock))

	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}

	// The entry expires and the lookup that would replace it fails.
	now = now.Add(time.Minute)
	branches.fail = true
	r.Refresh(ctx)

	if got := r.Snapshot().Sessions[0].Branch; got != "main" {
		t.Errorf("branch = %q, want main: a transient failure blanked a branch that was known", got)
	}

	// Once git answers again the fresh name wins, including a different one.
	branches.fail = false
	branches.branch = "feat/x"
	now = now.Add(time.Minute)
	r.Refresh(ctx)

	if got := r.Snapshot().Sessions[0].Branch; got != "feat/x" {
		t.Errorf("branch = %q, want feat/x: a stale name outlived its replacement", got)
	}
}

// Keeping a branch through a transient failure is right; keeping it forever is
// not. Once the name is old enough that there is no reason left to believe it,
// showing it as though it were current is the one thing musem must not do.
func TestAStaleBranchIsEventuallyGivenUp(t *testing.T) {
	ctx := context.Background()
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("a", "alpha", "/p/alpha", musem.StatusIdle),
	}}
	branches := &flakyBranches{branch: "main"}

	now := time.Now()
	ttl := 10 * time.Second
	r := New(d, branches, WithBranchTTL(ttl), WithClock(func() time.Time { return now }))

	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}

	// git stops answering for good.
	branches.fail = true

	// Just past the TTL the name survives: one slow call is noise.
	now = now.Add(2 * ttl)
	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "main" {
		t.Errorf("branch = %q just past the TTL, want main: a transient failure must not blank it", got)
	}

	// Far past it the name is no longer worth believing.
	now = now.Add(ttl * branchStaleGrace)
	r.Refresh(ctx)
	if got := r.Snapshot().Sessions[0].Branch; got != "" {
		t.Errorf("branch = %q long after resolution stopped working, want empty: a stale name was presented as current", got)
	}
}

// An end the adapter reports itself is discovery stating a fact. The registry
// infers ends of its own from a session no longer appearing, and only that
// inference is withdrawn by seeing the session again.
func TestAnAdapterReportedEndIsNotWipedByRediscovery(t *testing.T) {
	ctx := context.Background()
	ended := time.Now().Add(-time.Hour)
	s := session("a", "alpha", "/p/alpha", musem.StatusDead)
	s.EndedAt = &ended

	d := &fakeDiscoverer{sessions: []musem.Session{s}}
	r := New(d, fakeBranches{byDir: map[string]string{"/p/alpha": "main"}})

	r.Refresh(ctx)
	r.Refresh(ctx) // the second pass is where the end used to be wiped

	got := r.Snapshot().Sessions[0]
	if !got.Ended() {
		t.Error("an end the adapter reported was discarded on rediscovery")
	}
}

// Ended sessions are marked dead and never dropped, and dead outranks running.
// Without ending sorting below everything live, an afternoon of short sessions
// buries the one session actually working.
func TestEndedSessionsSortBelowLiveOnes(t *testing.T) {
	ctx := context.Background()
	d := &fakeDiscoverer{sessions: []musem.Session{
		session("live", "zzz-running", "/p/a", musem.StatusRunning),
		session("gone", "aaa-doomed", "/p/b", musem.StatusIdle),
	}}
	r := New(d, fakeBranches{})

	r.Refresh(ctx)

	// The second session stops appearing, so the registry marks it dead.
	d.sessions = []musem.Session{session("live", "zzz-running", "/p/a", musem.StatusRunning)}
	r.Refresh(ctx)

	sessions := r.Snapshot().Sessions
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].ID != "live" {
		t.Errorf("first session is %q (%s); a session that has ended must not outrank a running one",
			sessions[0].ID, sessions[0].Status)
	}
	if !sessions[1].Ended() {
		t.Error("the ended session belongs at the bottom")
	}
}
