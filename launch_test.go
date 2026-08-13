package musem_test

import (
	"strings"
	"testing"

	"github.com/MrSossa/musem"
)

// The worktree toggle defaults to enabled, and the default lives in the zero
// value rather than in whoever builds the request. A form that forgets to set it
// must produce the safe launch, not the one that puts two agents in one checkout.
func TestALaunchRequestDefaultsToAWorktree(t *testing.T) {
	var req musem.LaunchRequest

	if !req.Worktree() {
		t.Error("the zero value of a launch request has no worktree; the default cannot survive a form that forgets it")
	}

	req.SetWorktree(false)
	if req.Worktree() {
		t.Error("turning the worktree off had no effect")
	}

	req.SetWorktree(true)
	if !req.Worktree() {
		t.Error("turning the worktree back on had no effect")
	}
}

// The verdict a caller never filled in must not read as permission to delete.
func TestTheZeroCleanlinessIsNotClean(t *testing.T) {
	var v musem.Cleanliness

	if v.Clean() {
		t.Fatal("the zero verdict reads as clean; an unanswered check would authorise removing a worktree")
	}
	if v.Determined() {
		t.Error("the zero verdict claims to have established something")
	}
	if v.Reason() == "" {
		t.Error("a verdict that is not clean must say what is in the way")
	}
}

// The three states stay apart. Dirty and undetermined both keep the worktree,
// and both have to keep saying which they are: one is work the user has, the
// other is a question nobody answered.
func TestCleanlinessKeepsItsThreeStatesApart(t *testing.T) {
	clean := musem.CleanWorktree()
	if !clean.Clean() || !clean.Determined() || clean.Reason() != "" {
		t.Errorf("clean verdict = %+v, want clean, determined and unexplained", clean)
	}

	dirty := musem.DirtyWorktree("two files are modified")
	if dirty.Clean() {
		t.Error("a dirty worktree reads as clean")
	}
	if !dirty.Determined() {
		t.Error("a dirty verdict established the state and must say so")
	}
	if !strings.Contains(dirty.Reason(), "modified") {
		t.Errorf("reason = %q, want the named work", dirty.Reason())
	}

	unknown := musem.UndeterminedWorktree("git exited 128")
	if unknown.Clean() {
		t.Error("an undetermined worktree reads as clean; this is the reading that destroys work")
	}
	if unknown.Determined() {
		t.Error("an undetermined verdict claims to have established the state")
	}
	if !strings.Contains(unknown.Reason(), "128") {
		t.Errorf("reason = %q, want the failure that stopped the check", unknown.Reason())
	}
}

// The ownership record is what permits a removal, so an incomplete one has to be
// refused rather than stored and later believed.
func TestAWorktreeRecordNeedsEnoughToRemoveIt(t *testing.T) {
	for _, tt := range []struct {
		name string
		w    musem.Worktree
		want bool
	}{
		{"complete", musem.Worktree{SessionID: "s1", Path: "/w/api", Repo: "/r/api"}, true},
		{"no session", musem.Worktree{Path: "/w/api", Repo: "/r/api"}, false},
		{"no path", musem.Worktree{SessionID: "s1", Repo: "/r/api"}, false},
		{"no repository", musem.Worktree{SessionID: "s1", Path: "/w/api"}, false},
		{"blank session", musem.Worktree{SessionID: "  ", Path: "/w/api", Repo: "/r/api"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.w.Validate()
			if (err == nil) != tt.want {
				t.Errorf("Validate() = %v, want valid = %v", err, tt.want)
			}
			if err != nil && musem.ErrorCode(err) != musem.EINVALID {
				t.Errorf("code = %q, want %q", musem.ErrorCode(err), musem.EINVALID)
			}
		})
	}
}
