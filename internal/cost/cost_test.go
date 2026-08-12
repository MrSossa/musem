package cost

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

// fakeReader stands in for a transcript reader. Like the real one it keeps no
// memory of what it has already handed out: the cursor decides, which is what
// makes it able to model a restart at all.
type fakeReader struct {
	bySession map[string][]musem.ModelUsage
	skipped   int
	err       error

	// reset makes the next reading claim the record was replaced, which is how
	// a truncated or rotated transcript announces itself.
	reset bool
}

func (f *fakeReader) ReadUsage(_ context.Context, sessionID, cursor string) (musem.UsageReading, error) {
	if f.err != nil {
		return musem.UsageReading{}, f.err
	}

	all := f.bySession[sessionID]
	from := 0
	// A reset reading starts at the beginning of the record whatever the cursor
	// said, because the cursor referred to a file that no longer exists.
	if cursor != "" && !f.reset {
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
		Reset:   f.reset,
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

	fleet := a.Total([]string{"s1", "s2"})
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

	fleet := a.Total([]string{"s1", "s2"})
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

// The accumulated dollars must survive meeting a model with no rate. Reading
// them back out of the reported Cost destroyed them, because an unknown Cost
// yields no amount — and the loss was then persisted and never recovered.
func TestPricedDollarsSurviveAnUnpriceableEntry(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{OutputTokens: 1_000_000}}},
	}}
	a := New(NewRateTable(), reader, nil)

	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	sc, _ := a.Session("s1")
	earned, known := sc.Cost.Amount()
	if !known || earned <= 0 {
		t.Fatalf("first pass produced no priceable cost: %v (known=%v)", earned, known)
	}

	// A model musem has no rate for turns up.
	reader.bySession["s1"] = append(reader.bySession["s1"],
		musem.ModelUsage{Model: "claude-from-the-future", Usage: musem.Usage{OutputTokens: 500}})
	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatalf("second update: %v", err)
	}

	sc, _ = a.Session("s1")
	if sc.Cost.Known() {
		t.Error("a cost containing unpriceable usage must be reported as unknown")
	}
	if math.Abs(sc.Priced-earned) > 1e-9 {
		t.Errorf("accumulated dollars = %v, want %v: the earlier arithmetic was destroyed", sc.Priced, earned)
	}

	// Priceable usage arriving afterwards must build on what was already there
	// rather than restarting from zero.
	reader.bySession["s1"] = append(reader.bySession["s1"],
		musem.ModelUsage{Model: "claude-opus-5", Usage: musem.Usage{OutputTokens: 1_000_000}})
	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatalf("third update: %v", err)
	}

	sc, _ = a.Session("s1")
	if math.Abs(sc.Priced-2*earned) > 1e-9 {
		t.Errorf("accumulated dollars = %v, want %v: the accumulator restarted", sc.Priced, 2*earned)
	}
}

// A reading that reports itself as a restart must replace the totals derived
// from the same record, not be added to them. The consumer accumulates, so a
// rotated transcript would otherwise be counted twice.
func TestResetReplacesTotalsInsteadOfAddingToThem(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {
			{Model: "claude-opus-5", Usage: musem.Usage{OutputTokens: 1_000_000}},
			{Model: "claude-opus-5", Usage: musem.Usage{OutputTokens: 1_000_000}},
		},
	}}
	a := New(NewRateTable(), reader, nil)

	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	before, _ := a.Session("s1")

	// The record is replaced by a shorter one, and the reader says so.
	reader.bySession["s1"] = []musem.ModelUsage{
		{Model: "claude-opus-5", Usage: musem.Usage{OutputTokens: 1_000_000}},
	}
	reader.reset = true
	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatalf("update after reset: %v", err)
	}

	after, _ := a.Session("s1")
	if after.Usage.OutputTokens != 1_000_000 {
		t.Errorf("output tokens = %d, want 1000000: the reset reading was added to totals that already held it",
			after.Usage.OutputTokens)
	}
	if after.Priced >= before.Priced {
		t.Errorf("dollars after reset = %v, before = %v: a shorter record must not cost more",
			after.Priced, before.Priced)
	}
	if after.SessionID != "s1" {
		t.Error("the reset must not lose the session identity")
	}
}

// The total covers the sessions it is asked about, not everything the
// accountant has ever held. History outlives the sessions that produced it, so
// totalling the whole map would put a figure covering every session ever seen
// under a count of the few on screen.
func TestFleetTotalCoversOnlyTheSessionsAskedFor(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"live": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
		"old":  {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 9_000_000}}},
	}}
	a := New(NewRateTable(), reader, nil)

	for _, id := range []string{"live", "old"} {
		if err := a.Update(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	fleet := a.Total([]string{"live"})
	amount, known := fleet.Cost.Amount()
	if !known {
		t.Fatal("a fully priced session must produce a known total")
	}
	closeTo(t, amount, 5.0) // $5 for the live session, not $50 for both
	if fleet.Usage.InputTokens != 1_000_000 {
		t.Errorf("input tokens = %d, want 1000000: history was folded into the total",
			fleet.Usage.InputTokens)
	}
	if fleet.Unrecorded != 0 {
		t.Errorf("unrecorded = %d, want 0", fleet.Unrecorded)
	}
}

// A session with no accounting at all is not worth nothing; it is unknown, and
// a total that quietly leaves it out cannot be told apart from a complete one.
func TestFleetTotalAdmitsSessionsItHasNeverRecorded(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{bySession: map[string][]musem.ModelUsage{
		"s1": {{Model: "claude-opus-5", Usage: musem.Usage{InputTokens: 1_000_000}}},
	}}
	a := New(NewRateTable(), reader, nil)
	if err := a.Update(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	fleet := a.Total([]string{"s1", "never-read"})

	// The figure stays real and says what it is missing. Sessions are never
	// dropped from the inventory, so an em dash here would outlive the session
	// that caused it and hold the headline number hostage for the rest of the
	// run — an outage dressed as honesty.
	amount, known := fleet.Cost.Amount()
	if !known {
		t.Fatal("the total went unknown; a figure that names its gap is more use than an em dash")
	}
	closeTo(t, amount, 5.0)
	if fleet.Unrecorded != 1 {
		t.Errorf("unrecorded = %d, want 1", fleet.Unrecorded)
	}
	if !fleet.Partial() {
		t.Error("a total missing a whole session must report itself partial")
	}
}

// Introductory pricing expires. A table that holds the promotional figure past
// its end date understates every session on that model; one that ignores the
// promotion overstates every session before it. Both are confident and wrong.
func TestIntroductoryPricingAppliesOnlyBeforeItExpires(t *testing.T) {
	table := NewRateTable()

	during := sonnet5IntroEnds.Add(-24 * time.Hour)
	table.SetClock(func() time.Time { return during })
	rate, ok := table.Lookup("claude-sonnet-5")
	if !ok {
		t.Fatal("claude-sonnet-5 must be priced")
	}
	if rate.InputPerMTok != 2 || rate.OutputPerMTok != 10 {
		t.Errorf("during the introductory period: $%v/$%v per MTok, want $2/$10",
			rate.InputPerMTok, rate.OutputPerMTok)
	}

	after := sonnet5IntroEnds.Add(24 * time.Hour)
	table.SetClock(func() time.Time { return after })
	rate, _ = table.Lookup("claude-sonnet-5")
	if rate.InputPerMTok != 3 || rate.OutputPerMTok != 15 {
		t.Errorf("after the introductory period: $%v/$%v per MTok, want $3/$15",
			rate.InputPerMTok, rate.OutputPerMTok)
	}

	// The boundary itself is the first instant of standard pricing.
	table.SetClock(func() time.Time { return sonnet5IntroEnds })
	rate, _ = table.Lookup("claude-sonnet-5")
	if rate.InputPerMTok != 3 {
		t.Errorf("at the expiry instant: $%v per MTok, want the standard $3", rate.InputPerMTok)
	}
}

// Cache prices are multiples of the input rate, so they have to follow the
// introductory rate rather than the standard one.
func TestCachePricingFollowsTheIntroductoryRate(t *testing.T) {
	table := NewRateTable()
	table.SetClock(func() time.Time { return sonnet5IntroEnds.Add(-time.Hour) })

	rate, _ := table.Lookup("claude-sonnet-5")
	closeTo(t, rate.price(usageInput{cacheRead: 1_000_000}), 0.2) // 10% of $2, not of $3
}

// A dated snapshot is the same model as its undated name, and is priced as one.
// Nothing is guessed: the undated name still has to be in the table.
func TestDatedSnapshotsResolveToTheirModel(t *testing.T) {
	table := NewRateTable()
	table.SetClock(func() time.Time { return sonnet5IntroEnds.Add(24 * time.Hour) })

	for _, tc := range []struct {
		model string
		input float64
	}{
		{"claude-haiku-4-5-20251001", 1},
		{"claude-opus-4-5-20251101", 5},
		{"claude-sonnet-4-5-20250929", 3},
		{"claude-sonnet-5-20260101", 3},
	} {
		rate, ok := table.Lookup(tc.model)
		if !ok {
			t.Errorf("%s is unpriced; its undated name is in the table", tc.model)
			continue
		}
		if rate.InputPerMTok != tc.input {
			t.Errorf("%s: $%v per MTok input, want $%v", tc.model, rate.InputPerMTok, tc.input)
		}
	}

	// Stripping a date must not become a general prefix match: a model whose
	// undated name is unknown stays unpriced rather than borrowing a neighbour.
	if _, ok := table.Lookup("claude-nonesuch-9-20260101"); ok {
		t.Error("an unknown model was priced by stripping its date suffix")
	}
	if _, ok := table.Lookup("claude-sonnet-5-preview"); ok {
		t.Error("a non-dated suffix was treated as a snapshot")
	}
}

// What a record cost depends on when it was incurred, not on when musem got
// round to reading it. A session that ran under an introductory rate and is
// first read after that rate expired must still be priced at the rate it had.
func TestUsageIsPricedAtTheTimeItWasIncurred(t *testing.T) {
	table := NewRateTable()
	// Read well after the introductory period ended.
	table.SetClock(func() time.Time { return sonnet5IntroEnds.Add(30 * 24 * time.Hour) })

	during := sonnet5IntroEnds.Add(-24 * time.Hour)
	rate, ok := table.LookupAt("claude-sonnet-5", during)
	if !ok {
		t.Fatal("claude-sonnet-5 must be priced")
	}
	if rate.InputPerMTok != 2 {
		t.Errorf("$%v per MTok for usage incurred during the introductory period, want $2: "+
			"it was priced by the reading clock instead of the record's own",
			rate.InputPerMTok)
	}

	// A record with no timestamp falls back to the reading clock, which is the
	// only thing left to go on.
	rate, _ = table.LookupAt("claude-sonnet-5", time.Time{})
	if rate.InputPerMTok != 3 {
		t.Errorf("$%v per MTok for an undated record, want the current $3", rate.InputPerMTok)
	}
}
