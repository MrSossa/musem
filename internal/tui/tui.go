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
	case "q", "ctrl+c", "esc":
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

	var b strings.Builder
	b.WriteString(m.renderHeader(width))

	switch {
	case m.showHelp:
		b.WriteString(renderHelp())
	case len(m.snapshot.Rows) == 0:
		b.WriteString(m.renderEmpty())
	case m.detail:
		b.WriteString(m.renderDetail())
	default:
		b.WriteString(m.renderTable(width))
	}

	b.WriteString(styleDim.Render("\n  ?  help   q  quit\n"))
	return b.String()
}

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

func (m Model) renderTable(width int) string {
	visible := visibleColumns(width)

	var b strings.Builder
	b.WriteString("  ")
	for _, c := range visible {
		b.WriteString(styleHeader.Render(pad(c.title, c.width)) + " ")
	}
	b.WriteString("\n")

	for i, row := range m.snapshot.Rows {
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
		{"q / esc", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
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
