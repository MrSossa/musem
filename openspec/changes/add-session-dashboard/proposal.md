## Why

With several agent sessions open at once there is no way to tell what is
happening in them without checking window by window: which one finished, which
one has been waiting ten minutes for an approval, which one got stuck, and how
much the whole thing is costing. The information already exists and is exact —
the JSONL transcripts carry real `usage` and `claude agents --json` publishes
live status — but today nobody looks at it.

This first change builds the observatory: a read-only TUI that answers "what is
happening right now?" without launching or controlling anything. It is the data
layer everything else will be built on, and it is useful on its own from day
one.

## What Changes

- Bootstrap the Go module: package layout, `Makefile`, `golangci-lint`, and the
  CI matrix (macOS + Linux) that is a bare `echo` today.
- Discover live sessions by periodically polling `claude agents --json`, without
  coupling to any tool's rendered UI.
- Read the JSONL transcripts to derive per-session tokens and cost, with the
  cache breakdown kept separate (that is where the money goes) and a fleet-wide
  aggregate.
- A read-only TUI listing sessions with their status, working directory, git
  branch and accumulated cost, refreshing on its own.
- Local SQLite persistence so cost history survives restarts and does not depend
  on the transcripts still being there.

Deliberately out of scope for this change: launching sessions, managing
worktrees, embedding PTY panes, sending input to a session, outbound
notifications, and native Windows. The goal is to validate the data layer before
building on top of it; launching sessions comes in the next change.

## Capabilities

### New Capabilities

- `session-registry`: discovery and inventory of the live agent sessions on the
  machine, with their stable identity, working directory, git branch and
  observed status (running, waiting on the user, idle, dead), including how the
  inventory refreshes and what happens when a session disappears.
- `session-cost`: token and cost accounting per session derived from the
  structured data in the transcripts, with cache breakdown, cross-session
  aggregation and history persistence.
- `session-dashboard`: the read-only view — which columns it shows, how it
  orders them, how often it refreshes, how the user navigates, and how it
  communicates that a value is stale or unavailable.

### Modified Capabilities

None: the project has no prior specs.

## Impact

- **Code**: the repository has no product code today. The Go module and the
  initial package structure appear here.
- **Dependencies**: Bubble Tea, Lipgloss, Bubbles and `modernc.org/sqlite`.
  `CGO_ENABLED=0` becomes a project invariant, not a preference.
- **CI**: the workflow stops being a placeholder and starts actually protecting
  `main`, with a macOS + Linux matrix.
- **External coupling**: musem depends on the JSONL transcript format and on the
  output of `claude agents --json`, both surfaces that can change between
  versions. They are isolated behind an adapter so a foreign change breaks
  exactly one place.
- **Privacy**: musem reads transcripts containing source code and prompts. All
  processing is local; this change introduces no network egress.
- **`.gitignore`**: currently written for Node and Python; it needs adjusting
  for Go.
