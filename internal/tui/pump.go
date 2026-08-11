package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MrSossa/musem/internal/app"
)

// SnapshotSource produces the snapshots the dashboard renders.
type SnapshotSource interface {
	Snapshot() app.Snapshot
	Refresh(ctx context.Context)
}

// Pump is the only bridge between the goroutines that gather data and the UI
// loop. Everything else is single-threaded by construction, so this is the only
// place a data race can appear — and the only place to look when one does.
func Pump(ctx context.Context, p *tea.Program, src SnapshotSource, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}

	// Send once immediately so the first frame is not an empty screen for a
	// whole interval.
	p.Send(SnapshotMsg{Snapshot: src.Snapshot()})

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		src.Refresh(ctx)
		p.Send(SnapshotMsg{Snapshot: src.Snapshot()})
	}
}
