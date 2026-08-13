package launch

import (
	"context"

	"github.com/MrSossa/musem"
)

// Reclaim takes back the worktree musem created for a session that has ended,
// if it created one and if taking it back would destroy nothing.
//
// The second result reports whether anything happened at all, so a caller can
// tell "nothing to do here" from "kept, and here is why". Most sessions are the
// first: musem did not launch them, and their repositories are none of its
// business.
//
// It is called on the registry's end-of-session transition rather than from a
// sweep of its own. That is not a convenience. The registry refuses to call a
// session ended on the strength of a discovery pass that admits it could not
// read everything it found — so a session that merely appears to have gone,
// because a parser broke, never reaches this function at all. A timer walking
// worktrees would have to rebuild that guard, and getting it wrong would delete
// a running session's work.
func (l *Launcher) Reclaim(ctx context.Context, s musem.Session) (musem.Reclamation, bool) {
	if !s.Ended() {
		// Reclamation follows an end, not an absence. A live session's worktree
		// is a worktree in use.
		return musem.Reclamation{}, false
	}
	if l.owned == nil {
		return musem.Reclamation{}, false
	}

	w, ok, err := l.owned.WorktreeFor(ctx, s.ID)
	if err != nil {
		// Ownership could not be established, so it was not established. Nothing
		// is removed on a question that failed to be answered.
		return musem.Reclamation{}, false
	}
	if !ok {
		// R10: a worktree musem did not create is left alone, however clean it
		// is. This is the branch that keeps every repository the user merely
		// happens to be working in out of reach.
		return musem.Reclamation{}, false
	}

	verdict := l.trees.Cleanliness(ctx, w.Path)
	if !verdict.Clean() {
		kept := musem.Reclamation{
			SessionID: s.ID,
			Path:      w.Path,
			Reason:    verdict.Reason(),
			At:        l.now(),
		}
		l.keep(kept)
		return kept, true
	}

	if err := l.trees.Remove(ctx, w.Repo, w.Path); err != nil {
		// git refused. It refuses a worktree with modifications, which is the
		// second lock on this and may well have caught something the checks
		// above could not see from outside.
		kept := musem.Reclamation{
			SessionID: s.ID,
			Path:      w.Path,
			Reason:    musem.ErrorMessage(err),
			At:        l.now(),
		}
		l.keep(kept)
		return kept, true
	}

	// The record goes only after the worktree it granted permission over. A
	// failure here leaves permission over a path that no longer exists, which
	// costs nothing: the next pass finds no worktree to remove.
	_ = l.owned.ForgetWorktree(ctx, w.SessionID)
	l.forget(s.ID)

	return musem.Reclamation{
		SessionID: s.ID,
		Path:      w.Path,
		Removed:   true,
		At:        l.now(),
	}, true
}
