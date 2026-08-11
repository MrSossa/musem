package cost

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/MrSossa/musem"
)

// fakeReader stands in for a transcript reader. Like the real one it keeps no
// memory of what it has already handed out: the cursor decides, which is what
// makes it able to model a restart at all.
type fakeReader struct {
	bySession map[string][]musem.ModelUsage
	skipped   int
	err       error
}

func (f *fakeReader) ReadUsage(_ context.Context, sessionID, cursor string) (musem.UsageReading, error) {
	if f.err != nil {
		return musem.UsageReading{}, f.err
	}

	all := f.bySession[sessionID]
	from := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			return musem.UsageReading{}, err
		}
		from = minInt(n, len(all))
	}

	return musem.UsageReading{
		Entries: all[from:],
		Cursor:  strconv.Itoa(len(all)),
		Skipped: f.skipped,
	}, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fakeStore is history that survives an Accountant, so a restart can be modelled
// by building a second one over the same contents.
type fakeStore struct {
	rows map[string]musem.SessionCost
}

func newFakeStore() *fakeStore { return &fakeStore{rows: make(map[string]musem.SessionCost)} }

func (s *fakeStore) Load(context.Context) (map[string]musem.SessionCost, error) {
	out := make(map[string]musem.SessionCost, len(s.rows))
	for k, v := range s.rows {
		out[k] = v
	}
	return out, nil
}

func (s *fakeStore) Save(_ context.Context, sc musem.SessionCost) error {
	s.rows[sc.SessionID] = sc
	return nil
}

func closeTo(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %.10f, want %.10f", got, want)
	}
}

// Cache writes at the two time-to-live tiers are billed differently, and cache
// reads at a tenth of input. Merging any of them misreports the bill.
func TestPricingAppliesEachRateSeparately(t *testing.T) {
	rate := Rate{InputPerMTok: 5, OutputPerMTok: 25}

	got := rate.price(usageInput{
		input:        1_000_000,
		output:       1_000_000,
		cacheWrite5m: 1_000_000,
		cacheWrite1h: 1_000_000,
		cacheRead:    1_000_000,
	})

	// 5 input + 25 output + 6.25 (5m write) + 10 (1h write) + 0.50 (read)
	closeTo(t, got, 46.75)
}

func TestCacheWriteTiersDiffer(t *testing.T) {
	rate := Rate{InputPerMTok: 5, OutputPerMTok: 25}

	fiveMin := rate.price(usageInput{cacheWrite5m: 1_000_000})
	oneHour := rate.price(usageInput{cacheWrite1h: 1_000_000})

	closeTo(t, fiveMin, 6.25)
	closeTo(t, oneHour, 10.0)
	if fiveMin >= oneHour {
		t.Error("the one-hour tier must cost more than the five-minute tier")
	}
}

func TestSessionCost(t *testing.T) {
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{
			Model: "claude-opus-5",
			Usage: musem.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		}},
	}}
	a := New(NewRateTable(), reader, nil)

	if err := a.Update(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	sc, ok := a.Session("s1")
	if !ok {
		t.Fatal("session not recorded")
	}
	amount, known := sc.Cost.Amount()
	if !known {
		t.Fatal("cost should be known for a priced model")
	}
	closeTo(t, amount, 30.0) // $5 input + $25 output
	if sc.Partial() {
		t.Error("a fully priced session must not be partial")
	}
}

// Tokens are still counted for an unknown model; only the money is unavailable.
func TestUnknownModelKeepsTokensAndDropsCost(t *testing.T) {
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {
			{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}},
			{Model: "claude-from-the-future", Usage: musem.Usage{OutputTokens: 500}},
		},
	}}
	a := New(NewRateTable(), reader, nil)

	if err := a.Update(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	sc, _ := a.Session("s1")

	if sc.Usage.InputTokens != 1_000_000 || sc.Usage.OutputTokens != 500 {
		t.Errorf("tokens must be counted regardless of pricing: %+v", sc.Usage)
	}
	if sc.Cost.Known() {
		t.Error("cost must be unavailable when a model has no known rate")
	}
	if len(sc.UnknownModels) != 1 || sc.UnknownModels[0] != "claude-from-the-future" {
		t.Errorf("the unknown model must be named: %v", sc.UnknownModels)
	}
	if !sc.Partial() {
		t.Error("a session with an unpriced model is partial")
	}
}

// Zero usage is a fact, not an absence of information.
func TestZeroUsageIsKnownZeroNotUnknown(t *testing.T) {
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{}}},
	}}
	a := New(NewRateTable(), reader, nil)

	if err := a.Update(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	sc, _ := a.Session("s1")

	amount, known := sc.Cost.Amount()
	if !known {
		t.Fatal("zero usage must yield a known zero cost, not unknown")
	}
	closeTo(t, amount, 0)
	if !sc.Usage.IsZero() {
		t.Error("usage should be zero")
	}
}

// Usage already counted must never be recomputed — that is what lets the total
// survive the source record being deleted.
func TestUsageAccumulates(t *testing.T) {
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
	}}
	a := New(NewRateTable(), reader, nil)
	ctx := context.Background()

	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	// The reader now returns nothing new, as a deleted transcript would.
	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	sc, _ := a.Session("s1")
	if sc.Usage.InputTokens != 1_000_000 {
		t.Errorf("InputTokens = %d, want the accumulated 1000000", sc.Usage.InputTokens)
	}
	amount, _ := sc.Cost.Amount()
	closeTo(t, amount, 5.0)
}

// Restarting musem must resume, not recount. The totals are persisted and the
// reader's memory is not, so unless the cursor is persisted with them the first
// pass after startup adds the entire history to the total it just restored —
// and does it again on every launch after that.
func TestRestartResumesInsteadOfRecounting(t *testing.T) {
	usage := map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
	}
	store := newFakeStore()
	ctx := context.Background()

	first := New(NewRateTable(), &fakeReader{bySession: usage}, store)
	if err := first.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	before, _ := first.Session("s1")
	amount, _ := before.Cost.Amount()
	closeTo(t, amount, 5.0)

	// A second process over the same database and the same unchanged source.
	second := New(NewRateTable(), &fakeReader{bySession: usage}, store)
	if err := second.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	after, _ := second.Session("s1")
	if after.Usage.InputTokens != 1_000_000 {
		t.Errorf("InputTokens = %d after a restart, want the original 1000000", after.Usage.InputTokens)
	}
	amount, _ = after.Cost.Amount()
	closeTo(t, amount, 5.0)
}

// The cursor has to reach the store, because that is the only place it can
// survive the process that produced it.
func TestCursorIsPersistedWithTheTotals(t *testing.T) {
	store := newFakeStore()
	a := New(NewRateTable(), &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 10}}},
	}}, store)

	if err := a.Update(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if got := store.rows["s1"].Cursor; got != "1" {
		t.Errorf("persisted cursor = %q, want %q", got, "1")
	}
}

// Records that could not be read are usage that was never counted, so the
// figure beside them understates the truth and has to say so.
func TestSkippedRecordsAccumulateAndMarkTheCostDegraded(t *testing.T) {
	reader := &fakeReader{
		bySession: map[string][]musem.ModelUsage{
			"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
		},
		skipped: 2,
	}
	a := New(NewRateTable(), reader, nil)
	ctx := context.Background()

	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	sc, _ := a.Session("s1")
	if sc.Skipped != 4 {
		t.Errorf("Skipped = %d, want the 4 accumulated across both passes", sc.Skipped)
	}
	if !sc.Degraded() {
		t.Error("a session with unreadable records must report itself degraded")
	}
	// Degraded is not partial: these tokens were never counted at all, rather
	// than counted and left unpriced.
	if sc.Partial() {
		t.Error("unreadable records must not be reported as an unpriced model")
	}
}

func TestFleetTotal(t *testing.T) {
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
		"s2": {{Model: "claude-haiku-4-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
	}}
	a := New(NewRateTable(), reader, nil)
	ctx := context.Background()

	for _, id := range []string{"s1", "s2"} {
		if err := a.Update(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	fleet := a.Total()
	amount, known := fleet.Cost.Amount()
	if !known {
		t.Fatal("a fully priced fleet must have a known total")
	}
	closeTo(t, amount, 6.0) // $5 opus + $1 haiku
	if fleet.Usage.InputTokens != 2_000_000 {
		t.Errorf("fleet input tokens = %d, want 2000000", fleet.Usage.InputTokens)
	}
	if fleet.Partial() {
		t.Error("a fully priced fleet must not be partial")
	}
}

// One unpriceable session makes the fleet total unknown — a total silently
// missing a component cannot be told apart from a complete one.
func TestFleetTotalIsPartialWhenAnySessionIs(t *testing.T) {
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
		"s2": {{Model: "claude-from-the-future", Usage: musem.Usage{InputTokens: 10}}},
	}}
	a := New(NewRateTable(), reader, nil)
	ctx := context.Background()

	for _, id := range []string{"s1", "s2"} {
		if err := a.Update(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	fleet := a.Total()
	if fleet.Cost.Known() {
		t.Error("the fleet total must be unknown when any component is")
	}
	if !fleet.Partial() {
		t.Error("fleet must report itself partial")
	}
	if len(fleet.UnknownModels) != 1 {
		t.Errorf("the unpriced model must be named: %v", fleet.UnknownModels)
	}
	// Tokens still aggregate even when the money does not.
	if fleet.Usage.InputTokens != 1_000_010 {
		t.Errorf("fleet input tokens = %d, want 1000010", fleet.Usage.InputTokens)
	}
}

// A rate for a model released after this binary was built can be supplied
// without waiting for a new release.
func TestRateOverride(t *testing.T) {
	table := NewRateTable()
	if _, ok := table.Lookup("claude-brand-new"); ok {
		t.Fatal("model should be unknown before the override")
	}
	table.Set("claude-brand-new", Rate{InputPerMTok: 7, OutputPerMTok: 21})

	rate, ok := table.Lookup("claude-brand-new")
	if !ok {
		t.Fatal("override was not applied")
	}
	closeTo(t, rate.price(usageInput{input: 1_000_000}), 7.0)
}

// Matching is exact: a near-miss must not be priced with a neighbour's rate.
func TestLookupDoesNotPrefixMatch(t *testing.T) {
	table := NewRateTable()
	for _, model := range []string{"claude-opus", "claude-opus-5-turbo", "opus-5", ""} {
		if _, ok := table.Lookup(model); ok {
			t.Errorf("%q must not resolve to a rate", model)
		}
	}
	if _, ok := table.Lookup("claude-opus-5"); !ok {
		t.Error("the exact identifier must resolve")
	}
}
