// Command musem is a read-only observatory for AI coding agent sessions.
//
// This file is the composition root: the only place that knows which adapter
// satisfies which port. Everything is injected by constructor — there are no
// package-level globals, no init side effects, and no shared dependency
// container, because a struct carrying every dependency hides what each
// consumer actually needs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/app"
	"github.com/MrSossa/musem/internal/claude"
	"github.com/MrSossa/musem/internal/cost"
	"github.com/MrSossa/musem/internal/git"
	"github.com/MrSossa/musem/internal/inmem"
	"github.com/MrSossa/musem/internal/registry"
	"github.com/MrSossa/musem/internal/sqlite"
	"github.com/MrSossa/musem/internal/tui"
)

func main() {
	var (
		fake     = flag.Bool("fake", false, "serve fabricated sessions instead of real ones, for development")
		interval = flag.Duration("interval", registry.DefaultInterval, "how long to wait between refreshes")
	)
	flag.Parse()

	if err := run(*fake, *interval); err != nil {
		// Application errors get their curated message; anything else is
		// printed as-is. Collapsing an unrecognised startup failure into
		// "an internal error occurred" would hide the one thing the user
		// needs — a terminal that cannot be opened, a permission problem,
		// a port already taken.
		var appErr *musem.Error
		if errors.As(err, &appErr) {
			fmt.Fprintf(os.Stderr, "musem: %s\n", appErr.Message)
		} else {
			fmt.Fprintf(os.Stderr, "musem: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(fake bool, interval time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Adapters. Which one satisfies which port is decided here and nowhere else.
	var (
		discoverer registry.Discoverer
		branches   registry.BranchResolver
		usage      cost.UsageReader
	)
	if fake {
		fakeDiscoverer := inmem.NewDiscoverer()
		discoverer = fakeDiscoverer
		branches = inmem.BranchResolver{}
		usage = inmem.NewUsageReader()
	} else {
		discoverer = claude.NewDiscoverer()
		branches = git.NewBranchResolver()
		usage = claude.NewUsageReader()
	}

	// History is optional: without it musem still works, it just forgets. That
	// is a better outcome than refusing to start over a database problem.
	var store cost.HistoryStore
	if path, err := sqlite.DefaultPath(); err == nil {
		if s, err := sqlite.Open(ctx, path); err == nil {
			defer func() { _ = s.Close() }()
			store = s
		}
	}

	accountant := cost.New(cost.NewRateTable(), usage, store)
	if err := accountant.Restore(ctx); err != nil {
		// Losing history is not worth refusing to start.
		fmt.Fprintf(os.Stderr, "musem: could not restore history: %s\n", musem.ErrorMessage(err))
	}

	reg := registry.New(discoverer, branches, registry.WithInterval(interval))
	composer := app.New(reg, accountant)

	program := tea.NewProgram(tui.NewModel(), tea.WithAltScreen(), tea.WithContext(ctx))

	// One goroutine per source, each feeding the single pump.
	snapshots := make(chan registry.Snapshot, 1)
	go reg.Run(ctx, snapshots)
	go drain(ctx, snapshots)
	go tui.Pump(ctx, program, composer, interval)

	_, err := program.Run()
	return shutdownError(err, ctx.Err())
}

// shutdownError decides whether the UI loop stopping was a failure.
//
// Being asked to stop is not one. bubbletea reports a cancelled context as
// ErrProgramKilled, the same error it uses for a genuine kill, so the state of
// the signal context is what tells the two apart. Without this, a supervisor
// stopping musem with SIGTERM sees a non-zero exit and an error on stderr for
// what was an orderly shutdown.
func shutdownError(err, ctxErr error) error {
	if errors.Is(err, tea.ErrProgramKilled) && ctxErr != nil {
		return nil
	}
	return err
}

// drain consumes the registry's own snapshot channel. The registry publishes
// there so a second consumer could be added without touching it; musem reads
// composed snapshots through the pump instead, so this keeps the loop from
// blocking on a channel nobody is listening to.
func drain(ctx context.Context, ch <-chan registry.Snapshot) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		}
	}
}
