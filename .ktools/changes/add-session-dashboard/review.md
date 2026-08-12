# Review — add-session-dashboard

**Date**: 2026-08-12
**Mode**: conformance (change `add-session-dashboard`)
**Diff reviewed**: `main..HEAD` — 42 files, +10569/-28
**Lenses**: architecture, security, simplicity, correctness, tests, conformance
**Verdict**: APPROVE (was WARN; every finding below has been closed and re-verified)

## Summary

The change delivers the read-only observatory it proposed, and delivers it well:
the import graph matches the agreed design without drift, the concurrency story
is genuinely single-writer per piece of shared state, and the suite covers almost
every scenario in the spec by name.

The original review found sixteen requirements passing and two partial, sharing
one root: the degradation machinery the spec asks for was built thoroughly on the
transcript path and not at all on the discovery path. Its sharpest consequence
was that a change in the agent tool's record shape silently converted every live
session into a dead one, with no marker — the exact failure mode the design says
it exists to refuse.

All eight findings have since been fixed, and the two partial requirements now
pass. Two of them were decisions about the spec rather than the code and were
settled by amending it: R3 gained the ended-versus-dead distinction and the age
of a status, R16 was narrowed to exclude musem's own store. Two behaviours the
code now has and the spec did not describe were added as R19 and R20 rather than
left undocumented. Full validation re-run green, and each new architecture rule
was made to fail by injecting the violation it forbids before being restored.

## Conformance

| Req | Requirement | Status | Evidence |
| --- | --- | --- | --- |
| R1 | Live session discovery | PASS | `internal/registry/registry.go:184` (sequential `Run`), `:206` + `internal/claude/agents.go:52` · tests `registry_test.go:36`, `:295` |
| R2 | Stable session identity | PASS | `musem.go:70`, `internal/registry/registry.go:239` · tests `registry_test.go:84`, `:102`, `claude_test.go:62` |
| R3 | Observed session status | PASS | `musem.go:22-53` (`StatusEnded`), `musem.go:88` (`StatusSince`), `internal/registry/registry.go:246`, `:275` · tests `registry_test.go:147`, `:170`, `tui_test.go:951` |
| R4 | Session git branch | PASS | `internal/git/git.go:39`, `internal/registry/registry.go:283` · tests `git_test.go:31`, `:74`, `registry_test.go:68` |
| R5 | Session end of life | PASS | `internal/registry/registry.go:254-267`, `musem.go:91-97` · tests `registry_test.go:117`, `:522`, `app_test.go:117` |
| R6 | Degradation when a source is unavailable | PASS | `internal/registry/registry.go:206-213`, `:376-386`, `internal/claude/agents.go:166-186` · tests `registry_test.go:166`, `:187`, `:227`, `claude_test.go:85`, `:651` |
| R7 | Usage derived from structured data | PASS | `internal/claude/transcript.go:268`, `musem.go:120-127` · tests `claude_test.go:116`, `cost_test.go:174`, `musem_test.go:123` |
| R8 | Cache breakdown | PASS | `musem.go:120-135`, `internal/cost/rates.go` · tests `cost_test.go:89`, `:104` |
| R9 | Model with no known rate | PASS | `internal/cost/cost.go:139-168`, `musem.go:275-290` · test `cost_test.go:145` |
| R10 | Usage aggregation | PASS | `internal/cost/cost.go:181-239`, `:251-277` · tests `cost_test.go:309`, `:339`, `app_test.go:443` |
| R11 | History persistence | PASS | `internal/sqlite/sqlite.go:46`, `:102` · tests `sqlite_test.go:72`, `:152`, `cost_test.go:224`, `app_test.go:215` |
| R12 | Local processing | PASS | no networking imports anywhere · tests `arch_test.go:207`, `:220` |
| R13 | Fleet overview | PASS | `internal/app/app.go:133`, `internal/tui/tui.go:390` · tests `tui_test.go:61`, `:49` |
| R14 | Actionable sessions come first | PASS | `musem.go:52`, `internal/registry/registry.go:354-372` · tests `registry_test.go:264`, `app_test.go:69`, `musem_test.go:35` |
| R15 | Automatic refresh and data freshness | PASS | `internal/registry/registry.go:380-386`, `internal/tui/pump.go:27`, `tui.go:155` · tests `tui_test.go:74`, `:86`, `:121`, `registry_test.go:206` |
| R16 | Read only | PASS | `internal/tui/tui.go:64` · test `tui_test.go:301`; requirement wording narrowed to exclude musem's own store |
| R17 | Keyboard navigation | PASS | `internal/tui/tui.go:64`, `:635`, `cmd/musem/main.go:102` · tests `tui_test.go:160`, `:207`, `:222`, `:367` |
| R18 | Legibility in narrow terminals | PASS | `internal/tui/tui.go:203`, `:268-305` · tests `tui_test.go:235`, `:261`, `:280`, `:774`, `:787` |
| R19 | Foreign text carries no terminal instructions | PASS | `internal/claude/sanitise.go`, `internal/claude/agents.go:178-179`, `internal/claude/transcript.go:350` · tests `sanitise_test.go:15`, `:41`, `:52` |
| R20 | An unreadable record is reported | PASS | `musem.go:99-115`, `internal/claude/agents.go:170`, `internal/registry/registry.go:229`, `internal/app/app.go:167`, `internal/tui/tui.go:449` · tests `claude_test.go:661`, `registry_test.go:216`, `app_test.go:512` |

**Acceptance criteria**: 9/9 met. The original review noted that no acceptance
criterion reached the "since when" and "logged once" sub-clauses; both are now
covered by requirements of their own (R3's fourth scenario and R20).

**Tasks checked off without evidence**: none outstanding. The original review
found one — task 3.4's "and logging once", which nothing served on the discovery
path. musem writes no logs during a run by design, because it owns the alternate
screen; the intent is served instead by counting what could not be read and
saying so on screen. That mechanism existed on the transcript path
(`internal/cost/cost.go:197-201`) and now exists on the discovery path too, which
is what R20 states and task 8.1 delivers.

**Out of scope**: commit `a5154f9` (the `.ktools/` artifacts and `CLAUDE.md`)
answers no requirement. It is the planning material for this change and its
migration from OpenSpec, so it is expected here, not a concern.

**Design drift**: none. Every boundary D2, D3, D4, D5, D8 and D10 promise holds
in the import graph as built.

## Findings

### [HIGH] An unreadable discovery record makes a live session read as dead

**Lens**: correctness
**Where**: `internal/claude/agents.go:166-175`, with the consequence at `internal/registry/registry.go:224` and `:254-267`

**Problem**: `parseAgents` decodes the payload one record at a time and skips any
record it cannot read with a bare `continue` (`:169` for a decode failure, `:174`
for a missing session id). No counter, no error, no signal upstream. The function
returns `(sessions, nil)` even when every record was skipped, as long as the
outer array itself parsed.

Concrete trigger, and it is the one the suite already contemplates: the agent
tool changes `startedAt` from a number to a string, exactly the shape
`claude_test.go:616` encodes as a partial failure. If that field change applies
to every record rather than one, `Discover` returns an empty slice and a nil
error. `Refresh` then clears `lastErr` at `registry.go:224`, finds nothing in
`seen`, and the loop at `:256-267` marks every known session `EndedAt` and
`StatusDead`. Because the refresh *succeeded*, `updatedAt` advances and the
snapshot is not stale — so the dashboard shows a wall of dead sessions, with full
confidence and no marker, while every one of them is running.

Existing defenses do not catch it: the entire degradation path in `Refresh` is
keyed on `Discover` returning an error, and a per-record decode failure is
deliberately not one. `Session.Validate()` never runs, because no session was
constructed to validate. This is the precise outcome D7 says the project refuses
— "an honest I-don't-know is cheaper than a false idle" — reached through the one
door that was left unguarded.

**Status**: FIXED — see task 8.1.

**Fix applied**: counted skipped records in `parseAgents` and carry the count out alongside
the sessions, mirroring the `Skipped`/`Degraded` pair the transcript path already
has at `internal/cost/cost.go:197-201`. Surface it in `registry.Snapshot` so the
TUI can mark the inventory degraded. A discovery pass that dropped every record
should not be allowed to look like a discovery pass that found nothing.

**Requirements affected**: R6 (PARTIAL), with knock-on effects on R3 and R5

### [MEDIUM] Foreign strings reach the terminal without control characters stripped

**Lens**: security
**Where**: `internal/tui/tui.go:523-553` (`cell`), `:574-624` (`renderDetail`)

**Problem**: `Session.Name` and `Session.Dir` are copied verbatim from the CLI
payload at `internal/claude/agents.go:178-179`, and unpriced model names come
from a transcript's `model` field. None are checked for ASCII control bytes or
ANSI/OSC escape sequences before being written into the view; `pad`, `clip` and
lipgloss measure and truncate cell width but strip nothing. A directory or
session name carrying `\x1b]0;…\x07` or cursor-repositioning codes is re-rendered
on every refresh, letting foreign data spoof or corrupt the display — and on
terminals that honour OSC 52, write attacker-chosen bytes to the clipboard.

Scope is narrower than it first looks: git rejects control characters in ref
names, so `Branch` is not a practical vector. Directory names and model strings
are, and a developer pointing an agent at an untrusted repository is exactly the
situation musem is built for.

**Status**: FIXED — see task 8.2.

**Fix applied**: strip control characters and escape sequences from `Name`, `Dir`
and model strings as they cross into `musem.Session` / `musem.ModelUsage` in the
`claude` adapter, consistent with D8 keeping foreign handling in one place.

**Requirements affected**: R13, R14, R15 (integrity of what the dashboard shows)

### [MEDIUM] R3's status vocabulary diverges from the spec in two places

**Lens**: conformance
**Where**: `musem.go:84-93`, `internal/registry/registry.go:265`, `internal/tui/tui.go:581-590`

**Problem**: two clauses of R3 are not met. First, the scenario "Ambiguous
signal" requires indeterminate to be exposed "along with since when"; no field
records when a status was entered. `LastSeen` cannot serve — it is restamped
every refresh (`registry.go:236`), so for a session indeterminate for ten minutes
it reads as two seconds old. The detail pane at `tui.go:581-590` shows `Status`
and `Last seen` and nothing that answers the question. Second, R3 defines dead as
"it terminated abnormally", but `registry.go:265` assigns `StatusDead` to any
session that merely stopped appearing, so a session the user closed cleanly
displays as dead. The sort at `registry.go:354-372` compensates for the ordering
consequence and its comment acknowledges the conflation, but the status column
still shows the wrong word.

**Status**: FIXED — see tasks 8.3 and 8.4; both clauses were settled in the spec as well as the code.

**Fix applied**: added a status-entered timestamp and a distinct ended-vs-dead
representation, or amend R3 in the spec to match the behaviour actually wanted
and re-derive from there. The spec outranks the code, so this is a decision to
make explicitly rather than let stand.

**Requirements affected**: R3 (PARTIAL)

### [MEDIUM] `internal/archtest` asserts less than the design claims it does

**Lens**: tests
**Where**: `internal/archtest/arch_test.go:24-28`, `:104-115`

**Problem**: the design's test plan claims "dependencies point inward" and "the
UI fetches nothing" are asserted. `TestTUIDoesNotImportAdapters` checks `tui`
only against the `adapters` list, never against `orchestration`, so a future
`tui` importing `registry` or `cost` directly — the shortcut around `app` that
D5's rejected-alternatives paragraph specifically warns about — passes CI. And
`app` appears in no check at all, despite sitting on the inward-pointing chain;
`app` importing `internal/sqlite` would also pass. Neither is violated today
(`tui` imports only `app`; `app` imports only `musem`, `cost`, `registry`), so
this is a guard that does not yet guard, not a present breach.

**Status**: FIXED — see task 8.5.

**Fix applied**: extended the tui rule to the `orchestration` list and add
the equivalent check for `app`.

**Requirements affected**: none directly

### [MEDIUM] The registry's push channel has no consumer but cannot be removed

**Lens**: simplicity
**Where**: `internal/registry/registry.go:184-200`, `cmd/musem/main.go:105-107`, `:161-173`

**Problem**: `Run` publishes a snapshot to `out` after every refresh, and the
only consumer is `drain`, whose entire job is to read and discard. The real data
path is a pull — `tui.Pump` calls `Composer.Snapshot`, which calls
`registry.Snapshot`. The channel is not merely unused: because `Run` blocks on
the send at `:189`, deleting `drain` would stall the refresh loop once the
one-slot buffer filled, so the dead abstraction is load-bearing scaffolding for
itself. `drain`'s comment justifies it by a second consumer that does not exist
and that the spec does not imply.

**Status**: FIXED — see task 8.7.

**Fix applied**: reduced `Run` to `Run(ctx)` looping on `Refresh`, then delete the
`snapshots` channel, its goroutine and `drain`.

**Requirements affected**: none

### [MEDIUM] Subprocess timeout handling duplicated across two adapters

**Lens**: simplicity
**Where**: `internal/claude/agents.go:52-127`, `internal/git/git.go:39-131`

**Problem**: both adapters repeat the same shape — bounded context, `WaitDelay`,
`exec.Error` not-found handling, `ctx.Err()` timeout handling, and a helper of
the same name, `answeredBeforeItsChildLetGo`, encoding the same subtlety about a
forked child outliving a process that exited zero. The two have already diverged:
git's adds a trailing-newline check. That divergence is deliberate and documented
(`git.go:120-126` explains a fragment shown as a branch is the confident wrong
label the resolver exists to refuse; `agents.go:121-123` explains truncated JSON
fails in the parser instead), which is why this is not higher severity — but the
shared reasoning still has to be found and re-applied by hand in each package,
and a third adapter would need it a third time.

**Status**: FIXED — see task 8.6.

**Fix applied**: extracted the bounded-exec pattern into one internal
helper parameterised by the success predicate, keeping each adapter's documented
difference as the argument rather than as a copy.

**Requirements affected**: none

### [LOW] R16's wording forbids something the change deliberately does

**Lens**: conformance
**Where**: `.ktools/changes/add-session-dashboard/spec.md` R16

**Problem**: R16 says the dashboard "SHALL NOT ... modify a session's state or
the filesystem in any way", but R11 requires a SQLite history file and the
proposal puts it in scope. The scenario under R16 is narrower and correct — "no
operation that alters a session or the repository" — and the code matches the
scenario. The requirement sentence is simply broader than intended; it came over
verbatim from the OpenSpec text.

**Status**: FIXED — R16 now excludes musem's own store.

**Fix applied**: narrowed R16's wording to sessions and repositories, excluding musem's own
store, before it is folded into the living specs by `sdd-archive`.

**Requirements affected**: R16

### [LOW] `maxInt` / `minInt` reimplement language builtins

**Lens**: simplicity
**Where**: `internal/tui/tui.go:676-688`

**Problem**: `go.mod` requires Go 1.25.0; generic `max` and `min` have been
builtins since 1.21. Two hand-written equivalents are used at a dozen call sites.

**Status**: FIXED — see task 8.8.

**Fix applied**: replaced the calls with `max`/`min` and delete both functions.

**Requirements affected**: none

## Results by lens

Counts are as originally found; all are now closed.

| Lens | CRITICAL | HIGH | MEDIUM | LOW | Note |
| --- | --- | --- | --- | --- | --- |
| Architecture | 0 | 0 | 0 | 0 | clean — D2/D3/D4/D5/D8/D10 all hold in the import graph |
| Security | 0 | 0 | 1 | 0 | path traversal, argv injection and DSN confusion already defended; escape sequences were the remaining gap, now stripped at the adapter |
| Simplicity | 0 | 0 | 2 | 1 | ~60-70 lines removed, none behaviour-affecting |
| Correctness | 0 | 1 | 0 | 0 | concurrency, cursor/rotation and zero-vs-unknown all sound; the one finding was a degradation gap, now counted and surfaced |
| Tests | 0 | 0 | 1 | 0 | nearly every spec scenario covered by name; archtest now asserts the boundaries it only implied |
| Conformance | 0 | 0 | 1 | 1 | was 16 PASS / 2 PARTIAL; now 20 PASS, 0 PARTIAL, 0 FAIL |

## Validation

Commands taken from the final validation section of `tasks.md`, which is
authoritative for this change.

Re-run in full after the fixes.

| Check | Result |
| --- | --- |
| `make vet` | Pass |
| `make lint` | Pass — 0 issues |
| `make test` | Pass |
| `make race` | Pass |
| `make build` | Pass |

Run on Linux only. The macOS leg of the matrix is CI's to prove.

Each architecture rule added by task 8.5 was verified by injecting the violation
it forbids — `tui` importing `registry`, `app` importing `sqlite`, `execx`
importing the domain — confirming it failed, then restoring the tree. A rule that
cannot fail is worse than no rule, which is the standard this repository already
applies to its cgo check.

## Next step

`/ktools:sdd-archive`, to fold the delta into `.ktools/specs/`. Nothing is
outstanding: every requirement passes with code and a test cited, no finding
remains open, and the spec now describes the behaviour the code actually has —
including the two requirements (R19, R20) that the fixes introduced.

One thing to know before archiving, since it becomes the baseline: R3's status
set grew a fifth value, `ended`, and disappearing from discovery now yields it
instead of `dead`. Any later change that switches on status exhaustively has to
account for it.
