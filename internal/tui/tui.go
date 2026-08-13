// Package tui renders the dashboard.
//
// It receives snapshots and draws them. It fetches nothing, calls no adapter,
// and decides nothing about what is true — the core decides what is true, the
// UI decides how it looks. The test for anything that lands here: if a CLI
// front end would have to copy it, it does not belong in this package.
//
// The launch form is the one place something can be caused to happen, and it
// causes it by describing a request and handing it to a launcher. This package
// cannot open a file or run a process — a test parses its sources and fails on
// either — so the write path it fronts is one it can only ask for.
package tui

import (
	"context"
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

	// form is the launch form while it is open, nil otherwise. It is the only
	// pointer in this struct, and it is one so that "no form" is a state rather
	// than a flag beside a value nobody should read.
	form *form

	// launcher is what the form calls. Nil disables launching entirely, which is
	// what a musem with no substrate or no history store runs as: observing
	// still works, and the key that would open the form does nothing.
	launcher Launcher

	// cwd is the directory the form starts from. It is passed in rather than
	// read here: this package cannot touch the filesystem, which is asserted by
	// a test.
	cwd string

	// ctx bounds the work the form starts, so a musem being shut down does not
	// leave a validation running.
	ctx context.Context

	// clock is read for the one age the view computes itself: how long a
	// session has held its status. Every other age arrives already measured, by
	// the registry that owns the timestamps behind it. Replaceable so a test can
	// assert the rendered age without waiting for one.
	clock func() time.Time
}

// Option configures a Model.
type Option func(*Model)

// WithLauncher enables launching from the dashboard.
func WithLauncher(l Launcher) Option {
	return func(m *Model) { m.launcher = l }
}

// WithWorkingDir sets the directory the launch form starts from.
func WithWorkingDir(dir string) Option {
	return func(m *Model) { m.cwd = dir }
}

// WithContext bounds the work the form starts.
func WithContext(ctx context.Context) Option {
	return func(m *Model) { m.ctx = ctx }
}

// NewModel returns an empty dashboard.
func NewModel(opts ...Option) Model {
	var m Model
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// now reads the model's clock, falling back to the real one. The fallback is
// what lets Model stay usable as its zero value, which is how NewModel and every
// test that builds a model by hand construct it.
func (m Model) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

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
			m.cursor = max(0, len(m.snapshot.Rows)-1)
		}
		return m, nil

	case tea.KeyMsg:
		if m.form != nil {
			next, cmd := m.handleFormKey(msg)
			return next, cmd
		}
		return m.handleKey(msg)
	}

	// Everything the form starts comes back here, and only here. The launch runs
	// in its own goroutine; nothing it touches is reachable from outside Update.
	if next, cmd, handled := m.updateForm(msg); handled {
		return next, cmd
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
		m.cursor = max(0, len(m.snapshot.Rows)-1)
	case "enter":
		if len(m.snapshot.Rows) > 0 {
			m.detail = !m.detail
		}
	case "n":
		// The one key in this package that can cause anything to be created, and
		// it causes a form rather than a launch: nothing happens until the form
		// is confirmed.
		return m.openForm()
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
	default:
		return message
	}
}

// staleNotice says that the data is old and how old it is.
//
// The age is the point of the line. "Stale" on its own tells the user their
// figures are wrong without telling them how far wrong, so a dashboard three
// seconds behind reads exactly like one abandoned an hour ago — and they call
// for entirely different reactions. A zero age means no refresh has ever
// completed, which is a different statement from a refresh that has fallen
// behind, and is worded as one.
func staleNotice(age time.Duration) string {
	if age <= 0 {
		return "  ⚠ data is stale — no refresh has completed yet"
	}
	return "  ⚠ data is stale — last refreshed " + humanAge(age) + " ago"
}

// humanAge renders a duration at the coarsest unit that still says something,
// because the exact second stopped mattering once the figure went stale.
//
// Truncated rather than rounded, so a figure always stays inside the unit it is
// labelled with. Rounding pushes 59.7s to "60s" and 59m40s to "60m", which name
// the next unit up in the units of the one below and read as arithmetic nobody
// finished. The floor at 1 is the other end of the same rule: an age below a
// second is still an age, and "0s ago" reads as a claim that the data is current
// — the one thing this line exists to deny.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(1, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
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
	// Wide enough for the longest status and its symbol: "? indeterminate" is
	// fifteen cells. A column that cannot show it in full would truncate the one
	// status whose whole purpose is admitting that musem does not know.
	{title: "STATUS", width: 15, priority: 5},
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

	// One column can still be wider than the terminal, because the loop above
	// stops before dropping the last one. A row that overflows wraps, which
	// doubles its height, blows the budget View computed from a line count, and
	// scrolls the fleet total away — so the survivor is clamped to what is left
	// after the cursor gutter and its trailing space.
	if len(visible) == 1 {
		visible[0].width = max(1, min(visible[0].width, width-3))
	}
	return visible
}

// widths measures text the way the terminal draws it, which is what the locale
// reports.
//
// Whether an East-Asian ambiguous glyph takes one cell or two is a property of
// the terminal, not of musem, and this interface is full of them: the circles of
// a status, the em dash of a missing branch, the ellipsis truncation appends.
// Fixing the answer to "one" would make every measurement here reproducible and,
// on a terminal that draws them at two, wrong — each padded cell would come out
// a cell over its column, and the row would overflow the width the layout was
// budgeted against and wrap onto a line View never counted. That costs the fleet
// total exactly as an unclipped header does.
//
// Padding is therefore exact on either kind of terminal, which is what keeps the
// columns lined up. It is a variable so a test can stand on the other kind
// without being run there.
var widths = &runewidth.Condition{EastAsianWidth: runewidth.IsEastAsian()}

// widestWidths measures the same text on the assumption that every ambiguous
// glyph is drawn at two cells.
//
// It is the measurement for lines where only the ceiling matters and being a
// cell short costs nothing: it can reserve too much room but never too little,
// so it holds even where the locale and the terminal disagree. Padded columns
// use the ordinary condition instead, because there a reserved cell nothing
// fills is a visible misalignment rather than a safety margin.
var widestWidths = &runewidth.Condition{EastAsianWidth: true}

// pad fits s to exactly width terminal cells, so emoji and wide characters do
// not break alignment.
//
// Truncation is followed by padding rather than trusted to land on the mark: a
// double-width glyph at the cut can leave the result a cell short, and one
// missing cell shifts every column after it.
func pad(s string, width int) string { return padWith(widths, s, width) }

// padWide fits s to width cells even on a terminal that draws every ambiguous
// glyph at two. The result can be narrower than width elsewhere, which is the
// intended trade: a header line that overflows costs the fleet total.
func padWide(s string, width int) string { return padWith(widestWidths, s, width) }

// clip truncates s to width cells without padding it out, measured on the
// assumption that ambiguous glyphs are drawn wide.
//
// Used where a line ends in free text — a working directory, a note, a key
// description — and only the ceiling matters. A line over the ceiling wraps,
// which costs a terminal row that View budgeted by counting newlines, and the
// pane then pushes the header off the top of the screen.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if widestWidths.StringWidth(s) > width {
		return widestWidths.Truncate(s, width, "…")
	}
	return s
}

func padWith(c *runewidth.Condition, s string, width int) string {
	if width <= 0 {
		return ""
	}
	if c.StringWidth(s) > width {
		s = c.Truncate(s, width, "…")
	}
	if gap := width - c.StringWidth(s); gap > 0 {
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
	footer := footerText(width, m.launcher != nil)

	var b strings.Builder
	b.WriteString(header)

	// The help, empty and detail panes are written without regard to height, so
	// they are clipped to the same budget the table gets — including the one
	// line held back for bubbletea, which keeps the last m.height lines of the
	// view whatever produced them. An overlay that overflows costs the header
	// exactly as a long table does.
	body := max(1, m.height-lineCount(header)-lineCount(footer)-1)

	switch {
	case m.form != nil:
		// The form outranks every other pane. It is the one place in this
		// interface where something can be caused to happen, and a snapshot
		// arriving mid-edit must not be able to take it off the screen.
		b.WriteString(clipLines(m.renderForm(), body, m.height))
	case m.showHelp:
		b.WriteString(clipLines(renderHelp(width), body, m.height))
	case len(m.snapshot.Rows) == 0:
		b.WriteString(clipLines(m.renderEmpty(), body, m.height))
	case m.detail:
		b.WriteString(clipLines(m.renderDetail(), body, m.height))
	default:
		// Whatever the header and footer did not take, less the line the column
		// titles occupy and one more for the terminal itself.
		//
		// That last one is not slack. bubbletea splits the view on newlines and
		// keeps the final m.height elements, so a view whose text ends in a
		// newline — as this one does — splits into one more element than it has
		// lines, and filling the height exactly costs the first line: the fleet
		// total, the very thing the windowing exists to keep on screen.
		//
		// A height of zero means the terminal has not told us its size yet, and
		// a limit invented before the first WindowSizeMsg would hide rows for no
		// reason; renderTable takes that as "no limit".
		//
		// Once the height is known the budget is clamped to a single row.
		// Below about seven lines the header, the column titles and the footer
		// already fill the terminal and no arrangement fits — and one row that
		// overflows by a line is a bounded failure, where drawing every row
		// scrolls the whole frame away and leaves nothing readable at all.
		budget := 0
		if m.height > 0 {
			budget = max(1, m.height-lineCount(header)-lineCount(footer)-2)
		}
		b.WriteString(m.renderTable(width, budget))
	}

	b.WriteString(footer)
	return b.String()
}

// footerText is the key hint drawn under everything else.
//
// The newlines sit outside Render because lipgloss turns a trailing one into a
// padded blank line and drops the newline itself — so the fragment would occupy
// one more terminal row than lineCount reports, and every budget built on that
// count would be wrong by one.
func footerText(width int, launchable bool) string {
	keys := "  ?  help   q  quit"
	if launchable {
		keys = "  n  launch   ?  help   q  quit"
	}
	// Clipped like every other line here. The hint grew when launching was added
	// to it, and a footer that wraps costs a terminal row View budgeted by
	// counting newlines — which the header then pays for.
	return "\n" + styleDim.Render(clip(keys, width)) + "\n"
}

// lineCount reports how many terminal lines s occupies. Every fragment here
// ends in a newline, so counting newlines counts lines.
func lineCount(s string) int { return strings.Count(s, "\n") }

// clipLines truncates s to at most max lines. A height of zero means the
// terminal has not reported its size yet, and nothing is clipped — inventing a
// limit before the first WindowSizeMsg would hide content for no reason.
//
// The last kept line is replaced with an ellipsis so a clipped pane says it was
// clipped, rather than appearing to end where it does not.
func clipLines(s string, limit, height int) string {
	if height <= 0 || limit <= 0 || lineCount(s) <= limit {
		return s
	}

	lines := strings.SplitAfter(s, "\n")
	kept := lines[:max(0, limit-1)]
	return strings.Join(kept, "") + styleDim.Render("  …") + "\n"
}

func (m Model) renderHeader(width int) string {
	var b strings.Builder

	// Every line here is bounded to the terminal width, measured on the
	// assumption that ambiguous glyphs are drawn wide.
	//
	// View charges the header by its newlines, so a line long enough to wrap
	// occupies a row nobody budgeted for — and the table, handed a row too many,
	// pushes the total it sits above off the top of the screen. The glyphs at
	// stake are not exotic: the em dash of an unknown cost, the staleness
	// warning, and the ellipsis that truncation itself appends. A fixed spare
	// cell would not cover a line carrying several of them.
	const title = "  musem"
	b.WriteString(styleHeader.Render(title))

	fleet := m.snapshot.Fleet
	total := fleet.Cost.String()
	if !fleet.Cost.Known() && fleet.Priced > 0 {
		// An unknown total is not the same as no information: the dollars that
		// did price are still a floor, and a floor beats an em dash sitting
		// above rows that each show a figure.
		total = "at least " + musem.USD(fleet.Priced).String()
	}
	if fleet.Unpriceable() {
		// A partial total must never look like a complete one. Asked as
		// Unpriceable rather than Partial, because Partial is true of an
		// unaccounted session too and the line below already names those —
		// labelling one gap twice says less, not more.
		total += " (partial)"
	}
	if fleet.Unrecorded > 0 {
		// Named rather than folded into "(partial)": this is not a figure that
		// came out short, it is a figure that is missing whole sessions, and how
		// many is the part the user can act on.
		total += fmt.Sprintf(" (%d unaccounted)", fleet.Unrecorded)
	}
	if fleet.Degraded() {
		// Distinct from partial, and for the same reason the rows distinguish
		// "*" from "!": partial usage was counted but could not be priced,
		// degraded usage never reached the total at all. One figure is
		// incomplete, the other is simply too low.
		total += " (understated)"
	}
	summary := fmt.Sprintf("   %d sessions   %s total", len(m.snapshot.Rows), total)
	b.WriteString(styleDim.Render(padWide(summary, max(0, width-widths.StringWidth(title)))))
	b.WriteString("\n")

	if m.snapshot.Stale {
		b.WriteString(styleStale.Render(padWide(staleNotice(m.snapshot.StaleFor), width)))
		b.WriteString("\n")
	}
	if msg := errorMessage(m.snapshot.ErrCode, m.snapshot.ErrMessage); msg != "" {
		b.WriteString(styleErr.Render("  ✕ " + padWide(msg, max(0, width-4))))
		b.WriteString("\n")
	}
	if n := m.snapshot.Undiscovered; n > 0 {
		// Its own line, above the table rather than beside a row, because it
		// speaks for sessions that have no row to sit beside.
		//
		// It names both consequences, because a dropped record has two. A
		// session never seen before is simply absent from the list. One the
		// registry already knew keeps its row and its last observed status,
		// frozen: the registry refuses to call it ended on the strength of a
		// pass that admits it could not read everything, which is right, but it
		// leaves a status on screen that nothing refreshed. Saying only
		// "missing" would account for the first and quietly present the second
		// as current.
		b.WriteString(styleStale.Render(padWide(fmt.Sprintf(
			"  ! %d session records could not be read; sessions may be missing or stale", n), width)))
		b.WriteString("\n")
	}
	b.WriteString(m.renderKept(width))

	b.WriteString("\n")
	return b.String()
}

// maxKeptNotices is how many kept worktrees the header names before it starts
// counting them instead. Each one costs a terminal row that the table then does
// not get, and past a couple of them the list has stopped being a notice.
const maxKeptNotices = 2

// renderKept says which worktrees musem created and did not take back.
//
// It is in the header rather than beside a row because the sessions these
// belong to have ended, and an ended session scrolls to the bottom of a list the
// user is not looking at. The worktree is still on disk and still holding their
// work; the reason is the whole of what makes that actionable.
func (m Model) renderKept(width int) string {
	kept := m.snapshot.Kept
	if len(kept) == 0 {
		return ""
	}

	var b strings.Builder
	for i, r := range kept {
		if i >= maxKeptNotices {
			b.WriteString(styleStale.Render(padWide(clip(fmt.Sprintf(
				"  ⚠ and %d more worktrees were kept", len(kept)-maxKeptNotices), width), width)))
			b.WriteString("\n")
			break
		}
		b.WriteString(styleStale.Render(padWide(clip(
			"  ⚠ kept "+r.Path+" — "+r.Reason, width), width)))
		b.WriteString("\n")
	}
	return b.String()
}

// renderEmpty explains an empty dashboard, and says what to do about it.
//
// What to do depends on whether this musem can launch. Offering a key that does
// nothing is worse than offering none: it reads as a broken feature rather than
// an absent one, and the user goes looking for the fault in their keyboard.
func (m Model) renderEmpty() string {
	width := m.viewWidth()

	second := "  Start one and it will appear here on the next refresh."
	if m.launcher != nil {
		second = "  Press n to launch one, or start one elsewhere and it will appear here."
	}
	return styleDim.Render(
		clip("  No agent sessions are running.", width)+"\n\n"+
			clip(second, width)+"\n"+
			clip("  musem watches every session on this machine, whoever started it.", width)) + "\n"
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

	// The indicator costs a line, and at a budget of one there is no line to
	// spend: showing it would push the table past what View allowed and cost
	// the header instead. A single row with no indicator is the honest fit.
	indicator := maxRows >= 2

	start, end := 0, len(rows)
	if maxRows > 0 && len(rows) > maxRows {
		// One line goes to the position indicator, so it is never a surprise
		// that the list continues past the edge of the screen.
		window := maxRows
		if indicator {
			window = maxRows - 1
		}
		start = min(max(0, m.cursor-window/2), len(rows)-window)
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

	if indicator && end-start < len(rows) {
		// Clipped like every other line in the table. This one is a fixed
		// legend rather than a padded column, so nothing else bounds it — and a
		// wrapped indicator costs the same header row a wrapped session row
		// would, which is the whole reason the columns are clamped above.
		b.WriteString(styleDim.Render(
			clip(fmt.Sprintf("  ↕ %d–%d of %d", start+1, end, len(rows)), width)) + "\n")
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
	case musem.StatusEnded:
		// Distinct from the cross a dead session gets. Finishing is not failing,
		// and a glyph shared between them would undo in the symbol column the
		// distinction the status column just made.
		return "·"
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

	// A status without its age is half the answer: waiting for four seconds is
	// the loop working, waiting for ten minutes is a person blocked, and
	// indeterminate for an hour means the signal is not coming back.
	status := string(s.Status)
	if !s.StatusSince.IsZero() {
		status += " for " + humanAge(m.now().Sub(s.StatusSince))
	}

	lines := [][2]string{
		{"Session", s.Name},
		{"ID", s.ID},
		{"Status", status},
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
	if len(row.UnknownModels) > 0 {
		// The gap is only actionable if the user can see which rate is missing.
		lines = append(lines, [2]string{"Unpriced", strings.Join(row.UnknownModels, ", ")})
	}
	if !row.Cost.Known() && row.Priced > 0 {
		// An unknown cost is not the same as no information. The dollars that
		// did price are carried through the domain and the store precisely so
		// they survive an unpriceable model; showing them as a floor is what
		// makes that effort reach the user.
		lines = append(lines, [2]string{"At least", musem.USD(row.Priced).String()})
	}
	if row.Degraded {
		lines = append(lines, [2]string{"Note", "some records could not be read; this figure understates the true cost"})
	}

	// The label column and its surrounding spaces are fixed, so whatever is left
	// bounds the value. Values here are free text — a working directory, a note —
	// and an unbounded one wraps onto a row View never budgeted for.
	const labelled = 2 + 12 + 1

	var b strings.Builder
	for _, l := range lines {
		b.WriteString("  " + styleDim.Render(pad(l[0], 12)) + " " +
			clip(l[1], m.viewWidth()-labelled) + "\n")
	}
	b.WriteString("\n" + styleDim.Render("  enter  back") + "\n")
	return b.String()
}

// viewWidth is the terminal width to lay out against, defaulting the same way
// View does before the first WindowSizeMsg arrives.
func (m Model) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func renderHelp(width int) string { //nolint:revive // one list, read top to bottom
	rows := [][2]string{
		{"j / ↓", "next session"},
		{"k / ↑", "previous session"},
		{"g / G", "first / last"},
		{"enter", "session detail"},
		{"n", "launch a session"},
		{"?", "toggle this help"},
		{"q / esc", "close, or quit"},
		{"ctrl-c", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  " + styleHeader.Render(pad(r[0], 10)) + " " +
			styleDim.Render(clip(r[1], width-13)) + "\n")
	}

	b.WriteString("\n")
	for _, r := range [][2]string{
		{"—*", "counted, but some of it could not be priced"},
		{"$0.00!", "some records could not be read; the figure is too low"},
	} {
		b.WriteString("  " + styleHeader.Render(pad(r[0], 10)) + " " +
			styleDim.Render(clip(r[1], width-13)) + "\n")
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

// max and min are the language's own builtins, available since Go 1.21 and
// therefore not worth hand-writing here.
