# Review — report-launched-session

**Date**: 2026-08-13
**Mode**: conformance (change `report-launched-session`)
**Diff reviewed**: `docs/propose-session-launch..HEAD` — 9 files, +736/-4
**Lenses**: architecture, security, simplicity, correctness, tests, conformance
**Verdict**: APPROVE

## Summary

A successful launch used to close the form and leave the user on an unchanged
dashboard: everything had gone right and the screen was the evidence that nothing
had happened. The dashboard now reports what was started and keeps reporting it
until the session turns up in the inventory.

The correctness lens found one HIGH, and it was the good kind — not a subtle
race but the path the interface actively invites. It has been fixed and is
guarded by a test. Everything else came back clean across four lenses.

## Conformance

| Req | Requirement | Status | Evidence |
| --- | --- | --- | --- |
| R32 | A launch says what it started | PASS | `internal/tui/launch.go:279`, `internal/tui/tui.go:906` + tests `internal/tui/launch_test.go:612` (worktree), `:636` (no worktree), `:656` (failed launch announces nothing) |
| R33 | The report stands until the session is in the inventory | PASS | `internal/tui/tui.go:132`, `:868` (`stillMissing`), `:897` (`maxLaunchNotices`) + tests `internal/tui/launch_test.go:677` (stands while absent, and the waiting-agent case), `:702` (withdrawn on arrival), `:726` (bounded and counted) |
| R25 | Launching starts an observable session — **modified** | PASS | `.ktools/specs/session-launch/spec.md:105-133`: the inventory scenario now distinguishes an agent that starts unattended, the new scenario covers one that asks first, and both say it is observed under the identifier the launch chose |

**Acceptance criteria**: 7/7 met.

**Tasks checked off without evidence**: none. All nine tasks in `tasks.md` have code
and tests behind them.

**Out of scope** (code with no requirement behind it): none. Two commits landed
that were not in `tasks.md` and both are corrections to this change's own work —
sanitising the values the report draws, and escaping a direction override in the
test source. Neither adds behaviour.

**Design drift**: none. D1, D2 and D3 are implemented as written, and the
architecture lens confirmed D1's asymmetry with `Snapshot.Kept` is argued from a
real difference rather than convenience.

## Findings

None outstanding. One was found and fixed during the review:

### [HIGH · fixed] Leaving a launch running lost the report

**Lens**: correctness
**Where**: `internal/tui/launch.go:256` (the `LaunchedMsg` guard)
**Problem**: while a launch is in flight the form accepts one key, and the hint
offers it — `esc  leave this running and go back`. Taking that offer set
`m.form = nil`; the outcome then arrived, hit `if m.form == nil { return }` and
was discarded. A session that had started, in a worktree, with an agent possibly
waiting to be let in, produced nothing on screen at all. It is the exact case
R32 and R33 exist for, reached by doing what the interface suggested.
**Fix**: applied. The outcome is recorded whether or not the form is still open;
only the form's own state is touched conditionally. `closeForm` on an
already-closed form is a no-op, so both routes end in the same place.
**Verified by**: `internal/tui/launch_test.go:806`
(`TestLeavingTheFormDoesNotLoseTheReport`), written to fail against the old code
and confirmed failing before the fix.
**Requirements affected**: R32, R33

## Results by lens

| Lens | CRITICAL | HIGH | MEDIUM | LOW | Note |
| --- | --- | --- | --- | --- | --- |
| Architecture | 0 | 0 | 0 | 0 | D1's split is reasoned rather than arbitrary: a launch is a call the view makes and gets an answer to, a reclamation is decided by the registry with nothing watching. `internal/tui` importing `safetext` crosses neither the adapter nor the orchestration ban, and `launch.go` already imported it for the same purpose |
| Security | 0 | 0 | 0 | 0 | Every foreign value the report draws — the git-derived path, the branch — goes through `safetext.Clean`; the session name is left alone and was traced to a `crypto/rand` UUID with no external input. Cleaning happens before clipping, so truncation cannot reassemble an escape. Nothing in the diff creates, removes or writes anything |
| Simplicity | 0 | 0 | 0 | 0 | `renderLaunched` and `renderKept` share a shape and differ in payload; merging them would need a callback returning a variable number of styled lines and would blur two notices answering different questions. The defensive copying matches the precedent already in the file |
| Correctness | 0 | 0 | 0 | 0 | One HIGH found and fixed, above. Aliasing, `stillMissing` on nil and duplicates, the overflow arithmetic at 1/2/3+, and the header line budget all held |
| Tests | 0 | 0 | 0 | 0 | Every scenario in the delta has a test. One test was added beyond the plan, for the escape-sequence case the sanitising fix introduced |
| Conformance | 0 | 0 | 0 | 0 | Three of three requirements PASS |

## Validation

| Check | Result |
| --- | --- |
| `make vet` | Pass |
| `make lint` | Pass — 0 issues |
| `make test` | Pass |
| `make race` | Pass |
| `make build` | Pass |

Each was run with its own exit status checked. An earlier run in this change
reported "all green" from a pipeline whose exit code came from `tail` rather than
from `make`, and hid a real lint failure for one commit.

## Next step

APPROVE. `/ktools:sdd-archive` folds R32 and R33 into `.ktools/specs/session-launch/`
and archives the change.

One thing to carry forward rather than block on: a launch that **fails** after the
user has left the form still says nothing, because R32 announces what was started
and in that case nothing was. Surfacing a failure once the form is gone would be
a different kind of notice and deserves its own requirement.
