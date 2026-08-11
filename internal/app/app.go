// Package app joins what the registry knows about sessions with what the
// accountant knows about their cost, and hands the result out as one snapshot.
//
// It is deliberately thin. The dashboard needs a session and its cost in the
// same row, so something has to join them — and if that something were the UI,
// the join would have to be duplicated by any second front end. The moment this
// package grows rules of its own, those rules belong to registry or cost.
package app

import (
	"context"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/cost"
	"github.com/MrSossa/musem/internal/registry"
)

// Row is one session with its accounting attached.
type Row struct {
	Session musem.Session
	Cost    musem.Cost

	// Partial reports that some of this session's usage could not be priced.
	Partial bool
}

// Snapshot is everything the dashboard renders in one pass.
type Snapshot struct {
	Rows  []Row
	Fleet cost.Fleet

	// Stale, ErrCode and ErrMessage carry the registry's honesty about its own
	// data: how old it is and why, if the last refresh failed.
	Stale      bool
	ErrCode    string
	ErrMessage string
}

// Composer assembles snapshots.
type Composer struct {
	registry   *registry.Registry
	accountant *cost.Accountant
}

// New returns a Composer over the given sources.
func New(r *registry.Registry, a *cost.Accountant) *Composer {
	return &Composer{registry: r, accountant: a}
}

// Snapshot joins the current inventory with current costs, preserving the
// registry's ordering — which already puts sessions awaiting the user first.
func (c *Composer) Snapshot() Snapshot {
	inventory := c.registry.Snapshot()

	rows := make([]Row, 0, len(inventory.Sessions))
	for _, s := range inventory.Sessions {
		row := Row{Session: s, Cost: musem.USD(0)}
		if sc, ok := c.accountant.Session(s.ID); ok {
			row.Cost = sc.Cost
			row.Partial = sc.Partial()
		}
		rows = append(rows, row)
	}

	return Snapshot{
		Rows:       rows,
		Fleet:      c.accountant.Total(),
		Stale:      inventory.Stale,
		ErrCode:    inventory.ErrCode,
		ErrMessage: inventory.ErrMessage,
	}
}

// Refresh updates costs for every session currently known. Discovery has its
// own loop in the registry; this keeps the accounting in step with it.
//
// A session whose usage cannot be read is skipped rather than failing the pass:
// one unreadable transcript must not stop the other figures from updating.
func (c *Composer) Refresh(ctx context.Context) {
	for _, s := range c.registry.Snapshot().Sessions {
		_ = c.accountant.Update(ctx, s.ID)
	}
}
