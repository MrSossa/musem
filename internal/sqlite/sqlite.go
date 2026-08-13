// Package sqlite persists accumulated usage so it outlives both musem and the
// records it was derived from.
//
// The transcripts are not a store: they belong to another tool and can be
// rotated or deleted at any time. Once usage has been counted it lives here,
// and is never recomputed from a source that may no longer exist.
//
// The driver is modernc.org/sqlite (pure Go) rather than the more common
// mattn/go-sqlite3, because the latter needs cgo and cgo would cost the project
// single-machine cross-compilation.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MrSossa/musem"

	sqlitedriver "modernc.org/sqlite" // registers the "sqlite" driver
	sqlitelib "modernc.org/sqlite/lib"
)

// busy reports whether err is SQLite refusing to wait for a lock.
//
// Concurrent openers contend for the write lock while migrating, and the busy
// handler is deliberately not invoked for the lock upgrades that could deadlock
// — SQLite returns immediately instead and leaves the retry to the caller. The
// code is read from the driver's own error type rather than matched in its
// message, which is prose and not a contract.
func busy(err error) bool {
	var e *sqlitedriver.Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code() == sqlitelib.SQLITE_BUSY || e.Code() == sqlitelib.SQLITE_LOCKED
}

// DefaultPath returns the database location inside the user's config
// directory, creating the directory if needed.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot resolve the user config directory")
	}
	dir = filepath.Join(dir, "musem")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot create %s", dir)
	}
	return filepath.Join(dir, "musem.db"), nil
}

// Store is the SQLite-backed history.
type Store struct {
	db *sql.DB
}

// dsnPragmas are the settings every connection is opened with. Busy timeout so
// a concurrent musem waits briefly rather than failing outright; WAL so a reader
// is never blocked by a writer.
const dsnPragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

// dsn builds the connection string for a database file, refusing a path the
// driver would read as something other than a filename.
//
// The path is concatenated in front of a query string, and a path is an
// arbitrary filename where a DSN is closer to a URL — so the two disagree about
// one ordinary character. The driver splits at the first "?" it finds, so a "?"
// in a directory name starts the query early: the filename is cut short there
// and every pragma below it lands in the wrong half. It does not fail to open.
// It hands back a working database at a path nobody asked for, configured with
// settings nobody chose, which is how a busy timeout that was never applied
// becomes a lost write with no explanation attached to it.
//
// Only "?" is refused, and the list is deliberately no longer than that. A "#"
// looks like it belongs here and does not: the driver passes the filename to
// sqlite3_open_v2 with SQLITE_OPEN_URI set, but SQLite only parses a name as a
// URI when it begins with "file:", which this one never does — so a "#" is an
// ordinary character in an ordinary filename and the database opens where it
// should, pragmas intact. Refusing it would reject a path that works.
//
// Refused rather than escaped. Escaping means reimplementing the driver's own
// parsing from the outside and staying in step with it, to rescue a filename
// nobody has: this path comes from the user's config directory. An error names
// the problem and the user moves the directory; a silent misconfiguration is the
// failure mode this whole package is arranged against.
func dsn(path string) (string, error) {
	if strings.ContainsRune(path, '?') {
		return "", musem.Errorf(musem.EINVALID,
			`the history database path %s contains "?", which cannot be opened safely`,
			path)
	}
	return path + "?" + dsnPragmas, nil
}

// Open opens the database at path, creating and migrating it as needed.
func Open(ctx context.Context, path string) (*Store, error) {
	source, err := dsn(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", source)
	if err != nil {
		return nil, musem.Wrap(err, musem.EUNAVAILABLE, "cannot open the history database")
	}
	// One writer at a time: SQLite serialises writes anyway, and this avoids
	// spurious contention between pooled connections.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in order; each is applied once and never edited
// afterwards, so an existing database can always be brought forward.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS session_costs (
		session_id           TEXT PRIMARY KEY,
		input_tokens         INTEGER NOT NULL DEFAULT 0,
		output_tokens        INTEGER NOT NULL DEFAULT 0,
		cache_write_5m       INTEGER NOT NULL DEFAULT 0,
		cache_write_1h       INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens    INTEGER NOT NULL DEFAULT 0,
		cost_usd             REAL    NOT NULL DEFAULT 0,
		cost_known           INTEGER NOT NULL DEFAULT 1,
		unknown_models       TEXT    NOT NULL DEFAULT '[]',
		updated_at           TEXT    NOT NULL DEFAULT (datetime('now'))
	)`,

	// The cursor records how far the source record had been read when these
	// totals were written. It is stored in the same row, and therefore the same
	// write, precisely so the two cannot disagree: totals that survive a restart
	// while their cursor does not are what makes a restart recount usage it has
	// already billed for.
	`ALTER TABLE session_costs ADD COLUMN read_cursor TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_costs ADD COLUMN skipped_records INTEGER NOT NULL DEFAULT 0`,

	// Rows written before the column existed have totals but no cursor, and an
	// empty cursor means "from the start" — which would add their whole history
	// to itself once. They are resumed from wherever their source record has
	// reached instead: the stored total is kept as the truth for everything
	// before now, at the cost of anything appended since the last write, which
	// is bounded by one refresh interval. Counting a session twice is a wrong
	// number that looks right; losing a couple of seconds is neither.
	`UPDATE session_costs SET read_cursor = 'end' WHERE read_cursor = ''`,

	// The dollars accumulated from priceable usage, kept apart from cost_usd
	// because cost_usd is zero whenever cost_known is 0 — so a session that
	// met one unpriceable model would otherwise resume from nothing.
	`ALTER TABLE session_costs ADD COLUMN priced_usd REAL NOT NULL DEFAULT 0`,

	// Rows written before the column existed carry their accumulated dollars in
	// cost_usd, but only where the cost was known. Where it was not, those
	// dollars were already lost by the bug this column exists to fix and there
	// is nothing left to recover: the session resumes from zero and climbs
	// again as new usage arrives.
	`UPDATE session_costs SET priced_usd = cost_usd WHERE cost_known = 1`,

	// The worktrees musem created, and therefore the only ones it may ever
	// remove.
	//
	// It is a record of what musem did, so it has to outlive the process that
	// did it: a table rather than a map, because every restart would otherwise
	// be a licence to forget, and forgetting here means either abandoning
	// musem's own worktrees or — with ownership inferred from a path instead —
	// adopting somebody else's.
	//
	// An older store migrates forward into an empty table, which says musem owns
	// nothing and therefore removes nothing. That is the right answer for a
	// database written before this existed: musem had created no worktrees then,
	// and a record it never wrote must not become permission it never earned.
	`CREATE TABLE IF NOT EXISTS session_worktrees (
		session_id TEXT PRIMARY KEY,
		path       TEXT NOT NULL,
		repo       TEXT NOT NULL,
		branch     TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT ''
	)`,

	// One record per worktree, enforced rather than assumed. Two rows claiming
	// the same path would be two sessions each believing they may delete it, and
	// the second removal would be of a directory the first had already replaced.
	`CREATE UNIQUE INDEX IF NOT EXISTS session_worktrees_path ON session_worktrees(path)`,
}

func (s *Store) migrate(ctx context.Context) error {
	if err := retryWhileBusy(ctx, func() error {
		_, err := s.db.ExecContext(ctx,
			`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)
		return err
	}); err != nil {
		return musem.Wrap(err, musem.EINTERNAL, "cannot create the schema version table")
	}

	// Each migration and the record that it ran commit together. SQLite has
	// transactional DDL, so this is possible — and necessary: a process that
	// died between the two would leave a schema that has moved on and a version
	// that has not, and the next start would replay an ALTER whose column
	// already exists. That fails, and goes on failing, taking every session's
	// accumulated cost with it.
	//
	// The version is re-read inside each transaction rather than once up front.
	// Two musem processes starting together would otherwise both see the same
	// version and both apply the next migration: the second one's ALTER hits a
	// column that already exists, its Open fails, and it runs with history
	// disabled — silently forgetting every session's cost for that run. A
	// concurrent musem is a case the package sets out to support, not an
	// exotic one.
	for {
		var applied bool
		if err := retryWhileBusy(ctx, func() error {
			var err error
			applied, err = s.migrateOne(ctx)
			return err
		}); err != nil {
			return err
		}
		if !applied {
			return nil
		}
	}
}

// migrationRetries bounds how long an opener waits for another process to
// finish migrating before giving up. Contention lasts as long as one migration
// takes, which is milliseconds; the ceiling is here so a wedged peer cannot
// hold startup indefinitely.
const migrationRetries = 50

// retryWhileBusy runs f until it stops reporting a lock it will not wait for.
// Any other error is returned as it is, immediately.
func retryWhileBusy(ctx context.Context, f func() error) error {
	var err error
	for attempt := 0; attempt < migrationRetries; attempt++ {
		if err = f(); !busy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return err
}

// migrateOne applies the next outstanding migration, reporting whether there
// was one. It re-reads the schema version under the write lock, so a migration
// another process committed in the meantime is seen rather than repeated.
func (s *Store) migrateOne(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, musem.Wrap(err, musem.EINTERNAL, "cannot begin a migration")
	}
	defer func() { _ = tx.Rollback() }()

	// A write before the read, so this transaction holds the write lock while it
	// decides what to do. Without it two processes read the same version, both
	// decide the same migration is outstanding, and the loser's DDL fails
	// against a schema the winner has already moved.
	if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = version WHERE 0`); err != nil {
		return false, musem.Wrap(err, musem.EINTERNAL, "cannot take the migration lock")
	}

	var current int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return false, musem.Wrap(err, musem.EINTERNAL, "cannot read the schema version")
	}
	if current >= len(migrations) {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, migrations[current]); err != nil {
		return false, musem.Wrap(err, musem.EINTERNAL, "migration %d failed", current+1)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version (version) VALUES (?)`, current+1); err != nil {
		return false, musem.Wrap(err, musem.EINTERNAL, "cannot record schema version %d", current+1)
	}

	if err := tx.Commit(); err != nil {
		return false, musem.Wrap(err, musem.EINTERNAL, "cannot commit migration %d", current+1)
	}
	return true, nil
}

// Save writes one session's accounting, replacing any previous row.
func (s *Store) Save(ctx context.Context, sc musem.SessionCost) error {
	unknown, err := json.Marshal(sc.UnknownModels)
	if err != nil {
		return musem.Wrap(err, musem.EINTERNAL, "cannot encode the unknown model list")
	}

	amount, known := sc.Cost.Amount()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO session_costs (
			session_id, input_tokens, output_tokens,
			cache_write_5m, cache_write_1h, cache_read_tokens,
			cost_usd, cost_known, unknown_models,
			read_cursor, skipped_records, priced_usd, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(session_id) DO UPDATE SET
			input_tokens      = excluded.input_tokens,
			output_tokens     = excluded.output_tokens,
			cache_write_5m    = excluded.cache_write_5m,
			cache_write_1h    = excluded.cache_write_1h,
			cache_read_tokens = excluded.cache_read_tokens,
			cost_usd          = excluded.cost_usd,
			cost_known        = excluded.cost_known,
			unknown_models    = excluded.unknown_models,
			read_cursor       = excluded.read_cursor,
			skipped_records   = excluded.skipped_records,
			priced_usd        = excluded.priced_usd,
			updated_at        = excluded.updated_at`,
		sc.SessionID,
		sc.Usage.InputTokens, sc.Usage.OutputTokens,
		sc.Usage.CacheWrite5mTokens, sc.Usage.CacheWrite1hTokens, sc.Usage.CacheReadTokens,
		amount, boolToInt(known), string(unknown),
		sc.Cursor, sc.Skipped, sc.Priced,
	)
	if err != nil {
		return musem.Wrap(err, musem.EUNAVAILABLE, "cannot save usage for session %s", sc.SessionID)
	}
	return nil
}

// Load returns every persisted session, keyed by session identifier.
func (s *Store) Load(ctx context.Context) (map[string]musem.SessionCost, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, input_tokens, output_tokens,
		       cache_write_5m, cache_write_1h, cache_read_tokens,
		       cost_usd, cost_known, unknown_models,
		       read_cursor, skipped_records, priced_usd
		FROM session_costs`)
	if err != nil {
		return nil, musem.Wrap(err, musem.EUNAVAILABLE, "cannot read usage history")
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]musem.SessionCost)
	for rows.Next() {
		var (
			sc      musem.SessionCost
			amount  float64
			known   int
			unknown string
		)
		if err := rows.Scan(
			&sc.SessionID,
			&sc.Usage.InputTokens, &sc.Usage.OutputTokens,
			&sc.Usage.CacheWrite5mTokens, &sc.Usage.CacheWrite1hTokens, &sc.Usage.CacheReadTokens,
			&amount, &known, &unknown,
			&sc.Cursor, &sc.Skipped, &sc.Priced,
		); err != nil {
			return nil, musem.Wrap(err, musem.EUNPARSEABLE, "malformed history row")
		}

		if known == 1 {
			sc.Cost = musem.USD(amount)
		} else {
			sc.Cost = musem.UnknownCost()
		}
		if err := json.Unmarshal([]byte(unknown), &sc.UnknownModels); err != nil {
			// A corrupt list must not cost the caller the figures beside it;
			// the models are named to make a gap actionable, not to make the
			// row unusable.
			sc.UnknownModels = nil
		}

		out[sc.SessionID] = sc
	}
	if err := rows.Err(); err != nil {
		return nil, musem.Wrap(err, musem.EUNAVAILABLE, "reading usage history")
	}
	return out, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
