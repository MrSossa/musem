# Session dashboard

**Slug**: `add-session-dashboard`
**Created**: 2026-08-10

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

## What

A user running several agent sessions opens musem and sees, in a single screen,
every live session with its status, working directory, git branch and
accumulated cost, plus the fleet-wide total. Sessions waiting on the user come
first. The view refreshes on its own and says so visibly when a value stopped
being current. Cost history survives closing musem and survives the source
transcript disappearing.

Nothing in the interface can start, stop, modify or write to a session or a
repository.

## Scope

**In**:
- Bootstrap of the Go module: package layout, `Makefile`, `golangci-lint`, and
  the CI matrix (macOS + Linux) that is a bare `echo` today.
- Discovery of live sessions by periodically polling `claude agents --json`,
  without coupling to any tool's rendered UI.
- Reading the JSONL transcripts to derive per-session tokens and cost, with the
  cache breakdown kept separate (that is where the money goes) and a fleet-wide
  aggregate.
- A read-only TUI listing sessions with status, working directory, git branch
  and accumulated cost, refreshing on its own.
- Local SQLite persistence so cost history survives restarts and does not depend
  on the transcripts still being there.

**Out**:
- Launching sessions, managing worktrees, embedding PTY panes, sending input to
  a session and outbound notifications. The goal is to validate the data layer
  before building on top of it; launching sessions comes in the next change.
- Native Windows. Renouncing it is what makes depending on tmux possible later.
- Abstracting over multiple agent tools: the adapter seam is in place, the
  generalisation is not paid for up front.

## Capabilities

- `session-registry` (new): discovery and inventory of the live agent sessions
  on the machine, with their stable identity, working directory, git branch and
  observed status, including how the inventory refreshes and what happens when a
  session disappears.
- `session-cost` (new): token and cost accounting per session derived from the
  structured data in the transcripts, with cache breakdown, cross-session
  aggregation and history persistence.
- `session-dashboard` (new): the read-only view — columns, ordering, refresh
  cadence, navigation, and how it communicates that a value is stale or
  unavailable.

No capability is modified or removed: the project has no prior specs.

## Acceptance criteria

The change is done when:

- [ ] Sessions started outside musem appear in the inventory on the next refresh
      cycle, keyed by stable identifier rather than title or path (R1, R2).
- [ ] A session blocked on a user confirmation reads as "waiting" and not as
      "idle", and an ambiguous signal reads as indeterminate rather than as a
      guess (R3).
- [ ] Per-session tokens and cost come from the recorded `usage`, with
      cache-creation and cache-read tokens priced separately from ordinary input
      (R7, R8).
- [ ] A model missing from the rate table yields accounted tokens and a cost
      explicitly marked unavailable, never another model's rate (R9).
- [ ] Cost history survives closing and reopening musem, and survives deletion
      of the transcript it was derived from (R11).
- [ ] The dashboard shows every known session with name, status, directory,
      branch and cost, ordering sessions waiting on the user first (R13, R14).
- [ ] Stale data is visibly distinguishable from current data, with an
      indication of since when (R15).
- [ ] No operation reachable from the interface alters a session or the
      repository (R16).
- [ ] `make build`, `make test`, `make lint` and `make vet` pass on macOS and
      Linux with `CGO_ENABLED=0`.

## Risks and assumptions

| Type | Detail | Mitigation |
| --- | --- | --- |
| Risk | Foreign formats — the JSONL shape and `claude agents --json` output — change without warning between versions | A single adapter owns all foreign knowledge, plus tests with fixtures captured from real output so breakage shows up in CI rather than in use |
| Risk | Rates go stale with every new model | Explicit versioned rate table and degradation to "unavailable" instead of guessing. Periodic manual maintenance is accepted |
| Risk | Polling costs grow with many sessions | Bounded cadence and no overlapping queries. The exit, if it ever bites, is migrating to the tool's hooks, already anticipated |
| Risk | Character widths (emoji, CJK) break table alignment | Measure by terminal cell width from the start; retrofitting it forces revisiting every view |
| Risk | musem reads transcripts containing source code and prompts | Strictly local processing and no network egress, fixed as R12 and asserted by an architecture test, not by discipline |
| Assumption | The `usage` recorded per response is exact and does not need recomputing | Adapter tests against real captured transcripts |
| Assumption | Read-only is a real constraint, not a phase | R16 forbids any mutating operation; it forces solving observation properly before acting on what is observed |
