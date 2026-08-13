package musem

import (
	"strings"
	"time"
)

// LaunchRequest is what the user asked musem to start: where the agent should
// work, and whether that work gets a worktree of its own.
//
// The worktree toggle is stored inverted, so the zero value carries the default
// rather than the form having to remember to set it. A plain Worktree bool would
// make "nobody filled this in" and "the user turned it off" the same value, and
// of the two only one is safe to assume — the default has to be the one that
// costs a keystroke to leave, not the one you reach by forgetting.
type LaunchRequest struct {
	// Dir is the directory the user chose. With a worktree it is read as a
	// pointer at the repository; without one it is where the agent runs.
	Dir string

	// Branch is the branch the worktree should be on. Ignored when the worktree
	// is disabled, since there is nothing to check out.
	Branch string

	// ExistingBranch reports that Branch is already in the repository and must
	// be checked out rather than created. The two are separate git invocations
	// and git refuses the wrong one, so which is meant cannot be inferred at the
	// point of use.
	ExistingBranch bool

	noWorktree bool
}

// Worktree reports whether this launch should get a worktree of its own.
func (r LaunchRequest) Worktree() bool { return !r.noWorktree }

// SetWorktree turns the worktree on or off.
func (r *LaunchRequest) SetWorktree(enabled bool) { r.noWorktree = !enabled }

// LaunchPlan is what a launch would do, worked out before anything is created.
//
// It exists so the form can show the destination and the branch before the user
// commits to them, and so a launch that cannot proceed is refused while it is
// still only a form. A plan is not a promise: the disk can change between
// planning and launching, which is why the launch sequence undoes its own work
// rather than trusting that a validated plan will succeed.
type LaunchPlan struct {
	// Repo is the repository root the worktree will be added to, empty when the
	// launch creates no worktree.
	Repo string

	// Dir is where the agent will actually run: the new worktree when there is
	// one, and the requested directory when there is not.
	Dir string

	// Branch is the branch the worktree will be on, empty without a worktree.
	Branch string

	// NewBranch reports that Branch will be created rather than checked out.
	NewBranch bool

	// Worktree is the path the worktree will occupy, empty when none is created.
	Worktree string
}

// LaunchOutcome is what a completed launch produced.
type LaunchOutcome struct {
	// SessionID is the identifier the agent was started with, and therefore the
	// one discovery will report it under.
	SessionID string

	// Substrate names the host the session runs in, so a user who wants to
	// attach knows what to attach to.
	Substrate string

	Dir    string
	Branch string

	// Worktree is the path musem created, empty when the launch created none.
	Worktree string
}

// cleanliness is the closed set of answers to "may this worktree be removed?".
//
// The zero value is undetermined rather than clean, deliberately: a verdict that
// was never filled in is exactly the case where nothing is known, and the one
// reading of it that destroys work is "clean".
type cleanliness int

const (
	undetermined cleanliness = iota
	spotless
	soiled
)

// Cleanliness is a verdict on whether a worktree holds work that removing it
// would destroy.
//
// It is a closed three-state answer rather than a bool with an error beside it,
// because the third state is the one that matters: git failing to report is not
// the same as git reporting nothing to save, and a caller handed a bool has no
// way to keep the two apart once it has stopped looking at the error.
type Cleanliness struct {
	state  cleanliness
	reason string
}

// CleanWorktree returns the verdict that nothing would be lost.
func CleanWorktree() Cleanliness { return Cleanliness{state: spotless} }

// DirtyWorktree returns the verdict that removing the worktree would destroy
// the named work.
func DirtyWorktree(reason string) Cleanliness {
	return Cleanliness{state: soiled, reason: reason}
}

// UndeterminedWorktree returns the verdict that the state could not be
// established, with why. It is not clean, and reporting it as kept for an
// unknown reason is the honest outcome.
func UndeterminedWorktree(reason string) Cleanliness {
	return Cleanliness{state: undetermined, reason: reason}
}

// Clean reports whether the worktree may be removed. It is false for both the
// dirty and the undetermined verdicts, which is the whole point of the type.
func (c Cleanliness) Clean() bool { return c.state == spotless }

// Determined reports whether the state was established at all. Only meaningful
// where Clean is false: it separates "there is work here" from "nobody could
// tell", which read the same to the worktree and differently to the user.
func (c Cleanliness) Determined() bool { return c.state != undetermined }

// Reason says what is in the way, empty for a clean verdict.
func (c Cleanliness) Reason() string {
	if c.state == spotless {
		return ""
	}
	if c.reason == "" {
		return "the state of the worktree could not be established"
	}
	return c.reason
}

// Worktree is a worktree musem created, and the record of that fact.
//
// It is persisted rather than inferred, because it is the precondition for the
// only destructive thing musem does. A path shape is not evidence: rename a
// directory and inference either forgets musem's own worktree or adopts one that
// was never its to remove.
type Worktree struct {
	// SessionID is the session the worktree was made for.
	SessionID string

	// Path is the worktree itself.
	Path string

	// Repo is the repository it was added to, needed to remove it again: git
	// worktree remove is run from the repository, not from inside the worktree
	// it is about to delete.
	Repo string

	Branch    string
	CreatedAt time.Time
}

// Validate returns an error if the record is missing what removal needs. A
// half-formed record is worse than none: it is the thing that grants permission.
func (w *Worktree) Validate() error {
	if strings.TrimSpace(w.SessionID) == "" {
		return Errorf(EINVALID, "a worktree record needs the session it was created for")
	}
	if strings.TrimSpace(w.Path) == "" {
		return Errorf(EINVALID, "a worktree record needs the path it occupies")
	}
	if strings.TrimSpace(w.Repo) == "" {
		return Errorf(EINVALID, "a worktree record needs the repository it belongs to")
	}
	return nil
}

// Reclamation is what became of a worktree when its session ended.
//
// A kept worktree carries its reason because keeping is the outcome the user has
// to act on: the disk is still occupied and the work in it is still theirs to
// deal with. A removal needs no explanation, so Reason is empty there.
type Reclamation struct {
	SessionID string
	Path      string

	// Removed reports whether the worktree is gone.
	Removed bool

	// Reason says why it was kept, empty when it was removed.
	Reason string

	At time.Time
}
