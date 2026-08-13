package launch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

var at = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

// rig is a Launcher with every dependency doubled and one journal between them.
type rig struct {
	*Launcher
	j     *journal
	trees *fakeTrees
	sub   *fakeSubstrate
	agent *fakeAgent
	owned *fakeOwned
}

func newRig(t *testing.T) *rig {
	t.Helper()

	j := &journal{}
	r := &rig{
		j:     j,
		trees: &fakeTrees{j: j, verdict: musem.CleanWorktree()},
		sub:   &fakeSubstrate{j: j, exists: true},
		agent: &fakeAgent{j: j},
		owned: newFakeOwned(j),
	}
	r.Launcher = New(r.trees, r.sub, r.agent, r.owned, WithClock(func() time.Time { return at }))
	return r
}

// worktreeRequest is the default launch: a worktree, on a new branch.
func worktreeRequest() musem.LaunchRequest {
	return musem.LaunchRequest{Dir: "/r/api", Branch: "musem/session-1"}
}

// R5, R8, R10: the order is the substance. The worktree is recorded before the
// session is started, so a musem that dies between the two leaves a worktree it
// still recognises as its own rather than a directory nothing remembers.
func TestTheHappyPathRecordsTheWorktreeBeforeStartingTheSession(t *testing.T) {
	r := newRig(t)

	out, err := r.Launch(context.Background(), worktreeRequest())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	want := "repository → validbranch → checkedout → branches → occupied → agent-available → " +
		"substrate-available → add → record → start → exists"
	if got := r.j.String(); got != want {
		t.Errorf("sequence =\n  %s\nwant\n  %s", got, want)
	}

	if len(r.trees.added) != 1 || !strings.HasSuffix(r.trees.added[0], "musem/session-1 create") {
		t.Errorf("added = %v, want the branch created", r.trees.added)
	}
	if out.Worktree != "/r/api-musem-session-1" {
		t.Errorf("worktree = %q, want the derived destination", out.Worktree)
	}
	if out.Dir != out.Worktree {
		t.Errorf("dir = %q, want the session to run in its worktree", out.Dir)
	}

	// The session is started in the worktree, under the identifier musem chose,
	// so discovery will report it as the same session.
	if len(r.sub.started) != 1 {
		t.Fatalf("started = %v, want one session", r.sub.started)
	}
	if !strings.Contains(r.sub.started[0], out.SessionID) {
		t.Errorf("started = %q, want the chosen session id passed to the agent", r.sub.started[0])
	}
	if !strings.Contains(r.sub.started[0], out.Worktree) {
		t.Errorf("started = %q, want the session started in its worktree", r.sub.started[0])
	}

	rec, ok := r.owned.records[out.SessionID]
	if !ok {
		t.Fatal("the worktree was not recorded; musem could never take it back")
	}
	if rec.Path != out.Worktree || rec.Repo != "/r/api" || rec.Branch != "musem/session-1" {
		t.Errorf("record = %+v, want everything a removal needs", rec)
	}
	if !rec.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v", rec.CreatedAt, at)
	}
}

// R2, scenario "Launching without a worktree": the session starts in the
// directory as given, and nothing is created.
func TestLaunchingWithoutAWorktreeCreatesNothing(t *testing.T) {
	r := newRig(t)

	req := musem.LaunchRequest{Dir: "/home/u/project"}
	req.SetWorktree(false)

	out, err := r.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if out.Worktree != "" {
		t.Errorf("worktree = %q, want none", out.Worktree)
	}
	if out.Dir != "/home/u/project" {
		t.Errorf("dir = %q, want the directory as given", out.Dir)
	}
	if len(r.trees.added) != 0 {
		t.Errorf("added = %v; a launch with the toggle off creates no worktree", r.trees.added)
	}
	if len(r.owned.records) != 0 {
		t.Errorf("records = %v; there is nothing to own", r.owned.records)
	}
	if r.j.saw("repository") {
		t.Error("the repository was resolved for a launch that needs no worktree")
	}
}

// R8, scenario "The session fails after the worktree exists", and the same
// question asked at every other step: what was created before the failure is
// undone, and what was not created is not chased.
func TestAFailureAtEachStepLeavesNothingBehind(t *testing.T) {
	boom := errors.New("boom")

	for _, tc := range []struct {
		name string
		//nolint:revive // the rig is the subject, not a control flag
		inject      func(*rig)
		wantRemoved bool
		wantForgot  bool
		wantKilled  bool
	}{
		{
			// git refused: the repository is as it was, so there is nothing to
			// undo and nothing to chase.
			name:   "creating the worktree fails",
			inject: func(r *rig) { r.trees.addErr = boom },
		},
		{
			// A worktree musem cannot record is one it could never take back, so
			// it goes now rather than becoming a directory nobody owns.
			name:        "recording the worktree fails",
			inject:      func(r *rig) { r.owned.saveErr = boom },
			wantRemoved: true,
		},
		{
			name:        "starting the session fails",
			inject:      func(r *rig) { r.sub.startErr = boom },
			wantRemoved: true,
			wantForgot:  true,
		},
		{
			// The substrate started and the session is already gone: an agent
			// that cannot run looks exactly like this, and an exit code from the
			// command that created it says nothing about it.
			name:        "the session is gone as soon as it started",
			inject:      func(r *rig) { r.sub.exists = false },
			wantRemoved: true,
			wantForgot:  true,
			wantKilled:  true,
		},
		{
			// A confirmation that could not be obtained is not a confirmation.
			name:        "the session cannot be confirmed",
			inject:      func(r *rig) { r.sub.existsErr = boom },
			wantRemoved: true,
			wantForgot:  true,
			wantKilled:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.inject(r)

			out, err := r.Launch(context.Background(), worktreeRequest())
			if err == nil {
				t.Fatalf("Launch reported success: %+v", out)
			}

			if got := len(r.trees.removed) > 0; got != tc.wantRemoved {
				t.Errorf("worktree removed = %v, want %v (removed: %v)", got, tc.wantRemoved, r.trees.removed)
			}
			if got := len(r.owned.forgotten) > 0; got != tc.wantForgot {
				t.Errorf("record forgotten = %v, want %v", got, tc.wantForgot)
			}
			if got := len(r.sub.killed) > 0; got != tc.wantKilled {
				t.Errorf("session killed = %v, want %v", got, tc.wantKilled)
			}
			if len(r.owned.records) != 0 {
				t.Errorf("records = %v, want none left behind", r.owned.records)
			}
		})
	}
}

// R7: a machine without the substrate or without the agent tool is a refusal,
// and the refusal comes before anything has been created — a rollback that never
// needed to happen is still a rollback that can fail.
func TestAMissingToolRefusesBeforeAnythingIsCreated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*rig)
	}{
		{"no agent tool", func(r *rig) {
			r.agent.availableErr = musem.Errorf(musem.EUNAVAILABLE, "the Claude CLI was not found on PATH")
		}},
		{"no substrate", func(r *rig) {
			r.sub.availableErr = musem.Errorf(musem.EUNAVAILABLE, "tmux was not found on PATH")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.inject(r)

			_, err := r.Launch(context.Background(), worktreeRequest())
			if err == nil {
				t.Fatal("a launch with a missing tool reported success")
			}
			if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
				t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
			}
			if !strings.Contains(musem.ErrorMessage(err), "PATH") {
				t.Errorf("message = %q, want it to name what is missing", musem.ErrorMessage(err))
			}
			if len(r.trees.added) != 0 || len(r.owned.records) != 0 || len(r.sub.started) != 0 {
				t.Error("something was created before the launch was refused")
			}
		})
	}
}

// R8, scenario "Undo that cannot complete": what remains is named, rather than a
// clean rollback being claimed. A directory nobody knows about is the thing the
// user finds weeks later and does not dare delete.
func TestACleanupThatItselfFailsSaysWhatRemains(t *testing.T) {
	r := newRig(t)
	r.sub.startErr = musem.Errorf(musem.EUNAVAILABLE, "tmux refused to start the session")
	r.trees.removeErr = musem.Errorf(musem.EINVALID, "git refused to remove it")

	_, err := r.Launch(context.Background(), worktreeRequest())
	if err == nil {
		t.Fatal("a launch whose cleanup failed reported success")
	}

	msg := musem.ErrorMessage(err)
	if !strings.Contains(msg, "/r/api-musem-session-1") {
		t.Errorf("message = %q, want the path that remains on disk", msg)
	}
	if !strings.Contains(msg, "still on disk") {
		t.Errorf("message = %q, want it to say the worktree survived", msg)
	}
	if !strings.Contains(msg, "tmux refused") {
		t.Errorf("message = %q, want the original failure kept", msg)
	}

	// The record outlives a removal that failed: it is the permission to remove,
	// and dropping it would leave a worktree musem no longer recognises as its
	// own and would therefore never dare touch again.
	if len(r.owned.forgotten) != 0 {
		t.Error("the ownership record was dropped over a worktree that is still there")
	}
	if _, ok := r.owned.records["11111111-2222-4333-8444-555555555555"]; !ok {
		t.Error("the record of the surviving worktree was lost")
	}
}

// R4, scenario "Seeing where it will go": the destination is derived from the
// repository and the branch, and a branch's separators collapse into one
// directory name rather than becoming a tree of them.
func TestTheDestinationIsDerivedFromTheRepositoryAndBranch(t *testing.T) {
	for _, tc := range []struct {
		repo, branch, want string
	}{
		{"/home/u/api", "musem/session-1", "/home/u/api-musem-session-1"},
		{"/home/u/api", "main", "/home/u/api-main"},
		{"/home/u/api", "feat/deep/nesting", "/home/u/api-feat-deep-nesting"},
		{"/home/u/api", ".hidden", "/home/u/api--hidden"},
	} {
		if got := Destination(tc.repo, tc.branch); got != tc.want {
			t.Errorf("Destination(%q, %q) = %q, want %q", tc.repo, tc.branch, got, tc.want)
		}
	}
}

// R2, scenario "A directory that is not a repository": the form has to be able
// to say so, so the refusal arrives from planning rather than from a launch that
// half-happened.
func TestPlanningRefusesADirectoryThatIsNotARepository(t *testing.T) {
	r := newRig(t)
	r.trees.repoErr = musem.Errorf(musem.ENOTFOUND, "/tmp/scratch is not inside a git repository")

	_, err := r.Plan(context.Background(), worktreeRequest())
	if musem.ErrorCode(err) != musem.ENOTFOUND {
		t.Fatalf("err = %v, want an ENOTFOUND", err)
	}

	// And the same directory launches happily with the toggle off, which is the
	// other half of what the form offers the user.
	req := musem.LaunchRequest{Dir: "/tmp/scratch"}
	req.SetWorktree(false)
	if _, err := r.Plan(context.Background(), req); err != nil {
		t.Errorf("planning without a worktree: %v", err)
	}
}

// R3, scenario "A branch already checked out elsewhere": git permits one
// checkout of a branch at a time, and the refusal names where it is.
func TestPlanningRefusesABranchCheckedOutElsewhere(t *testing.T) {
	r := newRig(t)
	r.trees.checkedOut = map[string]string{"release": "/r/api-release"}

	req := musem.LaunchRequest{Dir: "/r/api", Branch: "release", ExistingBranch: true}
	_, err := r.Plan(context.Background(), req)
	if err == nil {
		t.Fatal("planning accepted a branch another worktree already has")
	}
	msg := musem.ErrorMessage(err)
	if !strings.Contains(msg, "/r/api-release") {
		t.Errorf("message = %q, want the worktree already holding it named", msg)
	}
}

// A proposed name that already exists is refused too, since git will not create
// a branch twice — and the refusal says which of the two moves fixes it.
func TestPlanningRefusesANewBranchThatAlreadyExists(t *testing.T) {
	r := newRig(t)
	r.trees.branches = []string{"main", "musem/session-1"}

	_, err := r.Plan(context.Background(), worktreeRequest())
	if err == nil {
		t.Fatal("planning accepted creating a branch that already exists")
	}
	if !strings.Contains(musem.ErrorMessage(err), "already exists") {
		t.Errorf("message = %q", musem.ErrorMessage(err))
	}
}

// R3, scenario "Reusing an existing branch": picking one checks it out rather
// than creating it, which is a different git invocation.
func TestAnExistingBranchIsCheckedOutRatherThanCreated(t *testing.T) {
	r := newRig(t)
	r.trees.branches = []string{"main", "release"}

	req := musem.LaunchRequest{Dir: "/r/api", Branch: "release", ExistingBranch: true}
	if _, err := r.Launch(context.Background(), req); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if len(r.trees.added) != 1 || !strings.HasSuffix(r.trees.added[0], "release checkout") {
		t.Errorf("added = %v, want the branch checked out, not created", r.trees.added)
	}
}

// R4, scenario "Occupied destination": the launch is refused with the path
// named, and nothing is written.
func TestPlanningRefusesAnOccupiedDestination(t *testing.T) {
	r := newRig(t)
	r.trees.occupied = map[string]bool{"/r/api-musem-session-1": true}

	_, err := r.Plan(context.Background(), worktreeRequest())
	if err == nil {
		t.Fatal("planning accepted a destination that already exists")
	}
	if !strings.Contains(musem.ErrorMessage(err), "/r/api-musem-session-1") {
		t.Errorf("message = %q, want the occupied path named", musem.ErrorMessage(err))
	}
	if len(r.trees.added) != 0 {
		t.Error("something was written into the occupied destination")
	}
}

// Without somewhere to record what it creates, musem would be making a worktree
// it could never take back. It says so instead, and points at the toggle.
func TestAWorktreeIsRefusedWhenItCannotBeRecorded(t *testing.T) {
	j := &journal{}
	trees := &fakeTrees{j: j}
	l := New(trees, &fakeSubstrate{j: j, exists: true}, &fakeAgent{j: j}, nil)

	_, err := l.Plan(context.Background(), worktreeRequest())
	if err == nil {
		t.Fatal("a worktree was planned with nowhere to record it")
	}
	if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
		t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
	}
	if !strings.Contains(musem.ErrorMessage(err), "without a worktree") {
		t.Errorf("message = %q, want it to point at the way forward", musem.ErrorMessage(err))
	}

	// Launching without one still works: observation and the plain path are not
	// held hostage by the history database.
	req := musem.LaunchRequest{Dir: "/home/u/project"}
	req.SetWorktree(false)
	if _, err := l.Launch(context.Background(), req); err != nil {
		t.Errorf("launching without a worktree: %v", err)
	}
}

// R3, scenario "The proposed branch": a name is proposed, and it is one that
// does not already exist.
func TestTheProposedBranchAvoidsNamesAlreadyTaken(t *testing.T) {
	r := newRig(t)
	r.trees.branches = []string{"main", "musem/session-1", "musem/session-2"}

	got := r.ProposeBranch(context.Background(), "/r/api")
	if got != "musem/session-3" {
		t.Errorf("proposal = %q, want the first unused number", got)
	}
}

// R3, D7: a name git would refuse is refused while the form is open, not after
// the launch has been confirmed. Left to Add, the form would say "ready" and the
// failure would arrive from an operation that had already started.
func TestPlanningRefusesANameGitWouldNotAccept(t *testing.T) {
	r := newRig(t)
	r.trees.branchErr = musem.Errorf(musem.EINVALID,
		"a branch name cannot start with \"-\"; git would read it as an option")

	_, err := r.Plan(context.Background(), musem.LaunchRequest{Dir: "/r/api", Branch: "--force"})
	if musem.ErrorCode(err) != musem.EINVALID {
		t.Fatalf("err = %v, want the refusal git would have given later", err)
	}
	if r.j.saw("occupied") {
		t.Error("planning went on to derive a destination from a name that cannot be used")
	}
}

// A repository that cannot be listed still gets a proposal: an ugly name that
// works beats a form that cannot offer anything.
func TestABranchIsProposedEvenWhenTheRepositoryCannotBeListed(t *testing.T) {
	r := newRig(t)
	r.trees.branchesErr = errors.New("no")

	got := r.ProposeBranch(context.Background(), "/r/api")
	if got == "" {
		t.Fatal("no branch was proposed")
	}
	if !strings.HasPrefix(got, "musem/session-") {
		t.Errorf("proposal = %q, want it to still look like musem's", got)
	}
}

// R8, scenario "Undo that cannot complete", for the two things a rollback can
// leave behind besides the worktree: a session it could not end, and a record it
// could not drop.
//
// Both are reported rather than swallowed. A session still running under a name
// nothing remembers, or a record granting permission over a path, are each
// something the user has to be told about — the alternative is claiming a clean
// rollback that did not happen.
func TestARollbackNamesEveryThingItCouldNotUndo(t *testing.T) {
	t.Run("the session could not be ended", func(t *testing.T) {
		r := newRig(t)
		r.sub.exists = false // forces the rollback
		r.sub.killErr = musem.Errorf(musem.EUNAVAILABLE, "tmux would not answer")

		_, err := r.Launch(context.Background(), worktreeRequest())
		if err == nil {
			t.Fatal("a launch whose cleanup failed reported success")
		}
		msg := musem.ErrorMessage(err)
		if !strings.Contains(msg, "still running") || !strings.Contains(msg, "musem-11111111") {
			t.Errorf("message = %q, want the session that survived named", msg)
		}
	})

	t.Run("the record could not be dropped", func(t *testing.T) {
		r := newRig(t)
		r.sub.startErr = musem.Errorf(musem.EUNAVAILABLE, "tmux refused")
		r.owned.forgetErr = musem.Errorf(musem.EUNAVAILABLE, "the history database is gone")

		_, err := r.Launch(context.Background(), worktreeRequest())
		if err == nil {
			t.Fatal("a launch whose cleanup failed reported success")
		}
		if !strings.Contains(musem.ErrorMessage(err), "record of the worktree could not be dropped") {
			t.Errorf("message = %q, want the surviving record reported", musem.ErrorMessage(err))
		}
	})
}

// A launch cannot proceed on a question git failed to answer. Each of these
// leaves the plan refused rather than guessed at, and the form shows the reason.
func TestPlanningStopsOnAQuestionGitCouldNotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*rig)
	}{
		{"the worktree list failed", func(r *rig) {
			r.trees.checkedOutErr = musem.Errorf(musem.EUNAVAILABLE, "git timed out listing the worktrees")
		}},
		{"the destination could not be checked", func(r *rig) {
			r.trees.occupiedErr = musem.Errorf(musem.EUNAVAILABLE, "cannot check whether the path exists")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.inject(r)

			_, err := r.Plan(context.Background(), worktreeRequest())
			if err == nil {
				t.Fatal("planning proceeded on a question nobody answered")
			}
			if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
				t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
			}
		})
	}
}

// An identifier that cannot be generated stops the launch before it creates
// anything: a session musem cannot name is one it could never find again.
func TestALaunchWithoutAnIdentifierCreatesNothing(t *testing.T) {
	r := newRig(t)
	r.agent.idErr = musem.Errorf(musem.EINTERNAL, "no entropy")

	if _, err := r.Launch(context.Background(), worktreeRequest()); err == nil {
		t.Fatal("a launch with no identifier reported success")
	}
	if len(r.trees.added) != 0 || len(r.owned.records) != 0 || len(r.sub.started) != 0 {
		t.Error("something was created for a session that could not be named")
	}
}
