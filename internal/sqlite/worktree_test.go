package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

// R10, scenario "The record survives a restart": a worktree created in an
// earlier run is still recognised as musem's own, which is what makes it a
// candidate for reclamation at all.
func TestAWorktreeRecordSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	created := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	first := open(t, path)
	if err := first.SaveWorktree(ctx, musem.Worktree{
		SessionID: "s1",
		Path:      "/w/api-feat",
		Repo:      "/r/api",
		Branch:    "musem/feat",
		CreatedAt: created,
	}); err != nil {
		t.Fatalf("SaveWorktree: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	got, ok, err := open(t, path).WorktreeFor(ctx, "s1")
	if err != nil {
		t.Fatalf("WorktreeFor: %v", err)
	}
	if !ok {
		t.Fatal("a worktree musem created in an earlier run was forgotten; it could never be reclaimed")
	}
	if got.Path != "/w/api-feat" || got.Repo != "/r/api" || got.Branch != "musem/feat" {
		t.Errorf("record = %+v, want the one that was saved", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
}

// R10: a session musem never launched has no record, and the absence is
// reported as an absence rather than as an empty record that reads as one.
func TestASessionWithNoRecordIsNotOwned(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	got, ok, err := s.WorktreeFor(ctx, "never-launched")
	if err != nil {
		t.Fatalf("WorktreeFor: %v", err)
	}
	if ok {
		t.Fatalf("a session musem never launched came back owned: %+v", got)
	}
}

// R10: ownership is keyed by the session and by nothing else.
//
// A lookup by directory used to sit beside this one, so that a session observed
// under an identifier musem did not issue could still be matched to a worktree
// musem made. It bought nothing — musem chooses the identifier it launches with,
// and the agent reports back the same one — and it cost a way for one session to
// be handed another's worktree: a second agent started by hand inside musem's
// worktree would match that worktree's record by path, and ending first would
// have it removed out from under the session still working in it. Cleanliness
// does not catch that, because the worktree is not dirty; it is in use.
//
// This asserts the second way in stays shut.
func TestAWorktreeBelongsOnlyToTheSessionItWasMadeFor(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	if err := s.SaveWorktree(ctx, musem.Worktree{
		SessionID: "s1", Path: "/w/api-feat", Repo: "/r/api",
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.WorktreeFor(ctx, "another-session-in-the-same-directory")
	if err != nil {
		t.Fatalf("WorktreeFor: %v", err)
	}
	if ok {
		t.Fatalf("another session was handed %+v; it could remove a worktree still in use", got)
	}

	// The session it was made for still finds it.
	if _, ok, _ := s.WorktreeFor(ctx, "s1"); !ok {
		t.Error("the owning session lost its own record")
	}
}

// The record is the permission to remove, so it must not be storable without
// everything a removal needs.
func TestAnIncompleteRecordIsRefused(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	err := s.SaveWorktree(ctx, musem.Worktree{SessionID: "s1", Path: "/w/api-feat"})
	if musem.ErrorCode(err) != musem.EINVALID {
		t.Fatalf("err = %v, want an EINVALID: a record with no repository cannot be acted on", err)
	}

	if _, ok, _ := s.WorktreeFor(ctx, "s1"); ok {
		t.Error("the refused record was stored anyway")
	}
}

// R9: once the worktree is gone the record goes with it, so a later pass does
// not keep offering to remove a directory that is no longer there.
func TestForgettingAWorktreeRemovesTheRecord(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	if err := s.SaveWorktree(ctx, musem.Worktree{
		SessionID: "s1", Path: "/w/api-feat", Repo: "/r/api",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetWorktree(ctx, "s1"); err != nil {
		t.Fatalf("ForgetWorktree: %v", err)
	}

	if _, ok, err := s.WorktreeFor(ctx, "s1"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("the record outlived the worktree it granted permission over")
	}

	// Forgetting what was already forgotten is not a failure: reclamation may
	// well run twice for the same session.
	if err := s.ForgetWorktree(ctx, "s1"); err != nil {
		t.Errorf("forgetting an absent record: %v", err)
	}
}

// Saving twice for one session replaces the record rather than adding a second,
// so a relaunch cannot leave a stale path behind that a later removal would act
// on.
func TestSavingAWorktreeTwiceKeepsOneRecord(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	for _, path := range []string{"/w/first", "/w/second"} {
		if err := s.SaveWorktree(ctx, musem.Worktree{
			SessionID: "s1", Path: path, Repo: "/r/api",
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, ok, err := s.WorktreeFor(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("WorktreeFor = %v, %v", ok, err)
	}
	if got.Path != "/w/second" {
		t.Errorf("path = %q, want the latest", got.Path)
	}

	// One row, not two: a superseded path left behind would be a removal
	// candidate pointing at a directory this session no longer works in.
	var rows int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_worktrees WHERE session_id = 's1'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d records for one session; the superseded path is still a removal candidate", rows)
	}
}

// R10, and the reason the ownership record is persisted at all: a database
// written before this table existed migrates forward into an empty one, and an
// empty table means musem owns nothing. A record it never wrote must not become
// permission it never earned.
func TestAnOlderStoreMigratesForwardOwningNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	// The schema as it stood before worktrees existed, with a session in it.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
		migrations[0],
		`INSERT INTO session_costs (session_id, input_tokens, cost_usd) VALUES ('s1', 500, 2.50)`,
	} {
		if _, err := legacy.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s := open(t, path)

	// The history the older store held is still there.
	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("migrating an existing database: %v", err)
	}
	if got, ok := loaded["s1"]; !ok || got.Usage.InputTokens != 500 {
		t.Errorf("the existing history did not survive the migration: %+v", got)
	}

	// And musem owns no worktree in it, including the one that session is
	// working in.
	if _, ok, err := s.WorktreeFor(ctx, "s1"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a store written before the table existed claims ownership of a worktree")
	}
}
