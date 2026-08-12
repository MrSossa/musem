package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MrSossa/musem"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sample() musem.SessionCost {
	return musem.SessionCost{
		SessionID: "s1",
		Usage: musem.Usage{
			InputTokens:        10,
			OutputTokens:       20,
			CacheWrite5mTokens: 30,
			CacheWrite1hTokens: 40,
			CacheReadTokens:    50,
		},
		Cost: musem.USD(1.25),
	}
}

func TestSaveAndLoad(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	if err := s.Save(ctx, sample()); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded["s1"]
	if !ok {
		t.Fatal("session not persisted")
	}

	// The cache tiers must survive the round trip as separate figures: they are
	// priced differently, so collapsing them on the way to disk would lose the
	// only thing that makes the stored cost reproducible.
	if got.Usage.CacheWrite5mTokens != 30 || got.Usage.CacheWrite1hTokens != 40 {
		t.Errorf("cache tiers not round-tripped: %+v", got.Usage)
	}
	if got.Usage.CacheReadTokens != 50 {
		t.Errorf("CacheReadTokens = %d, want 50", got.Usage.CacheReadTokens)
	}
	amount, known := got.Cost.Amount()
	if !known || amount != 1.25 {
		t.Errorf("cost = %v (known=%v), want 1.25", amount, known)
	}
}

// History must survive musem closing and reopening.
func TestHistorySurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	first := open(t, path)
	if err := first.Save(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := open(t, path)
	loaded, err := second.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := loaded["s1"]; !ok || got.Usage.InputTokens != 10 {
		t.Errorf("history did not survive the restart: %+v", loaded)
	}
}

// An unknown cost must not come back from disk as a plausible zero.
func TestUnknownCostRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	sc := sample()
	sc.Cost = musem.UnknownCost()
	sc.UnknownModels = []string{"claude-from-the-future"}
	if err := s.Save(ctx, sc); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded["s1"]

	if got.Cost.Known() {
		t.Error("an unknown cost must not be restored as a known zero")
	}
	if len(got.UnknownModels) != 1 || got.UnknownModels[0] != "claude-from-the-future" {
		t.Errorf("unknown models not round-tripped: %v", got.UnknownModels)
	}
	if !got.Partial() {
		t.Error("restored session should report itself partial")
	}
}

// Saving the same session twice updates it rather than duplicating it.
func TestSaveIsIdempotentPerSession(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	sc := sample()
	if err := s.Save(ctx, sc); err != nil {
		t.Fatal(err)
	}
	sc.Usage.InputTokens = 999
	sc.Cost = musem.USD(9.99)
	if err := s.Save(ctx, sc); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("got %d rows, want 1", len(loaded))
	}
	if loaded["s1"].Usage.InputTokens != 999 {
		t.Errorf("InputTokens = %d, want the updated 999", loaded["s1"].Usage.InputTokens)
	}
}

// The store is the record of truth once usage is counted: deleting whatever it
// was derived from must not disturb it.
func TestHistoryOutlivesItsSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	transcript := filepath.Join(dir, "source.jsonl")
	if err := writeFile(transcript, "irrelevant"); err != nil {
		t.Fatal(err)
	}

	s := open(t, filepath.Join(dir, "musem.db"))
	if err := s.Save(ctx, sample()); err != nil {
		t.Fatal(err)
	}

	if err := removeFile(transcript); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded["s1"]
	if !ok {
		t.Fatal("history vanished with its source")
	}
	if got.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10 — counted usage must not be recomputed to zero", got.Usage.InputTokens)
	}
}

// Opening an existing database must not fail or lose data.
func TestMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	first := open(t, path)
	if err := first.Save(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	for i := 0; i < 3; i++ {
		s := open(t, path)
		loaded, err := s.Load(ctx)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if len(loaded) != 1 {
			t.Fatalf("reopen %d: got %d rows, want 1", i, len(loaded))
		}
		_ = s.Close()
	}
}

func TestLoadEmptyDatabase(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "musem.db"))

	loaded, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("an empty database is not an error: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("got %d rows, want none", len(loaded))
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func removeFile(path string) error { return os.Remove(path) }

// The cursor is the whole reason a restart resumes instead of recounting, so it
// has to survive the same trip the totals do.
func TestCursorRoundTrips(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	sc := sample()
	sc.Cursor = "4096"
	sc.Skipped = 3

	first := open(t, path)
	if err := first.Save(ctx, sc); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	loaded, err := open(t, path).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded["s1"]
	if got.Cursor != "4096" {
		t.Errorf("Cursor = %q, want %q", got.Cursor, "4096")
	}
	if got.Skipped != 3 {
		t.Errorf("Skipped = %d, want 3", got.Skipped)
	}
}

// A database written before the cursor column existed has totals but nothing
// saying how much of the source produced them. Reading such a row from the
// start would add its whole history to itself, so it is carried forward at the
// end of its source instead.
func TestRowsWrittenBeforeCursorsResumeAtTheEnd(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	// Build the schema as it stood before the cursor was added, and put a row
	// in it, exactly as an existing installation would have.
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

	loaded, err := open(t, path).Load(ctx)
	if err != nil {
		t.Fatalf("migrating an existing database: %v", err)
	}

	got, ok := loaded["s1"]
	if !ok {
		t.Fatal("the existing row was lost by the migration")
	}
	if got.Usage.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want the preserved 500", got.Usage.InputTokens)
	}
	if got.Cursor != "end" {
		t.Errorf("Cursor = %q, want %q so the row is not counted a second time", got.Cursor, "end")
	}
}

// The accumulated dollars have to outlive the process independently of the
// reported cost. A session that met an unpriceable model reports an unknown
// cost, and if only that is stored it resumes from nothing.
func TestPricedDollarsSurviveARestartWhileTheCostIsUnknown(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "musem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	want := musem.SessionCost{
		SessionID:     "s1",
		Usage:         musem.Usage{InputTokens: 10, OutputTokens: 20},
		Cost:          musem.UnknownCost(),
		Priced:        4.25,
		UnknownModels: []string{"claude-from-the-future"},
		Cursor:        "128",
	}
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded["s1"]
	if !ok {
		t.Fatal("session was not persisted")
	}
	if got.Cost.Known() {
		t.Error("an unknown cost must load as unknown")
	}
	if got.Priced != want.Priced {
		t.Errorf("priced dollars = %v, want %v: the accumulator did not survive the restart", got.Priced, want.Priced)
	}
}

// A migration and the record that it ran must commit together. If they do not,
// a process dying between them leaves a schema that has moved on and a version
// that has not — and the next start replays an ALTER whose column already
// exists, failing then and on every start afterwards, taking the user's entire
// cost history with it.
//
// The interruption is simulated rather than waited for: a migration that drops
// the version table makes the INSERT after it fail, which is the same ordering
// a crash produces.
func TestMigrationAndItsVersionCommitTogether(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "musem.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, musem.SessionCost{SessionID: "s1", Cost: musem.USD(7), Priced: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	original := migrations
	t.Cleanup(func() { migrations = original })

	// The DDL succeeds and the version bump that should follow it cannot.
	migrations = append(append([]string(nil), original...), `DROP TABLE schema_version`)

	if _, err := Open(ctx, path); err == nil {
		t.Fatal("a migration whose version could not be recorded must fail the open")
	}

	// Rolled back as one, so the database is exactly where it was: the next
	// start resumes at the right migration instead of replaying from zero.
	migrations = original
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening after an interrupted migration: %v", err)
	}
	defer func() { _ = s.Close() }()

	var version int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(original) {
		t.Errorf("schema version = %d, want %d", version, len(original))
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("history after recovery: %v", err)
	}
	if got, ok := loaded["s1"]; !ok || got.Priced != 7 {
		t.Errorf("history = %+v, want the session preserved with its accumulated dollars", loaded)
	}
}

// Two musem processes starting together must both come up with history intact.
// Reading the schema version once, outside the migrations, lets both see the
// same number and both apply the next one: the loser's ALTER hits a column that
// already exists, its Open fails, and it runs with history disabled — silently
// forgetting every session's cost for that run.
func TestConcurrentOpensAllMigrateCleanly(t *testing.T) {
	ctx := context.Background()

	for round := 0; round < 10; round++ {
		path := filepath.Join(t.TempDir(), "musem.db")

		// A barrier, so the openers actually overlap rather than queueing behind
		// whichever one happened to start first.
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 8)

		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				s, err := Open(ctx, path)
				errs[i] = err
				if s != nil {
					_ = s.Close()
				}
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, opener %d: %v", round, i, err)
			}
		}

		// And the database is left at exactly one copy of the schema.
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != len(migrations) {
			t.Errorf("round %d: schema version = %d, want %d", round, version, len(migrations))
		}
		var rows int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != len(migrations) {
			t.Errorf("round %d: %d version rows for %d migrations; a migration ran twice",
				round, rows, len(migrations))
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
