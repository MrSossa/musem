# Technical design — add-session-dashboard

## Technical context

A repository with no product code: everything is decided from scratch. See
`proposal.md` for motivation and `spec.md` for the behaviour contract.

Three constraints shape everything else:

- **macOS and Linux.** Native Windows is out of scope by decision, not by
  oversight. That renunciation is what will later allow leaning on tmux.
- **The good data already exists.** Real usage lives in the JSONL transcripts and
  live status in the agent tool's query output. Neither has to be invented nor
  inferred from a screen.
- **They are foreign surfaces.** Third-party formats and commands that can change
  between versions without warning.

**Goals**: lay down the data layer — discovery, status and usage — well enough
that later changes build on top of it instead of redoing it; ensure a change in a
foreign format breaks exactly one point in the code; bootstrap the Go project
with the invariants that are expensive to recover later.

**Non-goals**: terminal emulation, PTY handling or multiplexing; a daemon or
background process (musem lives and dies with its TUI); abstracting over multiple
agent tools.

Files and boundaries this change creates:

```
musem.go                   domain: Session, Status, Usage, Cost
error.go                   application error codes
cmd/musem/main.go          wiring: the only place that knows the whole graph
internal/
├── claude/                adapter: `claude agents --json` + JSONL transcripts
├── sqlite/                adapter: history persistence
├── git/                   adapter: branch resolution by shelling out
├── inmem/                 adapter: fake sessions for development and tests
├── execx/                 helper: bounded subprocesses, a leaf by rule
├── safetext/              helper: strips terminal instructions from foreign text
├── registry/              orchestration: discovery, lifecycle, staleness
├── cost/                  orchestration: rates, computation, aggregation
├── app/                   composer: joins registry + cost into one snapshot
└── tui/                   model, update, view, messages, pump
```

## Decisions

### D1 · Go, single static binary, no cgo

**Chosen**: Go, with `CGO_ENABLED=0` as a project invariant and
`modernc.org/sqlite` (pure Go) for persistence.

Go for three concrete reasons: cross-compilation to macOS and Linux from a
single machine with no runtime to install, a concurrency model that fits "N
sources emitting events against one UI loop", and `os.UserConfigDir()` in the
standard library already resolving both systems' path conventions.

**Rejected alternatives**: Rust with ratatui — a stronger PTY ecosystem, unused
here, and a steeper curve; Node with Ink — requires a runtime on the target;
Python with Textual — painful distribution. `mattn/go-sqlite3`, the default
choice for most people, is ruled out because it drags cgo in.

**Consequences**: the moment cgo enters, single-machine cross-compilation is
lost, so the invariant is pinned in CI and the regression catches itself. The
race detector is the one exception: it requires cgo and runs as its own step,
because the invariant governs the shipped binary, not the test runner.

**Requirements it supports**: R11

### D2 · Package layout, and import direction asserted by a test

**Chosen**: the root package holds the domain and is named after the project;
everything else lives under `internal/`. Five rules hold it up:

1. **The root package imports nothing.** Owned types at the centre with no
   dependencies; everything else depends on them. This is what makes the
   adapters genuinely replaceable rather than nominally so. It also reads
   better at use sites: `musem.Session` over `domain.Session`.
2. **Adapters are named after what they wrap.** `sqlite`, not `store`; `git`,
   not `gitinfo`. A generic name invites unrelated things to accumulate; a
   dependency name answers "does this belong here?" without discussion.
3. **Consumers declare the interfaces.** `registry` defines the `Discoverer` it
   needs and `claude` happens to satisfy it. Go idiom, and it allows testing
   with trivial doubles and no mocking framework. A deliberate departure from
   layouts that gather every service interface in the root package: doing that
   turns it into the file everyone has to edit.
4. **Orchestration gets its own packages.** See D3.
5. **The TUI calls nobody**: it receives messages. See D10.

The dependency graph always points inward (`cmd` → `tui`/`app` → orchestration →
adapters → root) and has no cycles. `main.go` injects by constructor. Ports
declared by their consumers, so far: `Discoverer` and `BranchResolver` in
`registry`; `UsageReader` and `HistoryStore` in `cost`.

**Rejected alternatives**: layouts built around `pkg/`, `api/` and `build/`
directories — not part of the language's conventions and at this scale pure
overhead. Package-level globals, `init()` with side effects, a service locator,
and a dependency container passed around wholesale — a struct carrying every
dependency hides what each consumer actually needs and is a service locator in
disguise.

**Consequences**: nobody can import musem as a library, and the compiler enforces
that without anyone maintaining a stable API. Import direction is asserted by a
test rather than left to discipline.

**Requirements it supports**: all of them, indirectly; it is the structure the
rest of the decisions hang off.

### D3 · `registry` and `cost` as capability packages

**Chosen**: two orchestration packages that mirror the spec capabilities one to
one.

They are **not use cases**. A use case is fine-grained and stateless: one user
intention, input in, output out. `registry` owns the inventory — it runs the
discovery loop, holds the map of live sessions, detects status transitions and
marks sessions that die. It has state and a lifecycle; a use case has neither.
`cost` likewise owns the rate table and the running totals.

They are separate packages because each can exist without importing the other:
different dependencies, different reasons to change, no shared state. `cost`
does not need to know what a live session is, only an identifier; `registry`
does not know what a token is worth. In Go this matters concretely: the package
is the unit of encapsulation, so merging them would grant each access to the
other's internals, and that coupling then happens without anyone deciding it.

**Rejected alternatives**: pushing the orchestration into an adapter. Some
applications legitimately need no such packages — where one dependency dominates,
a CRUD service whose storage effectively is the application, the domain interface
can be implemented directly by the storage adapter, keeping the permission check
in the same transaction as the read and the write and closing a TOCTOU window a
layer above would open. musem has no dominant dependency: `claude` is the primary
source, `sqlite` only holds history, `git` answers one question, and the
interesting logic spans all of them. Pushing that into any single adapter would
force the claude adapter to call sqlite and git from inside.

**Consequences**: orchestration that belongs to no adapter has a home, at the
cost of two packages that a smaller design would not need.

**Requirements it supports**: R1, R5, R6, R10

### D4 · Rich domain types and application error codes

**Chosen**: the root package holds behaviour, not just data — validation as
methods on the types and rules as pure functions beside them. Adapters call them;
they do not reimplement them. `error.go` defines transport-independent error
codes (`ENOTFOUND`, `EUNAVAILABLE`, `ESTALE`, `EUNKNOWNMODEL`) and `tui` decides
how each is rendered.

Types must make illegal states unrepresentable: `Status` is not an arbitrary
string, and `Cost` must be able to hold "unknown" rather than silently reading
as zero.

**Rejected alternatives**: passing untyped maps around, and letting adapters
carry presentation-shaped errors.

**Consequences**: the zero-versus-unknown distinction the specs depend on holds
in the type system rather than by convention. An unavailable discovery source,
stale data, and a model with no known rate can all travel from an adapter to the
screen without the adapter knowing anything about presentation.

**Requirements it supports**: R3, R6, R7, R9, R15

### D5 · Composition lives in `app`, not in `tui`

**Chosen**: a thin composer in `app` that exposes a single snapshot with session
and cost already joined. `tui` renders and does not think.

**Rejected alternatives**: `tui` calling `registry` and `cost` and joining the
results — that puts composition logic in a presentation adapter, where any second
front end would have to duplicate it.

**Consequences**: `app` stays deliberately thin; the moment it grows rules of its
own, those rules belong to `registry` or `cost`. The dividing line for any later
decision: **the core decides what is true, `tui` decides how it looks.** Which
statuses exist and which is more urgent is a domain fact; ordering waiting
sessions first is presentation policy. That a value is 40 seconds old is a
registry fact; painting it amber is presentation. A useful test when in doubt: if
a CLI front end would have to copy it, it does not belong in `tui`.

**Requirements it supports**: R13, R14

### D6 · Rejected: DDD and explicit hexagonal scaffolding

**Chosen**: none of it. The layout in D2 already *is* ports and adapters — the
root package is the core, `registry` declares the port, `claude` implements the
adapter — with the ceremony omitted.

**Rejected alternatives**: `ports/`, `adapters/`, `application/`,
`infrastructure/` directories. In Go, consumer-defined interfaces provide the
hexagon almost for free; promoting it to directory names adds indirection without
adding isolation, and makes every change touch more places. The ceremony also
fails quietly in a specific way worth naming: a codebase can carry every one of
those directories and still have its persistence library imported by the core,
with storage-flavoured queries and stringly-typed field names sitting inside the
use cases. The directory names promise an isolation the import graph does not
deliver.

DDD is rejected outright. It pays off when the domain is complex and contested:
domain experts to align with, rules nobody can quite state, invariants worth
protecting. musem's domain is a list of sessions with a status and a token count.
The hard parts here are all technical — robustly parsing someone else's JSONL,
not lying about staleness, terminal rendering, concurrency — and DDD has nothing
to say about any of them. One idea is worth taking from it, and it has its own
entry: rich domain types (D4).

**Consequences**: fewer files for the same behaviour, and a boundary enforced by
the direction of the imports rather than by directory names.

**Requirements it supports**: none directly; it is a constraint on how the rest
is built.

### D7 · Three observation lanes, with explicit priority

**Chosen**: an agent can be observed through structured channels (transcripts,
query output, hooks), through process signals, or by scraping the rendered pane,
and the project rule is that order — **never derive from rendered text something
that exists as structured data.**

**Rejected alternatives**: scraping the pane as a general strategy. Pane text is
a presentation layer and rots with every foreign UI change; structured data is a
contract.

**Consequences**: where only an ambiguous signal exists, status is exposed as
indeterminate instead of risking a verdict. An honest "I don't know" is cheaper
than a false "idle", because the latter makes the user ignore a session that is
waiting for them.

**Requirements it supports**: R1, R3, R7

### D8 · One adapter for everything foreign

**Chosen**: all knowledge about external formats — the shape of the JSONL, the
query command's output, the names of the usage fields — lives behind an adapter.
The rest of the code works with musem's own types.

**Rejected alternatives**: parsing foreign shapes at the point of use.

**Consequences**: this is the concrete mitigation for the coupling risk in
`proposal.md`. When a foreign format changes, and it will, there is one place to
touch and one place to test.

**Requirements it supports**: R1, R6, R7

### D9 · Polling now, events later

**Chosen**: periodic polling of the discovery source and incremental re-reading
of the transcripts. Boring, portable and sufficient for one person's session
count.

**Rejected alternatives**: the agent tool's hooks, which allow pushing events
instead of polling and are the natural path for notifications. Left out
deliberately: they require installing configuration on the user's machine, and
that is a different conversation to have for a read-only dashboard.

**Consequences**: bounded cadence with no overlapping queries. Migrating to hooks
later is anticipated and additive.

**Requirements it supports**: R1, R15

### D10 · One goroutine per source, one owner of state

**Chosen**: each data source runs in its own goroutine and pushes typed messages
toward the TUI loop through a single pump. **Nobody mutates the model outside its
update function.**

**Rejected alternatives**: the TUI reaching out and fetching; shared mutable
state across goroutines.

**Consequences**: it is the pattern Bubble Tea imposes and the only concurrency
rule needed. Respected, shared state disappears as a category of problem;
violated, races show up as render flicker and are very expensive to diagnose. The
pump is the only bridge between goroutines and the UI loop, and therefore the
only place a data race can appear.

**Requirements it supports**: R15

### D11 · Local SQLite for history

**Chosen**: usage is persisted to SQLite in the user's config directory, resolved
via `os.UserConfigDir()`.

**Rejected alternatives**: recomputing everything from the transcripts on each
start. Simpler, but it fails the requirement and makes startup proportional to
accumulated history.

**Consequences**: the specs require history to survive musem closing and the
source transcript disappearing, and transcripts are not a store — they belong to
someone else and can be deleted.

**Requirements it supports**: R11

### D12 · Git by shelling out

**Chosen**: resolve the branch by invoking the `git` binary.

**Rejected alternatives**: `go-git` — a large dependency to answer a small
question.

**Consequences**: git is installed by definition on machines where musem makes
sense. Non-repository directories must be tolerated rather than treated as
errors.

**Requirements it supports**: R4

### D13 · Explicit rates, and "unavailable" over guessing

**Chosen**: the per-model rate table is explicit, versioned data, not constants
scattered around. When an unknown model appears — and it will — tokens are
reported and cost is marked unavailable.

**Rejected alternatives**: applying another model's rate as an approximation.
That produces plausible wrong numbers, which is the worst possible outcome:
nobody audits them because nothing looks broken.

**Consequences**: the table needs periodic manual maintenance, accepted as a
trade.

**Requirements it supports**: R9, R10

### D14 · A degraded read is a value, not an absence

**Chosen**: every reader that can partially fail returns a count of what it could
not read, alongside what it could. `musem.UsageReading` does it for transcripts
and `musem.Discovery` does it for the session list; both counts travel to the
view and are rendered as their own state, distinct from an error and distinct
from staleness.

**Rejected alternatives**: skipping unreadable records silently, which is what
the discovery path did until the phase-3 review. It reads as defensive — one bad
record does not cost you the others — and hides the case where every record is
bad: an empty list is indistinguishable from a machine running nothing, and the
registry answers that by marking every known session ended.

**Consequences**: three states have to be told apart wherever data arrives —
failed, stale, and incomplete — and each needs its own channel to the view. That
is the cost. What it buys is that no foreign format change can empty the
dashboard quietly.

**Requirements it supports**: R6, R20

### D15 · Foreign text is stripped where it becomes a musem value

**Chosen**: control characters are removed from foreign text at the point the
payload becomes a `musem` type — session names, directories and model
identifiers in the `claude` adapter, branch names in `git`. The mechanism is
`internal/safetext`, a leaf on the same terms as `execx` (D16), because the
moment a second adapter needed it, a copy in each was a security predicate
maintained twice.

**Rejected alternatives**: escaping in the renderer. The dashboard is not the
only consumer a value can reach, and a defence in the view has to be repeated by
every future view; one at the boundary holds for all of them. Also rejected:
replacing stripped characters with a substitution glyph, which would let a
crafted name masquerade as a legitimately odd one.

Also rejected: stripping identifiers. Cleaning suits text that exists to be read
and ruins a value that exists to be matched — a stripped session id designates a
session nobody has and a transcript file that cannot be opened, and two ids
differing only in control characters collapse into one. Identifiers are therefore
refused and counted among the records a pass could not use.

**Consequences**: a name is no longer byte-identical to what the source reported.
That is the trade, and it is the right way round — the dashboard redraws every
refresh, so a name carrying an escape sequence is an instruction re-issued to the
terminal continuously, not a one-off glitch.

The filter is `unicode.IsPrint`, and deliberately wider than "control bytes":
git refuses a ref name containing an ASCII control character but accepts one
containing U+202E, so a direction override that reorders the branch column is a
name a repository can genuinely carry. Two earlier review rounds waved the branch
through on the assumption that git's own validation covered it.

**Requirements it supports**: R19

### D16 · Shared subprocess handling in a leaf package

**Chosen**: `internal/execx` runs a command under a timeout and classifies the
outcome into the four shapes callers act on differently — not found, timed out,
exited non-zero, failed. Both shelling adapters use it.

**Rejected alternatives**: leaving each adapter with its own copy. The subtle
part — a forked child holding the pipe open past a process that exited zero — was
already implemented twice and had already drifted.

**Consequences**: `execx` is not an adapter, and the distinction matters enough
to assert: it wraps the standard library rather than a foreign system, so it
imports nothing from musem and stays a leaf. A shared package that starts
importing the domain becomes a second composition root every adapter depends on.
The success predicate stays with the caller, because the evidence genuinely
differs between them.

**Requirements it supports**: none directly; it serves R1 and R4 by keeping their
adapters honest about what "no answer" means.

## Impact

| Area | Impact |
| --- | --- |
| Data schema | New SQLite schema for usage history under `os.UserConfigDir()`, created on first run, with a migration path from the start |
| Public API | None. The binary is the product; `internal/` makes importing musem as a library impossible by construction |
| Security | musem reads transcripts containing source code and prompts. Processing is strictly local and no network egress is introduced (R12), asserted by an architecture test |
| Performance | Bounded polling cadence with no overlapping queries; transcripts are read incrementally rather than reparsed; widths measured by terminal cell so rendering stays correct with emoji and CJK |
| Dependencies | Bubble Tea, Lipgloss, Bubbles for the TUI; `modernc.org/sqlite` for persistence. `CGO_ENABLED=0` becomes a project invariant, not a preference |
| CI | The workflow stops being a placeholder: macOS + Linux matrix running build, test and lint, with `CGO_ENABLED=0` pinned and a check that fails if the binary ends up dynamically linked |
| Repository hygiene | `.gitignore` currently targets Node and Python; it is adjusted for Go |

## Test plan

- **Architecture tests** (`internal/archtest`): the root package imports nothing
  outside the standard library, dependencies point inward, adapters wrap exactly
  one foreign thing each, the UI fetches nothing, and nothing reaches the
  network. Covers D2, D6 and the scenario "No network egress" of R12.
- **Adapter tests against captured fixtures**: real output of the live session
  query and representative JSONL transcripts, so a foreign format change fails in
  CI rather than in use. Covers R1, R6 ("Unknown format"), R7, R8.
- **Registry unit tests** with trivial doubles for `Discoverer` and
  `BranchResolver`: rename, two sessions sharing a directory, ambiguous signal,
  disappearance, missing agent tool. Covers R1–R6.
- **Cost unit tests**: cache breakdown, unknown model, partial aggregate, and the
  zero-versus-unknown distinction across the whole data path. Covers R7–R10.
- **Persistence tests** against a real SQLite file: restart keeps history, and
  deleting the source transcript does not recompute accounted usage to zero.
  Covers R11.
- **TUI tests**: default ordering with a session transitioning to waiting, stale
  marking, empty state, narrow-terminal column dropping and live resize. Covers
  R13–R15, R18.
- **Manual end-to-end**: real parallel sessions checked for status, cost and
  freshness; keyboard navigation, detail, help and clean terminal restore; and a
  sweep confirming no operation reachable from the interface mutates anything.
  Covers R16, R17.

## Open questions

- Exact polling cadence. Start with a conservative value and tune it with real
  use; it changes neither specs nor task breakdown.
- Whether the rate table is embedded in the binary or left user-configurable.
  Start embedded; making it configurable later is additive.
- Support for other agent tools. Out of scope; the adapter seam leaves it open
  without committing today.
