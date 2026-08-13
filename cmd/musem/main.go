// Command musem is an observatory for AI coding agent sessions, and the place
// they are started from.
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
	"github.com/MrSossa/musem/internal/launch"
	"github.com/MrSossa/musem/internal/registry"
	"github.com/MrSossa/musem/internal/sqlite"
	"github.com/MrSossa/musem/internal/tmux"
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
		branches = inmem.NewBranchResolver()
		usage = inmem.NewUsageReader()
	} else {
		discoverer = claude.NewDiscoverer()
		branches = git.NewBranchResolver()
		usage = claude.NewUsageReader()
	}

	// History is optional: without it musem still works, it just forgets. That
	// is a better outcome than refusing to start over a database problem — but
	// forgetting silently is not. Every restart would reset every session's
	// cost to zero with nothing on screen or on stderr to explain why, and a
	// read-only config directory would look exactly like a fresh install.
	var (
		store cost.HistoryStore
		owned launch.Ownership
	)
	path, err := sqlite.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "musem: history is disabled: %s\n", musem.ErrorMessage(err))
	} else if s, err := sqlite.Open(ctx, path); err != nil {
		fmt.Fprintf(os.Stderr, "musem: history is disabled: %s\n", musem.ErrorMessage(err))
	} else {
		defer func() { _ = s.Close() }()
		store = s
		// The same store holds the record of which worktrees musem created.
		// Without it, launching into a worktree is refused rather than done and
		// forgotten: the record is what permits the removal, and a worktree musem
		// could never take back is one it should not make.
		owned = s
	}

	accountant := cost.New(cost.NewRateTable(), usage, store)
	if err := accountant.Restore(ctx); err != nil {
		// Losing history is not worth refusing to start.
		fmt.Fprintf(os.Stderr, "musem: could not restore history: %s\n", musem.ErrorMessage(err))
	}

	// Launching is composed here and nowhere else: git provides the worktrees,
	// tmux the substrate, the Claude CLI the agent, and SQLite the record of what
	// was created. The launcher knows none of those names.
	launcher := launch.New(git.NewWorktrees(), tmux.NewSessions(), claude.NewAgent(), owned)

	// Reclamation hangs off the registry's own end-of-session transition, which
	// is only ever reached by a discovery pass that could read everything it
	// found. A session that merely appears to have gone never gets here, and its
	// worktree is never a candidate for deletion.
	ends := make(chan musem.Session, endBacklog)
	reg := registry.New(discoverer, branches,
		registry.WithInterval(interval),
		registry.WithSessionEnded(func(s musem.Session) {
			select {
			case ends <- s:
			default:
				// The observer runs on the refresh loop and must not block it. A
				// reclamation dropped here is not lost work: the worktree stays,
				// which is the direction every decision in this path errs in.
			}
		}),
	)
	composer := app.New(reg, accountant, app.WithReclamations(launcher))

	cwd, err := os.Getwd()
	if err != nil {
		// The form can still be filled in by hand; it just starts empty.
		cwd = ""
	}

	program := tea.NewProgram(
		tui.NewModel(
			tui.WithLauncher(launcher),
			tui.WithWorkingDir(cwd),
			tui.WithContext(ctx),
		),
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)

	// One goroutine per source, each feeding the single pump.
	go reg.Run(ctx)
	go reclaim(ctx, launcher, ends)

	quit := make(chan struct{})
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		tui.Pump(ctx, program, composer, interval, quit)
	}()

	_, err = program.Run()

	// Captured before stop(), which makes ctx.Err() non-nil unconditionally and
	// would otherwise make every genuine kill look like an orderly shutdown.
	cause := ctx.Err()

	// The store is closed by a defer registered above, so it closes when this
	// function returns — after everything here. Waiting for the pump is what
	// keeps it from writing to a database that has already gone: a Save landing
	// in that gap fails, and its error is deliberately discarded upstream, so
	// the loss would be silent.
	//
	// The pump is asked to stop before ctx is cancelled, not by cancelling it.
	// Cancelling is what the in-flight write is handed, so doing it first would
	// fail the very write this wait exists to let finish.
	close(quit)
	select {
	case <-pumped:
	case <-time.After(shutdownGrace):
		// A pump wedged in I/O must not hold the terminal hostage. Closing the
		// store under it risks one failed write, which is the lesser harm.
	}
	stop()

	return shutdownError(err, cause)
}

// shutdownGrace bounds how long musem waits for the refresh loop to notice it
// has been cancelled.
const shutdownGrace = 2 * time.Second

// endBacklog is how many ended sessions may be waiting to have their worktrees
// reclaimed. Reclamation shells out to git several times per session, and the
// refresh loop that announces the ends must not queue behind it.
const endBacklog = 64

// reclaim takes back the worktrees of sessions that have ended, one at a time.
//
// Serial by construction: two removals at once would be two git processes
// writing to the same repository's administrative files, and there is nothing
// here worth the concurrency. The result reaches the interface through the
// launcher, which the composer reads on every frame.
func reclaim(ctx context.Context, launcher *launch.Launcher, ends <-chan musem.Session) {
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-ends:
			launcher.Reclaim(ctx, s)
		}
	}
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
