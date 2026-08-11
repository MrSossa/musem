package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	runewidth "github.com/mattn/go-runewidth"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/app"
	"github.com/MrSossa/musem/internal/cost"
)

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

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
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
	}
}
