# Review — add-session-launch

**Date**: 2026-08-12
**Mode**: conformance (change `add-session-launch`)
**Diff reviewed**: working tree vs `HEAD` — 18 modified files (+640/-120) plus 20 new files
**Lenses**: architecture, security, simplicity, correctness, tests, conformance
**Verdict**: APPROVE

This is the second pass. The first found one PARTIAL requirement and seven
findings; all are fixed, and every lens was re-run over the corrected code rather
than trusted to have stayed fixed. The first-pass report is superseded by this
one — the section at the end records what changed, so the trail is not lost.

## Summary

musem launches a session into a worktree it creates, and takes that worktree back
when the session ends and there is nothing in it left to lose. All eleven
requirements pass with code and tests cited below.

The destructive path is the part that mattered and it holds end to end:
ownership is a persisted record keyed by the session it was made for and nothing
else, cleanliness is four separately-tested conditions whose unanswerable cases
keep the worktree, and git's own refusal to delete a dirty worktree is left in
place behind musem's own check. Every lens re-ran clean.

## Conformance

| Req | Requirement | Status | Evidence |
| --- | --- | --- | --- |
| R1 | Launching is reachable from the dashboard | PASS | `internal/tui/tui.go:171`, `internal/tui/launch.go:141` + tests `internal/tui/launch_test.go:164`, `:191` |
| R2 | Working directory and worktree are the user's to choose | PASS | `launch.go:35`, `internal/tui/launch.go:150`, `internal/launch/launch.go:177` + tests `launch_test.go:13`, `internal/tui/launch_test.go:214`, `:231`, `internal/launch/launch_test.go:98`, `:296` |
| R3 | The branch is proposed and can be replaced | PASS | `internal/launch/launch.go:145`, `internal/git/worktree.go:132`, `:165`, `:216` + tests `internal/launch/launch_test.go:409`, `:316`, `:348`, `:421`, `internal/tui/launch_test.go:261`, `:284`, `internal/git/worktree_test.go:132`, `:167` |
| R4 | The destination is derived and visible | PASS | `internal/launch/launch.go:262`, `internal/git/worktree.go:202`, `internal/tui/launch.go:521` + tests `internal/launch/launch_test.go:278`, `:364`, `internal/tui/launch_test.go:340`, `internal/git/worktree_test.go:198`, `internal/launch/e2e_test.go:429` |
| R5 | Launching starts an observable session | **PASS** | `internal/launch/launch.go:296`, `internal/claude/launch.go:78`, `internal/tmux/tmux.go:97` + tests `internal/launch/launch_test.go:48`, and — closing the first pass's gap — `internal/launch/e2e_test.go:491` and `:526`, which run a real registry discovery pass over a launched session and assert it appears with its working directory and the branch the launch created, resolved by the same git adapter every other session's branch goes through. Survival is `e2e_test.go:243` plus the `-d` flag asserted at `internal/tmux/tmux_test.go:56` |
| R6 | The dashboard stays usable while a launch is in flight | PASS | `internal/tui/launch.go:200`, `:73` + test `internal/tui/launch_test.go:422`, and `make race` |
| R7 | A launch that cannot proceed explains itself | PASS | `internal/launch/launch.go:303-310`, `internal/tmux/tmux.go:76`, `internal/claude/launch.go:48` + tests `internal/launch/launch_test.go:207`, `internal/tmux/tmux_test.go:74`, `internal/claude/launch_test.go:68`, `internal/tui/launch_test.go:467` |
| R8 | A failed launch leaves nothing half-created | PASS | `internal/launch/launch.go:397` + tests `internal/launch/launch_test.go:129` (failure injected at each of five steps), `:243`, `:458` (every kind of remains: worktree, session, record) |
| R9 | A worktree is reclaimed only when it is clean | PASS | `internal/git/worktree.go:311`–`:390`, `internal/launch/reclaim.go:24` + tests `internal/git/worktree_test.go:307`–`:420` (one per condition, including no-remote and undetermined), `internal/launch/reclaim_test.go:28`, `:58`, `internal/launch/e2e_test.go:287`, `:312`, `:391` |
| R10 | Only what musem created is ever removed | PASS | `internal/sqlite/worktree.go:58`, `internal/launch/reclaim.go:34` + tests `internal/launch/reclaim_test.go:106`, `:185`, `internal/sqlite/worktree_test.go:16`, `:77`, `:164`, `internal/launch/e2e_test.go:414` |
| R11 | Destructive actions are narrow, recorded and explained | PASS | `internal/tui/tui_test.go:323` (the view cannot import `os`/`os/exec`), `internal/launch/reclaim.go:41` + tests `internal/tui/launch_test.go:514`, `internal/launch/e2e_test.go:414` |
| R16 | Read only (REMOVED) | PASS | Replaced rather than deleted: `internal/tui/tui_test.go:323` keeps the boundary that survives R16 — the view cannot touch disk directly — and drops the read-only claim |

**Acceptance criteria**: 7/8 met.

- Criterion 8 ("pass on macOS and Linux") is met on Linux; there is no macOS
  machine in this environment, so the macOS half is unverified rather than false.
  Nothing in the change is platform-specific beyond what already shipped.
- The boxes in `proposal.md` are still unticked. Not a defect; worth doing at
  archive time so the document does not read as untouched.

**Tasks checked off without evidence**: none. Every `- [x]` in `tasks.md` has code
and tests behind it. Tasks 10.1 and 10.2 were reworded so the recorded
verification matches what was actually done (an automated end-to-end over a real
repository with tmux and the Claude CLI stubbed), with the one run that remains
outstanding — against real tmux and a real Claude session — stated explicitly
rather than implied by a ticked box.

**Out of scope** (code with no requirement or task behind it): three small
consequences of removing R16, all of which would otherwise leave the interface
asserting something now false —

- `internal/tui/tui.go:566` (`renderEmpty`) said "musem observes sessions started
  elsewhere; it does not start them".
- `musem.go:1` and `cmd/musem/main.go:1` described musem as read-only.
- `footerText` gained a width parameter and clips, required by task 8.6 after the
  longer hint overflowed a 20-column terminal.

**Design drift**: none outstanding. `design.md`'s Impact row claimed the form
would use `bubbles/textinput` "already an indirect dependency"; `bubbles` is not
in the module graph, so the form carries a small in-package editor instead. The
row now records what was actually done and why.

## Findings

None.

Seven findings from the first pass were fixed and verified by re-running every
lens over the corrected code:

| First pass | Fix | Verified by |
| --- | --- | --- |
| [MEDIUM] The directory fallback could hand one session's worktree to another | The `OR path = ?` clause and the `dir` parameter are gone; `WorktreeFor` is keyed by session id alone (`internal/sqlite/worktree.go:58`) | Security lens re-check, plus `internal/sqlite/worktree_test.go:77` and `internal/launch/reclaim_test.go:185`, which assert the second way in stays shut while the owning session still reclaims |
| [MEDIUM] The `execx` kind-unwrap copied into both new adapters | `execx.KindOf` (`internal/execx/execx.go:164`); both adapters call it, and so does the new `internal/claude/launch.go:65` | Simplicity lens re-check; the meaningless-Kind-on-success wart went with it |
| [MEDIUM] Third copy of the stderr first-line helper | `safetext.FirstLine` (`internal/safetext/safetext.go:85`); `claude`, `git` and `tmux` all delegate | Simplicity lens re-check; correctness lens confirmed behaviour is unchanged from the three originals |
| [LOW] `state.done` written and never read | Field and deferred assignment deleted | Correctness and simplicity lens re-checks |
| [LOW] The destination preview skipped the sanitisation `design.md` specifies | `safetext.Clean` on the drawn value (`internal/tui/launch.go:521`), and — the more valuable half — branch names are now refused at plan time via `ValidBranch` (`internal/git/worktree.go:216`, called at `internal/launch/launch.go:212`), so a name git would reject never reaches the preview or a "ready" form | Security lens re-check; new test `internal/launch/launch_test.go:421` |
| [LOW] `itoa` reimplemented `strconv.Itoa` | `strconv.Itoa` (`internal/launch/launch.go:163`) | Simplicity lens re-check |
| [LOW] `design.md` named a dependency the project does not have | Impact row corrected | This report |

Two further things were cleaned up while fixing the above, neither of which any
lens had flagged: dead scaffolding left in the test doubles (`wantRecorded`,
`fakeOwned.byPath`), and five injection points on the doubles that no test ever
set. The latter were turned into coverage rather than deleted — the rollback's
"session still running" and "record could not be dropped" branches, and the
plan-time failure paths, were genuinely untested (`internal/launch/launch_test.go:458`,
`:490`, `:518`).

## Results by lens

Every lens was re-run over the corrected code. All four returned clean.

| Lens | CRITICAL | HIGH | MEDIUM | LOW | Note |
| --- | --- | --- | --- | --- | --- |
| Architecture | 0 | 0 | 0 | 0 | D1–D8 confirmed across both passes. The new leaf helpers (`execx.KindOf`, `safetext.FirstLine`) import nothing from musem; `internal/tui` importing `safetext` crosses neither the adapter nor the orchestration ban, and a string transform is not a mutating call; `ValidBranch` follows the consumer-declared-interface discipline already used for `Occupied` and `CheckedOut`; the test-only `registry` import in `launch_test` matches what `app_test` already does |
| Security | 0 | 0 | 0 | 0 | Both first-pass findings verified fixed and no bypass found. No shell anywhere, every user-influenced argv element validated first, absolute paths so nothing reads as a flag, parameterised SQL throughout, and `Cleanliness` unreachable as clean by accident |
| Simplicity | 0 | 0 | 0 | 0 | All four fixes are genuine deletions with real call sites for the new helpers. Every field on every test double is both set and read |
| Correctness | 0 | 0 | 0 | 0 | `KindOf` never misread on the success path — every caller consults it only inside an `err != nil` branch. `FirstLine` equivalent to the three originals. Rollback ordering, `Reclaim`'s sequence, porcelain parsing, the debounce generation counter, editor rune arithmetic, the registry lock release and the reclaim goroutine all held |
| Tests | 0 | 0 | 0 | 0 | 305 tests across the project. Every spec scenario now has a test, including R5's "the session joins the inventory" |
| Conformance | 0 | 0 | 0 | 0 | Eleven of eleven requirements PASS |

## Validation

Commands from the "Final validation" section of `tasks.md`.

| Check | Result |
| --- | --- |
| `make vet` | Pass |
| `make lint` | Pass — 0 issues |
| `make test` | Pass — 14 packages |
| `make race` | Pass — 14 packages |
| `make build` | Pass — statically linked, `CGO_ENABLED=0` |

## Next step

APPROVE. `/ktools:sdd-archive` to fold the delta into the living specs.

One thing to carry forward rather than block on: the end-to-end run against real
tmux and a real Claude session has not happened, because tmux is not installed on
this machine. It is recorded against tasks 10.1 and 10.2 and is worth doing once
on a machine that has both, before anyone relies on launching in earnest.
