package cost

import (
	"regexp"
	"sync"
	"time"
)

// Rate is the published price for one model, in US dollars per million tokens.
//
// Only the base input and output prices are stored. Cache prices are fixed
// multiples of the input price, so deriving them removes four numbers per model
// that could drift out of step with each other.
type Rate struct {
	InputPerMTok  float64
	OutputPerMTok float64

	// IntroInputPerMTok and IntroOutputPerMTok are an introductory price, in
	// effect until IntroUntil. A zero IntroUntil means there is no such price
	// and the base rates apply always.
	//
	// Promotional pricing is carried here rather than folded into the base rates
	// because it expires. A table that quietly holds an introductory figure past
	// its end date understates every session on that model; one that ignores the
	// promotion overstates every session before it. Both are confident wrong
	// numbers, which is the failure this package exists to prevent.
	IntroInputPerMTok  float64
	IntroOutputPerMTok float64
	IntroUntil         time.Time
}

// at returns the rate in effect at t, resolving any introductory pricing.
func (r Rate) at(t time.Time) Rate {
	if r.IntroUntil.IsZero() || !t.Before(r.IntroUntil) {
		return Rate{InputPerMTok: r.InputPerMTok, OutputPerMTok: r.OutputPerMTok}
	}
	return Rate{InputPerMTok: r.IntroInputPerMTok, OutputPerMTok: r.IntroOutputPerMTok}
}

// Cache pricing multipliers, applied to a model's input rate.
//
// Writes cost more than plain input because the entry has to be created; the
// one-hour tier costs more than the five-minute tier because it is held longer.
// Reads are the point of the whole exercise, at a tenth of the input price.
const (
	cacheWrite5mMultiplier = 1.25
	cacheWrite1hMultiplier = 2.0
	cacheReadMultiplier    = 0.1
)

// RatesVersion identifies this table's vintage. Prices change; when a figure
// here is questioned, this is what says how old the answer is.
const RatesVersion = "2026-08-11"

// sonnet5IntroEnds is the first instant at which Claude Sonnet 5 is billed at
// its standard rate. The introductory price runs through 2026-08-31.
var sonnet5IntroEnds = time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

// defaultRates is the published price list, keyed by exact model identifier.
//
// Matching is exact and deliberately so: a prefix match would silently price a
// model musem has never seen using its neighbour's rate, which is the failure
// this package exists to avoid. Dated snapshots are the one exception, and are
// derived rather than listed — see snapshotSuffix.
var defaultRates = map[string]Rate{
	"claude-fable-5":    {InputPerMTok: 10, OutputPerMTok: 50},
	"claude-mythos-5":   {InputPerMTok: 10, OutputPerMTok: 50},
	"claude-opus-5":     {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-8":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-7":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-6":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-5":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5},

	"claude-sonnet-5": {
		InputPerMTok: 3, OutputPerMTok: 15,
		IntroInputPerMTok: 2, IntroOutputPerMTok: 10,
		IntroUntil: sonnet5IntroEnds,
	},
}

// snapshotSuffix matches the dated form of a model identifier — the same model
// as its undated name, pinned to one release.
//
// Stripping it is not the prefix match the exactness rule forbids: it resolves
// one identifier for a model to another identifier for the same model, and only
// succeeds when the undated name is itself in the table. Deriving it beats
// listing every dated form, which is a second place for the same price to drift.
var snapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// RateTable resolves a model identifier to its price.
//
// It is safe to use from several goroutines. Set exists so a model released
// after this binary was built can be priced without waiting for a new release,
// which means it can be called while the refresh loop is already looking prices
// up — an unsynchronised map would make that a data race, and one that shows up
// as a corrupted read rather than an honest failure.
type RateTable struct {
	mu    sync.RWMutex
	rates map[string]Rate

	// now decides which side of an introductory price's expiry we are on. It is
	// a field so a test can stand either side of that date without waiting.
	now func() time.Time
}

// NewRateTable returns the built-in table.
func NewRateTable() *RateTable {
	rates := make(map[string]Rate, len(defaultRates))
	for model, rate := range defaultRates {
		rates[model] = rate
	}
	return &RateTable{rates: rates, now: time.Now}
}

// SetClock replaces the clock used to resolve introductory pricing.
func (t *RateTable) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}

// Set overrides or adds a rate, so a model released after this binary was built
// can be priced without waiting for a new release.
func (t *RateTable) Set(model string, rate Rate) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rates == nil {
		t.rates = make(map[string]Rate)
	}
	t.rates[model] = rate
}

// Lookup returns the rate in effect for a model now, and whether one is known.
func (t *RateTable) Lookup(model string) (Rate, bool) {
	return t.LookupAt(model, time.Time{})
}

// LookupAt returns the rate that applied to a model at a given moment. A zero
// moment means "whatever applies now", for a record that carried no timestamp.
func (t *RateTable) LookupAt(model string, at time.Time) (Rate, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	rate, ok := t.rates[model]
	if !ok {
		// A dated snapshot is the same model as its undated name, and is priced
		// as one. Nothing is guessed: the undated name still has to be listed.
		if base := snapshotSuffix.ReplaceAllString(model, ""); base != model {
			rate, ok = t.rates[base]
		}
	}
	if !ok {
		return Rate{}, false
	}

	if at.IsZero() {
		now := time.Now
		if t.now != nil {
			now = t.now
		}
		at = now()
	}
	return rate.at(at), true
}

// price computes the dollar cost of one usage record at this rate.
func (r Rate) price(u usageInput) float64 {
	const perMillion = 1_000_000.0

	input := r.InputPerMTok / perMillion
	output := r.OutputPerMTok / perMillion

	return float64(u.input)*input +
		float64(u.output)*output +
		float64(u.cacheWrite5m)*input*cacheWrite5mMultiplier +
		float64(u.cacheWrite1h)*input*cacheWrite1hMultiplier +
		float64(u.cacheRead)*input*cacheReadMultiplier
}

// usageInput is the token breakdown price operates on, kept local so the rate
// table does not depend on the shape of the domain type.
type usageInput struct {
	input        int64
	output       int64
	cacheWrite5m int64
	cacheWrite1h int64
	cacheRead    int64
}
