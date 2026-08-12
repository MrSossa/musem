package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	runewidth "github.com/mattn/go-runewidth"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/app"
	"github.com/MrSossa/musem/internal/cost"
)

// ansi matches the escape sequences lipgloss emits when it decides the output
// supports colour. They occupy no cells on screen, so measuring a rendered line
// has to discount them.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

func snapshot(rows ...app.Row) app.Snapshot {
	return app.Snapshot{Rows: rows, Fleet: cost.Fleet{Cost: musem.USD(0)}}
}

func row(id, name string, st musem.Status) app.Row {
	return app.Row{
		Session: musem.Session{ID: id, Name: name, Dir: "/p/" + name, Status: st, LastSeen: time.Now()},
		Cost:    musem.USD(1.5),
	}
}

func withSnapshot(m Model, s app.Snapshot) Model {
	next, _ := m.Update(SnapshotMsg{Snapshot: s})
	return next.(Model)
}

func press(m Model, key string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return next.(Model)
}

func TestEmptyStateExplains(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot())
	view := m.View()

	if !strings.Contains(view, "No agent sessions are running") {
		t.Error("an empty dashboard must explain itself, not show a blank table")
	}
	if !strings.Contains(view, "does not start them") {
		t.Error("the empty state should say what musem does and does not do")
	}
}

func TestTableShowsEveryColumn(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(row("a", "api", musem.StatusWaiting)))
	m.width = 120
	view := m.View()

	for _, want := range []string{"STATUS", "SESSION", "BRANCH", "DIRECTORY", "COST", "api", "$1.50"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}
}

// Staleness must be visible, not implied by an absence.
func TestStaleDataIsMarked(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Stale = true

	view := withSnapshot(NewModel(), s).View()
	if !strings.Contains(view, "stale") {
		t.Error("stale data must be marked visibly")
	}
}

// The spec requires the marker to say since when. "Stale" alone tells the user
// their figures are wrong without telling them how wrong.
func TestStaleMarkerSaysHowOldTheDataIs(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"seconds", 42 * time.Second, "42s ago"},
		{"minutes", 9 * time.Minute, "9m ago"},
		{"hours", 3 * time.Hour, "3h ago"},
		// Under a second is still an age. "0s ago" would read as a claim that
		// the data is current, which is what the line exists to deny.
		{"sub-second", 200 * time.Millisecond, "1s ago"},
		// Just short of the next unit stays in this one. Rounding would print
		// "60s" and "60m", which name the next unit up in the units of the one
		// below.
		{"just under a minute", 59700 * time.Millisecond, "59s ago"},
		{"just under an hour", 59*time.Minute + 40*time.Second, "59m ago"},
		{"just under a day", 23*time.Hour + 50*time.Minute, "23h ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := snapshot(row("a", "api", musem.StatusIdle))
			s.Stale, s.StaleFor = true, tt.age

			view := stripANSI(withSnapshot(NewModel(), s).View())
			if !strings.Contains(view, tt.want) {
				t.Errorf("stale marker should contain %q, got:\n%s", tt.want, view)
			}
		})
	}
}

// A snapshot that has never refreshed is a different statement from one that
// has fallen behind, and must not be reported as "0s ago".
func TestStaleMarkerBeforeAnyRefresh(t *testing.T) {
	s := snapshot()
	s.Stale, s.StaleFor = true, 0

	view := stripANSI(withSnapshot(NewModel(), s).View())
	if !strings.Contains(view, "no refresh has completed yet") {
		t.Errorf("a never-refreshed snapshot must say so, got:\n%s", view)
	}
	if strings.Contains(view, "ago") {
		t.Errorf("no age should be claimed before the first refresh, got:\n%s", view)
	}
}

// An error code becomes an actionable sentence here, not in the adapter.
func TestErrorCodeIsRenderedAsGuidance(t *testing.T) {
	s := snapshot()
	s.ErrCode = musem.EUNAVAILABLE
	s.ErrMessage = "the Claude CLI was not found on PATH"

	view := withSnapshot(NewModel(), s).View()
	if !strings.Contains(view, "Source unavailable") {
		t.Error("EUNAVAILABLE should render as a source-unavailable message")
	}
	if !strings.Contains(view, "not found on PATH") {
		t.Error("the underlying reason must survive to the screen")
	}
}

// A partial total must never look like a complete one.
func TestPartialFleetTotalIsFlagged(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Fleet = cost.Fleet{Cost: musem.UnknownCost(), UnknownModels: []string{"claude-from-the-future"}}

	view := withSnapshot(NewModel(), s).View()
	if !strings.Contains(view, "partial") {
		t.Error("a partial fleet total must be labelled")
	}
}

func TestKeyboardNavigation(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(
		row("a", "one", musem.StatusWaiting),
		row("b", "two", musem.StatusRunning),
		row("c", "three", musem.StatusIdle),
	))

	if m.cursor != 0 {
		t.Fatalf("cursor starts at %d, want 0", m.cursor)
	}
	m = press(m, "j")
	m = press(m, "j")
	if m.cursor != 2 {
		t.Errorf("cursor = %d after two downs, want 2", m.cursor)
	}
	m = press(m, "j") // past the end
	if m.cursor != 2 {
		t.Errorf("cursor = %d, must not move past the last row", m.cursor)
	}
	m = press(m, "k")
	if m.cursor != 1 {
		t.Errorf("cursor = %d after up, want 1", m.cursor)
	}
	m = press(m, "g")
	if m.cursor != 0 {
		t.Errorf("cursor = %d after g, want 0", m.cursor)
	}
}

// The list reorders as sessions change status, so the cursor must never be left
// pointing past the end.
func TestCursorIsClampedWhenTheListShrinks(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(
		row("a", "one", musem.StatusIdle),
		row("b", "two", musem.StatusIdle),
		row("c", "three", musem.StatusIdle),
	))
	m = press(m, "j")
	m = press(m, "j")

	m = withSnapshot(m, snapshot(row("a", "one", musem.StatusIdle)))
	if m.cursor != 0 {
		t.Errorf("cursor = %d after the list shrank, want 0", m.cursor)
	}
	_ = m.View() // must not panic
}

func TestHelpIsDiscoverable(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(row("a", "api", musem.StatusIdle)))
	if !strings.Contains(m.View(), "?  help") {
		t.Error("the footer should advertise help")
	}

	m = press(m, "?")
	view := m.View()
	for _, want := range []string{"next session", "session detail", "quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("help is missing %q", want)
		}
	}
}

func TestQuitEmitsQuitCommand(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot())
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q must produce a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q must quit, restoring the terminal")
	}
}

// Narrow terminals drop the lowest-priority columns rather than truncating
// unpredictably, and the highest-priority ones always survive.
func TestNarrowTerminalDropsColumnsByPriority(t *testing.T) {
	wide := visibleColumns(200)
	if len(wide) != len(columns) {
		t.Errorf("a wide terminal shows %d columns, want all %d", len(wide), len(columns))
	}

	narrow := visibleColumns(40)
	if len(narrow) >= len(columns) {
		t.Fatalf("a narrow terminal still shows %d columns", len(narrow))
	}

	titles := make(map[string]bool)
	for _, c := range narrow {
		titles[c.title] = true
	}
	if !titles["STATUS"] {
		t.Error("STATUS is the highest-priority column and must survive")
	}
	if titles["DIRECTORY"] {
		t.Error("DIRECTORY is the lowest-priority column and should be dropped first")
	}
}

// Wide characters must not break alignment. The invariant is about terminal
// cells, not runes or bytes: a CJK glyph and an emoji each occupy two cells, so
// counting runes would misalign every column after them.
func TestPadUsesTerminalCellWidth(t *testing.T) {
	for _, s := range []string{"abc", "日本語", "🚀 ship", "", "señor"} {
		for _, width := range []int{4, 8, 12} {
			got := pad(s, width)
			if w := runewidth.StringWidth(got); w != width {
				t.Errorf("pad(%q, %d) occupies %d cells, want %d (got %q)", s, width, w, width, got)
			}
		}
	}

	if got := pad("abcdefgh", 4); got != "abc…" {
		t.Errorf("truncation = %q, want %q", got, "abc…")
	}
	if got := pad("abc", 0); got != "" {
		t.Errorf("pad to zero = %q, want empty", got)
	}
}

// Resizing must re-adapt without corrupting content.
func TestLiveResize(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(row("a", "api", musem.StatusIdle)))

	for _, width := range []int{200, 100, 40, 20, 120} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = next.(Model)
		view := m.View()
		if view == "" {
			t.Fatalf("empty view at width %d", width)
		}
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "\x00") {
				t.Errorf("corrupted line at width %d", width)
			}
		}
	}
}

// The dashboard observes and nothing else. This scans the package for calls
// that could change a session or the filesystem — a rule that is otherwise only
// a promise in the design document.
func TestDashboardHasNoMutatingOperations(t *testing.T) {
	forbidden := map[string]string{
		"exec":  "running a process",
		"os":    "touching the filesystem",
		"Kill":  "signalling a process",
		"Write": "writing",
	}

	// The sources are listed and parsed one by one rather than with
	// parser.ParseDir, which is deprecated: it associates files with packages
	// without consulting build tags, so what it hands back is a guess about
	// which files are even part of the build. Nothing here needs the grouping
	// into packages — every file that is not a test is scanned the same way —
	// so reading the directory directly costs a loop and removes the guess.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	scanned := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++

		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if reason, bad := forbidden[path]; bad {
				t.Errorf("%s imports %q (%s); the dashboard is read-only", name, path, reason)
			}
			if strings.HasPrefix(path, "os/exec") || path == "os" {
				t.Errorf("%s imports %q; the dashboard is read-only", name, path)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Remove", "RemoveAll", "WriteFile", "Create", "Kill":
				t.Errorf("%s calls %s; the dashboard must not alter anything", name, sel.Sel.Name)
			}
			return true
		})
	}

	// A scan that matched nothing would pass without having looked at anything,
	// which is the one result this test must not be able to report.
	if scanned == 0 {
		t.Fatal("no sources were scanned; this check cannot vouch for anything")
	}
}

// Ctrl-C means "stop this program" everywhere else in a terminal. A pane that
// swallows it leaves the user pressing it harder at something that has decided
// it means "close a pane".
func TestCtrlCQuitsEvenWithAPaneOpen(t *testing.T) {
	base := withSnapshot(NewModel(), snapshot(row("a", "api", musem.StatusIdle)))

	for _, tc := range []struct {
		name string
		open func(Model) Model
	}{
		{"nothing open", func(m Model) Model { return m }},
		{"help open", func(m Model) Model { return press(m, "?") }},
		{"detail open", func(m Model) Model {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			return next.(Model)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.open(base)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if cmd == nil {
				t.Fatal("ctrl-c must produce a command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Error("ctrl-c must quit whatever is on screen")
			}
		})
	}
}

// Drawing every row regardless of height scrolls the header, the fleet total
// and the selection indicator out of the alt-screen buffer — on a dashboard
// whose whole purpose is surfacing the session that is waiting on you.
func TestTableFitsTheTerminalAndFollowsTheCursor(t *testing.T) {
	rows := make([]app.Row, 0, 30)
	for i := 0; i < 30; i++ {
		rows = append(rows, row(string(rune('a'+i%26))+string(rune('0'+i/26)), "svc"+string(rune('a'+i%26)), musem.StatusIdle))
	}

	m := withSnapshot(NewModel(), snapshot(rows...))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = next.(Model)

	// Measured the way bubbletea measures it: the view is split on newlines and
	// the last m.height elements are kept. A view ending in a newline splits
	// into one more element than it has lines, so filling the height exactly
	// still costs the first line.
	if got := len(strings.Split(m.View(), "\n")); got > 24 {
		t.Errorf("the view splits into %d lines in a 24-line terminal; the first will be dropped", got)
	}

	// The fleet total lives in the header and must survive.
	if !strings.Contains(m.View(), "30 sessions") {
		t.Error("the header scrolled away")
	}

	// The selection indicator has to stay on screen wherever the cursor goes.
	for _, key := range []string{"G", "g"} {
		m = press(m, key)
		if !strings.Contains(m.View(), "▸") {
			t.Errorf("the cursor is off screen after %q", key)
		}
		if got := lineCount(m.View()); got > 24 {
			t.Errorf("the view is %d lines after %q, want at most 24", got, key)
		}
	}

	m = press(m, "G")
	if !strings.Contains(m.View(), "of 30") {
		t.Error("a scrolled list must say that it continues past the screen")
	}
}

// A terminal whose height is unknown must still draw something.
func TestTableWithoutAKnownHeightDrawsEveryRow(t *testing.T) {
	rows := make([]app.Row, 0, 12)
	for i := 0; i < 12; i++ {
		rows = append(rows, row(string(rune('a'+i)), "svc"+string(rune('a'+i)), musem.StatusIdle))
	}

	m := withSnapshot(NewModel(), snapshot(rows...))
	view := m.View()
	for i := 0; i < 12; i++ {
		if !strings.Contains(view, "svc"+string(rune('a'+i))) {
			t.Fatalf("row %d is missing when the height is unknown", i)
		}
	}
}

// A figure that was never counted is not the same as one that could not be
// priced, and the difference has to be visible.
func TestDegradedCostIsMarkedDistinctlyFromPartial(t *testing.T) {
	degraded := row("a", "api", musem.StatusIdle)
	degraded.Degraded = true
	partial := row("b", "web", musem.StatusIdle)
	partial.Partial = true

	view := withSnapshot(NewModel(), snapshot(degraded, partial)).View()

	if !strings.Contains(view, "$1.50!") {
		t.Error("a cost missing unreadable records must be flagged")
	}
	if !strings.Contains(view, "$1.50*") {
		t.Error("an unpriced cost must keep its own marker")
	}
}

// A total missing usage that was never counted is too low, and says so. This is
// the aggregate of the per-row "!" marker, which existed before the header did.
func TestDegradedFleetTotalIsFlagged(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Fleet = cost.Fleet{Cost: musem.USD(12.34), Skipped: 3}

	view := withSnapshot(NewModel(), s).View()
	if !strings.Contains(view, "understated") {
		t.Error("a total with unread records behind it must be labelled, not printed as if complete")
	}
}

// View charges the header by its newlines, so a header line long enough to wrap
// takes a terminal row nobody accounted for — and the table, given a row too
// many, scrolls the total off the top of the screen it sits on.
func TestHeaderLinesNeverExceedTheTerminalWidth(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	// The widest header musem can produce: both markers and a large figure.
	s.Fleet = cost.Fleet{
		Cost:          musem.USD(123456.78),
		UnknownModels: []string{"claude-from-the-future"},
		Skipped:       4,
	}
	s.Stale = true

	for _, width := range []int{20, 30, 40, 60, 80} {
		m := withSnapshot(NewModel(), s)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(Model)

		header := m.renderHeader(width)
		for i, line := range strings.Split(strings.TrimSuffix(header, "\n"), "\n") {
			if got := runewidth.StringWidth(stripANSI(line)); got > width {
				t.Errorf("width %d: header line %d is %d cells wide and will wrap: %q",
					width, i, got, stripANSI(line))
			}
		}
	}
}

// bubbletea keeps the last m.height lines of the view and discards the rest
// from the top, so a view one line too tall loses its first line — which here
// is the fleet total, the one figure the windowing exists to keep on screen.
func TestViewLeavesRoomForTheLineBubbleteaDrops(t *testing.T) {
	for _, height := range []int{10, 24, 40} {
		rows := make([]app.Row, 0, 60)
		for i := 0; i < 60; i++ {
			rows = append(rows, row("s", "session", musem.StatusIdle))
		}

		m := withSnapshot(NewModel(), snapshot(rows...))
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
		m = next.(Model)

		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > height {
			t.Errorf("height %d: view splits into %d lines, so bubbletea drops %q",
				height, len(lines), stripANSI(lines[0]))
			continue
		}
		if !strings.Contains(stripANSI(lines[0]), "musem") {
			t.Errorf("height %d: first line is %q, want the header", height, stripANSI(lines[0]))
		}
	}
}

// The position indicator costs a line. At a budget of one there is no line to
// spend on it, and taking one anyway pushes the table past what View allowed —
// costing the header instead, which is the bug the budget exists to prevent.
func TestVeryShortTerminalStillKeepsTheHeader(t *testing.T) {
	rows := make([]app.Row, 0, 10)
	for i := 0; i < 10; i++ {
		rows = append(rows, row("s", "session", musem.StatusIdle))
	}

	// Seven is the shortest terminal that can hold the frame at all: the header
	// takes two lines, the column titles one, the footer two, and bubbletea
	// wants one in hand. Below that nothing fits and the clamp only bounds how
	// badly it does not.
	for height := 7; height <= 14; height++ {
		m := withSnapshot(NewModel(), snapshot(rows...))
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
		m = next.(Model)

		lines := strings.Split(m.View(), "\n")
		if len(lines) > height {
			t.Errorf("height %d: view splits into %d lines, so bubbletea drops %q",
				height, len(lines), stripANSI(lines[0]))
		}
	}
}

// Naming the unpriced models is the whole justification for carrying the list
// through the domain, the accountant and the store; if it never reaches the
// screen the gap is not actionable.
func TestUnpricedModelsAreNamedInTheDetailPane(t *testing.T) {
	r := row("a", "api", musem.StatusIdle)
	r.Cost = musem.UnknownCost()
	r.Partial = true
	r.UnknownModels = []string{"claude-from-the-future"}

	m := withSnapshot(NewModel(), snapshot(r))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = next.(Model)
	view := press(m, "enter").View()

	if !strings.Contains(view, "claude-from-the-future") {
		t.Error("the detail pane says the cost is incomplete but never says which rate is missing")
	}
}

// The help, empty and detail panes are drawn without regard to height. An
// overlay that overflows costs the header exactly as a long table does —
// bubbletea keeps the last m.height lines whatever produced them.
func TestOverlayPanesAreClippedToTheTerminal(t *testing.T) {
	long := make([]app.Row, 0, 10)
	for i := 0; i < 10; i++ {
		long = append(long, row("s", "session", musem.StatusIdle))
	}

	panes := map[string]func(Model) Model{
		"help":   func(m Model) Model { return press(m, "?") },
		"detail": func(m Model) Model { return press(m, "enter") },
	}

	for name, open := range panes {
		for _, height := range []int{8, 10, 14, 20} {
			m := withSnapshot(NewModel(), snapshot(long...))
			next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
			m = open(next.(Model))

			lines := strings.Split(m.View(), "\n")
			if len(lines) > height {
				t.Errorf("%s pane at height %d: view splits into %d lines, so bubbletea drops %q",
					name, height, len(lines), stripANSI(lines[0]))
			}
		}
	}
}

// The empty state has the same obligation, and reaches it by a different path.
func TestEmptyStateIsClippedToTheTerminal(t *testing.T) {
	for _, height := range []int{7, 8, 10} {
		m := withSnapshot(NewModel(), snapshot())
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
		m = next.(Model)

		lines := strings.Split(m.View(), "\n")
		if len(lines) > height {
			t.Errorf("empty state at height %d: view splits into %d lines", height, len(lines))
		}
	}
}

// The em dash of an unknown cost and the staleness warning are East-Asian
// ambiguous: measured as one cell here, drawn as two by some terminals. A
// header line that fills the width exactly then wraps onto a row nobody
// budgeted for, and bubbletea drops the fleet total off the top.
func TestHeaderLeavesRoomForAmbiguousWidthGlyphs(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Fleet = cost.Fleet{Cost: musem.UnknownCost(), Unrecorded: 1, Skipped: 2}
	s.Stale = true

	wide := runewidth.NewCondition()
	wide.EastAsianWidth = true // a terminal that draws the ambiguous glyphs at two cells

	for _, width := range []int{30, 40, 60, 80} {
		m := withSnapshot(NewModel(), s)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(Model)

		for i, line := range strings.Split(strings.TrimSuffix(m.renderHeader(width), "\n"), "\n") {
			if got := wide.StringWidth(stripANSI(line)); got > width {
				t.Errorf("width %d: header line %d draws %d cells on a terminal that widens ambiguous glyphs: %q",
					width, i, got, stripANSI(line))
			}
		}
	}
}

// Every status has to be readable in full, and indeterminate most of all: it is
// the one whose entire purpose is admitting that musem does not know.
func TestEveryStatusFitsItsColumn(t *testing.T) {
	var status column
	for _, c := range columns {
		if c.title == "STATUS" {
			status = c
		}
	}
	if status.title == "" {
		t.Fatal("no STATUS column")
	}

	for _, s := range []musem.Status{
		musem.StatusRunning, musem.StatusWaiting, musem.StatusIdle,
		musem.StatusDead, musem.StatusEnded, musem.StatusIndeterminate,
	} {
		m := Model{}
		cell := m.cell(Row{Session: musem.Session{Status: s}}, "STATUS")
		if got := runewidth.StringWidth(cell); got > status.width {
			t.Errorf("status %q needs %d cells but the column is %d: it renders as %q",
				s, got, status.width, pad(cell, status.width))
		}
	}
}

// A total missing whole sessions must say how many. Sessions are never dropped
// from the inventory, so one session that ended before its transcript could be
// found would otherwise hold the headline figure at an em dash for the rest of
// the run — however many priced sessions came and went.
func TestUnaccountedSessionsAreCountedInTheHeader(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Fleet = cost.Fleet{Cost: musem.USD(12.34), Unrecorded: 2}

	view := withSnapshot(NewModel(), s).View()
	if !strings.Contains(view, "$12.34") {
		t.Error("the figure that is known must still be shown")
	}
	if !strings.Contains(view, "2 unaccounted") {
		t.Error("a total missing whole sessions must say how many")
	}
}

// The table must fit terminals too narrow for even one column. A row that
// overflows wraps, which doubles its height, blows the budget View computed
// from a line count, and scrolls the fleet total away.
func TestTableFitsAVeryNarrowTerminal(t *testing.T) {
	rows := []app.Row{row("a", "api", musem.StatusIdle), row("b", "web", musem.StatusRunning)}

	for width := 6; width <= 20; width++ {
		m := withSnapshot(NewModel(), snapshot(rows...))
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(Model)

		for i, line := range strings.Split(m.renderTable(width, 10), "\n") {
			if got := runewidth.StringWidth(stripANSI(line)); got > width {
				t.Errorf("width %d: table line %d is %d cells and wraps: %q",
					width, i, got, stripANSI(line))
			}
		}
	}
}

// lipgloss turns a trailing newline into a padded blank line and drops the
// newline itself, so a fragment ending inside Render occupies one more terminal
// row than lineCount reports — and every budget built on that count is wrong.
func TestRenderedFragmentsDoNotHideALine(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)

	for name, fragment := range map[string]string{
		"footer": footerText(),
		"empty":  m.renderEmpty(),
		"help":   renderHelp(80),
	} {
		drawn := len(strings.Split(strings.TrimSuffix(fragment, "\n"), "\n"))
		if counted := lineCount(fragment); counted != drawn {
			t.Errorf("%s: lineCount says %d lines, it draws %d", name, counted, drawn)
		}
	}
}

// Overlay panes end in free text — a working directory, a note, a key
// description — and an unbounded line wraps onto a terminal row View never
// budgeted for, pushing the header and its fleet total off the top.
func TestOverlayPaneLinesNeverExceedTheTerminalWidth(t *testing.T) {
	r := row("a", "api", musem.StatusIdle)
	r.Session.Dir = "/home/dev/projects/" + strings.Repeat("deep/", 20) + "service"
	r.Cost = musem.UnknownCost()
	r.Partial = true
	r.Degraded = true
	r.UnknownModels = []string{"claude-from-the-future", "claude-also-unknown"}

	for _, width := range []int{20, 40, 60, 80, 120} {
		m := withSnapshot(NewModel(), snapshot(r))
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		m = next.(Model)

		panes := map[string]string{
			"detail": press(m, "enter").renderDetail(),
			"help":   renderHelp(width),
			"empty":  m.renderEmpty(),
		}
		for name, pane := range panes {
			for i, line := range strings.Split(strings.TrimSuffix(pane, "\n"), "\n") {
				if got := runewidth.StringWidth(stripANSI(line)); got > width {
					t.Errorf("%s at width %d: line %d is %d cells and wraps: %q",
						name, width, i, got, stripANSI(line))
				}
			}
		}
	}
}

// The layout test below proves the arithmetic holds when the measurement agrees
// with the terminal, but it pins the measurement itself to do so — so it cannot
// see the measurement being fixed to a constant again. This is what does.
//
// A constant is reproducible and, on the terminal that disagrees with it, wrong
// in the one direction that costs a line: cells padded to a width the terminal
// does not draw them at.
func TestWidthsAreMeasuredTheWayTheTerminalDrawsThem(t *testing.T) {
	if widths.EastAsianWidth != runewidth.IsEastAsian() {
		t.Errorf("ambiguous glyphs are measured as %d cells while the environment reports a terminal that draws them as %d",
			widths.StringWidth("—"), widestWidths.StringWidth("—"))
	}
}

// A terminal that draws East-Asian ambiguous glyphs at two cells is one musem
// has to fit, and this interface is made of them: the circle of a status, the
// em dash of a missing branch or an unknown cost, the ellipsis truncation
// appends, the cursor's own arrow. Measured as one cell there, every padded cell
// comes out over its column and the row wraps onto a line View never counted —
// which is what pushes the fleet total off the top of the screen.
func TestEveryLineFitsATerminalThatWidensAmbiguousGlyphs(t *testing.T) {
	wide := &runewidth.Condition{EastAsianWidth: true}
	restore := widths
	widths = wide // a terminal that draws the ambiguous glyphs at two cells
	t.Cleanup(func() { widths = restore })

	rows := []app.Row{
		row("a", "api", musem.StatusRunning),
		row("b", "web", musem.StatusWaiting),
		row("c", "worker", musem.StatusIdle),
		row("d", "batch", musem.StatusDead),
		row("e", "mystery", musem.StatusIndeterminate),
	}
	// Every ambiguous glyph the table can produce, on one row or another.
	rows[0].Session.Branch = "main"
	rows[1].Session.Dir = "/home/dev/" + strings.Repeat("deep/", 20) + "service"
	rows[2].Cost, rows[2].Partial = musem.UnknownCost(), true
	rows[3].Degraded = true
	rows[4].Session.Name = strings.Repeat("名", 30)

	s := snapshot(rows...)
	s.Fleet = cost.Fleet{Cost: musem.UnknownCost(), Priced: 12.5, Unrecorded: 1, Skipped: 3}
	s.Stale = true

	for _, width := range []int{20, 40, 69, 80, 102, 140} {
		for _, height := range []int{8, 12, 40} {
			m := withSnapshot(NewModel(), s)
			next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
			m = next.(Model)

			panes := map[string]string{
				"table":  m.View(),
				"help":   press(m, "?").View(),
				"detail": press(m, "enter").View(),
			}
			for name, pane := range panes {
				for i, line := range strings.Split(strings.TrimSuffix(pane, "\n"), "\n") {
					if got := wide.StringWidth(stripANSI(line)); got > width {
						t.Errorf("%s at %dx%d: line %d draws %d cells and wraps: %q",
							name, width, height, i, got, stripANSI(line))
					}
				}
			}
		}
	}
}

// One gap, one label. Partial is true of an unaccounted session too, so testing
// it beside the unaccounted count renders both and says less, not more.
func TestEachGapIsLabelledOnce(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Fleet = cost.Fleet{Cost: musem.USD(1.23), Unrecorded: 1}

	view := withSnapshot(NewModel(), s).View()
	if strings.Contains(view, "(partial)") {
		t.Error("an unaccounted session was labelled both '(partial)' and by count")
	}
	if !strings.Contains(view, "1 unaccounted") {
		t.Error("the count must still be shown")
	}

	// An unpriceable model is a different gap and keeps its own label.
	s.Fleet = cost.Fleet{Cost: musem.UnknownCost(), UnknownModels: []string{"claude-from-the-future"}}
	if view := withSnapshot(NewModel(), s).View(); !strings.Contains(view, "(partial)") {
		t.Error("usage that could not be priced must still be labelled partial")
	}
}

// The dollars that did price are carried through the domain and the store so
// they survive an unpriceable model. Never showing them wastes that.
func TestPricedDollarsAreShownAsAFloorWhenTheCostIsUnknown(t *testing.T) {
	r := row("a", "api", musem.StatusIdle)
	r.Cost = musem.UnknownCost()
	r.Partial = true
	r.Priced = 4.25
	r.UnknownModels = []string{"claude-from-the-future"}

	m := withSnapshot(NewModel(), snapshot(r))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := press(next.(Model), "enter").View()

	if !strings.Contains(view, "$4.25") {
		t.Error("an unknown cost showed nothing of the dollars that did price")
	}
}

// The legend must describe a rendering that can actually occur: a partial row's
// cost is always unknown, so it renders as an em dash and never as a figure.
func TestHelpLegendMatchesWhatRowsRender(t *testing.T) {
	r := row("a", "api", musem.StatusIdle)
	r.Cost = musem.UnknownCost()
	r.Partial = true

	m := Model{}
	rendered := m.cell(r, "COST")

	legend := renderHelp(120)
	if !strings.Contains(legend, rendered) {
		t.Errorf("a partial row renders its cost as %q, which the legend does not describe", rendered)
	}
}

// The indicator is a fixed legend rather than a padded column, so nothing else
// bounds it — and a wrapped indicator costs the same header row a wrapped
// session row would.
func TestScrollIndicatorFitsANarrowTerminal(t *testing.T) {
	rows := make([]app.Row, 0, 40)
	for i := 0; i < 40; i++ {
		rows = append(rows, row("s", "session", musem.StatusIdle))
	}

	for width := 6; width <= 24; width++ {
		m := withSnapshot(NewModel(), snapshot(rows...))
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(Model)

		// A budget small enough that the window cannot hold every row, so the
		// indicator branch is actually entered — the gap a sweep with more
		// budget than rows never reaches.
		table := m.renderTable(width, 6)
		if !strings.Contains(stripANSI(table), "↕") {
			t.Fatalf("width %d: the indicator branch was not exercised", width)
		}
		for i, line := range strings.Split(strings.TrimSuffix(table, "\n"), "\n") {
			if got := runewidth.StringWidth(stripANSI(line)); got > width {
				t.Errorf("width %d: table line %d is %d cells and wraps: %q",
					width, i, got, stripANSI(line))
			}
		}
	}
}

// A summary must not be less informative than the list it summarises: one
// unpriceable session would otherwise discard every priced dollar above it.
func TestFleetHeaderShowsAFloorWhenTheTotalIsUnknown(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Fleet = cost.Fleet{
		Cost:          musem.UnknownCost(),
		Priced:        412,
		UnknownModels: []string{"claude-from-the-future"},
	}

	view := withSnapshot(NewModel(), s).View()
	if !strings.Contains(view, "$412.00") {
		t.Error("the headline discarded every priced dollar because one session could not be priced")
	}
	if !strings.Contains(view, "at least") {
		t.Error("a floor must be labelled as one, not shown as if it were the total")
	}
	if !strings.Contains(view, "(partial)") {
		t.Error("a floor is still a partial figure and must say so")
	}
}

// Sessions dropped before they could be read have no row to be marked in, so
// the count goes above the table. Without it a source whose record shape changed
// empties the list, and every session left in the registry reads as one that
// ended — a screen full of confident, wrong statuses.
func TestUnreadableSessionRecordsAreAnnounced(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Undiscovered = 2

	view := stripANSI(withSnapshot(NewModel(), s).View())
	if !strings.Contains(view, "2 session records could not be read") {
		t.Errorf("the dropped records were not announced:\n%s", view)
	}

	// And it says nothing when there is nothing to say: a warning that is always
	// on screen is one nobody reads.
	s.Undiscovered = 0
	if view := stripANSI(withSnapshot(NewModel(), s).View()); strings.Contains(view, "could not be read") {
		t.Errorf("a clean pass must not carry the warning:\n%s", view)
	}
}

// A status without its age is half the answer: waiting for four seconds is the
// loop working, waiting for ten minutes is a person blocked.
func TestDetailSaysHowLongTheStatusHasHeld(t *testing.T) {
	now := time.Now()
	r := row("a", "api", musem.StatusWaiting)
	r.Session.StatusSince = now.Add(-9 * time.Minute)

	m := Model{clock: func() time.Time { return now }}
	m = withSnapshot(m, snapshot(r))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	view := stripANSI(m.View())
	if !strings.Contains(view, "waiting for 9m") {
		t.Errorf("the detail pane does not say since when:\n%s", view)
	}
}

// A session musem has not yet timed shows the status alone rather than an age
// computed from a zero timestamp, which would claim it has been waiting since
// the year one.
func TestDetailOmitsTheAgeWhenItIsNotKnown(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(row("a", "api", musem.StatusWaiting)))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	if view := stripANSI(m.View()); strings.Contains(view, "waiting for") {
		t.Errorf("an unknown age must not be rendered:\n%s", view)
	}
}
