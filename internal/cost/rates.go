package cost

// Rate is the published price for one model, in US dollars per million tokens.
//
// Only the base input and output prices are stored. Cache prices are fixed
// multiples of the input price, so deriving them removes four numbers per model
// that could drift out of step with each other.
type Rate struct {
	InputPerMTok  float64
	OutputPerMTok float64
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

// defaultRates is the published price list, keyed by exact model identifier.
//
// Matching is exact and deliberately so: a prefix match would silently price a
// model musem has never seen using its neighbour's rate, which is the failure
// this package exists to avoid.
var defaultRates = map[string]Rate{
	"claude-fable-5":            {InputPerMTok: 10, OutputPerMTok: 50},
	"claude-mythos-5":           {InputPerMTok: 10, OutputPerMTok: 50},
	"claude-opus-5":             {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-8":           {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-7":           {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-6":           {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-5":           {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-sonnet-5":           {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-6":         {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-5":         {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5":          {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-haiku-4-5-20251001": {InputPerMTok: 1, OutputPerMTok: 5},
}

// RateTable resolves a model identifier to its price.
type RateTable struct {
	rates map[string]Rate
}

// NewRateTable returns the built-in table.
func NewRateTable() *RateTable {
	rates := make(map[string]Rate, len(defaultRates))
	for model, rate := range defaultRates {
		rates[model] = rate
	}
	return &RateTable{rates: rates}
}

// Set overrides or adds a rate, so a model released after this binary was built
// can be priced without waiting for a new release.
func (t *RateTable) Set(model string, rate Rate) { t.rates[model] = rate }

// Lookup returns the rate for a model and whether one is known.
func (t *RateTable) Lookup(model string) (Rate, bool) {
	rate, ok := t.rates[model]
	return rate, ok
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
