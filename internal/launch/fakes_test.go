package launch

import (
	"context"
	"strings"
	"sync"

	"github.com/MrSossa/musem"
)

// journal records the order in which the doubles were called.
//
// Order is most of what this package is: which step runs before which is what
// decides whether a failure leaves a worktree nothing remembers, and it is not
// observable from the return value of anything.
type journal struct {
	mu     sync.Mutex
	events []string
}

func (j *journal) add(event string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
}

func (j *journal) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return strings.Join(j.events, " → ")
}

func (j *journal) saw(event string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.events {
		if e == event {
			return true
		}
	}
	return false
}

type fakeTrees struct {
	j *journal

	repo    string
	repoErr error

	branches    []string
	branchesErr error

	// checkedOut maps a branch to the worktree already holding it.
	checkedOut    map[string]string
	checkedOutErr error

	occupied    map[string]bool
	occupiedErr error

	branchErr error

	addErr error
	added  []string

	removeErr error
	removed   []string

	verdict musem.Cleanliness
}

func (f *fakeTrees) Repository(_ context.Context, dir string) (string, error) {
	f.j.add("repository")
	if f.repoErr != nil {
		return "", f.repoErr
	}
	if f.repo != "" {
		return f.repo, nil
	}
	return dir, nil
}

func (f *fakeTrees) Branches(_ context.Context, _ string) ([]string, error) {
	f.j.add("branches")
	return f.branches, f.branchesErr
}

func (f *fakeTrees) CheckedOut(_ context.Context, _, branch string) (string, error) {
	f.j.add("checkedout")
	if f.checkedOutErr != nil {
		return "", f.checkedOutErr
	}
	return f.checkedOut[branch], nil
}

func (f *fakeTrees) ValidBranch(branch string) error {
	f.j.add("validbranch")
	return f.branchErr
}

func (f *fakeTrees) Occupied(path string) (bool, error) {
	f.j.add("occupied")
	if f.occupiedErr != nil {
		return false, f.occupiedErr
	}
	return f.occupied[path], nil
}

func (f *fakeTrees) Add(_ context.Context, repo, path, branch string, create bool) error {
	f.j.add("add")
	verb := "checkout"
	if create {
		verb = "create"
	}
	f.added = append(f.added, repo+" "+path+" "+branch+" "+verb)
	return f.addErr
}

func (f *fakeTrees) Remove(_ context.Context, repo, path string) error {
	f.j.add("remove")
	f.removed = append(f.removed, repo+" "+path)
	return f.removeErr
}

func (f *fakeTrees) Cleanliness(_ context.Context, _ string) musem.Cleanliness {
	f.j.add("cleanliness")
	return f.verdict
}

type fakeSubstrate struct {
	j *journal

	availableErr error

	startErr error
	started  []string

	exists    bool
	existsErr error

	killErr error
	killed  []string
}

func (f *fakeSubstrate) Available(context.Context) error {
	f.j.add("substrate-available")
	return f.availableErr
}

func (f *fakeSubstrate) Start(_ context.Context, name, dir string, command []string) error {
	f.j.add("start")
	f.started = append(f.started, name+" "+dir+" "+strings.Join(command, " "))
	return f.startErr
}

func (f *fakeSubstrate) Exists(_ context.Context, _ string) (bool, error) {
	f.j.add("exists")
	return f.exists, f.existsErr
}

func (f *fakeSubstrate) Kill(_ context.Context, name string) error {
	f.j.add("kill")
	f.killed = append(f.killed, name)
	return f.killErr
}

type fakeAgent struct {
	j *journal

	availableErr error
	id           string
	idErr        error
}

func (f *fakeAgent) Available(context.Context) error {
	f.j.add("agent-available")
	return f.availableErr
}

func (f *fakeAgent) NewSessionID() (string, error) {
	if f.idErr != nil {
		return "", f.idErr
	}
	if f.id == "" {
		return "11111111-2222-4333-8444-555555555555", nil
	}
	return f.id, nil
}

func (f *fakeAgent) Command(sessionID string) []string {
	return []string{"claude", "--session-id", sessionID}
}

type fakeOwned struct {
	j *journal

	records map[string]musem.Worktree

	saveErr   error
	lookupErr error
	forgetErr error

	forgotten []string
}

func newFakeOwned(j *journal) *fakeOwned {
	return &fakeOwned{j: j, records: make(map[string]musem.Worktree)}
}

func (f *fakeOwned) SaveWorktree(_ context.Context, w musem.Worktree) error {
	f.j.add("record")
	if f.saveErr != nil {
		return f.saveErr
	}
	f.records[w.SessionID] = w
	return nil
}

func (f *fakeOwned) WorktreeFor(_ context.Context, sessionID string) (musem.Worktree, bool, error) {
	f.j.add("lookup")
	if f.lookupErr != nil {
		return musem.Worktree{}, false, f.lookupErr
	}
	if w, ok := f.records[sessionID]; ok {
		return w, true, nil
	}
	return musem.Worktree{}, false, nil
}

func (f *fakeOwned) ForgetWorktree(_ context.Context, sessionID string) error {
	f.j.add("forget")
	if f.forgetErr != nil {
		return f.forgetErr
	}
	f.forgotten = append(f.forgotten, sessionID)
	delete(f.records, sessionID)
	return nil
}
