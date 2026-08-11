## Context

A repository with no product code: everything is decided from scratch. See
`proposal.md` — Why for motivation, and the `session-registry`, `session-cost`
and `session-dashboard` specs for the behaviour contract.

Three constraints shape everything else:

- **macOS and Linux.** Native Windows is out of scope by decision, not by
  oversight. That renunciation is what will later allow leaning on tmux.
- **The good data already exists.** Real usage lives in the JSONL transcripts and
  live status in the agent tool's query output. Neither has to be invented nor
  inferred from a screen.
- **They are foreign surfaces.** Third-party formats and commands that can change
  between versions without warning.

## Goals / Non-Goals

**Goals:**

- Lay down the data layer — discovery, status and usage — well enough that
  later changes build on top of it instead of redoing it.
- Ensure a change in a foreign format breaks exactly one point in the code.
- Bootstrap the Go project with the invariants that are expensive to recover
  later.

**Non-Goals:**

- Terminal emulation, PTY handling or multiplexing. None of it enters here.
- A daemon or background process: musem lives and dies with its TUI.
- Abstracting over multiple agent tools. Designed for one, with the seam in
  place, but without paying up front for the generalisation.

## Decisions

### Go, single binary, no cgo

Go for three concrete reasons: cross-compilation to macOS and Linux from a
single machine with no runtime to install, a concurrency model that fits "N
sources emitting events against one UI loop", and `os.UserConfigDir()` in the
standard library already resolving both systems' path conventions.

`CGO_ENABLED=0` is a **project invariant**, not a preference: the moment cgo
enters, single-machine cross-compilation is lost. `modernc.org/sqlite` (pure Go)
follows from that, and `mattn/go-sqlite3` — the default choice for most people —
is ruled out because it drags cgo in. Pinned in CI so the regression catches
itself.

*Alternatives:* Rust with ratatui — a stronger PTY ecosystem, unused here, and a
steeper curve; Node with Ink — requires a runtime on the target; Python with
Textual — painful distribution.

### Package layout

The root package holds the domain and is named after the project. Everything
else lives under `internal/`, because nobody is going to import musem as a
library and the compiler enforces that without anyone maintaining a stable API.
Layouts built around `pkg/`, `api/` and `build/` directories are rejected: they
are not part of the language's conventions and at this scale they are pure
overhead.

```
musem.go                   domain: Session, Status, Usage, Cost
error.go                   application error codes
cmd/musem/main.go          wiring: the only place that knows the whole graph
internal/
├── claude/                adapter: `claude agents --json` + JSONL transcripts
├── sqlite/                adapter: history persistence
├── git/                   adapter: branch resolution by shelling out
├── inmem/                 adapter: fake sessions for development and tests
├── registry/              orchestration: discovery, lifecycle, staleness
├── cost/                  orchestration: rates, computation, aggregation
├── app/                   composer: joins registry + cost into one snapshot
└── tui/                   model, update, view, messages, pump
```

Five rules hold the design up:

1. **The root package imports nothing.** Owned types at the centre with no
   dependencies; everything else depends on them. This is what makes the
   adapters genuinely replaceable rather than nominally so. It also reads
   better at use sites: `musem.Session` over `domain.Session`.
2. **Adapters are named after what they wrap.** `sqlite`, not `store`; `git`,
   not `gitinfo`. A generic name invites unrelated things to accumulate; a
   dependency name answers "does this belong here?" without discussion.
3. **Consumers declare the interfaces.** `registry` defines the `Discoverer` it
   needs and `claude` happens to satisfy it. Go idiom, and it allows testing
   with trivial doubles and no mocking framework. Note the deliberate departure
   from layouts that gather every service interface in the root package: doing
   that turns it into the file everyone has to edit.
4. **Orchestration gets its own packages.** See the next decision for why
   `registry` and `cost` exist at all.
5. **The TUI calls nobody**: it receives messages. Each source runs in its own
   goroutine and writes to a channel; a single pump translates those events into
   UI messages. That pump is the only bridge between goroutines and the UI loop,
   and therefore the only place a data race can appear.

The dependency graph always points inward (`cmd` → `tui`/`app` → orchestration →
adapters → root) and has no cycles. `main.go` injects by constructor: no
package-level globals, no `init()` with side effects, no service locator, and no
dependency container passed around wholesale — a struct carrying every
dependency hides what each consumer actually needs and is a service locator in
disguise.

Ports declared by their consumers, so far: `Discoverer` and `BranchResolver` in
`registry`; `UsageReader` and `HistoryStore` in `cost`.

### Why `registry` and `cost` are packages, and what they are not

They are **not use cases**. A use case is fine-grained and stateless: one user
intention, input in, output out. `registry` owns the inventory — it runs the
discovery loop, holds the map of live sessions, detects status transitions and
marks sessions that die. It has state and a lifecycle; a use case has neither.
`cost` likewise owns the rate table and the running totals. They are capability
packages, and they mirror the spec capabilities one to one.

They are separate packages because each can exist without importing the other:
different dependencies, different reasons to change, no shared state. `cost`
does not need to know what a live session is, only an identifier; `registry`
does not know what a token is worth. Had the answer been "one needs the other",
they would be one package. In Go this matters concretely: the package is the
unit of encapsulation, so merging them would grant each access to the other's
internals, and that coupling then happens without anyone deciding it.

Some applications legitimately need no such packages. Where one dependency
dominates — a CRUD service whose storage effectively is the application — the
domain interface can be implemented directly by the storage adapter, with
authorisation and validation enforced inside it. That is defensible there:
keeping the permission check in the same transaction as the read and the write
closes a TOCTOU window that a layer above would open, and it avoids an anaemic
layer that only forwards calls.

musem has no dominant dependency. `claude` is the primary source, `sqlite` only
holds history, `git` answers one question, and the interesting logic — the
polling loop, staleness tracking, cost aggregation across transcripts and
history — spans all of them. Pushing that into any single adapter would force,
say, the claude adapter to call sqlite and git from inside. Orchestration that
belongs to no adapter needs a home of its own.

### Rich domain types and application error codes

The root package holds behaviour, not just data: validation as methods on the
types and rules as pure functions beside them. Adapters call them; they do not
reimplement them.

`error.go` defines transport-independent error codes — `ENOTFOUND`,
`EUNAVAILABLE`, `ESTALE`, `EUNKNOWNMODEL` — and `tui` decides how each is
rendered. This is the concrete mechanism behind several spec requirements: an
unavailable discovery source, stale data, and a model with no known rate all
need to travel from an adapter to the screen without the adapter knowing
anything about presentation.

Types must make illegal states unrepresentable: `Status` is not an arbitrary
string, and `Cost` must be able to hold "unknown" rather than silently reading
as zero. The specs' zero-versus-unknown distinction depends on this being true
in the type system rather than by convention.

### Composing sessions and cost

The dashboard needs a session and its cost in the same row, so something has to
join them. That composition lives in `app`, not in `tui`: a thin composer that
exposes a single snapshot with the rows already assembled. `tui` renders and
does not think.

The alternative — `tui` calling `registry` and `cost` and joining the results —
puts composition logic in a presentation adapter, where it would have to be
duplicated by any second front end. `app` stays deliberately thin: the moment it
grows rules of its own, those rules belong to `registry` or `cost`.

The dividing line, for any decision that comes up later: **the core decides what
is true, `tui` decides how it looks.** Which statuses exist and which is more
urgent is a domain fact; ordering waiting sessions first is presentation policy.
That a value is 40 seconds old is a registry fact; painting it amber is
presentation. A useful test when in doubt: if a CLI front end would have to
copy it, it does not belong in `tui`.

### Considered and rejected: DDD and explicit hexagonal scaffolding

The layout above already *is* ports and adapters: the root package is the core,
`registry` declares the port, `claude` implements the adapter. What it omits is
the ceremony — `ports/`, `adapters/`, `application/`, `infrastructure/`
directories. In Go, consumer-defined interfaces provide the hexagon almost for
free; promoting it to directory names adds indirection without adding isolation,
and makes every change touch more places.

The ceremony also fails quietly in a specific way worth naming: a codebase can
carry every one of those directories and still have its persistence library
imported by the core, with storage-flavoured queries and stringly-typed field
names sitting inside the use cases. The directory names promise an isolation the
import graph does not deliver. Naming the layers is not what enforces the
boundary — the direction of the imports is, which is why that direction is
asserted by a test here rather than left to discipline.

The same reasoning rules out passing a shared dependency container to every
consumer. It looks like injection and behaves like a service locator: the
constructor signature stops saying what the code actually needs, and testing one
piece requires assembling all of them.

DDD is rejected outright. It pays off when the domain is complex and contested:
domain experts to align with, rules nobody can quite state, invariants worth
protecting. musem's domain is a list of sessions with a status and a token
count. The hard parts here are all technical — robustly parsing someone else's
JSONL, not lying about staleness, terminal rendering, concurrency — and DDD has
nothing to say about any of them. Applying its tactical patterns without the
strategic problem that justifies them buys aggregates and repositories at
several times the file count for the same behaviour.

One idea is worth taking from it, and it has its own decision above: rich domain
types instead of passing untyped maps around.

### Three observation lanes, with explicit priority

An agent can be observed through structured channels (transcripts, query output,
hooks), through process signals, or by scraping the rendered pane. The project
rule: **never derive from rendered text something that exists as structured
data.** Pane text is a presentation layer and rots with every foreign UI change;
structured data is a contract.

Direct consequence in the specs: where only an ambiguous signal exists, status is
exposed as indeterminate instead of risking a verdict. An honest "I don't know"
is cheaper than a false "idle", because the latter makes the user ignore a
session that is waiting for them.

### Polling now, events later

For this scope: periodic polling of the discovery source and incremental
re-reading of the transcripts. Boring, portable and sufficient for one person's
session count.

The agent tool's hooks allow pushing events instead of polling, and they are the
natural path for notifications. They are deliberately left out: they require
installing configuration on the user's machine, and that is a different
conversation to have for a read-only dashboard.

### One adapter for everything foreign

All knowledge about external formats — the shape of the JSONL, the query
command's output, the names of the usage fields — lives behind an adapter. The
rest of the code works with musem's own types.

This is the concrete mitigation for the coupling risk: when a foreign format
changes, and it will, there is one place to touch and one place to test.

### One goroutine per source, one owner of state

Each data source runs in its own goroutine and pushes typed messages toward the
TUI loop. **Nobody mutates the model outside its update function.** It is the
pattern Bubble Tea imposes and the only concurrency rule needed: respected,
shared state disappears as a category of problem; violated, races show up as
render flicker and are very expensive to diagnose.

### Local SQLite for history

Usage is persisted to SQLite in the user's config directory. Reason: the specs
require history to survive musem closing and the source transcript disappearing,
and transcripts are not a store — they belong to someone else and can be
deleted.

*Rejected alternative:* recomputing everything from the transcripts on each
start. Simpler, but it fails the requirement and makes startup proportional to
accumulated history.

### Git by shelling out

Resolve the branch by invoking the `git` binary. It is installed by definition on
machines where this makes sense, and it avoids a large dependency to answer a
small question. `go-git` is ruled out.

### Explicit rates, and "unavailable" over guessing

The per-model rate table is explicit, versioned data, not constants scattered
around. When an unknown model appears — and it will — tokens are reported and
cost is marked unavailable.

Guessing by applying another model's rate produces plausible wrong numbers,
which is the worst possible outcome: nobody audits them because nothing looks
broken.

## Risks / Trade-offs

- **Foreign formats change without warning** → single adapter, plus tests with
  fixtures captured from real output so breakage shows up in CI rather than in
  use.
- **Rates go stale with every new model** → explicit table and degradation to
  "unavailable". Periodic manual maintenance is accepted.
- **Polling has a cost** with many sessions → bounded cadence and no overlapping
  queries. If it ever becomes a problem, the exit is migrating to hooks, already
  anticipated.
- **Character widths** (emoji, CJK) break table alignment → measure by terminal
  cell width from the start. Retrofitting it forces revisiting every view.
- **Privacy**: musem reads transcripts containing source code and prompts. The
  spec fixes strictly local processing and no network egress. It is a guarantee
  that has to be actively defended in every future change.
- **Read-only is a real constraint**, not a phase: it forces solving observation
  properly before the temptation to act on what is observed.

## Migration Plan

New project: no migration and no rollback strategy. Two adjustments to the
existing repository: the CI matrix goes from an `echo` to building and testing on
macOS and Linux with `CGO_ENABLED=0` pinned, and the `.gitignore` — currently
oriented to Node and Python — is adjusted for Go.

## Open Questions

- Exact polling cadence. Start with a conservative value and tune it with real
  use; it changes neither specs nor task breakdown.
- Whether the rate table is embedded in the binary or left user-configurable.
  Start embedded; making it configurable later is additive.
- Support for other agent tools. Out of scope; the adapter seam leaves it open
  without committing today.
