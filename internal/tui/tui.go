// Package tui renders the dashboard.
//
// It receives snapshots and draws them. It fetches nothing, calls no adapter,
// and decides nothing about what is true — the core decides what is true, the
// UI decides how it looks. The test for anything that lands here: if a CLI
// front end would have to copy it, it does not belong in this package.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	runewidth "github.com/mattn/go-runewidth"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/app"
)

// SnapshotMsg carries a new snapshot into the update loop.
type SnapshotMsg struct{ Snapshot app.Snapshot }

// Model is the dashboard state.
type Model struct {
	snapshot app.Snapshot
	cursor   int
	width    int
	height   int
	showHelp bool
	detail   bool
}

// NewModel returns an empty dashboard.
func NewModel() Model { return Model{} }

// Init satisfies tea.Model; the snapshot pump drives everything.
func (m Model) Init() tea.Cmd { return nil }

// Update folds one message into the model. It is the only place the model
// changes — every source pushes messages here rather than mutating state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case SnapshotMsg:
		m.snapshot = msg.Snapshot
		// The list reorders as sessions change status, so the cursor is clamped
		// rather than left pointing past the end.
		if m.cursor >= len(m.snapshot.Rows) {
			m.cursor = maxInt(0, len(m.snapshot.Rows)-1)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Unconditional, and deliberately not grouped with the keys below.
		// Ctrl-C means "stop this program" everywhere else in a terminal; a
		// pane that swallows it leaves the user pressing it harder at something
		// that has decided it means "close a pane".
		return m, tea.Quit
	case "q", "esc":
		if m.detail || m.showHelp {
			m.detail, m.showHelp = false, false
			return m, nil
		}
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "j", "down":
		if m.cursor < len(m.snapshot.Rows)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = maxInt(0, len(m.snapshot.Rows)-1)
	case "enter":
		if len(m.snapshot.Rows) > 0 {
			m.detail = !m.detail
		}
	}
	return m, nil
}

// Styles. Colours are chosen for meaning: amber demands attention, red is a
// failure, dim is background information.
var (
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleWaiting = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleDead    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleStale   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Italic(true)
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleCursor  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
)

// statusStyle maps a domain status to how it is drawn. Which statuses exist and
// which is more urgent are domain facts; the colour is presentation.
func statusStyle(s musem.Status) lipgloss.Style {
	switch s {
	case musem.StatusWaiting:
		return styleWaiting
	case musem.StatusRunning:
		return styleRunning
	case musem.StatusDead:
		return styleDead
	default:
		return styleDim
	}
}

// errorMessage turns an application error code into something a user can act
// on. The adapter reports what went wrong; this decides how it reads.
func errorMessage(code, message string) string {
	switch code {
	case "":
		return ""
	case musem.EUNAVAILABLE:
		return "Source unavailable — " + message
	case musem.ENOTFOUND:
		return "Not found — " + message
	case musem.EUNPARSEABLE:
		return "Unrecognised data — " + message
	case musem.EUNKNOWNMODEL:
		return "Unpriced model — " + message
	default:
		return message
	}
}

// column describes one table column and how readily it is dropped when the
// terminal is too narrow. Higher priority survives longer.
type column struct {
	title    string
	width    int
	priority int
}

var columns = []column{
	{title: "STATUS", width: 14, priority: 5},
	{title: "SESSION", width: 20, priority: 4},
	{title: "BRANCH", width: 18, priority: 2},
	{title: "DIRECTORY", width: 32, priority: 1},
	{title: "COST", width: 10, priority: 3},
}

// visibleColumns drops the lowest-priority columns until the table fits,
// degrading in a defined order rather than truncating unpredictably.
func visibleColumns(width int) []column {
	visible := append([]column(nil), columns...)

	for len(visible) > 1 {
		total := 2 // cursor gutter
		for _, c := range visible {
			total += c.width + 1
		}
		if total <= width {
			break
		}

		lowest, at := visible[0].priority, 0
		for i, c := range visible {
			if c.priority < lowest {
				lowest, at = c.priority, i
			}
		}
		visible = append(visible[:at], visible[at+1:]...)
	}
	return visible
}

// pad fits s to exactly width terminal cells, so emoji and wide characters do
// not break alignment.
//
// Truncation is followed by padding rather than trusted to land on the mark: a
// double-width glyph at the cut can leave the result a cell short, and one
// missing cell shifts every column after it.
func pad(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) > width {
		s = runewidth.Truncate(s, width, "…")
	}
	if gap := width - runewidth.StringWidth(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// View renders the dashboard.
func (m Model) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}

	header := m.renderHeader(width)
	footer := styleDim.Render("\n  ?  help   q  quit\n")

	var b strings.Builder
	b.WriteString(header)

	switch {
	case m.showHelp:
		b.WriteString(renderHelp())
	case len(m.snapshot.Rows) == 0:
		b.WriteString(m.renderEmpty())
	case m.detail:
		b.WriteString(m.renderDetail())
	default:
		// Whatever the header and footer did not take, less the one line the
		// column titles occupy. A non-positive result means the terminal is too
		// short to be reasoned about, and renderTable falls back to drawing
		// everything rather than nothing.
		b.WriteString(m.renderTable(width, m.height-lineCount(header)-lineCount(footer)-1))
	}

	b.WriteString(footer)
	return b.String()
}

// lineCount reports how many terminal lines s occupies. Every fragment here
// ends in a newline, so counting newlines counts lines.
func lineCount(s string) int { return strings.Count(s, "\n") }

func (m Model) renderHeader(width int) string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("  musem"))

	fleet := m.snapshot.Fleet
	total := fleet.Cost.String()
	if fleet.Partial() {
		// A partial total must never look like a complete one.
		total += " (partial)"
	}
	b.WriteString(styleDim.Render(fmt.Sprintf("   %d sessions   %s total\n", len(m.snapshot.Rows), total)))

	if m.snapshot.Stale {
		b.WriteString(styleStale.Render("  ⚠ data is stale — the last refresh did not complete\n"))
	}
	if msg := errorMessage(m.snapshot.ErrCode, m.snapshot.ErrMessage); msg != "" {
		b.WriteString(styleErr.Render("  ✕ " + pad(msg, maxInt(0, width-4)) + "\n"))
	}
	b.WriteString("\n")
	return b.String()
}

func (m Model) renderEmpty() string {
	return styleDim.Render(
		"  No agent sessions are running.\n\n" +
			"  musem observes sessions started elsewhere; it does not start them.\n" +
			"  Open one and it will appear here on the next refresh.\n")
}

// renderTable draws at most maxRows lines of sessions, scrolled so the cursor
// is always among them. A maxRows of zero or less means no limit.
//
// Without this the table draws every row it has and lets the terminal scroll
// the top of the screen away — taking the fleet total, the staleness warning
// and, when the cursor is far enough down, the selection indicator with it. A
// dashboard whose whole purpose is surfacing the session waiting on you cannot
// be one that hides which session is selected.
func (m Model) renderTable(width, maxRows int) string {
	visible := visibleColumns(width)
	rows := m.snapshot.Rows

	start, end := 0, len(rows)
	if maxRows > 0 && len(rows) > maxRows {
		// One line goes to the position indicator, so it is never a surprise
		// that the list continues past the edge of the screen.
		window := maxInt(1, maxRows-1)
		start = minInt(maxInt(0, m.cursor-window/2), len(rows)-window)
		end = start + window
	}

	var b strings.Builder
	b.WriteString("  ")
	for _, c := range visible {
		b.WriteString(styleHeader.Render(pad(c.title, c.width)) + " ")
	}
	b.WriteString("\n")

	for i := start; i < end; i++ {
		row := rows[i]
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("▸ ")
		}
		b.WriteString(cursor)

		for _, c := range visible {
			cell := m.cell(row, c.title)
			padded := pad(cell, c.width)
			if c.title == "STATUS" {
				padded = statusStyle(row.Session.Status).Render(padded)
			}
			b.WriteString(padded + " ")
		}
		b.WriteString("\n")
	}

	if end-start < len(rows) {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ↕ %d–%d of %d\n", start+1, end, len(rows))))
	}
	return b.String()
}

func (m Model) cell(row Row, title string) string {
	switch title {
	case "STATUS":
		return statusSymbol(row.Session.Status) + " " + string(row.Session.Status)
	case "SESSION":
		if row.Session.Name == "" {
			return row.Session.ID
		}
		return row.Session.Name
	case "BRANCH":
		if row.Session.Branch == "" {
			return "—"
		}
		return row.Session.Branch
	case "DIRECTORY":
		return row.Session.Dir
	case "COST":
		cost := row.Cost.String()
		if row.Partial {
			cost += "*"
		}
		if row.Degraded {
			// Distinct from the partial marker on purpose. A starred figure was
			// counted but could not be priced; a flagged one was never counted
			// at all, and is therefore too low rather than incomplete.
			cost += "!"
		}
		return cost
	}
	return ""
}

// Row aliases the composed row so the view can be read without hopping
// packages; the shape is owned by app.
type Row = app.Row

func statusSymbol(s musem.Status) string {
	switch s {
	case musem.StatusRunning:
		return "●"
	case musem.StatusWaiting:
		return "◐"
	case musem.StatusIdle:
		return "○"
	case musem.StatusDead:
		return "✕"
	default:
		return "?"
	}
}

func (m Model) renderDetail() string {
	if m.cursor >= len(m.snapshot.Rows) {
		return ""
	}
	row := m.snapshot.Rows[m.cursor]
	s := row.Session

	lines := [][2]string{
		{"Session", s.Name},
		{"ID", s.ID},
		{"Status", string(s.Status)},
		{"Directory", s.Dir},
		{"Branch", orDash(s.Branch)},
		{"Cost", row.Cost.String()},
		{"Started", formatTime(s.Started)},
		{"Last seen", formatTime(s.LastSeen)},
	}
	if s.Ended() {
		lines = append(lines, [2]string{"Ended", formatTime(*s.EndedAt)})
	}
	if row.Partial {
		lines = append(lines, [2]string{"Note", "some usage could not be priced; tokens counted, cost incomplete"})
	}
	if row.Degraded {
		lines = append(lines, [2]string{"Note", "some records could not be read; this figure understates the true cost"})
	}

	var b strings.Builder
	for _, l := range lines {
		b.WriteString("  " + styleDim.Render(pad(l[0], 12)) + " " + l[1] + "\n")
	}
	b.WriteString(styleDim.Render("\n  enter  back\n"))
	return b.String()
}

func renderHelp() string {
	rows := [][2]string{
		{"j / ↓", "next session"},
		{"k / ↑", "previous session"},
		{"g / G", "first / last"},
		{"enter", "session detail"},
		{"?", "toggle this help"},
		{"q / esc", "close, or quit"},
		{"ctrl-c", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  " + styleHeader.Render(pad(r[0], 10)) + " " + styleDim.Render(r[1]) + "\n")
	}

	b.WriteString("\n")
	for _, r := range [][2]string{
		{"$0.00*", "counted, but some of it could not be priced"},
		{"$0.00!", "some records could not be read; the figure is too low"},
	} {
		b.WriteString("  " + styleHeader.Render(pad(r[0], 10)) + " " + styleDim.Render(r[1]) + "\n")
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
