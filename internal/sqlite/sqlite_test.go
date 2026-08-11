package sqlite

import (
	"context"
	"os"
	"path/filepath"
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
