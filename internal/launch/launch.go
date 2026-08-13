// Package launch owns the sequence that starts a session, and the sequence that
// takes back what starting one created.
//
// It is orchestration rather than an adapter: creating a worktree, recording
// that musem created it, starting a substrate and starting an agent in it belong
// to no single foreign system, and the ordering between them — which is where
// all the difficulty is — belongs to none of them either. The steps are arranged
// so that each failure leaves the state before it intact, and what was created
// before a failure is undone.
//
// Like every other orchestration package here it declares the interfaces it
// needs and stays ignorant of who satisfies them; only main knows that git,
// tmux, claude and sqlite are on the other side.
package launch

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MrSossa/musem"
)

// Worktrees is what launching needs from a version control system: somewhere to
// put a session, and the truth about whether taking it back would destroy work.
type Worktrees interface {
	// Repository returns the root of the repository containing dir.
	Repository(ctx context.Context, dir string) (string, error)

	// Branches lists the branches that already exist.
	Branches(ctx context.Context, dir string) ([]string, error)

	// CheckedOut returns the worktree already holding branch, empty when none
	// does.
	CheckedOut(ctx context.Context, dir, branch string) (string, error)

	// Occupied reports whether anything already exists at path.
	Occupied(path string) (bool, error)

	// ValidBranch reports whether a name can be used as a branch, without using
	// it.
	ValidBranch(branch string) error

	// Add creates a worktree at path on branch, creating the branch when create.
	Add(ctx context.Context, repo, path, branch string, create bool) error

	// Remove deletes a worktree.
	Remove(ctx context.Context, repo, path string) error

	// Cleanliness reports whether removing the worktree at path would destroy
	// work. It returns no error: a failure to establish the state is one of the
	// verdicts, precisely so it cannot be dropped on the way to the caller.
	Cleanliness(ctx context.Context, path string) musem.Cleanliness
}

// Substrate is whatever hosts a session so that it outlives musem.
type Substrate interface {
	Available(ctx context.Context) error
	Start(ctx context.Context, name, dir string, command []string) error
	Exists(ctx context.Context, name string) (bool, error)
	Kill(ctx context.Context, name string) error
}

// Agent is the coding tool a session runs.
type Agent interface {
	Available(ctx context.Context) error

	// NewSessionID returns an identifier the tool will accept, so the session
	// musem starts is the session musem later discovers.
	NewSessionID() (string, error)

	// Command returns the argv that starts the agent for that session.
	Command(sessionID string) []string
}

// Ownership records what musem created, and is the only thing that can grant
// permission to remove it.
type Ownership interface {
	SaveWorktree(ctx context.Context, w musem.Worktree) error
	WorktreeFor(ctx context.Context, sessionID string) (musem.Worktree, bool, error)
	ForgetWorktree(ctx context.Context, sessionID string) error
}

// Launcher starts sessions and reclaims the worktrees they leave behind.
type Launcher struct {
	trees     Worktrees
	substrate Substrate
	agent     Agent
	owned     Ownership

	now func() time.Time

	// mu guards the notices. Launching and reclaiming both run off the UI
	// goroutine and the view reads them on every frame.
	mu   sync.Mutex
	kept map[string]musem.Reclamation
}

// Option configures a Launcher.
type Option func(*Launcher)

// WithClock replaces the time source, so a test can assert what was stamped.
func WithClock(now func() time.Time) Option {
	return func(l *Launcher) {
		if now != nil {
			l.now = now
		}
	}
}

// New returns a Launcher over the given dependencies. Ownership may be nil,
// which disables launching into a worktree: musem does not create what it could
// not record having created.
func New(t Worktrees, s Substrate, a Agent, o Ownership, opts ...Option) *Launcher {
	l := &Launcher{
		trees:     t,
		substrate: s,
		agent:     a,
		owned:     o,
		now:       time.Now,
		kept:      make(map[string]musem.Reclamation),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Branches lists the branches the form may offer instead of the proposed name.
func (l *Launcher) Branches(ctx context.Context, dir string) ([]string, error) {
	repo, err := l.trees.Repository(ctx, dir)
	if err != nil {
		return nil, err
	}
	return l.trees.Branches(ctx, repo)
}

// ProposeBranch returns a branch name for a new session in dir.
//
// Numbered rather than stamped with the time, because the name is the label the
// user will live with in every git command afterwards, and "musem/session-3" is
// something a person can type. The first unused number is chosen, so relaunching
// after one session ends does not reuse a branch that still exists. A repository
// that cannot be listed falls back to the clock: an ugly name that works beats a
// form that cannot propose anything.
func (l *Launcher) ProposeBranch(ctx context.Context, dir string) string {
	const prefix = "musem/session-"

	existing, err := l.Branches(ctx, dir)
	if err != nil {
		return prefix + l.now().Format("20060102-150405")
	}

	taken := make(map[string]bool, len(existing))
	for _, b := range existing {
		taken[b] = true
	}
	for n := 1; ; n++ {
		name := prefix + strconv.Itoa(n)
		if !taken[name] {
			return name
		}
	}
}

// Plan works out what a launch would do, without doing any of it.
//
// Everything that can refuse a launch is asked here, so the form can show the
// refusal while it is still only a form. A plan is not a promise: the disk can
// change between planning and launching, which is why Launch checks again and
// undoes its own work when it has to.
func (l *Launcher) Plan(ctx context.Context, req musem.LaunchRequest) (musem.LaunchPlan, error) {
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		return musem.LaunchPlan{}, musem.Errorf(musem.EINVALID, "a working directory is required")
	}

	if !req.Worktree() {
		// Nothing is derived and nothing is checked: the session starts where the
		// user said, and whether that directory is a repository is not musem's
		// business when it is not making a worktree in it.
		return musem.LaunchPlan{Dir: dir}, nil
	}

	if l.owned == nil {
		// The record is what permits the removal. Without somewhere to write it,
		// musem would be creating a worktree it could never take back — so it
		// says so instead, and the toggle is right there.
		return musem.LaunchPlan{}, musem.Errorf(musem.EUNAVAILABLE,
			"musem cannot record the worktree it would create, so it will not create one; launch without a worktree instead")
	}

	repo, err := l.trees.Repository(ctx, dir)
	if err != nil {
		return musem.LaunchPlan{}, err
	}

	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		return musem.LaunchPlan{}, musem.Errorf(musem.EINVALID, "a branch name is required for a worktree")
	}
	// Asked here rather than left to the creation. A name git will refuse is a
	// refusal the form can show while it is being typed, and finding out at
	// confirmation time means a round trip through an operation that reports the
	// same thing later and less usefully. It also keeps a name carrying anything
	// unprintable from reaching the destination preview, which is drawn on every
	// keystroke.
	if err := l.trees.ValidBranch(branch); err != nil {
		return musem.LaunchPlan{}, err
	}

	// git permits one checkout of a branch at a time, whether the branch is
	// being created or reused — so this is asked for both. A new branch that
	// already exists is caught by the same call, since an existing branch is
	// either checked out somewhere or listed among the repository's own.
	holder, err := l.trees.CheckedOut(ctx, repo, branch)
	if err != nil {
		return musem.LaunchPlan{}, err
	}
	if holder != "" {
		return musem.LaunchPlan{}, musem.Errorf(musem.EINVALID,
			"the branch %s is already checked out at %s, and git permits one checkout of a branch at a time",
			branch, holder)
	}

	if !req.ExistingBranch {
		existing, err := l.trees.Branches(ctx, repo)
		if err != nil {
			return musem.LaunchPlan{}, err
		}
		for _, b := range existing {
			if b == branch {
				return musem.LaunchPlan{}, musem.Errorf(musem.EINVALID,
					"the branch %s already exists; choose it as an existing branch or pick another name", branch)
			}
		}
	}

	dest := Destination(repo, branch)
	occupied, err := l.trees.Occupied(dest)
	if err != nil {
		return musem.LaunchPlan{}, err
	}
	if occupied {
		return musem.LaunchPlan{}, musem.Errorf(musem.EINVALID,
			"%s already exists; musem will not write into it", dest)
	}

	return musem.LaunchPlan{
		Repo:      repo,
		Dir:       dest,
		Branch:    branch,
		NewBranch: !req.ExistingBranch,
		Worktree:  dest,
	}, nil
}

// Destination derives where a worktree for branch in repo will live.
//
// A sibling of the repository, named after it and the branch. Sibling rather
// than inside, because a worktree inside its own repository is a directory git
// then has to be told to ignore, and every tool that walks the tree finds a
// second copy of the project. The branch's separators collapse into dashes: it
// is one directory name, and a branch called feat/x must not become two.
func Destination(repo, branch string) string {
	name := filepath.Base(repo) + "-" + slug(branch)
	return filepath.Join(filepath.Dir(repo), name)
}

// slug turns a branch name into one path component.
func slug(branch string) string {
	var b strings.Builder
	for _, r := range branch {
		switch {
		case r == '/' || r == '\\' || r == filepath.Separator:
			b.WriteByte('-')
		case r == '.' && b.Len() == 0:
			// A leading dot would make the worktree hidden, which is not what
			// anyone asked for by naming a branch.
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "session"
	}
	return out
}

// Launch creates what the plan describes and starts the session in it.
//
// The order is the substance of this function. Everything that can refuse
// happens before anything is created; the worktree is recorded before the
// session is started, so a musem that dies in between leaves a worktree it still
// recognises as its own; and the session is confirmed to exist rather than
// assumed from an exit code. A failure after something was created undoes it.
func (l *Launcher) Launch(ctx context.Context, req musem.LaunchRequest) (musem.LaunchOutcome, error) {
	plan, err := l.Plan(ctx, req)
	if err != nil {
		return musem.LaunchOutcome{}, err
	}

	// Both availability checks come first, and both before the worktree. A
	// machine without tmux or without the agent tool is a refusal, and finding
	// it out after a checkout means a rollback that never needed to happen.
	if err := l.agent.Available(ctx); err != nil {
		return musem.LaunchOutcome{}, err
	}
	if err := l.substrate.Available(ctx); err != nil {
		return musem.LaunchOutcome{}, err
	}

	sessionID, err := l.agent.NewSessionID()
	if err != nil {
		return musem.LaunchOutcome{}, err
	}
	name := substrateName(sessionID)

	var st state

	if plan.Worktree != "" {
		if err := l.trees.Add(ctx, plan.Repo, plan.Worktree, plan.Branch, plan.NewBranch); err != nil {
			// Nothing was created: git either refused or failed, and either way
			// the repository is as it was.
			return musem.LaunchOutcome{}, err
		}
		st.worktree = plan.Worktree
		st.repo = plan.Repo

		record := musem.Worktree{
			SessionID: sessionID,
			Path:      plan.Worktree,
			Repo:      plan.Repo,
			Branch:    plan.Branch,
			CreatedAt: l.now(),
		}
		if err := l.owned.SaveWorktree(ctx, record); err != nil {
			// A worktree musem cannot record is a worktree musem could never take
			// back, so it goes now rather than being left as a directory nobody
			// owns.
			return musem.LaunchOutcome{}, l.undo(ctx, st, sessionID, err)
		}
		st.recorded = true
	}

	if err := l.substrate.Start(ctx, name, plan.Dir, l.agent.Command(sessionID)); err != nil {
		return musem.LaunchOutcome{}, l.undo(ctx, st, sessionID, err)
	}
	st.started = name

	// Confirmed rather than assumed. An agent that cannot run leaves a substrate
	// that started and a session that is already gone, and an exit code from the
	// command that created it says nothing about that.
	//
	// A check that fails to answer is treated as a failure too: the alternative
	// is reporting a launch as successful on the strength of a question nobody
	// managed to ask.
	switch exists, err := l.substrate.Exists(ctx, name); {
	case err != nil:
		return musem.LaunchOutcome{}, l.undo(ctx, st, sessionID,
			musem.Wrap(err, musem.ErrorCode(err), "the session could not be confirmed to have started"))
	case !exists:
		return musem.LaunchOutcome{}, l.undo(ctx, st, sessionID,
			musem.Errorf(musem.EUNAVAILABLE,
				"the session stopped as soon as it started; the agent may not be able to run in %s", plan.Dir))
	}

	return musem.LaunchOutcome{
		SessionID: sessionID,
		Substrate: name,
		Dir:       plan.Dir,
		Branch:    plan.Branch,
		Worktree:  plan.Worktree,
	}, nil
}

// state is what a launch has created so far, and therefore what it has to undo.
type state struct {
	worktree string
	repo     string
	recorded bool
	started  string
}

// undo takes back what the launch created and returns the error to report.
//
// A clean rollback returns the original cause unchanged: the launch failed for
// the reason it failed for, and nothing was left behind. A rollback that could
// not finish says what remains and where, because the alternative is claiming a
// clean rollback that did not happen — and a directory nobody knows about is
// exactly the thing the user will find weeks later and not dare delete.
//
// The order mirrors the creation, backwards, and the record is dropped last: it
// is the permission to remove the worktree, so it must outlive any removal that
// failed.
func (l *Launcher) undo(ctx context.Context, st state, sessionID string, cause error) error {
	var remains []string

	if st.started != "" {
		if err := l.substrate.Kill(ctx, st.started); err != nil {
			remains = append(remains, "the session "+st.started+" is still running ("+musem.ErrorMessage(err)+")")
		}
	}

	removed := true
	if st.worktree != "" {
		if err := l.trees.Remove(ctx, st.repo, st.worktree); err != nil {
			removed = false
			remains = append(remains, "the worktree at "+st.worktree+" is still on disk ("+musem.ErrorMessage(err)+")")
		}
	}

	if st.recorded && removed {
		if err := l.owned.ForgetWorktree(ctx, sessionID); err != nil {
			// The worktree is gone and the record survives it. That is the
			// harmless direction: the record grants permission over a path that
			// no longer exists, and the next reclamation finds nothing to remove.
			remains = append(remains, "the record of the worktree could not be dropped ("+musem.ErrorMessage(err)+")")
		}
	}

	if len(remains) == 0 {
		return cause
	}
	return musem.Wrap(cause, musem.EINTERNAL,
		"%s; the launch could not be fully undone: %s",
		musem.ErrorMessage(cause), strings.Join(remains, "; "))
}

// substrateName is what the session is called in the substrate.
//
// Derived from the session identifier rather than from the branch or the
// directory, because those are the two things a user renames. The prefix says
// who made it, to anyone looking at a list of tmux sessions and wondering.
func substrateName(sessionID string) string {
	short := sessionID
	if i := strings.IndexByte(short, '-'); i > 0 {
		short = short[:i]
	}
	return "musem-" + short
}

// Notices returns the worktrees that were kept when their session ended, newest
// first, so the dashboard can say which and why.
//
// Only the kept ones. A removal needs no explanation — the worktree is gone and
// the disk is back — whereas a worktree that survived is still occupying space
// and still holding work the user has to decide about.
func (l *Launcher) Notices() []musem.Reclamation {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]musem.Reclamation, 0, len(l.kept))
	for _, r := range l.kept {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func (l *Launcher) keep(r musem.Reclamation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kept[r.SessionID] = r
}

func (l *Launcher) forget(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.kept, sessionID)
}
