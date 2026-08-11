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
