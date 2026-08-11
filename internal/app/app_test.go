package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/app"
	"github.com/MrSossa/musem/internal/cost"
	"github.com/MrSossa/musem/internal/inmem"
	"github.com/MrSossa/musem/internal/registry"
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

// The registry orders by urgency and the composer must not disturb it.
func TestComposerPreservesUrgencyOrdering(t *testing.T) {
	composer, _ := wire(t)
	rows := composer.Snapshot().Rows

	if len(rows) < 2 {
		t.Skip("not enough rows to check ordering")
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Session.Status.Urgency() > rows[i].Session.Status.Urgency() {
			t.Fatalf("row %d (%s) sorts after row %d (%s)",
				i-1, rows[i-1].Session.Status, i, rows[i].Session.Status)
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
