package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
