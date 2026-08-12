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
//
// It stops on either ctx or quit, and the two mean different things. Closing
// quit asks for no further passes while leaving ctx alive, so a refresh already
// under way finishes and its writes land; cancelling ctx stops the work itself,
// which is what a signal calls for. Having only the second would mean an
// ordinary quit tore up whatever was mid-flight.
func Pump(ctx context.Context, p *tea.Program, src SnapshotSource, interval time.Duration, quit <-chan struct{}) {
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
		case <-quit:
			return
		case <-time.After(interval):
		}

		src.Refresh(ctx)
		p.Send(SnapshotMsg{Snapshot: src.Snapshot()})
	}
}
