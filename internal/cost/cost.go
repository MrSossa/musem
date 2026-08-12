// Package cost turns recorded token usage into money.
//
// It owns the rate table and the running totals, and it is deliberately strict
// about the difference between "this cost nothing" and "I could not work out
// what this cost". A plausible wrong number is worse than an admitted gap: no
// one audits a figure that looks fine.
package cost

import (
	"context"
	"sort"
	"sync"

	"github.com/MrSossa/musem"
)

// UsageReader reports usage recorded for a session since the given cursor,
// returning the cursor to resume from next time.
//
// Declared here by the consumer: cost states what it needs and stays ignorant
// of transcripts, files, and the tool that wrote them. The cursor is opaque
// here for the same reason — the accountant stores it and hands it back, and
// never asks what it means.
type UsageReader interface {
	ReadUsage(ctx context.Context, sessionID, cursor string) (musem.UsageReading, error)
}

// HistoryStore persists accumulated usage so it survives both a restart and the
// disappearance of whatever recorded it in the first place.
type HistoryStore interface {
	Load(ctx context.Context) (map[string]musem.SessionCost, error)
	Save(ctx context.Context, sc musem.SessionCost) error
}

// Accountant keeps the running cost of every session it has seen.
type Accountant struct {
	rates  *RateTable
	reader UsageReader
	store  HistoryStore

	// updateMu serialises the read-modify-write in Update. The data lock cannot
	// do it: reading usage is I/O, and holding a lock the UI needs across it
	// would show up on screen as a freeze. Without this, two passes could both
	// read from the same cursor and count the same tokens twice.
	updateMu sync.Mutex

	mu    sync.RWMutex
	costs map[string]musem.SessionCost
}

// New returns an Accountant. The store may be nil, in which case history lives
// only for the lifetime of the process.
func New(rates *RateTable, reader UsageReader, store HistoryStore) *Accountant {
	if rates == nil {
		rates = NewRateTable()
	}
	return &Accountant{
		rates:  rates,
		reader: reader,
		store:  store,
		costs:  make(map[string]musem.SessionCost),
	}
}

// Restore loads persisted history. Called once at startup.
func (a *Accountant) Restore(ctx context.Context) error {
	if a.store == nil {
		return nil
	}
	loaded, err := a.store.Load(ctx)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for id, sc := range loaded {
		a.costs[id] = sc
	}
	return nil
}

// Update folds any newly recorded usage for a session into its running total.
//
// Usage already counted is never recomputed — it is accumulated. That is what
// lets the total survive the source record being deleted. The reader is told
// where the last pass stopped, and where this one stopped is saved in the same
// write as the total it produced: a total that persists while its cursor does
// not is exactly how a restart ends up counting the same tokens twice.
func (a *Accountant) Update(ctx context.Context, sessionID string) error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()

	a.mu.RLock()
	cursor := a.costs[sessionID].Cursor
	a.mu.RUnlock()

	reading, err := a.reader.ReadUsage(ctx, sessionID, cursor)
	if err != nil {
		return err
	}
	if len(reading.Entries) == 0 && reading.Skipped == 0 && reading.Cursor == cursor && !reading.Reset {
		return nil
	}
	fresh := reading.Entries

	a.mu.Lock()
	sc := a.costs[sessionID]
	if reading.Reset {
		// The source record was replaced or truncated and is being read from
		// its beginning again, so everything derived from it has to go. Adding
		// this reading to totals that already contain the same records would
		// double them; keeping the old totals and the new records together
		// would be the same mistake spread over two passes.
		sc = musem.SessionCost{}
	}
	sc.SessionID = sessionID
	sc.Cursor = reading.Cursor
	sc.Skipped += reading.Skipped

	unknown := make(map[string]bool, len(sc.UnknownModels))
	for _, m := range sc.UnknownModels {
		unknown[m] = true
	}

	// Accumulated dollars live in their own field rather than being read back
	// out of the domain Cost, so a single unpriceable entry does not destroy
	// the arithmetic for everything priced before or after it.
	known := sc.Priced

	for _, mu := range fresh {
		sc.Usage.Add(mu.Usage)

		rate, ok := a.rates.LookupAt(mu.Model, mu.At)
		if !ok {
			unknown[mu.Model] = true
			continue
		}
		known += rate.price(usageInput{
			input:        mu.Usage.InputTokens,
			output:       mu.Usage.OutputTokens,
			cacheWrite5m: mu.Usage.CacheWrite5mTokens,
			cacheWrite1h: mu.Usage.CacheWrite1hTokens,
			cacheRead:    mu.Usage.CacheReadTokens,
		})
	}

	sc.Priced = known
	sc.UnknownModels = sortedKeys(unknown)
	if len(sc.UnknownModels) > 0 {
		// Tokens are still counted; only the money is unavailable. Reporting a
		// partial figure as if it were the whole bill is the failure to avoid.
		sc.Cost = musem.UnknownCost()
	} else {
		sc.Cost = musem.USD(known)
	}

	a.costs[sessionID] = sc
	a.mu.Unlock()

	if a.store != nil {
		return a.store.Save(ctx, sc)
	}
	return nil
}

// Session returns the accounting for one session.
func (a *Accountant) Session(sessionID string) (musem.SessionCost, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	sc, ok := a.costs[sessionID]
	return sc, ok
}

// Fleet is the aggregate across every known session.
type Fleet struct {
	Usage musem.Usage
	Cost  musem.Cost

	// UnknownModels names every model that could not be priced anywhere in the
	// fleet. Non-empty means Cost is unknown, and says which gaps to close.
	UnknownModels []string

	// Skipped is how many records were passed over as unreadable across the
	// whole fleet. It is carried here for the same reason it is carried per
	// session: without it the aggregate silently understates, and a total that
	// is too low with nothing to say so is the number nobody audits.
	Skipped int

	// Priced is the dollars accumulated across these sessions from usage that
	// did have a rate. It is the floor under an unknown total.
	//
	// Without it the headline discards every priced dollar the moment one
	// session touches a model with no rate — nine sessions worth $412 render as
	// an em dash while the rows beneath them still show their figures, leaving
	// the summary strictly less informative than the list it summarises. It is
	// the same gap SessionCost.Priced closes one level down.
	Priced float64

	// Unrecorded counts sessions asked for that the accountant holds no figures
	// for at all, because their usage has never been read successfully. They
	// contribute nothing to the total, so it is missing whole sessions rather
	// than merely parts of them.
	//
	// The total stays a real figure rather than going unknown, and is reported
	// beside this count. Sessions are never dropped from the inventory, so one
	// session that ended before its transcript could be found would otherwise
	// hold the headline figure at an em dash for the rest of the run however
	// many priced sessions came and went — an outage dressed as honesty. A
	// figure that names what it is missing is neither silent nor useless.
	Unrecorded int
}

// Partial reports whether some part of the fleet total is missing, whether
// because it could not be priced or because a whole session never reached the
// accounting. It is the one-question form; a caller that distinguishes the two
// gaps asks Unpriceable and reads Unrecorded.
func (f Fleet) Partial() bool { return f.Unpriceable() || f.Unrecorded > 0 }

// Unpriceable reports whether usage reached the total but could not be priced.
func (f Fleet) Unpriceable() bool { return len(f.UnknownModels) > 0 }

// Degraded reports whether some usage never reached the total at all, because
// the records carrying it could not be read. Distinct from Partial: partial
// means counted but unpriceable, degraded means never counted.
func (f Fleet) Degraded() bool { return f.Skipped > 0 }

// Total aggregates the named sessions. Tokens always add up; the cost is
// unknown as soon as any component is, because a total silently missing a
// component cannot be told apart from a complete one.
//
// The caller names the sessions rather than getting everything the accountant
// holds. History outlives the sessions that produced it — the store is loaded
// whole at startup and nothing is ever evicted — so totalling the map would
// pair a figure covering every session ever seen with a count of the few on
// screen, and one old session that met an unpriced model would hold the total
// unknown for every future run.
func (a *Accountant) Total(sessionIDs []string) Fleet {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var fleet Fleet
	unknown := make(map[string]bool)
	total := musem.USD(0)

	for _, id := range sessionIDs {
		sc, ok := a.costs[id]
		if !ok {
			// Asked for and never recorded: its usage could not be read, so
			// whatever it spent is missing from every figure here.
			fleet.Unrecorded++
			continue
		}
		fleet.Usage.Add(sc.Usage)
		total = total.Add(sc.Cost)
		fleet.Priced += sc.Priced
		fleet.Skipped += sc.Skipped
		for _, m := range sc.UnknownModels {
			unknown[m] = true
		}
	}

	fleet.Cost = total
	fleet.UnknownModels = sortedKeys(unknown)
	return fleet
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
