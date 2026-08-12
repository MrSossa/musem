# Tasks — add-session-dashboard

Scaffolding tasks that advance no single requirement are marked `infra`.

## 1. Project scaffolding

- [x] 1.1 Initialise the Go module and package layout (root domain package, `cmd/musem`, `internal/...`) · infra
  - Verification: `go build ./...`
- [x] 1.2 Add a `Makefile` with build, test and lint targets — `Makefile` · infra
  - Verification: `make build && make test && make lint`
- [x] 1.3 Configure `golangci-lint` with the project's settings — `.golangci.yml` · infra
  - Verification: `make lint` reports no findings
- [x] 1.4 Adjust `.gitignore` for Go (it currently targets Node and Python) — `.gitignore` · infra
  - Verification: `git status` is clean after `make build`
- [x] 1.5 Replace the CI workflow's `echo` with a macOS + Linux matrix running build, test and lint — `.github/workflows/ci.yml` · infra
  - Verification: the workflow runs green on both platforms
- [x] 1.6 Pin `CGO_ENABLED=0` in CI and add a check that fails if the binary ends up dynamically linked · infra
  - Verification: the check fails when the invariant is deliberately broken
- [x] 1.7 Add a test asserting import direction: the root package imports nothing outside the standard library, and no orchestration package imports an adapter — `internal/archtest` · infra
  - Verification: `go test ./internal/archtest`

## 2. Domain

- [x] 2.1 Define the owned types in the root package (`Session`, `Status`, `Usage`, `Cost`), with `Status` closed over its valid values and `Cost` able to hold "unknown" distinctly from zero — `musem.go` · R3, R7, R9
  - Verification: a test asserting unknown `Cost` never reads as zero
- [x] 2.2 Add validation methods and pure predicates on the domain types, to be called by adapters rather than reimplemented by them — `musem.go` · R3
  - Verification: `go test .`
- [x] 2.3 Define application error codes in `error.go` (`EINTERNAL`, `EINVALID`, `ENOTFOUND`, `EUNAVAILABLE`, `EUNPARSEABLE`), independent of any presentation concern — `error.go` · R6, R9, R15
  - Verification: `go test .` plus the archtest rule that the root package imports nothing
  - Note: this task originally listed `ESTALE` and `EUNKNOWNMODEL` too. Both were built and neither was ever produced: staleness became `registry.Snapshot`'s `Stale`/`Age` and an unpriced model became `SessionCost.UnknownModels`, because both are facts a snapshot carries about itself and an error can only be raised instead of the answer, not alongside it. Removed in task 9.3.

## 3. Agent tool adapter

- [x] 3.1 Capture real-output fixtures: live session query and representative JSONL transcripts — `internal/claude` · R1, R7
  - Verification: fixtures committed and loaded by the adapter tests
- [x] 3.2 Implement parsing of the live session query against the fixtures — `internal/claude` · R1, R3
  - Verification: `go test ./internal/claude`
- [x] 3.3 Implement incremental transcript reading and per-response usage extraction — `internal/claude` · R7, R8
  - Verification: a test asserting tokens are read as recorded, not recomputed
- [x] 3.4 Handle unknown fields and unrecognised formats, preserving what is interpretable and logging once — `internal/claude` · R6
  - Verification: test for scenario "Unknown format" of R6
- [x] 3.5 Adapter tests that fail if the foreign format changes — `internal/claude` · R6
  - Verification: mutating a fixture field turns the suite red
- [x] 3.6 Add an `inmem` adapter serving fake sessions, so the TUI can be developed without live agents — `internal/inmem` · infra
  - Verification: `./bin/musem --fake` renders a populated table

## 4. Session registry

- [x] 4.1 Declare the `Discoverer` and `BranchResolver` ports in `registry`, defined by what the package needs rather than by what the adapters offer — `internal/registry` · R1, R4
  - Verification: archtest confirms `registry` does not import `claude` or `git`
- [x] 4.2 Implement the periodic discovery loop with no overlapping queries — `internal/registry` · R1
  - Verification: test for scenario "Refresh does not block" of R1
- [x] 4.3 Index sessions by stable identifier, not by title or path — `internal/registry` · R2
  - Verification: tests for both scenarios of R2
- [x] 4.4 Derive observed status and expose "indeterminate" when the signal is ambiguous — `internal/registry` · R3
  - Verification: tests for both scenarios of R3
- [x] 4.5 Resolve the working directory's git branch by shelling out, tolerating non-repository directories — `internal/git` · R4
  - Verification: tests for both scenarios of R4
- [x] 4.6 Mark disappeared sessions as ended while preserving their history — `internal/registry` · R5
  - Verification: test for scenario "The session disappears" of R5
- [x] 4.7 Degrade when the source is unavailable: last known data, staleness marker and an actionable reason — `internal/registry` · R6
  - Verification: test for scenario "The agent tool is not installed" of R6
- [x] 4.8 Tests for the `session-registry` scenarios, including rename, sessions sharing a directory, and a missing agent tool — `internal/registry` · R1, R2, R3, R4, R5, R6
  - Verification: `go test ./internal/registry`

## 5. Usage and cost

- [x] 5.1 Declare the `UsageReader` and `HistoryStore` ports in `cost` — `internal/cost` · R7, R11
  - Verification: archtest confirms `cost` does not import `claude` or `sqlite`
- [x] 5.2 Define the per-model rate table as explicit, versioned data — `internal/cost` · R9
  - Verification: `go test ./internal/cost`
- [x] 5.3 Compute per-session cost with the cache breakdown kept separate from ordinary input — `internal/cost` · R8
  - Verification: test for scenario "Cache-heavy session" of R8
- [x] 5.4 Mark cost unavailable for a model with no known rate, preserving the tokens — `internal/cost` · R9
  - Verification: test for scenario "A new model appears" of R9
- [x] 5.5 Implement cross-session aggregation, flagging the total as partial where applicable — `internal/cost` · R10
  - Verification: test for scenario "Fleet total" of R10
- [x] 5.6 Distinguish zero usage from unknown usage across the whole data path — `internal/cost` · R7
  - Verification: test for scenario "Session with no recorded activity" of R7
- [x] 5.7 Tests for the `session-cost` scenarios, including the unknown model — `internal/cost` · R7, R8, R9, R10
  - Verification: `go test ./internal/cost`

## 6. Persistence

- [x] 6.1 Define the SQLite schema for usage history and its location via `os.UserConfigDir()` — `internal/sqlite` · R11
  - Verification: the database is created under the config dir on first run
- [x] 6.2 Implement the `sqlite` adapter with `modernc.org/sqlite`, including initial creation and migration — `internal/sqlite` · R11
  - Verification: `go test ./internal/sqlite` with `CGO_ENABLED=0`
- [x] 6.3 Persist accumulated usage so it survives shutdown and the disappearance of the source transcript — `internal/sqlite` · R11
  - Verification: `go test ./internal/sqlite`
- [x] 6.4 Restart test: history is still available after closing and reopening — `internal/sqlite` · R11
  - Verification: test for scenario "musem restarts" of R11
- [x] 6.5 Source-deletion test: already-accounted usage is not recomputed to zero — `internal/sqlite` · R11
  - Verification: test for scenario "The source record disappears" of R11

## 7. Composition and dashboard

- [x] 7.1 Implement the `app` composer exposing a single snapshot with session and cost already joined — `internal/app` · R13
  - Verification: `go test ./internal/app`
- [x] 7.2 Wire the graph in `main.go` by constructor injection, with no shared dependency container — `cmd/musem/main.go` · infra
  - Verification: `make build && ./bin/musem`
- [x] 7.3 Build the Bubble Tea skeleton with one goroutine per source and a single pump feeding typed messages to the UI loop — `internal/tui` · R15
  - Verification: `make race`
- [x] 7.4 Render the session table with name, status, directory, branch and cost — `internal/tui` · R13
  - Verification: test for scenario "Several active sessions" of R13
- [x] 7.5 Order by default with sessions waiting on the user first — `internal/tui` · R14
  - Verification: test for scenario "A session starts waiting" of R14
- [x] 7.6 Implement automatic refresh and visible staleness signalling — `internal/tui` · R15
  - Verification: tests for both scenarios of R15
- [x] 7.7 Map application error codes to their presentation in `tui`, keeping the decision out of the adapters — `internal/tui` · R6, R9, R15
  - Verification: archtest confirms adapters carry no presentation concern
- [x] 7.8 Implement keyboard navigation, session detail and clean terminal restore on quit — `internal/tui` · R17
  - Verification: test for scenario "Navigate and quit" of R17
- [x] 7.9 Add in-interface key help — `internal/tui` · R17
  - Verification: test for scenario "Discoverable help" of R17
- [x] 7.10 Measure widths by terminal cell width and drop columns gracefully in narrow terminals — `internal/tui` · R18
  - Verification: test for scenario "Narrow terminal" of R18
- [x] 7.11 Handle live resize without corrupting content — `internal/tui` · R18
  - Verification: test for scenario "Live resize" of R18
- [x] 7.12 Show an explanatory empty state when there are no sessions — `internal/tui` · R13
  - Verification: test for scenario "No sessions" of R13
- [x] 7.13 Verify no operation exists that can alter a session or the repository · R16
  - Verification: manual sweep of every key binding, `TestDashboardHasNoMutatingOperations` (which scans the `tui` sources for `os`/`exec` imports and mutating calls), and the archtest network rules

## 8. Review remediation

Added after the phase-3 review. Each closes a finding recorded in `review.md`.

- [x] 8.1 Count records a discovery pass could not read and carry the count to the view — `musem.go`, `internal/claude/agents.go`, `internal/registry/registry.go`, `internal/app/app.go`, `internal/tui/tui.go` · R20
  - Verification: `TestAPassThatCouldReadNothingSaysSoRatherThanReportingNoSessions`, `TestUnreadableRecordsAreReportedRatherThanReadAsAnEmptyMachine`, `TestUnreadRecordsReachTheView`
- [x] 8.2 Strip control characters from foreign text at the adapter boundary — `internal/claude/sanitise.go`, `internal/claude/transcript.go` · R19
  - Verification: `TestForeignTextCannotCarryTerminalInstructions`, `TestSanitisePreservesOrdinaryText`
- [x] 8.3 Record when a session entered its status and report the age in the detail pane — `musem.go`, `internal/registry/registry.go`, `internal/tui/tui.go` · R3
  - Verification: `TestStatusSinceSurvivesRefreshesAndResetsOnChange`, `TestDetailSaysHowLongTheStatusHasHeld`
- [x] 8.4 Separate a session that ended from one that died, keeping a source-reported death intact — `musem.go`, `internal/registry/registry.go` · R3
  - Verification: `TestDisappearedSessionIsMarkedNotDropped`, `TestAnAdapterReportedDeathIsNotSoftenedToEnded`
- [x] 8.5 Extend the architecture tests to the boundaries the design promised but nothing asserted — `internal/archtest/arch_test.go` · infra
  - Verification: each new rule was made to fail by injecting the violation it forbids, then restored
- [x] 8.6 Extract the bounded-subprocess pattern shared by the two shelling adapters — `internal/execx` · infra
  - Verification: `go test ./internal/execx`, plus `TestSharedHelpersStayLeaves` keeping it a leaf
- [x] 8.7 Drop the registry's unconsumed push channel and its drain — `internal/registry/registry.go`, `cmd/musem/main.go` · infra
  - Verification: `TestRunDoesNotOverlapQueries` still holds with the loop pulling only
- [x] 8.8 Replace the hand-written maxInt/minInt with the language builtins — `internal/tui/tui.go` · infra
  - Verification: `make lint`, `make test`

## 9. Second review round

Added after the round-2 review. Round 1's fixes held except for 8.1, which
delivered the counter it promised but never wired it into the decision the
finding was about.

- [x] 9.1 Stop the ending sweep from running on a pass that could not read every record it found — `internal/registry/registry.go` · R20
  - Verification: `TestUnreadableRecordsAreReportedRatherThanReadAsAnEmptyMachine` (now asserting the session's status, not only the counter), `TestAPartiallyReadPassEndsNothing`, `TestACleanPassAfterADegradedOneStillEnds`
- [x] 9.2 Sanitise the CLI's stderr explanation before it becomes an error message — `internal/claude/agents.go` · R19
  - Verification: `TestTheCLIExplanationCannotCarryTerminalInstructions`, `TestADisarmedSequenceIsLeftAsInertText`, `TestAnExplanationOfPureControlBytesIsSkipped`
- [x] 9.3 Drop the two error codes nothing produces and the unreachable branch that rendered one — `error.go`, `internal/tui/tui.go` · infra
  - Verification: `make lint`, `make test`; no `Errorf`/`Wrap` call site constructs either code
- [x] 9.4 Correct the registry sort comment, which still described the pre-8.4 conflation of ended and dead — `internal/registry/registry.go` · infra
  - Verification: reading it against `TestEndedSessionsSortBelowLiveOnes` and `TestAnAdapterReportedDeathIsNotSoftenedToEnded`
- [x] 9.5 Say that a dropped record leaves rows stale as well as missing, which is the consequence 9.1 introduced — `internal/tui/tui.go` · R15, R20
  - Verification: `TestUnreadableSessionRecordsAreAnnounced`, extended to assert both consequences are named
- [x] 9.6 Refuse a discovery record whose identifier carries control bytes, rather than stripping it — `internal/claude/agents.go` · R19, R2
  - Verification: `TestAnIdentifierCarryingControlBytesIsRefusedRatherThanCleaned`; the check was made to fail by neutralising it, then restored
- [x] 9.7 Extract the sanitiser into a shared leaf so both shelling adapters use one copy, and clean the git branch name with it — `internal/safetext`, `internal/git/git.go`, `internal/claude` · R19
  - Verification: `go test ./internal/safetext`, `TestAHostileBranchNameCannotDriveTheTerminal`, and `TestSharedHelpersStayLeaves` with `safetext` added to the helper list. Rounds 1 and 2 both dismissed the branch as safe because git rejects control characters in ref names; that is true of ASCII controls and false of U+202E, which `git branch` accepts and `rev-parse` returns verbatim — confirmed against the installed git before fixing.
- [x] 9.8 Compare the detached-HEAD sentinel before cleaning, so cleaning cannot manufacture it — `internal/git/git.go` · infra
  - Verification: `TestACleanedNameCannotImpersonateDetachedHEAD`
- [x] 9.9 Keep the U+202E rationale in one place and rename the test file left pointing at a deleted symbol — `internal/git`, `internal/safetext`, `internal/claude/foreign_text_test.go` · infra
  - Verification: `make lint`, `make test`; the explanation now lives only in `safetext`, with each call site keeping its own decision

## 10. Wrap-up

- [x] 10.1 End-to-end verification with real parallel sessions: status, cost and freshness · R1, R13, R15
  - Verification: manual run against live agent sessions
- [x] 10.2 Confirm no network request originates from transcript content · R12
  - Verification: archtest rule that nothing reaches the network
- [x] 10.3 Document in the README what musem is, what it observes and how to run it — `README.md` · infra
  - Verification: a reader can build and run musem from the README alone

## Final validation

- [x] Every requirement in the spec has code and a test
- [x] `make vet`
- [x] `make lint`
- [x] `make test`
- [x] `make race`
- [x] `make build`
