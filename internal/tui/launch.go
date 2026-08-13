package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/safetext"
)

// Launcher is what the form needs in order to say what a launch would do, and
// then to do it.
//
// Declared here, by the view, and satisfied elsewhere. The dashboard cannot
// touch the disk itself — that is asserted by a test, and it is the reason a
// form with a write path behind it does not quietly turn the UI into a place
// where things happen. Everything below crosses this boundary as a message.
type Launcher interface {
	// ProposeBranch returns a branch name for a new session in dir.
	ProposeBranch(ctx context.Context, dir string) string

	// Branches lists the branches the user may pick instead.
	Branches(ctx context.Context, dir string) ([]string, error)

	// Plan says what the launch would do, or why it cannot proceed.
	Plan(ctx context.Context, req musem.LaunchRequest) (musem.LaunchPlan, error)

	// Launch does it.
	Launch(ctx context.Context, req musem.LaunchRequest) (musem.LaunchOutcome, error)
}

// Messages the form sends itself. Everything that touches the disk runs in its
// own goroutine as a command and comes back here, which is what keeps the
// interface drawing while a checkout takes seconds.
type (
	// formReadyMsg carries the branch proposal and the branches on offer.
	formReadyMsg struct {
		gen      int
		branch   string
		branches []string
	}

	// validateTickMsg is the debounce firing. The generation is what makes a
	// tick from three keystrokes ago harmless.
	validateTickMsg struct{ gen int }

	// plannedMsg is the answer to "what would this launch do".
	plannedMsg struct {
		gen  int
		plan musem.LaunchPlan
		err  error
	}

	// LaunchedMsg reports a finished launch, successful or not. It is exported
	// because it is the one message a caller outside this package might want to
	// observe.
	LaunchedMsg struct {
		Outcome musem.LaunchOutcome
		Err     error
	}
)

// validateDelay is how long the form waits after a keystroke before asking what
// the launch would do.
//
// The questions behind it shell out to git, and the refresh loop is doing the
// same thing on its own schedule. Validating on every keystroke would put the
// two in a queue for one binary and make the form feel slower the faster you
// type, which is the wrong way round.
const validateDelay = 200 * time.Millisecond

// formField is which part of the form has the keyboard.
type formField int

const (
	fieldDir formField = iota
	fieldWorktree
	fieldBranch
)

// form is the launch form's state.
//
// Held by value inside the model, like everything else here: bubbletea hands
// Update a copy and takes back what it returns, and a pointer would make an
// abandoned form's edits survive being abandoned.
type form struct {
	dir    editor
	branch editor

	worktree bool

	// existing reports that the branch in the field is one that already exists,
	// so it is checked out rather than created. It is cleared by editing the
	// name, because a name that has been typed over is no longer the one that
	// was picked.
	existing bool

	offered []string

	// picking is the branch list being shown. It borrows the form's keyboard
	// rather than being a field of its own.
	picking bool
	pick    int

	focus formField

	// gen counts edits, so a validation that comes back for a value the user has
	// already typed past is discarded instead of shown.
	gen int

	plan    musem.LaunchPlan
	planned bool

	// problem is why the launch cannot proceed, empty when it can.
	problem string

	// launching reports a launch in flight. The form stays on screen while it
	// runs, saying so, because the alternative is an interface that appears to
	// have done nothing for the length of a checkout.
	launching bool

	// failed is why the last launch failed, empty otherwise.
	failed string
}

// request builds the launch request the form describes.
func (f form) request() musem.LaunchRequest {
	req := musem.LaunchRequest{
		Dir:            strings.TrimSpace(f.dir.value),
		Branch:         strings.TrimSpace(f.branch.value),
		ExistingBranch: f.existing,
	}
	req.SetWorktree(f.worktree)
	return req
}

// openForm starts a launch form over the model's working directory.
func (m Model) openForm() (Model, tea.Cmd) {
	if m.launcher == nil {
		return m, nil
	}

	f := form{dir: editorOf(m.cwd)}
	// R2: the toggle is not set here from a constant. It is read off the zero
	// value of a launch request, which is where the default lives, so the form
	// cannot be the place it gets lost.
	f.worktree = musem.LaunchRequest{}.Worktree()
	f.gen = 1

	m.form = &f
	return m, tea.Batch(m.prepareCmd(f), m.validateCmd(f))
}

// closeForm abandons the form. Nothing has been created, because nothing is
// created until the launch itself runs.
func (m Model) closeForm() Model {
	m.form = nil
	return m
}

// prepareCmd fetches the branch proposal and the branches on offer.
func (m Model) prepareCmd(f form) tea.Cmd {
	launcher, ctx, gen, dir := m.launcher, m.context(), f.gen, strings.TrimSpace(f.dir.value)
	return func() tea.Msg {
		if dir == "" {
			return formReadyMsg{gen: gen}
		}
		branches, err := launcher.Branches(ctx, dir)
		if err != nil {
			// A directory that is not a repository has no branches to offer, and
			// the refusal the user needs is the plan's, not this one's.
			branches = nil
		}
		return formReadyMsg{gen: gen, branch: launcher.ProposeBranch(ctx, dir), branches: branches}
	}
}

// validateCmd asks what the launch would do, off the UI goroutine.
func (m Model) validateCmd(f form) tea.Cmd {
	launcher, ctx, gen, req := m.launcher, m.context(), f.gen, f.request()
	return func() tea.Msg {
		plan, err := launcher.Plan(ctx, req)
		return plannedMsg{gen: gen, plan: plan, err: err}
	}
}

// debounce schedules a validation for the current generation.
func debounce(gen int) tea.Cmd {
	return tea.Tick(validateDelay, func(time.Time) tea.Msg { return validateTickMsg{gen: gen} })
}

// launchCmd runs the launch in its own goroutine.
//
// R6: this is what keeps the dashboard drawing while a worktree is created. The
// pump goes on sending snapshots into Update, the form goes on rendering, and
// the result arrives as one more message.
func (m Model) launchCmd(f form) tea.Cmd {
	launcher, ctx, req := m.launcher, m.context(), f.request()
	return func() tea.Msg {
		outcome, err := launcher.Launch(ctx, req)
		return LaunchedMsg{Outcome: outcome, Err: err}
	}
}

// context returns the context commands run under, so a musem being shut down
// does not leave a validation running.
func (m Model) context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// updateForm folds a form message into the model.
func (m Model) updateForm(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case formReadyMsg:
		if m.form == nil || msg.gen != m.form.gen {
			return m, nil, true
		}
		f := *m.form
		f.offered = msg.branches
		if strings.TrimSpace(f.branch.value) == "" && msg.branch != "" {
			f.branch = editorOf(msg.branch)
		}
		m.form = &f
		return m, nil, true

	case validateTickMsg:
		if m.form == nil || msg.gen != m.form.gen {
			// The user has typed since. A stale tick fires no query at all,
			// which is the whole point of counting generations rather than
			// cancelling.
			return m, nil, true
		}
		return m, m.validateCmd(*m.form), true

	case plannedMsg:
		if m.form == nil || msg.gen != m.form.gen {
			return m, nil, true
		}
		f := *m.form
		f.planned = true
		if msg.err != nil {
			f.plan, f.problem = musem.LaunchPlan{}, musem.ErrorMessage(msg.err)
		} else {
			f.plan, f.problem = msg.plan, ""
		}
		m.form = &f
		return m, nil, true

	case LaunchedMsg:
		// Deliberately not guarded on the form still being open. While a launch
		// is in flight the form takes one key, and the hint offers it: "esc
		// leave this running and go back". A user who does exactly that arrives
		// here with no form — and dropping the outcome on that basis loses the
		// report for a session that started, which is the one case it exists
		// for, reached by doing what the interface suggested.
		if msg.Err != nil {
			// Nothing is announced as started, because nothing was. The reason
			// goes back to the form when there is still a form to hold it.
			if m.form != nil {
				f := *m.form
				f.launching, f.failed = false, musem.ErrorMessage(msg.Err)
				m.form = &f
			}
			return m, nil, true
		}

		// The form's work is done, but the dashboard has nothing to show yet:
		// discovery runs on its own interval, and an agent that asks to be let
		// into a directory before it starts will wait in its pane until somebody
		// answers. Closing on silence is what made a successful launch look like
		// a keypress that did nothing.
		//
		// Appended to a fresh slice rather than in place: the model arrives here
		// by value, and appending under a shared backing array would write into
		// the copy bubbletea is holding.
		next := make([]musem.LaunchOutcome, len(m.launched), len(m.launched)+1)
		copy(next, m.launched)
		m.launched = append(next, msg.Outcome)

		// Closing a form that is already closed is a no-op, which is what makes
		// the two ways of getting here — confirming and waiting, or confirming
		// and leaving — end in the same place.
		return m.closeForm(), nil, true
	}
	return m, nil, false
}

// handleFormKey is the form's keyboard. It has the keys while it is open, which
// is why the dashboard's own bindings are not consulted here.
func (m Model) handleFormKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	f := *m.form

	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// A launch in flight takes no input except leaving. Editing the form under a
	// running launch would show values that no longer describe what is happening.
	if f.launching {
		if msg.String() == "esc" {
			return m.closeForm(), nil
		}
		return m, nil
	}

	if f.picking {
		return m.handlePickKey(msg, f)
	}

	switch msg.String() {
	case "esc":
		return m.closeForm(), nil

	case "enter":
		return m.submit(f)

	case "tab", "down":
		f.focus = f.nextField(1)
		m.form = &f
		return m, nil

	case "shift+tab", "up":
		f.focus = f.nextField(-1)
		m.form = &f
		return m, nil

	case " ":
		if f.focus == fieldWorktree {
			f.worktree = !f.worktree
			return m.edited(f)
		}
		f = f.typed(" ")
		return m.edited(f)

	case "ctrl+b":
		// The existing branches, offered rather than imposed: the proposal is a
		// new branch, and this is the other half of R3.
		if f.worktree && len(f.offered) > 0 {
			f.picking, f.pick = true, 0
			m.form = &f
			return m, nil
		}
		return m, nil

	case "left":
		f = f.onFocused(editor.left)
		m.form = &f
		return m, nil

	case "right":
		f = f.onFocused(editor.right)
		m.form = &f
		return m, nil

	case "home", "ctrl+a":
		f = f.onFocused(editor.home)
		m.form = &f
		return m, nil

	case "end", "ctrl+e":
		f = f.onFocused(editor.end)
		m.form = &f
		return m, nil

	case "backspace":
		f = f.onFocused(editor.backspace)
		return m.edited(f)

	case "delete":
		f = f.onFocused(editor.remove)
		return m.edited(f)
	}

	if msg.Type == tea.KeyRunes {
		f = f.typed(string(msg.Runes))
		return m.edited(f)
	}
	return m, nil
}

// handlePickKey drives the list of existing branches.
func (m Model) handlePickKey(msg tea.KeyMsg, f form) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+b":
		f.picking = false
		m.form = &f
		return m, nil

	case "j", "down":
		if f.pick < len(f.offered)-1 {
			f.pick++
		}
		m.form = &f
		return m, nil

	case "k", "up":
		if f.pick > 0 {
			f.pick--
		}
		m.form = &f
		return m, nil

	case "enter":
		if f.pick < len(f.offered) {
			// R3: the worktree checks the branch out rather than creating one.
			f.branch = editorOf(f.offered[f.pick])
			f.existing = true
		}
		f.picking = false
		return m.edited(f)
	}
	return m, nil
}

// edited records that the form changed and schedules a fresh validation.
func (m Model) edited(f form) (Model, tea.Cmd) {
	f.gen++
	f.planned, f.problem, f.failed = false, "", ""
	m.form = &f
	return m, debounce(f.gen)
}

// submit launches, if the plan says it can.
//
// A form that has not been validated yet does not launch: the answer is on its
// way, and starting anyway would be acting on a question still in flight. A form
// that was refused does not launch either, which is R2's "refuses to launch until
// the toggle is disabled or the directory is changed".
func (m Model) submit(f form) (Model, tea.Cmd) {
	if !f.planned || f.problem != "" {
		m.form = &f
		return m, nil
	}
	f.launching, f.failed = true, ""
	m.form = &f
	return m, m.launchCmd(f)
}

// nextField moves the focus, skipping the branch when there is no worktree to
// put it on.
func (f form) nextField(by int) formField {
	fields := []formField{fieldDir, fieldWorktree}
	if f.worktree {
		fields = append(fields, fieldBranch)
	}
	at := 0
	for i, field := range fields {
		if field == f.focus {
			at = i
		}
	}
	next := (at + by + len(fields)) % len(fields)
	return fields[next]
}

// onFocused applies an editor operation to whichever field has the keyboard.
func (f form) onFocused(op func(editor) editor) form {
	switch f.focus {
	case fieldDir:
		f.dir = op(f.dir)
	case fieldBranch:
		f.branch = op(f.branch)
		f.existing = false
	case fieldWorktree:
	}
	return f
}

// typed inserts text into the focused field.
func (f form) typed(s string) form {
	switch f.focus {
	case fieldDir:
		f.dir = f.dir.insert(s)
	case fieldBranch:
		f.branch = f.branch.insert(s)
		// A picked name that has been typed over is no longer the picked name.
		f.existing = false
	case fieldWorktree:
	}
	return f
}

// maxOffered is how many existing branches the picker shows at once. The list
// is windowed rather than drawn whole: a repository with two hundred branches
// would otherwise push the rest of the form off the screen.
const maxOffered = 6

// renderForm draws the launch form.
//
// Every line is bounded to the terminal width, on the same discipline the rest
// of this package follows: View charges each pane by its newlines, so a line long
// enough to wrap occupies a row nobody budgeted for and costs the header.
func (m Model) renderForm() string {
	f := *m.form
	width := m.viewWidth()

	// The label column and its surrounding spaces are fixed, so whatever is left
	// bounds the value. Values here are the two freest pieces of text in the
	// interface: a path the user typed and a branch name they chose.
	const labelled = 2 + 12 + 1

	var b strings.Builder
	b.WriteString(styleHeader.Render(clip("  Launch a session", width)) + "\n\n")

	field := func(label, value string, focused bool) {
		marker := "  "
		if focused {
			marker = styleCursor.Render("▸ ")
		}
		b.WriteString(marker + styleDim.Render(pad(label, 12)) + " " +
			clip(value, width-labelled) + "\n")
	}

	field("Directory", f.dir.display(f.focus == fieldDir), f.focus == fieldDir)
	field("Worktree", worktreeText(f.worktree), f.focus == fieldWorktree)

	if f.worktree {
		branch := f.branch.display(f.focus == fieldBranch)
		if f.existing {
			branch += "  (existing)"
		} else if strings.TrimSpace(f.branch.value) != "" {
			branch += "  (new)"
		}
		field("Branch", branch, f.focus == fieldBranch)

		// R4: the path the worktree will occupy, before anything occupies it. It
		// is shown only once validation has worked it out — a guess here would be
		// a path the launch might not use.
		//
		// Cleaned on the way to the screen. The path is derived from the
		// repository root, which is whatever git printed for a directory somebody
		// else named, and this line is redrawn on every keystroke — the same
		// reasons every other foreign value in this interface is cleaned before it
		// is drawn. What the launch actually uses is the uncleaned value in the
		// plan, because that one is a path rather than a label.
		dest := safetext.Clean(f.plan.Worktree)
		if dest == "" {
			dest = "—"
		}
		field("Creates", dest, false)

		if f.picking {
			b.WriteString(m.renderOffered(f, width))
		}
	}

	b.WriteString("\n")
	switch {
	case f.launching:
		// R6: the launch is in flight and says so, while the dashboard behind it
		// goes on refreshing.
		b.WriteString(styleStale.Render(padWide(clip("  ⟳ launching…", width), width)) + "\n")
	case f.failed != "":
		b.WriteString(styleErr.Render("  ✕ "+clip(f.failed, max(0, width-4))) + "\n")
	case f.problem != "":
		b.WriteString(styleErr.Render("  ✕ "+clip(f.problem, max(0, width-4))) + "\n")
	case !f.planned:
		b.WriteString(styleDim.Render(clip("  checking…", width)) + "\n")
	default:
		b.WriteString(styleDim.Render(clip("  ready", width)) + "\n")
	}

	b.WriteString("\n" + styleDim.Render(clip(formKeys(f), width)) + "\n")
	return b.String()
}

// renderOffered draws the existing branches, windowed around the selection.
func (m Model) renderOffered(f form, width int) string {
	start := 0
	if len(f.offered) > maxOffered {
		start = min(max(0, f.pick-maxOffered/2), len(f.offered)-maxOffered)
	}
	end := min(start+maxOffered, len(f.offered))

	var b strings.Builder
	b.WriteString("\n")
	for i := start; i < end; i++ {
		marker := "      "
		name := f.offered[i]
		if i == f.pick {
			marker = styleCursor.Render("    ▸ ")
		}
		b.WriteString(marker + clip(name, max(0, width-6)) + "\n")
	}
	if end-start < len(f.offered) {
		b.WriteString(styleDim.Render(clip(fmt.Sprintf("      ↕ %d–%d of %d", start+1, end, len(f.offered)), width)) + "\n")
	}
	return b.String()
}

func worktreeText(on bool) string {
	if on {
		return "[x]  a worktree of its own"
	}
	return "[ ]  run in the directory as given"
}

// formKeys is the hint line, which changes with what the form can currently do.
func formKeys(f form) string {
	switch {
	case f.launching:
		return "  esc  leave this running and go back"
	case f.picking:
		return "  ↑↓  choose   enter  use this branch   esc  back to the proposed name"
	case f.focus == fieldWorktree:
		return "  space  toggle   tab  next field   enter  launch   esc  cancel"
	case f.focus == fieldBranch && len(f.offered) > 0:
		return "  ctrl-b  existing branches   tab  next field   enter  launch   esc  cancel"
	default:
		return "  tab  next field   enter  launch   esc  cancel"
	}
}
