package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/app"
	"github.com/MrSossa/musem/internal/claude"
	"github.com/MrSossa/musem/internal/cost"
	"github.com/MrSossa/musem/internal/inmem"
	"github.com/MrSossa/musem/internal/registry"
	"github.com/MrSossa/musem/internal/sqlite"
	"github.com/MrSossa/musem/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func sizeMsg(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

// wire builds the same graph main.go builds, with the in-memory adapters
// standing in for the real ones. It is the end-to-end check that the pieces
// actually fit: ports, composition, and rendering in one pass.
func wire(t *testing.T) (*app.Composer, *inmem.Discoverer) {
	t.Helper()

	discoverer := inmem.NewDiscoverer()
	accountant := cost.New(cost.NewRateTable(), inmem.NewUsageReader(), nil)
	reg := registry.New(discoverer, inmem.BranchResolver{})

	reg.Refresh(context.Background())
	composer := app.New(reg, accountant)
	composer.Refresh(context.Background())

	return composer, discoverer
}

func TestComposedSnapshotJoinsSessionsAndCost(t *testing.T) {
	composer, _ := wire(t)
	snap := composer.Snapshot()

	if len(snap.Rows) == 0 {
		t.Fatal("no rows composed")
	}

	var priced int
	for _, row := range snap.Rows {
		if amount, known := row.Cost.Amount(); known && amount > 0 {
			priced++
		}
	}
	if priced == 0 {
		t.Error("no row carried a cost; the join between sessions and accounting is not happening")
	}

	if amount, known := snap.Fleet.Cost.Amount(); !known || amount <= 0 {
		t.Errorf("fleet total = %v (known=%v), want a positive figure", amount, known)
	}
}

// The registry orders live sessions by urgency and puts everything that has
// ended below them; the composer must not disturb either part.
func TestComposerPreservesUrgencyOrdering(t *testing.T) {
	composer, _ := wire(t)
	rows := composer.Snapshot().Rows

	if len(rows) < 2 {
		t.Skip("not enough rows to check ordering")
	}
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1].Session, rows[i].Session
		if prev.Ended() && !cur.Ended() {
			t.Fatalf("row %d has ended and sorts above row %d, which is still live", i-1, i)
		}
		if prev.Ended() != cur.Ended() {
			continue
		}
		if prev.Status.Urgency() > cur.Status.Urgency() {
			t.Fatalf("row %d (%s) sorts after row %d (%s)",
				i-1, prev.Status, i, cur.Status)
		}
	}
	if rows[0].Session.Status != musem.StatusWaiting {
		t.Errorf("first row is %q; a waiting session should lead", rows[0].Session.Status)
	}
	if last := rows[len(rows)-1].Session; !last.Ended() {
		t.Errorf("last row is %q and has not ended; finished sessions belong at the bottom", last.Status)
	}
}

// The whole graph renders: adapters through orchestration through composition
// to a frame of text.
func TestGraphRendersAFrame(t *testing.T) {
	composer, _ := wire(t)

	m := tui.NewModel()
	next, _ := m.Update(tui.SnapshotMsg{Snapshot: composer.Snapshot()})
	model := next.(tui.Model)
	sized, _ := model.Update(sizeMsg(140, 40))

	view := sized.(tui.Model).View()

	for _, want := range []string{"musem", "STATUS", "SESSION", "api", "waiting", "$"} {
		if !strings.Contains(view, want) {
			t.Errorf("rendered frame is missing %q\n---\n%s", want, view)
		}
	}
}

// A session that disappears is kept and marked, and its cost stays reachable.
func TestEndedSessionKeepsItsCost(t *testing.T) {
	discoverer := inmem.NewDiscoverer()
	accountant := cost.New(cost.NewRateTable(), inmem.NewUsageReader(), nil)
	reg := registry.New(discoverer, inmem.BranchResolver{})
	ctx := context.Background()

	reg.Refresh(ctx)
	composer := app.New(reg, accountant)
	composer.Refresh(ctx)

	before := len(composer.Snapshot().Rows)
	if before == 0 {
		t.Fatal("no sessions to begin with")
	}

	discoverer.SetSessions(nil)
	reg.Refresh(ctx)

	after := composer.Snapshot()
	if len(after.Rows) != before {
		t.Errorf("got %d rows after every session vanished, want the same %d", len(after.Rows), before)
	}
	for _, row := range after.Rows {
		if !row.Session.Ended() {
			t.Errorf("session %s should be marked ended", row.Session.ID)
		}
	}
	if amount, known := after.Fleet.Cost.Amount(); !known || amount <= 0 {
		t.Error("accumulated cost must survive the sessions ending")
	}
}

// countingReader records how often each session is read, so a refresh loop that
// keeps working on sessions that stopped changing shows up as a number.
type countingReader struct {
	reads map[string]int
}

func (c *countingReader) ReadUsage(_ context.Context, sessionID, cursor string) (musem.UsageReading, error) {
	if c.reads == nil {
		c.reads = make(map[string]int)
	}
	c.reads[sessionID]++
	if cursor != "" {
		return musem.UsageReading{Cursor: cursor}, nil
	}
	return musem.UsageReading{
		Entries: []musem.ModelUsage{{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 10}}},
		Cursor:  "done",
	}, nil
}

// The registry never drops a session, so without a stop the refresh loop grows
// for as long as musem runs and spends every pass re-reading records that
// stopped changing hours ago. An ended session is read once more — to catch
// anything written just before it vanished — and then left alone.
func TestEndedSessionsAreReadOnceMoreAndThenLeftAlone(t *testing.T) {
	discoverer := inmem.NewDiscoverer()
	reader := &countingReader{}
	accountant := cost.New(cost.NewRateTable(), reader, nil)
	reg := registry.New(discoverer, inmem.BranchResolver{})
	ctx := context.Background()

	reg.Refresh(ctx)
	composer := app.New(reg, accountant)
	composer.Refresh(ctx)

	var id string
	for _, row := range composer.Snapshot().Rows {
		id = row.Session.ID
		break
	}
	if id == "" {
		t.Fatal("no sessions to begin with")
	}
	live := reader.reads[id]

	discoverer.SetSessions(nil)
	reg.Refresh(ctx)

	composer.Refresh(ctx) // the final pass, which must happen
	if reader.reads[id] != live+1 {
		t.Fatalf("reads = %d, want one final pass after the session ended", reader.reads[id])
	}

	for i := 0; i < 5; i++ {
		composer.Refresh(ctx)
	}
	if reader.reads[id] != live+1 {
		t.Errorf("reads = %d after five further refreshes, want no further reads", reader.reads[id])
	}
}

// The original defect lived in the seam between the real reader and the real
// store: totals were persisted, the reader's position was not, so the first
// pass after startup added the whole transcript to the total it had just
// restored — and did it again on every launch. The fakes above cannot show
// that; only the two real adapters together can.
func TestRestartDoesNotRecountWithTheRealAdapters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	projects := filepath.Join(dir, "projects", "repo")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projects, "s1.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1000000,"output_tokens":0}}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "musem.db")

	// Each launch builds the graph from scratch, exactly as main.go does.
	launch := func() musem.SessionCost {
		t.Helper()

		store, err := sqlite.Open(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()

		reader := claude.NewUsageReader()
		reader.ProjectsDir = filepath.Join(dir, "projects")

		accountant := cost.New(cost.NewRateTable(), reader, store)
		if err := accountant.Restore(ctx); err != nil {
			t.Fatal(err)
		}
		if err := accountant.Update(ctx, "s1"); err != nil {
			t.Fatal(err)
		}

		sc, ok := accountant.Session("s1")
		if !ok {
			t.Fatal("session not accounted for")
		}
		return sc
	}

	first := launch()
	if first.Usage.InputTokens != 1_000_000 {
		t.Fatalf("InputTokens = %d on the first launch, want 1000000", first.Usage.InputTokens)
	}

	for i := 2; i <= 4; i++ {
		got := launch()
		if got.Usage.InputTokens != 1_000_000 {
			t.Errorf("InputTokens = %d on launch %d, want the unchanged 1000000", got.Usage.InputTokens, i)
		}
		amount, known := got.Cost.Amount()
		if !known {
			t.Fatalf("launch %d: cost should be known", i)
		}
		if amount < 4.99 || amount > 5.01 {
			t.Errorf("cost = %.2f on launch %d, want the unchanged 5.00", amount, i)
		}
	}
}

// controllableReader reports usage for a session and can be made to fail, so a
// final read can be forced to fail exactly when a session ends.
type controllableReader struct {
	mu     sync.Mutex
	fail   bool
	reads  map[string]int
	failed map[string]int
}

func newControllableReader() *controllableReader {
	return &controllableReader{reads: make(map[string]int), failed: make(map[string]int)}
}

// failCount reports how many reads were attempted while the reader was failing.
func (r *controllableReader) failCount(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed[sessionID]
}

func (r *controllableReader) ReadUsage(_ context.Context, sessionID, cursor string) (musem.UsageReading, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fail {
		r.failed[sessionID]++
		return musem.UsageReading{}, musem.Errorf(musem.ENOTFOUND, "no transcript for %s", sessionID)
	}
	r.reads[sessionID]++

	// Each successful read reports a little more usage, so a read that was
	// skipped is visible as a total that failed to move.
	return musem.UsageReading{
		Entries: []musem.ModelUsage{{
			Model: "claude-opus-5",
			Usage: musem.Usage{OutputTokens: 1_000_000},
		}},
		Cursor: strconv.Itoa(r.reads[sessionID]),
	}, nil
}

func (r *controllableReader) readCount(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads[sessionID]
}

func (r *controllableReader) setFail(fail bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = fail
}

// wireWith builds the same graph as wire around a caller-supplied reader and
// discoverer, so a test can drive both ends.
func wireWith(d *inmem.Discoverer, reader cost.UsageReader) (*app.Composer, *registry.Registry) {
	reg := registry.New(d, inmem.BranchResolver{})
	return app.New(reg, cost.New(cost.NewRateTable(), reader, nil)), reg
}

func live(id string) []musem.Session {
	return []musem.Session{{ID: id, Name: id, Dir: "/p/" + id, Status: musem.StatusRunning}}
}

// An ended session gets one last read. If that read fails it has told us
// nothing, and settling on it abandons the session's final usage for good.
func TestFailedFinalReadIsRetried(t *testing.T) {
	ctx := context.Background()
	d := inmem.NewDiscoverer()
	d.SetSessions(live("s1"))

	reader := newControllableReader()
	composer, reg := wireWith(d, reader)

	reg.Refresh(ctx)
	composer.Refresh(ctx)

	// The session ends, and its final read fails — the transcript has not been
	// written yet, or the filesystem was briefly unavailable.
	d.SetSessions(nil)
	reg.Refresh(ctx)
	reader.setFail(true)
	composer.Refresh(ctx)

	before := reader.readCount("s1")
	reader.setFail(false)
	composer.Refresh(ctx)

	if reader.readCount("s1") == before {
		t.Error("a session settled on a failed read is never read again, so its last usage is lost")
	}
}

// A session that comes back is live again, and its second ending deserves the
// same final read as its first.
func TestResumedSessionIsSettledAgainOnItsSecondEnding(t *testing.T) {
	ctx := context.Background()
	d := inmem.NewDiscoverer()
	d.SetSessions(live("s1"))

	reader := newControllableReader()
	composer, reg := wireWith(d, reader)

	reg.Refresh(ctx)
	composer.Refresh(ctx)

	// It ends, and is settled.
	d.SetSessions(nil)
	reg.Refresh(ctx)
	composer.Refresh(ctx)
	settled := reader.readCount("s1")

	// Settling has to hold while it stays gone.
	composer.Refresh(ctx)
	if reader.readCount("s1") != settled {
		t.Fatal("a settled session must not be re-read on every pass")
	}

	// claude --resume: the same session appears again.
	d.SetSessions(live("s1"))
	reg.Refresh(ctx)
	composer.Refresh(ctx)

	// And ends a second time.
	d.SetSessions(nil)
	reg.Refresh(ctx)
	resumed := reader.readCount("s1")
	composer.Refresh(ctx)

	if reader.readCount("s1") == resumed {
		t.Error("the second ending skipped its final read: settling was never cleared when the session came back")
	}
}

// A session the accountant has no record of is one whose usage could not be
// read. Rendering that as $0.00 is a plausible wrong number, which is the one
// outcome the whole design is built to avoid.
func TestSessionWithNoAccountingIsNotShownAsFree(t *testing.T) {
	ctx := context.Background()
	d := inmem.NewDiscoverer()
	d.SetSessions(live("s1"))

	reader := newControllableReader()
	reader.setFail(true)
	composer, reg := wireWith(d, reader)

	reg.Refresh(ctx)
	composer.Refresh(ctx)

	rows := composer.Snapshot().Rows
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if amount, known := rows[0].Cost.Amount(); known {
		t.Errorf("cost = $%.2f (known), want unknown: an unreadable session must not read as free", amount)
	}
	if got := rows[0].Cost.String(); got != "—" {
		t.Errorf("cost renders as %q, want an em dash", got)
	}
}

// The row and the total have to agree. A session with no accounting renders as
// an em dash; a fleet total that quietly leaves it out while printing a
// confident figure is the two halves of the screen contradicting each other.
func TestFleetTotalAdmitsSessionsItCouldNotAccountFor(t *testing.T) {
	ctx := context.Background()
	d := inmem.NewDiscoverer()
	d.SetSessions(live("s1"))

	reader := newControllableReader()
	reader.setFail(true)
	composer, reg := wireWith(d, reader)

	reg.Refresh(ctx)
	composer.Refresh(ctx)

	snap := composer.Snapshot()
	if _, known := snap.Fleet.Cost.Amount(); !known {
		t.Error("the fleet total went unknown rather than reporting what it is missing")
	}
	if !snap.Fleet.Partial() {
		t.Error("a total missing a whole session must be flagged as partial")
	}
	if snap.Fleet.Unrecorded != 1 {
		t.Errorf("unrecorded = %d, want 1", snap.Fleet.Unrecorded)
	}

	// Once the session can be read, the total becomes a real figure again.
	reader.setFail(false)
	composer.Refresh(ctx)

	snap = composer.Snapshot()
	if amount, known := snap.Fleet.Cost.Amount(); !known || amount <= 0 {
		t.Errorf("fleet total = %v (known=%v), want a positive figure once the session is accounted for", amount, known)
	}
	if snap.Fleet.Partial() {
		t.Error("a fully accounted fleet must not be flagged as partial")
	}
}

// A failed final read is retried, but not forever: the registry never drops a
// session, and a transcript that was deleted never becomes readable, so each
// pass would otherwise walk every project directory looking for it again.
//
// The bound is wall-clock time rather than a count of passes, because the
// reader answers most passes from its own memory of the failed search without
// going to look — so a budget of attempts would be spent in seconds without the
// transcript having had any real chance to appear.
func TestFinalReadRetriesAreBoundedByTime(t *testing.T) {
	ctx := context.Background()
	d := inmem.NewDiscoverer()
	d.SetSessions(live("s1"))

	now := time.Now()
	reader := newControllableReader()
	reg := registry.New(d, inmem.BranchResolver{})
	composer := app.New(reg, cost.New(cost.NewRateTable(), reader, nil),
		app.WithClock(func() time.Time { return now }))

	reg.Refresh(ctx)
	composer.Refresh(ctx)

	// It ends, and its transcript can never be read again.
	d.SetSessions(nil)
	reg.Refresh(ctx)
	reader.setFail(true)

	// Many passes inside the grace: the session is still worth chasing, because
	// a transcript that is merely late has not had time to appear.
	for i := 0; i < 20; i++ {
		composer.Refresh(ctx)
	}
	// Twenty passes inside the grace means twenty attempts: a budget spent
	// after two or three would be gone before a late transcript could appear.
	if got := reader.failCount("s1"); got < 20 {
		t.Errorf("chased %d times over 20 passes inside the grace; a late transcript is given up on too early", got)
	}

	// Past the grace it is settled, and chasing stops for good.
	now = now.Add(2 * time.Hour)
	composer.Refresh(ctx)
	settled := reader.failCount("s1")

	for i := 0; i < 20; i++ {
		composer.Refresh(ctx)
	}
	if got := reader.failCount("s1"); got != settled {
		t.Errorf("an unreadable ended session was chased %d more times after the grace expired", got-settled)
	}
}

// The count has to survive the whole path — adapter, registry, composer, view —
// because every hop it does not cross is a hop where an incomplete inventory
// starts looking like a complete one.
func TestUnreadRecordsReachTheView(t *testing.T) {
	discoverer := inmem.NewDiscoverer()
	discoverer.Skipped = 3
	reg := registry.New(discoverer, inmem.BranchResolver{})
	reg.Refresh(context.Background())

	composer := app.New(reg, cost.New(cost.NewRateTable(), inmem.NewUsageReader(), nil))
	snap := composer.Snapshot()
	if snap.Undiscovered != 3 {
		t.Fatalf("Undiscovered = %d, want 3", snap.Undiscovered)
	}

	model, _ := tui.NewModel().Update(tui.SnapshotMsg{Snapshot: snap})
	if view := model.View(); !strings.Contains(view, "could not be read") {
		t.Errorf("the view does not report the records that were dropped:\n%s", view)
	}
}

// fakeReclamations is a source of worktrees kept when their session ended.
type fakeReclamations struct{ notices []musem.Reclamation }

func (f fakeReclamations) Notices() []musem.Reclamation { return f.notices }

// R9: the reason a worktree was kept has to reach the view, and the view reads
// composed snapshots. Joining it here rather than in the UI is what keeps a
// second front end from having to repeat the join.
func TestKeptWorktreesReachTheSnapshot(t *testing.T) {
	d := inmem.NewDiscoverer()
	d.SetSessions(live("a"))
	reg := registry.New(d, inmem.BranchResolver{})
	reg.Refresh(context.Background())

	kept := []musem.Reclamation{
		{SessionID: "a", Path: "/r/api-musem-session-1", Reason: "it has uncommitted changes"},
	}
	composer := app.New(reg, cost.New(cost.NewRateTable(), inmem.NewUsageReader(), nil),
		app.WithReclamations(fakeReclamations{notices: kept}))

	snap := composer.Snapshot()
	if len(snap.Kept) != 1 {
		t.Fatalf("Kept = %+v, want the one worktree that survived", snap.Kept)
	}
	if snap.Kept[0].Reason != "it has uncommitted changes" {
		t.Errorf("reason = %q, want it carried through", snap.Kept[0].Reason)
	}
}

// A musem that cannot launch has no reclamations, and the snapshot is the same
// otherwise rather than being unbuildable.
func TestASnapshotWithoutAReclamationSourceIsFine(t *testing.T) {
	d := inmem.NewDiscoverer()
	d.SetSessions(live("a"))
	reg := registry.New(d, inmem.BranchResolver{})
	reg.Refresh(context.Background())

	snap := app.New(reg, cost.New(cost.NewRateTable(), inmem.NewUsageReader(), nil)).Snapshot()
	if len(snap.Kept) != 0 {
		t.Errorf("Kept = %+v, want none", snap.Kept)
	}
	if len(snap.Rows) != 1 {
		t.Errorf("rows = %d, want the session still there", len(snap.Rows))
	}
}
