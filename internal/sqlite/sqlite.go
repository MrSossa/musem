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
	"os"
	"path/filepath"

	"github.com/MrSossa/musem"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

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

// Open opens the database at path, creating and migrating it as needed.
func Open(ctx context.Context, path string) (*Store, error) {
	// Busy timeout so a concurrent musem waits briefly rather than failing
	// outright; WAL so a reader is never blocked by a writer.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
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
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return musem.Wrap(err, musem.EINTERNAL, "cannot create the schema version table")
	}

	var current int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current)
	if err != nil {
		return musem.Wrap(err, musem.EINTERNAL, "cannot read the schema version")
	}

	for i := current; i < len(migrations); i++ {
		if _, err := s.db.ExecContext(ctx, migrations[i]); err != nil {
			return musem.Wrap(err, musem.EINTERNAL, "migration %d failed", i+1)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			return musem.Wrap(err, musem.EINTERNAL, "cannot record schema version %d", i+1)
		}
	}
	return nil
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
			read_cursor, skipped_records, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
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
			updated_at        = excluded.updated_at`,
		sc.SessionID,
		sc.Usage.InputTokens, sc.Usage.OutputTokens,
		sc.Usage.CacheWrite5mTokens, sc.Usage.CacheWrite1hTokens, sc.Usage.CacheReadTokens,
		amount, boolToInt(known), string(unknown),
		sc.Cursor, sc.Skipped,
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
		       read_cursor, skipped_records
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
			&sc.Cursor, &sc.Skipped,
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
