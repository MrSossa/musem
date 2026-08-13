package launch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

// owning puts a record in place as though musem had launched the session.
func (r *rig) owning(sessionID, path string) {
	r.owned.records[sessionID] = musem.Worktree{
		SessionID: sessionID, Path: path, Repo: "/r/api", Branch: "musem/session-1",
	}
}

// ended returns a session that has stopped appearing in discovery.
func ended(id, dir string) musem.Session {
	endedAt := at
	return musem.Session{ID: id, Dir: dir, Status: musem.StatusEnded, EndedAt: &endedAt}
}

// R9, scenario "Clean worktree": everything committed and pushed, so the
// worktree goes and the record goes with it.
func TestACleanWorktreeIsReclaimed(t *testing.T) {
	r := newRig(t)
	r.owning("s1", "/r/api-musem-session-1")
	r.trees.verdict = musem.CleanWorktree()

	got, acted := r.Reclaim(context.Background(), ended("s1", "/r/api-musem-session-1"))
	if !acted {
		t.Fatal("a clean worktree musem created was left alone")
	}
	if !got.Removed {
		t.Errorf("reclamation = %+v, want it removed", got)
	}
	if len(r.trees.removed) != 1 || !strings.Contains(r.trees.removed[0], "/r/api-musem-session-1") {
		t.Errorf("removed = %v, want the worktree", r.trees.removed)
	}
	if len(r.owned.forgotten) != 1 {
		t.Errorf("forgotten = %v, want the record dropped after the worktree", r.owned.forgotten)
	}

	// The record goes only after the worktree it granted permission over.
	if want := "lookup → cleanliness → remove → forget"; r.j.String() != want {
		t.Errorf("sequence = %s, want %s", r.j.String(), want)
	}
	if len(r.Notices()) != 0 {
		t.Error("a removed worktree left a notice; there is nothing for the user to do about it")
	}
}

// R9, scenarios "Uncommitted work" and "Committed but never pushed": the
// worktree is kept, and the dashboard is told which of the two it was.
func TestADirtyWorktreeIsKeptWithItsReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict musem.Cleanliness
		want    string
	}{
		{"uncommitted", musem.DirtyWorktree("it has uncommitted changes"), "uncommitted"},
		{"untracked", musem.DirtyWorktree("it has untracked files"), "untracked"},
		{"unpushed", musem.DirtyWorktree("it has commits the remote does not have"), "remote"},
		{"no remote", musem.DirtyWorktree("its branch tracks no remote, so its commits exist nowhere else"), "no remote"},
		{"undetermined", musem.UndeterminedWorktree("git could not report the state of the worktree"), "could not report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.owning("s1", "/r/api-musem-session-1")
			r.trees.verdict = tc.verdict

			got, acted := r.Reclaim(context.Background(), ended("s1", "/r/api-musem-session-1"))
			if !acted {
				t.Fatal("nothing was reported for a worktree that was kept")
			}
			if got.Removed {
				t.Fatal("a worktree that is not clean was removed")
			}
			if !strings.Contains(got.Reason, tc.want) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tc.want)
			}
			if len(r.trees.removed) != 0 {
				t.Errorf("removed = %v, want nothing removed", r.trees.removed)
			}
			if len(r.owned.forgotten) != 0 {
				t.Error("the record was dropped over a worktree that is still there")
			}

			// R9: the dashboard says why it was kept, so the reason has to
			// survive past the call that produced it.
			notices := r.Notices()
			if len(notices) != 1 || !strings.Contains(notices[0].Reason, tc.want) {
				t.Errorf("notices = %+v, want the reason available to the view", notices)
			}
		})
	}
}

// R10, scenario "A worktree musem did not create", and R11, scenario "Someone
// else's repository": the worktree is left alone however clean it is, and musem
// does not even ask — asking would mean a code path where the answer could lead
// somewhere.
func TestAWorktreeMusemDidNotCreateIsNeverTouched(t *testing.T) {
	r := newRig(t)
	// Deliberately clean and deliberately unowned: the two together are the case
	// where a check on cleanliness alone would delete somebody's repository.
	r.trees.verdict = musem.CleanWorktree()

	got, acted := r.Reclaim(context.Background(), ended("someone-elses", "/home/u/their-project"))
	if acted {
		t.Fatalf("musem acted on a worktree it never created: %+v", got)
	}
	if len(r.trees.removed) != 0 {
		t.Fatalf("removed = %v; musem removed a directory it does not own", r.trees.removed)
	}
	if r.j.saw("cleanliness") {
		t.Error("cleanliness was checked for an unowned worktree; ownership is the first gate, not the second")
	}
	if r.j.saw("remove") {
		t.Error("removal was attempted for an unowned worktree")
	}
}

// Ownership that could not be established was not established. A store that
// cannot answer must not become a licence to act.
func TestAnUnansweredOwnershipQuestionRemovesNothing(t *testing.T) {
	r := newRig(t)
	r.owned.lookupErr = musem.Errorf(musem.EUNAVAILABLE, "the history database is gone")
	r.trees.verdict = musem.CleanWorktree()

	if _, acted := r.Reclaim(context.Background(), ended("s1", "/r/api-musem-session-1")); acted {
		t.Fatal("musem acted on ownership it could not establish")
	}
	if len(r.trees.removed) != 0 {
		t.Errorf("removed = %v, want nothing", r.trees.removed)
	}
}

// R9, R10: reclamation follows an end, not an absence. A session still running
// is a session whose worktree is in use, and this is the launch-side half of the
// guard the registry already applies to a degraded discovery pass.
func TestOnlyAnEndedSessionIsAReclamationCandidate(t *testing.T) {
	r := newRig(t)
	r.owning("s1", "/r/api-musem-session-1")
	r.trees.verdict = musem.CleanWorktree()

	live := musem.Session{ID: "s1", Dir: "/r/api-musem-session-1", Status: musem.StatusRunning}
	if _, acted := r.Reclaim(context.Background(), live); acted {
		t.Fatal("a running session's worktree was reclaimed")
	}
	if r.j.saw("lookup") || len(r.trees.removed) != 0 {
		t.Error("a live session was considered for reclamation at all")
	}
}

// git refusing to remove is the second lock, and it is not silently swallowed:
// the worktree is kept and git's reason is what the user is shown.
func TestARefusedRemovalKeepsTheWorktreeAndSaysWhy(t *testing.T) {
	r := newRig(t)
	r.owning("s1", "/r/api-musem-session-1")
	r.trees.verdict = musem.CleanWorktree()
	r.trees.removeErr = musem.Errorf(musem.EINVALID, "git refused: contains modified files")

	got, acted := r.Reclaim(context.Background(), ended("s1", "/r/api-musem-session-1"))
	if !acted || got.Removed {
		t.Fatalf("reclamation = %+v, acted = %v; want kept", got, acted)
	}
	if !strings.Contains(got.Reason, "modified files") {
		t.Errorf("reason = %q, want git's own refusal", got.Reason)
	}
	if len(r.owned.forgotten) != 0 {
		t.Error("the record was dropped over a worktree git would not remove")
	}
}

// R10: a session that merely shares a directory with one of musem's worktrees is
// not that worktree's owner.
//
// This is the case that made the directory fallback unsafe: a second agent
// started by hand inside musem's worktree, ending while the session the worktree
// was actually made for is still working in it. The tree is clean at that moment
// — it is not dirty, it is in use — so cleanliness would not have saved it.
func TestASessionSharingADirectoryIsNotItsOwner(t *testing.T) {
	r := newRig(t)
	r.owning("s1", "/r/api-musem-session-1")
	r.trees.verdict = musem.CleanWorktree()

	got, acted := r.Reclaim(context.Background(), ended("started-by-hand", "/r/api-musem-session-1"))
	if acted {
		t.Fatalf("reclamation = %+v; a live session's worktree was pulled out from under it", got)
	}
	if len(r.trees.removed) != 0 {
		t.Errorf("removed = %v, want nothing", r.trees.removed)
	}
	if r.j.saw("cleanliness") {
		t.Error("cleanliness was consulted; ownership is the first gate and it should have stopped here")
	}

	// The session it was made for still reclaims it.
	if _, acted := r.Reclaim(context.Background(), ended("s1", "/r/api-musem-session-1")); !acted {
		t.Error("the owning session can no longer reclaim its own worktree")
	}
}

// Notices are newest first and one per session, so a worktree kept over several
// passes does not fill the dashboard with the same line.
func TestNoticesAreNewestFirstAndOnePerSession(t *testing.T) {
	r := newRig(t)
	r.owning("s1", "/w/one")
	r.owning("s2", "/w/two")
	r.trees.verdict = musem.DirtyWorktree("it has uncommitted changes")

	clock := at
	r.now = func() time.Time { return clock }

	r.Reclaim(context.Background(), ended("s1", "/w/one"))
	clock = at.Add(time.Minute)
	r.Reclaim(context.Background(), ended("s2", "/w/two"))
	clock = at.Add(2 * time.Minute)
	r.Reclaim(context.Background(), ended("s1", "/w/one"))

	notices := r.Notices()
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want one per session", len(notices))
	}
	if notices[0].SessionID != "s1" {
		t.Errorf("first notice = %q, want the most recent", notices[0].SessionID)
	}
}
