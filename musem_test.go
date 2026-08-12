package musem_test

import (
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

// The domain package is the one every other package depends on, so its
// invariants are exercised indirectly all over the suite. That is not the same
// as being tested: an invariant checked only through a caller is one that moves
// the day the caller changes shape. These tests state the rules directly.

func TestStatusValid(t *testing.T) {
	for _, s := range []musem.Status{
		musem.StatusRunning, musem.StatusWaiting, musem.StatusIdle,
		musem.StatusDead, musem.StatusIndeterminate,
	} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}

	for _, s := range []musem.Status{"", "busy", "RUNNING", "unknown"} {
		if s.Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

// TestStatusUrgencyOrder pins the order rather than the numbers. What the
// constants are is an implementation detail; that waiting outranks everything
// is the domain fact the dashboard's whole purpose rests on.
func TestStatusUrgencyOrder(t *testing.T) {
	ordered := []musem.Status{
		musem.StatusWaiting,
		musem.StatusDead,
		musem.StatusRunning,
		musem.StatusIndeterminate,
		musem.StatusIdle,
	}

	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].Urgency() >= ordered[i].Urgency() {
			t.Errorf("%q should be more urgent than %q, got %d and %d",
				ordered[i-1], ordered[i], ordered[i-1].Urgency(), ordered[i].Urgency())
		}
	}

	// An unrecognised status sorts below every known one rather than landing in
	// the middle of them, where it would displace a session that is waiting.
	if got, want := musem.Status("nonsense").Urgency(), musem.StatusIdle.Urgency(); got <= want {
		t.Errorf("unknown status urgency = %d, want greater than idle's %d", got, want)
	}
}

func TestSessionValidate(t *testing.T) {
	tests := []struct {
		name    string
		session musem.Session
		code    string
	}{
		{"valid", musem.Session{ID: "a", Status: musem.StatusIdle}, ""},
		{"missing id", musem.Session{Status: musem.StatusIdle}, musem.EINVALID},
		{"blank id", musem.Session{ID: "   ", Status: musem.StatusIdle}, musem.EINVALID},
		{"unknown status", musem.Session{ID: "a", Status: "sleeping"}, musem.EINVALID},
		{"empty status", musem.Session{ID: "a"}, musem.EINVALID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.session.Validate()
			if got := musem.ErrorCode(err); got != tt.code {
				t.Fatalf("Validate() code = %q, want %q (err: %v)", got, tt.code, err)
			}
		})
	}
}

func TestSessionEnded(t *testing.T) {
	var s musem.Session
	if s.Ended() {
		t.Error("a session with no end date has not ended")
	}

	at := time.Now()
	s.EndedAt = &at
	if !s.Ended() {
		t.Error("a session with an end date has ended")
	}
}

func TestUsageAddAndTotals(t *testing.T) {
	var u musem.Usage
	if !u.IsZero() {
		t.Error("the zero usage consumed nothing")
	}

	u.Add(musem.Usage{
		InputTokens: 10, OutputTokens: 20,
		CacheWrite5mTokens: 3, CacheWrite1hTokens: 4, CacheReadTokens: 5,
	})
	u.Add(musem.Usage{InputTokens: 1, CacheWrite1hTokens: 1})

	if got, want := u.InputTokens, int64(11); got != want {
		t.Errorf("InputTokens = %d, want %d", got, want)
	}
	if got, want := u.CacheWriteTokens(), int64(8); got != want {
		t.Errorf("CacheWriteTokens() = %d, want %d", got, want)
	}
	if got, want := u.Total(), int64(11+20+8+5); got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
	if u.IsZero() {
		t.Error("usage that consumed tokens is not zero")
	}
}

// TestUsageZeroIsNotUnknown guards the distinction the whole cost model rests
// on: a session that has produced nothing has zero usage, which is a fact, and
// is not the same as usage nobody could read.
func TestUsageZeroIsNotUnknown(t *testing.T) {
	if !(musem.Usage{}).IsZero() {
		t.Error("no usage recorded is zero usage")
	}
	if _, known := musem.USD(0).Amount(); !known {
		t.Error("a zero cost is a known cost")
	}
	if _, known := musem.UnknownCost().Amount(); known {
		t.Error("an unknown cost yields no amount")
	}
}

func TestCostKnownAndAmount(t *testing.T) {
	c := musem.USD(12.5)
	if !c.Known() {
		t.Fatal("USD returns a known cost")
	}
	amount, known := c.Amount()
	if !known || amount != 12.5 {
		t.Fatalf("Amount() = %v, %v; want 12.5, true", amount, known)
	}

	// The zero value is a known zero, not an unknown. A Cost that had to be
	// constructed to be known would read as unknown anywhere one is declared.
	if !(musem.Cost{}).Known() {
		t.Error("the zero Cost is a known zero")
	}

	u := musem.UnknownCost()
	if u.Known() {
		t.Fatal("UnknownCost is not known")
	}
	if amount, known := u.Amount(); known || amount != 0 {
		t.Fatalf("Amount() = %v, %v; want 0, false", amount, known)
	}
}

// TestCostAddUnknownIsContagious is the rule that keeps a total from quietly
// omitting a component: a sum missing part of itself must not be presentable as
// a complete figure.
func TestCostAddUnknownIsContagious(t *testing.T) {
	tests := []struct {
		name  string
		a, b  musem.Cost
		known bool
		want  float64
	}{
		{"known + known", musem.USD(1.5), musem.USD(2.25), true, 3.75},
		{"known + unknown", musem.USD(1.5), musem.UnknownCost(), false, 0},
		{"unknown + known", musem.UnknownCost(), musem.USD(1.5), false, 0},
		{"unknown + unknown", musem.UnknownCost(), musem.UnknownCost(), false, 0},
		{"zero + unknown", musem.USD(0), musem.UnknownCost(), false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			amount, known := tt.a.Add(tt.b).Amount()
			if known != tt.known || amount != tt.want {
				t.Fatalf("Add() = %v, %v; want %v, %v", amount, known, tt.want, tt.known)
			}
		})
	}
}

func TestCostString(t *testing.T) {
	tests := []struct {
		cost musem.Cost
		want string
	}{
		{musem.USD(0), "$0.00"},
		{musem.USD(1), "$1.00"},
		{musem.USD(12.345), "$12.35"},
		{musem.UnknownCost(), "—"},
	}

	for _, tt := range tests {
		if got := tt.cost.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

// TestSessionCostPartialAndDegraded pins the two gaps apart. Partial means the
// tokens were counted but could not be priced; degraded means they were never
// counted. Collapsing them would leave the interface unable to say which of the
// two a figure is suffering from.
func TestSessionCostPartialAndDegraded(t *testing.T) {
	tests := []struct {
		name     string
		sc       musem.SessionCost
		partial  bool
		degraded bool
	}{
		{"complete", musem.SessionCost{Cost: musem.USD(1)}, false, false},
		{"unpriced model", musem.SessionCost{
			Cost: musem.UnknownCost(), UnknownModels: []string{"claude-unheard-of"},
		}, true, false},
		{"unknown cost alone", musem.SessionCost{Cost: musem.UnknownCost()}, true, false},
		{"unreadable records", musem.SessionCost{Cost: musem.USD(1), Skipped: 3}, false, true},
		{"both", musem.SessionCost{
			Cost: musem.UnknownCost(), UnknownModels: []string{"x"}, Skipped: 1,
		}, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sc.Partial(); got != tt.partial {
				t.Errorf("Partial() = %v, want %v", got, tt.partial)
			}
			if got := tt.sc.Degraded(); got != tt.degraded {
				t.Errorf("Degraded() = %v, want %v", got, tt.degraded)
			}
		})
	}
}
