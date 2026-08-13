package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MrSossa/musem"
)

// fakeLauncher stands in for everything behind the form. It records what it was
// asked to do, which is how the tests below tell "the user looked at a form"
// from "something was created".
type fakeLauncher struct {
	mu sync.Mutex

	proposed string
	branches []string

	plan    musem.LaunchPlan
	planErr error

	planned  []musem.LaunchRequest
	launched []musem.LaunchRequest

	launchErr   error
	launchDelay time.Duration
	outcome     musem.LaunchOutcome
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{
		proposed: "musem/session-1",
		branches: []string{"main", "release"},
		plan: musem.LaunchPlan{
			Repo:      "/r/api",
			Dir:       "/r/api-musem-session-1",
			Branch:    "musem/session-1",
			NewBranch: true,
			Worktree:  "/r/api-musem-session-1",
		},
	}
}

func (f *fakeLauncher) ProposeBranch(context.Context, string) string { return f.proposed }

func (f *fakeLauncher) Branches(context.Context, string) ([]string, error) {
	return f.branches, nil
}

func (f *fakeLauncher) Plan(_ context.Context, req musem.LaunchRequest) (musem.LaunchPlan, error) {
	f.mu.Lock()
	f.planned = append(f.planned, req)
	f.mu.Unlock()

	if f.planErr != nil {
		return musem.LaunchPlan{}, f.planErr
	}
	if !req.Worktree() {
		return musem.LaunchPlan{Dir: req.Dir}, nil
	}
	return f.plan, nil
}

func (f *fakeLauncher) Launch(_ context.Context, req musem.LaunchRequest) (musem.LaunchOutcome, error) {
	if f.launchDelay > 0 {
		time.Sleep(f.launchDelay)
	}
	f.mu.Lock()
	f.launched = append(f.launched, req)
	f.mu.Unlock()

	if f.launchErr != nil {
		return musem.LaunchOutcome{}, f.launchErr
	}
	return f.outcome, nil
}

func (f *fakeLauncher) launches() []musem.LaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]musem.LaunchRequest(nil), f.launched...)
}

// settle runs a command and feeds whatever it produces back into the model,
// until nothing is left. It is what a running bubbletea program does; here it is
// done by hand so a test can assert on the model in between.
func settle(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}

	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = settle(t, m, c)
		}
		return m
	}
	if msg == nil {
		return m
	}
	next, follow := m.Update(msg)
	return settle(t, next.(Model), follow)
}

// dashboard is a model with a launcher attached and one session on screen.
func dashboard(t *testing.T, l Launcher) Model {
	t.Helper()
	m := NewModel(WithLauncher(l), WithWorkingDir("/r/api"))
	m.width, m.height = 100, 30
	return withSnapshot(m, snapshot(row("a", "api", musem.StatusIdle)))
}

// key sends one key and settles whatever it starts.
func key(t *testing.T, m Model, s string) Model {
	t.Helper()

	var msg tea.KeyMsg
	switch s {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	case " ":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+b":
		msg = tea.KeyMsg{Type: tea.KeyCtrlB}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}

	next, cmd := m.Update(msg)
	return settle(t, next.(Model), cmd)
}

// revalidate fires the debounce by hand, so a test does not wait for it.
func revalidate(t *testing.T, m Model) Model {
	t.Helper()
	if m.form == nil {
		t.Fatal("no form is open")
	}
	next, cmd := m.Update(validateTickMsg{gen: m.form.gen})
	return settle(t, next.(Model), cmd)
}

// R1, scenario "Opening the form": a form appears, with the working directory
// filled in and editable.
func TestTheLaunchFormOpensWithTheWorkingDirectory(t *testing.T) {
	m := key(t, dashboard(t, newFakeLauncher()), "n")

	if m.form == nil {
		t.Fatal("n did not open the launch form")
	}
	if m.form.dir.value != "/r/api" {
		t.Errorf("directory = %q, want it filled in", m.form.dir.value)
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "Launch a session") {
		t.Errorf("the form is not on screen:\n%s", view)
	}
	if !strings.Contains(view, "/r/api") {
		t.Errorf("the working directory is not shown:\n%s", view)
	}

	// Editable: typing changes it.
	m = key(t, m, "x")
	if m.form.dir.value != "/r/apix" {
		t.Errorf("directory = %q after typing, want the field to accept input", m.form.dir.value)
	}
}

// R1, scenario "Nothing happens until it is confirmed": abandoning the form
// creates nothing, and the launcher was never asked to.
func TestAbandoningTheFormCreatesNothing(t *testing.T) {
	l := newFakeLauncher()
	m := key(t, dashboard(t, l), "n")

	m = key(t, m, "x")
	m = key(t, m, "y")
	m = key(t, m, "esc")

	if m.form != nil {
		t.Error("esc left the form open")
	}
	if got := l.launches(); len(got) != 0 {
		t.Errorf("launched %v; abandoning a form must create nothing", got)
	}

	// And the dashboard is back, unchanged.
	if !strings.Contains(stripANSI(m.View()), "STATUS") {
		t.Error("the dashboard did not come back")
	}
}

// R2, scenario "The default is a worktree": the toggle is already on, and the
// default comes from the domain rather than from a constant in the view.
func TestTheWorktreeToggleIsOnByDefault(t *testing.T) {
	m := key(t, dashboard(t, newFakeLauncher()), "n")

	if !m.form.worktree {
		t.Fatal("the worktree toggle is off when the form opens")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "[x]") {
		t.Errorf("the toggle does not read as enabled:\n%s", view)
	}
	if !strings.Contains(view, "Branch") {
		t.Errorf("a form with a worktree shows no branch:\n%s", view)
	}
}

// R2, scenario "Launching without a worktree": the session starts in the
// directory as given, and the request says so.
func TestLaunchingWithTheToggleOffAsksForNoWorktree(t *testing.T) {
	l := newFakeLauncher()
	m := key(t, dashboard(t, l), "n")

	m = key(t, m, "tab") // onto the toggle
	m = key(t, m, " ")   // off
	if m.form.worktree {
		t.Fatal("space did not turn the worktree off")
	}
	if strings.Contains(stripANSI(m.View()), "Creates") {
		t.Error("a launch with no worktree still shows a destination")
	}

	m = revalidate(t, m)
	m = key(t, m, "enter")

	got := l.launches()
	if len(got) != 1 {
		t.Fatalf("launched %d times, want 1", len(got))
	}
	if got[0].Worktree() {
		t.Error("the request asked for a worktree the user turned off")
	}
	if got[0].Dir != "/r/api" {
		t.Errorf("dir = %q, want the directory as given", got[0].Dir)
	}
}

// R3, scenario "The proposed branch": a new branch name is proposed, and
// confirming creates that branch.
func TestABranchIsProposedAndCreated(t *testing.T) {
	l := newFakeLauncher()
	m := key(t, dashboard(t, l), "n")

	if m.form.branch.value != "musem/session-1" {
		t.Fatalf("branch = %q, want the proposal", m.form.branch.value)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "musem/session-1") || !strings.Contains(view, "(new)") {
		t.Errorf("the proposed branch is not shown as new:\n%s", view)
	}

	m = key(t, m, "enter")
	got := l.launches()
	if len(got) != 1 {
		t.Fatalf("launched %d times, want 1", len(got))
	}
	if got[0].Branch != "musem/session-1" || got[0].ExistingBranch {
		t.Errorf("request = %+v, want the proposed branch created", got[0])
	}
}

// R3, scenario "Reusing an existing branch": picking one checks it out instead.
func TestAnExistingBranchCanBePickedInstead(t *testing.T) {
	l := newFakeLauncher()
	m := key(t, dashboard(t, l), "n")

	m = key(t, m, "tab") // toggle
	m = key(t, m, "tab") // branch
	m = key(t, m, "ctrl+b")
	if !m.form.picking {
		t.Fatal("the existing branches were not offered")
	}
	if !strings.Contains(stripANSI(m.View()), "release") {
		t.Error("the branches on offer are not shown")
	}

	m = key(t, m, "down")
	m = key(t, m, "enter")

	if m.form.branch.value != "release" {
		t.Fatalf("branch = %q, want the picked one", m.form.branch.value)
	}
	if !m.form.existing {
		t.Fatal("the picked branch is not marked as existing; it would be created instead")
	}
	if !strings.Contains(stripANSI(m.View()), "(existing)") {
		t.Error("the form does not say the branch already exists")
	}

	m = revalidate(t, m)
	m = key(t, m, "enter")

	got := l.launches()
	if len(got) != 1 || got[0].Branch != "release" || !got[0].ExistingBranch {
		t.Errorf("request = %+v, want the existing branch checked out", got)
	}
}

// Typing over a picked name makes it a new branch again: a name that has been
// edited is no longer the one that was picked.
func TestEditingAPickedBranchMakesItNewAgain(t *testing.T) {
	m := key(t, dashboard(t, newFakeLauncher()), "n")
	m = key(t, m, "tab")
	m = key(t, m, "tab")
	m = key(t, m, "ctrl+b")
	m = key(t, m, "enter") // picks "main"

	if !m.form.existing {
		t.Fatal("the picked branch is not marked as existing")
	}
	m = key(t, m, "2")
	if m.form.existing {
		t.Error("a branch typed over is still marked as the one that was picked")
	}
}

// R4, scenario "Seeing where it will go": the path the worktree will occupy is
// shown before anything occupies it.
func TestTheDerivedDestinationIsShown(t *testing.T) {
	m := key(t, dashboard(t, newFakeLauncher()), "n")

	view := stripANSI(m.View())
	if !strings.Contains(view, "Creates") {
		t.Errorf("the form does not say what it will create:\n%s", view)
	}
	if !strings.Contains(view, "/r/api-musem-session-1") {
		t.Errorf("the derived destination is not shown:\n%s", view)
	}
}

// R2, R3 and R4: a refusal is shown inside the form, and the form will not
// launch while it stands.
func TestARefusalIsShownInTheFormAndBlocksLaunching(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			"not a repository",
			musem.Errorf(musem.ENOTFOUND, "/tmp/scratch is not inside a git repository"),
			"not inside a git repository",
		},
		{
			"branch checked out elsewhere",
			musem.Errorf(musem.EINVALID, "the branch release is already checked out at /r/api-release"),
			"already checked out",
		},
		{
			"occupied destination",
			musem.Errorf(musem.EINVALID, "/r/api-musem-session-1 already exists; musem will not write into it"),
			"already exists",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newFakeLauncher()
			l.planErr = tc.err
			m := key(t, dashboard(t, l), "n")

			view := stripANSI(m.View())
			if !strings.Contains(view, tc.want) {
				t.Errorf("the form does not explain the refusal:\n%s", view)
			}

			m = key(t, m, "enter")
			if got := l.launches(); len(got) != 0 {
				t.Errorf("launched %v despite the refusal", got)
			}
			if m.form == nil {
				t.Error("a refused form closed itself; the user has nothing left to correct")
			}
		})
	}
}

// A form whose validation has not come back yet does not launch either: the
// answer is in flight, and acting anyway is acting on a question nobody has
// answered.
func TestAFormThatHasNotBeenCheckedDoesNotLaunch(t *testing.T) {
	l := newFakeLauncher()
	m := dashboard(t, l)

	// Opened without settling the commands it started, so no plan has arrived.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = next.(Model)

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := l.launches(); len(got) != 0 {
		t.Errorf("launched %v before knowing whether it could", got)
	}
	if m2.(Model).form == nil {
		t.Error("the form closed while its answer was still in flight")
	}
	if !strings.Contains(stripANSI(m2.(Model).View()), "checking") {
		t.Error("the form does not say it is still checking")
	}
}

// R6, scenario "A slow creation": the launch runs off the update loop, the
// dashboard keeps drawing, and the form says the launch is in progress.
func TestASlowLaunchDoesNotBlockTheInterface(t *testing.T) {
	l := newFakeLauncher()
	l.launchDelay = 250 * time.Millisecond

	m := key(t, dashboard(t, l), "n")

	// Confirm without settling: the command is handed back rather than run, and
	// running it is bubbletea's job on another goroutine.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("confirming produced no command; the launch would have to run inside Update")
	}
	if !m.form.launching {
		t.Fatal("the form does not know a launch is under way")
	}
	if !strings.Contains(stripANSI(m.View()), "launching") {
		t.Error("the form does not say the launch is in progress")
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	// While that runs, the interface keeps taking snapshots and drawing them.
	deadline := time.After(2 * time.Second)
	for i := 0; i < 20; i++ {
		m = withSnapshot(m, snapshot(row("a", "api", musem.StatusRunning)))
		if m.View() == "" {
			t.Fatal("the dashboard stopped drawing while a launch was in flight")
		}
	}

	select {
	case msg := <-done:
		next, _ := m.Update(msg)
		if next.(Model).form != nil {
			t.Error("the form stayed open after a successful launch")
		}
	case <-deadline:
		t.Fatal("the launch never came back")
	}
}

// R7: a launch that failed says why, inside the form, and leaves it open so the
// user can change something and try again.
func TestAFailedLaunchExplainsItselfInTheForm(t *testing.T) {
	l := newFakeLauncher()
	l.launchErr = musem.Errorf(musem.EUNAVAILABLE, "tmux was not found on PATH")

	m := key(t, dashboard(t, l), "n")
	m = key(t, m, "enter")

	if m.form == nil {
		t.Fatal("the form closed on a failed launch, taking the reason with it")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "tmux was not found") {
		t.Errorf("the failure is not on screen:\n%s", view)
	}
	if m.form.launching {
		t.Error("the form still reports a launch in progress")
	}
}

// R6, R18: the form is subject to the same width discipline as everything else.
// A line that wraps costs a terminal row that View budgeted by counting
// newlines, and the header goes off the top of the screen.
func TestTheFormStaysLegibleInANarrowTerminal(t *testing.T) {
	l := newFakeLauncher()
	l.planErr = musem.Errorf(musem.EINVALID,
		"/home/a/very/long/path/that/goes/on/api-musem-session-1 already exists; musem will not write into it")

	for _, width := range []int{20, 30, 40, 60, 120} {
		m := dashboard(t, l)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		m = key(t, next.(Model), "n")

		view := m.View()
		for _, line := range strings.Split(view, "\n") {
			if w := widestWidths.StringWidth(stripANSI(line)); w > width {
				t.Errorf("at width %d a line occupies %d cells: %q", width, w, stripANSI(line))
			}
		}
		if lines := strings.Count(view, "\n"); lines > 12 {
			t.Errorf("at width %d the form draws %d lines into a terminal of 12", width, lines)
		}
	}
}

// R11, scenario "Navigating changes nothing": every key that moves through the
// dashboard leaves the world alone. The one key that leads anywhere opens a
// form, and a form is not a launch.
func TestNavigatingChangesNothing(t *testing.T) {
	l := newFakeLauncher()
	m := dashboard(t, l)
	m = withSnapshot(m, snapshot(
		row("a", "one", musem.StatusWaiting),
		row("b", "two", musem.StatusRunning),
		row("c", "three", musem.StatusIdle),
	))

	for _, k := range []string{"j", "k", "down", "up", "g", "G", "enter", "?", "?", "enter", "esc"} {
		m = key(t, m, k)
		_ = m.View()
	}

	if got := l.launches(); len(got) != 0 {
		t.Errorf("navigating launched %v", got)
	}
	l.mu.Lock()
	planned := len(l.planned)
	l.mu.Unlock()
	if planned != 0 {
		t.Errorf("navigating asked for %d launch plans; nothing on this path should be consulting the disk", planned)
	}
}

// R9: the reason a worktree was kept reaches the view. The session it belonged
// to has ended and sorts to the bottom of a list nobody is reading, so the
// notice goes where the user is already looking.
func TestAKeptWorktreeAndItsReasonReachTheView(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	s.Kept = []musem.Reclamation{
		{SessionID: "s1", Path: "/r/api-musem-session-1", Reason: "it has uncommitted changes"},
	}

	view := stripANSI(withSnapshot(NewModel(), s).View())
	if !strings.Contains(view, "/r/api-musem-session-1") {
		t.Errorf("the kept worktree is not named:\n%s", view)
	}
	if !strings.Contains(view, "uncommitted changes") {
		t.Errorf("the reason it was kept is not shown:\n%s", view)
	}
}

// Past a couple of them the list has stopped being a notice and started being a
// pane, so the rest are counted instead of drawn.
func TestManyKeptWorktreesAreCountedRatherThanListed(t *testing.T) {
	s := snapshot(row("a", "api", musem.StatusIdle))
	for i := 0; i < 5; i++ {
		s.Kept = append(s.Kept, musem.Reclamation{
			SessionID: string(rune('a' + i)),
			Path:      "/r/api-" + string(rune('a'+i)),
			Reason:    "it has untracked files",
		})
	}

	view := stripANSI(withSnapshot(NewModel(), s).View())
	if !strings.Contains(view, "and 3 more worktrees were kept") {
		t.Errorf("the remaining kept worktrees are not accounted for:\n%s", view)
	}
}

// A musem with nothing to launch with does not advertise a key that does
// nothing.
func TestLaunchingIsNotOfferedWithoutALauncher(t *testing.T) {
	m := withSnapshot(NewModel(), snapshot(row("a", "api", musem.StatusIdle)))

	if strings.Contains(stripANSI(m.View()), "n  launch") {
		t.Error("the footer offers a key that cannot do anything")
	}
	m = key(t, m, "n")
	if m.form != nil {
		t.Error("a form opened with nothing behind it")
	}
}

// launchedInto returns an outcome shaped like a real one for the fake to return.
func launchedInto(id, branch, dir string) musem.LaunchOutcome {
	out := musem.LaunchOutcome{
		SessionID: id,
		Substrate: "musem-" + id,
		Dir:       dir,
		Branch:    branch,
	}
	if branch != "" {
		out.Worktree = dir
	}
	return out
}

// launchAndSettle presses n, confirms, and returns the dashboard afterwards.
func launchAndSettle(t *testing.T, l *fakeLauncher) Model {
	t.Helper()
	m := key(t, dashboard(t, l), "n")
	return key(t, m, "enter")
}

// R32, scenario "A session in a worktree": what was started is named — where it
// is, what it is on, and how to reach it.
func TestASuccessfulLaunchSaysWhatItStarted(t *testing.T) {
	l := newFakeLauncher()
	l.outcome = launchedInto("abc123", "musem/session-1", "/r/api-musem-session-1")

	m := launchAndSettle(t, l)
	if m.form != nil {
		t.Fatal("the form is still open after a successful launch")
	}

	view := stripANSI(m.View())
	for _, want := range []string{
		"musem-abc123",                // the name it is hosted under
		"musem/session-1",             // the branch
		"/r/api-musem-session-1",      // where it is working
		"tmux attach -t musem-abc123", // how to get a terminal in it
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the report does not mention %q:\n%s", want, view)
		}
	}
}

// R32, scenario "A session without a worktree": the directory as it was given,
// and no branch invented for a launch that made none.
func TestALaunchWithoutAWorktreeIsReportedWithoutOne(t *testing.T) {
	l := newFakeLauncher()
	l.outcome = launchedInto("abc123", "", "/home/u/project")

	m := launchAndSettle(t, l)
	view := stripANSI(m.View())

	if !strings.Contains(view, "/home/u/project") {
		t.Errorf("the directory is not reported:\n%s", view)
	}
	if !strings.Contains(view, "tmux attach -t musem-abc123") {
		t.Errorf("the way to reach it is not reported:\n%s", view)
	}
	if strings.Contains(view, " in /home/u/project") {
		t.Errorf("a branch was implied for a launch that created none:\n%s", view)
	}
}

// R32, scenario "A launch that failed": nothing is announced as started,
// because nothing was.
func TestAFailedLaunchAnnouncesNothingAsStarted(t *testing.T) {
	l := newFakeLauncher()
	l.launchErr = musem.Errorf(musem.EUNAVAILABLE, "tmux was not found on PATH")
	l.outcome = launchedInto("abc123", "musem/session-1", "/r/api-musem-session-1")

	m := launchAndSettle(t, l)

	if len(m.launched) != 0 {
		t.Fatalf("launched = %+v after a failure; nothing was started", m.launched)
	}
	if strings.Contains(stripANSI(m.View()), "started musem-abc123") {
		t.Error("a launch that failed was announced as started")
	}
}

// R33, scenario "The session has not appeared yet": the report stands, and says
// so, across refreshes that do not contain it.
//
// This is also scenario "The agent is waiting to be let in": an agent asking to
// be let into a directory sits in its pane until somebody answers, which from
// the dashboard's side looks exactly like this — and the answer is to attach.
func TestAReportStandsWhileTheSessionIsAbsent(t *testing.T) {
	l := newFakeLauncher()
	l.outcome = launchedInto("abc123", "musem/session-1", "/r/api-musem-session-1")

	m := launchAndSettle(t, l)

	// Several refreshes go by carrying somebody else's sessions.
	for i := 0; i < 5; i++ {
		m = withSnapshot(m, snapshot(row("other", "other", musem.StatusRunning)))
	}

	if len(m.launched) != 1 {
		t.Fatalf("the report was withdrawn while the session was still absent: %+v", m.launched)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "not in the inventory yet") {
		t.Errorf("the report does not say the session has not appeared:\n%s", view)
	}
	if !strings.Contains(view, "tmux attach -t musem-abc123") {
		t.Errorf("the report stopped saying how to reach it:\n%s", view)
	}
}

// R33, scenario "The session appears": the report is withdrawn on its own once
// the session is in the inventory, where it has a row like any other.
func TestAReportIsWithdrawnWhenTheSessionArrives(t *testing.T) {
	l := newFakeLauncher()
	l.outcome = launchedInto("abc123", "musem/session-1", "/r/api-musem-session-1")

	m := launchAndSettle(t, l)
	if len(m.launched) != 1 {
		t.Fatalf("nothing was reported: %+v", m.launched)
	}

	m = withSnapshot(m, snapshot(
		row("other", "other", musem.StatusRunning),
		row("abc123", "api", musem.StatusRunning),
	))

	if len(m.launched) != 0 {
		t.Fatalf("the report outlived the session's arrival: %+v", m.launched)
	}
	if strings.Contains(stripANSI(m.View()), "not in the inventory yet") {
		t.Error("the dashboard still says the session has not appeared")
	}
}

// R33, scenario "More launches than there is room for": the ones that do not
// fit are counted rather than drawn, and the header stays a header.
func TestManyOutstandingLaunchesAreCountedRatherThanDrawn(t *testing.T) {
	m := NewModel(WithLauncher(newFakeLauncher()))
	m.width, m.height = 100, 30
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		m.launched = append(m.launched, launchedInto(id, "musem/session-1", "/r/api-"+id))
	}
	m = withSnapshot(m, snapshot(row("other", "other", musem.StatusIdle)))

	view := stripANSI(m.View())
	if !strings.Contains(view, "and 3 more launched sessions have not appeared yet") {
		t.Errorf("the outstanding launches are not accounted for:\n%s", view)
	}
	if strings.Count(view, "tmux attach") > maxLaunchNotices {
		t.Errorf("more than %d reports were drawn in full:\n%s", maxLaunchNotices, view)
	}
}

// R32, R33: the report is held to the same width discipline as the rest of the
// header. A line that wraps costs a terminal row View budgeted by counting
// newlines, and the fleet total goes off the top of the screen.
func TestTheLaunchReportStaysInsideANarrowTerminal(t *testing.T) {
	long := "/home/a/very/long/path/that/keeps/going/api-musem-session-with-a-long-branch-name"

	for _, width := range []int{20, 30, 40, 60, 120} {
		m := NewModel(WithLauncher(newFakeLauncher()))
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 14})
		m = next.(Model)
		m.launched = []musem.LaunchOutcome{
			launchedInto("abc123", "feat/a-branch-with-a-long-name", long),
		}
		m = withSnapshot(m, snapshot(row("other", "other", musem.StatusIdle)))

		view := m.View()
		for _, line := range strings.Split(view, "\n") {
			if w := widestWidths.StringWidth(stripANSI(line)); w > width {
				t.Errorf("at width %d a line occupies %d cells: %q", width, w, stripANSI(line))
			}
		}
		if lines := strings.Count(view, "\n"); lines > 14 {
			t.Errorf("at width %d the dashboard draws %d lines into a terminal of 14", width, lines)
		}
	}
}

// A path or a branch is foreign text on its way to a terminal, where an escape
// sequence is an instruction rather than a glyph — and this line is drawn on
// every frame for as long as the session takes to appear.
func TestTheLaunchReportCannotDriveTheTerminal(t *testing.T) {
	m := NewModel(WithLauncher(newFakeLauncher()))
	m.width, m.height = 100, 24
	m.launched = []musem.LaunchOutcome{
		launchedInto("abc123", "feat/\u202eniam", "/r/api\x1b[2J-musem"),
	}
	m = withSnapshot(m, snapshot(row("other", "other", musem.StatusIdle)))

	view := stripANSI(m.View())
	if strings.ContainsRune(view, '\u202e') {
		t.Error("the report still carries a direction override")
	}
	if strings.Contains(view, "\x1b[2J") {
		t.Error("the report still carries an escape sequence")
	}
	// Cleaned, not refused: the readable part of an odd name is worth more than
	// a blank line where a session should be.
	if !strings.Contains(view, "musem-abc123") {
		t.Errorf("the report lost the session it was announcing:\n%s", view)
	}
}
