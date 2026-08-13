package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MrSossa/musem"
)

// SaveWorktree records that musem created a worktree for a session.
//
// It is written before the session it was made for is started, so a musem that
// dies between the two leaves a record of a worktree that exists — which is
// recoverable — rather than a worktree nothing remembers, which is not.
func (s *Store) SaveWorktree(ctx context.Context, w musem.Worktree) error {
	if err := w.Validate(); err != nil {
		return err
	}

	created := w.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_worktrees (session_id, path, repo, branch, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			path       = excluded.path,
			repo       = excluded.repo,
			branch     = excluded.branch,
			created_at = excluded.created_at`,
		w.SessionID, w.Path, w.Repo, w.Branch, created.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return musem.Wrap(err, musem.EUNAVAILABLE,
			"cannot record the worktree created for session %s", w.SessionID)
	}
	return nil
}

// WorktreeFor returns the worktree musem created for a session, if it created
// one. The second result reports whether a record was found.
//
// Keyed by the session and by nothing else. A lookup by directory used to sit
// beside this one, so that a session observed under an identifier musem did not
// issue could still be matched to a worktree musem made. It bought nothing —
// musem chooses the identifier it launches with, and the agent reports back the
// same one — and it cost a way for one session to be handed another's worktree:
// a second agent started by hand inside musem's worktree would match that
// worktree's record by path, and ending first would have it removed out from
// under the session still working in it. Cleanliness does not catch that, because
// the worktree is not dirty; it is merely in use.
//
// Failing to reclaim is safe. Reclaiming the wrong worktree is not.
func (s *Store) WorktreeFor(ctx context.Context, sessionID string) (musem.Worktree, bool, error) {
	var (
		w       musem.Worktree
		created string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT session_id, path, repo, branch, created_at
		FROM session_worktrees
		WHERE session_id = ?`,
		sessionID,
	).Scan(&w.SessionID, &w.Path, &w.Repo, &w.Branch, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return musem.Worktree{}, false, nil
	}
	if err != nil {
		return musem.Worktree{}, false, musem.Wrap(err, musem.EUNAVAILABLE,
			"cannot read the worktree record for session %s", sessionID)
	}

	if created != "" {
		// A timestamp that will not parse costs the field, not the record. The
		// record is what permits a removal; when it was written is context.
		if at, perr := time.Parse(time.RFC3339Nano, created); perr == nil {
			w.CreatedAt = at
		}
	}
	return w, true, nil
}

// ForgetWorktree drops the record for a session.
//
// Called after the worktree has been removed, and only then. The record is the
// permission to remove, so dropping it first would leave a directory on disk
// that musem no longer recognises as its own — a worktree it could never
// reclaim, and would not dare to.
func (s *Store) ForgetWorktree(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM session_worktrees WHERE session_id = ?`, sessionID)
	if err != nil {
		return musem.Wrap(err, musem.EUNAVAILABLE,
			"cannot forget the worktree record for session %s", sessionID)
	}
	return nil
}
