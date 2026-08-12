# Review — add-session-dashboard

**Date**: 2026-08-12 (rounds 2–6)
**Mode**: conformance (change `add-session-dashboard`)
**Diff reviewed**: `main..HEAD` plus the working tree — 48 files, ~+12000/-28
**Lenses**: architecture, security, simplicity, correctness, tests, conformance
**Verdict**: APPROVE — every finding closed, all four lenses report zero on the
final state

## Summary

Round 1 found eight issues and reported them fixed. Re-verifying against the code
rather than against that write-up showed one had not held, and the rounds that
followed turned up four more that earlier passes had waved through on
assumptions. Everything is now closed, with each fix verified by injecting the
failure it prevents.

The one that mattered most: round 1's HIGH — a foreign format change turning a
fleet of live sessions into a wall of dead ones — had been answered with the
counter it asked for, and the counter was never wired into the decision. It
reached the screen as a banner and nothing else. `Refresh` still ended every
session absent from a pass that had openly failed to read its records, which is
the literal negation of what R20 asks for.

The other four came from questioning what earlier rounds had asserted rather than
checked. Rounds 1 and 2 both cleared the git branch name on the grounds that git
rejects control characters in ref names; that is true of ASCII controls and false
of U+202E, which `git branch` accepts and `rev-parse` hands back verbatim —
confirmed against the installed git before fixing. Two error codes had been built
and never produced. The session identifier reached the detail pane unsanitised,
three lines below two fields that were being sanitised.

One fix was deliberately not the obvious one. The identifier is not display text:
it is the registry's key and the stem of the transcript filename, so cleaning it
would point musem at a file that does not exist and collapse two identities into
one. It is refused and counted instead — recorded as a new scenario under R19
rather than left as an undocumented divergence.

## Conformance

| Req | Requirement | Status | Evidence |
| --- | --- | --- | --- |
| R1 | Live session discovery | PASS | `internal/registry/registry.go:198`, `internal/claude/agents.go:51` · tests `registry_test.go:37`, `:402` |
| R2 | Stable session identity | PASS | `musem.go:85`, `internal/registry/registry.go:253`, `internal/claude/agents.go:161` (identifiers refused, never rewritten) · tests `registry_test.go:85`, `:103`, `foreign_text_test.go:89` |
| R3 | Observed session status | PASS | `musem.go:21-47`, `:113`, `internal/registry/registry.go:251,268` · tests `registry_test.go:179`, `claude_test.go:104`, `tui_test.go:964` |
| R4 | Session git branch | PASS | `internal/git/git.go:101-114`, `internal/registry/registry.go:327` · tests `git_test.go:31`, `:74`, `:153`, `:178` |
| R5 | Session end of life | PASS | `internal/registry/registry.go:291-311` · tests `registry_test.go:118`, `:257`, `app_test.go:117` |
| R6 | Degradation when a source is unavailable | PASS | `internal/registry/registry.go:214-221`, `internal/claude/agents.go:70-93` · tests `registry_test.go:273`, `:294`, `claude_test.go:87` |
| R7 | Usage derived from structured data | PASS | `internal/claude/transcript.go:351`, `musem.go:170-201` · tests `claude_test.go:119`, `cost_test.go:174` |
| R8 | Cache breakdown | PASS | `musem.go:170-182`, `internal/cost/rates.go` · tests `cost_test.go:89`, `:104` |
| R9 | Model with no known rate | PASS | `internal/cost/cost.go:122-180` · test `cost_test.go:145` |
| R10 | Usage aggregation | PASS | `internal/cost/cost.go:231-280` · tests `cost_test.go:309`, `:339`, `app_test.go:443` |
| R11 | History persistence | PASS | `internal/sqlite/sqlite.go` · tests `sqlite_test.go:72`, `:152`, `cost_test.go:224` |
| R12 | Local processing | PASS | no networking in the graph · tests `arch_test.go:266` (transitive), `:279` (first-party) |
| R13 | Fleet overview | PASS | `internal/app/app.go:139`, `internal/tui/tui.go:491` · tests `tui_test.go:49`, `:61` |
| R14 | Actionable sessions come first | PASS | `musem.go:61`, `internal/registry/registry.go:398-419` · tests `registry_test.go:371`, `app_test.go:69` |
| R15 | Automatic refresh and data freshness | PASS | `internal/registry/registry.go:426-430`, `internal/tui/tui.go:453,461` · tests `tui_test.go:74`, `:86`, `:121`, `:945` |
| R16 | Read only | PASS | only production write is `os.MkdirAll` on musem's own store (`internal/sqlite/sqlite.go:52`) · test `tui_test.go:301` |
| R17 | Keyboard navigation | PASS | `internal/tui/tui.go:80`, `:674` · tests `tui_test.go:160`, `:207`, `:222`, `:367` |
| R18 | Legibility in narrow terminals | PASS | `internal/tui/tui.go:219`, `:284-320` · tests `tui_test.go:235`, `:280`, `:698`, `:787` |
| R19 | Foreign text carries no terminal instructions | PASS | `internal/safetext/safetext.go`, applied at `agents.go:109,181-182`, `transcript.go:351`, `git.go:107`; identifiers refused at `agents.go:161` · tests `safetext_test.go`, `foreign_text_test.go`, `git_test.go:153` |
| R20 | An unreadable record is reported | PASS | counted at `internal/claude/agents.go:144,150,175`, gating the ending sweep at `internal/registry/registry.go:291`, surfaced at `internal/tui/tui.go:461` · tests `registry_test.go:216`, `:237`, `:257` |

20 PASS, 0 PARTIAL, 0 FAIL. **Acceptance criteria**: 9/9, now ticked in
`proposal.md`.

**Tasks checked off without support**: none outstanding. Two citations were
corrected rather than left standing — task 7.13 pointed at an archtest write rule
that does not exist (the check lives in `tui_test.go:301`), and task 2.3 listed
two error codes that were subsequently removed.

**Design drift**: none outstanding. D15 was amended to describe the extraction of
the sanitiser into `internal/safetext` and the refuse-versus-clean split, since
the code had outgrown a decision that named only the `claude` adapter and three
fields. The package tree in `design.md` gained `safetext`.

**Out of scope**: commit `a5154f9` (`.ktools/` artifacts, `CLAUDE.md`) answers no
requirement — it is this change's planning material and its migration off
OpenSpec. Expected.

## Findings

All closed. Each fix was verified by injecting the failure it prevents and
confirming the suite went red, then restoring — the standard this repository
already applies to its cgo check.

### [HIGH] The skipped-record count never reached the decision it was added for

**Lens**: correctness / conformance · **Where**: `internal/registry/registry.go:291`

`Refresh` stored `discovery.Skipped` and passed it to the view, and nothing else.
The ending sweep consulted only `seen`, built exclusively from the records that
parsed. So the round-1 trigger still fired: a field changing type across the
board made every record undecodable, `Discover` returned an empty slice with a
**nil error**, and every known session was flipped to `ended` and demoted. The
banner did not cover it — it said sessions "may be missing from this list" while
the sessions in question sat in the list labelled as finished.

**Fixed** (task 9.1): the sweep is gated on `discovery.Skipped == 0`. A pass that
admits it could not read everything has not established that anything is absent.
The cost is that a session which genuinely ended during a degraded pass stays
listed until a clean one arrives, which is the right direction to err: a finished
session shown as live for a cycle is noise, a live session shown as finished is
one the user stops looking at. `registry_test.go:216` now asserts the session's
status rather than only the counter, joined by `TestAPartiallyReadPassEndsNothing`
and `TestACleanPassAfterADegradedOneStillEnds` — the last of which keeps the
guard from becoming a latch.

### [MEDIUM] CLI stderr reached the terminal without sanitisation

**Lens**: security · **Where**: `internal/claude/agents.go:109`

Round 1 sanitised three fields and not the error text built from the CLI's own
stderr, which becomes `Snapshot.ErrMessage` and is redrawn every frame for as
long as discovery keeps failing.

**Fixed** (task 9.2): `firstLine` cleans each line before returning it. This also
closed a latent bug — `strings.TrimSpace` does not remove control bytes, so a
line of pure escapes counted as non-empty and was returned raw.

### [MEDIUM] The session identifier reached the detail pane unsanitised

**Lens**: security · **Where**: `internal/claude/agents.go:161`

`Name` and `Dir` were sanitised; `ID`, three lines away, was not. It is rendered
at `tui.go:559` (table fallback when a session has no name) and `:626` (detail
pane, unconditionally).

**Fixed** (task 9.6), and deliberately not by sanitising it. The ID is the
registry's key and the stem of the transcript filename globbed at
`usage.go:140`, so cleaning it would designate a session nobody has and a file
that cannot be opened, and two identifiers differing only in control bytes would
collapse into one. The record is refused and counted among those the pass could
not use. Recorded in the spec as a second scenario under R19.

### [MEDIUM] The git branch name was never sanitised

**Lens**: security · **Where**: `internal/git/git.go:107`

Rounds 1 and 2 both cleared this field on the reasoning that git rejects control
characters in ref names. That reasoning is half right: git refuses an ASCII
control byte and accepts U+202E. Verified directly — `git branch` created a ref
containing the override and `rev-parse --abbrev-ref HEAD` returned it byte for
byte. A branch can therefore be named to reorder the column the user scans to
tell their worktrees apart.

**Fixed** (task 9.7): the branch is cleaned. Since a second adapter now needed the
same predicate, the sanitiser moved to `internal/safetext`, a leaf on the same
terms as `execx` (D16) and held to it by `TestSharedHelpersStayLeaves`. A
security predicate maintained in two copies is how one of them ends up stale.

A follow-on from that fix (task 9.8): the sentinel comparison now runs on the raw
value, because cleaning first would let a branch named `H<zero-width>EAD` arrive
at the comparison as `HEAD` and be reported as no branch — the defence
manufacturing the value it tests for.

### [MEDIUM] The U+202E rationale was restated in four places

**Lens**: simplicity · **Where**: `internal/safetext/safetext.go:54-63`

The same git-specific fact was argued in full in the helper, both its tests, and
the git adapter. Prose that must change in lockstep is duplication.

**Fixed** (task 9.9): `safetext` owns the fact; each call site keeps only its own
decision.

### [LOW] Two error codes were declared and never produced

**Lens**: simplicity · **Where**: `error.go`, `internal/tui/tui.go:156`

Nothing constructed `ESTALE` or `EUNKNOWNMODEL`; staleness travels as
`Snapshot.Stale`/`Age` and unpriced models as `SessionCost.UnknownModels`.
`EUNKNOWNMODEL` had a render branch that could not fire.

**Fixed** (task 9.3): both removed, with a comment recording why an error is the
wrong shape for a fact a snapshot carries about itself.

### [LOW] A registry comment contradicted the ended/dead split

**Lens**: tests / documentation · **Where**: `internal/registry/registry.go:398`

The comment justifying the `Ended()` guard still said ended sessions are marked
dead, which task 8.4 made false. The guard remains correct for the case the
comment no longer described — a source-reported death that then disappears keeps
`StatusDead`, whose urgency outranks running.

**Fixed** (task 9.4): rewritten to describe the case it actually guards.

### [LOW] A banner accounted for only one of its two consequences

**Lens**: conformance · **Where**: `internal/tui/tui.go:461`

Introduced by the fix to the HIGH: a previously-known session whose record
becomes unreadable keeps its row frozen at its last observed status. The banner
spoke only of sessions being missing, presenting the frozen row as current.

**Fixed** (task 9.5): it now reads "sessions may be missing or stale", and the
test asserts both consequences are named.

### [LOW] A test file was named after a deleted symbol

**Lens**: simplicity · **Where**: `internal/claude/sanitise_test.go`

**Fixed** (task 9.9): renamed to `foreign_text_test.go`, which is what it tests.

## Results by lens

Final state, after all fixes.

| Lens | CRITICAL | HIGH | MEDIUM | LOW | Note |
| --- | --- | --- | --- | --- | --- |
| Architecture | 0 | 0 | 0 | 0 | import graph verified independently with `go list -deps`; D2/D3/D5/D8/D16 hold, `safetext` correctly classed as a leaf |
| Security | 0 | 0 | 0 | 0 | every string reaching the terminal traced to origin and either cleaned or refused; argv, glob, DSN, SQL and resource bounds all sound |
| Simplicity | 0 | 0 | 0 | 0 | `safetext` justified by the `execx` precedent; no dead code, no restated rationale |
| Correctness | 0 | 0 | 0 | 0 | extraction behaviour-preserving; the degradation guard recovers and cannot latch |
| Tests | 0 | 0 | 0 | 0 | 203 tests; every fix verified by injecting the failure it prevents |
| Conformance | 0 | 0 | 0 | 0 | 20 PASS |

## Validation

| Check | Result |
| --- | --- |
| `make vet` | Pass |
| `make lint` | Pass — 0 issues |
| `make test` | Pass — every package |
| `make race` | Pass |
| `make build` | Pass |
| `gofmt -l .` | Clean |

Run on Linux; the macOS leg is CI's to prove. The workflow runs both, pins
`CGO_ENABLED=0`, reads the setting back off the built binary rather than
inferring it, and collapses the matrix into the single `ci` context the branch
ruleset requires.

Each fix was made to fail before being accepted: the ending-sweep guard
neutralised (two tests red), `Clean` removed from `firstLine` (three red), the
identifier check disabled (one red), and `Clean` removed from the git branch path
(one red) — each restored afterwards.

## Next step

`/ktools:sdd-archive`. Nothing is outstanding: every requirement passes with code
and a test cited, no finding remains open, and the spec and design now describe
the behaviour the code actually has.

Two things to carry forward, since they become the baseline:

- R3's status set has a fifth value, `ended`, and disappearing from discovery
  yields it rather than `dead`. Any later change switching on status exhaustively
  has to account for it.
- Foreign text is cleaned where it is read and refused where it is matched. A new
  adapter gets `safetext` rather than its own copy, and a new identifier-shaped
  field is refused rather than cleaned.
