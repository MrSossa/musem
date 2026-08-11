## 1. Project scaffolding

- [x] 1.1 Initialise the Go module and package layout (root domain package, `cmd/musem`, `internal/...`)
- [x] 1.2 Add a `Makefile` with build, test and lint targets
- [x] 1.3 Configure `golangci-lint` with the project's settings
- [x] 1.4 Adjust `.gitignore` for Go (it currently targets Node and Python)
- [x] 1.5 Replace the CI workflow's `echo` with a macOS + Linux matrix running build, test and lint
- [x] 1.6 Pin `CGO_ENABLED=0` in CI and add a check that fails if the binary ends up dynamically linked
- [x] 1.7 Add a test asserting import direction: the root package imports nothing outside the standard library, and no orchestration package imports an adapter

## 2. Domain

- [x] 2.1 Define the owned types in the root package (`Session`, `Status`, `Usage`, `Cost`), with `Status` closed over its valid values and `Cost` able to hold "unknown" distinctly from zero
- [x] 2.2 Add validation methods and pure predicates on the domain types, to be called by adapters rather than reimplemented by them
- [x] 2.3 Define application error codes in `error.go` (`ENOTFOUND`, `EUNAVAILABLE`, `ESTALE`, `EUNKNOWNMODEL`), independent of any presentation concern

## 3. Agent tool adapter

- [x] 3.1 Capture real-output fixtures: live session query and representative JSONL transcripts
- [x] 3.2 Implement parsing of the live session query against the fixtures
- [x] 3.3 Implement incremental transcript reading and per-response usage extraction
- [x] 3.4 Handle unknown fields and unrecognised formats, preserving what is interpretable and logging once
- [x] 3.5 Adapter tests that fail if the foreign format changes
- [x] 3.6 Add an `inmem` adapter serving fake sessions, so the TUI can be developed without live agents

## 4. Session registry

- [x] 4.1 Declare the `Discoverer` and `BranchResolver` ports in `registry`, defined by what the package needs rather than by what the adapters offer
- [x] 4.2 Implement the periodic discovery loop with no overlapping queries
- [x] 4.3 Index sessions by stable identifier, not by title or path
- [x] 4.4 Derive observed status and expose "indeterminate" when the signal is ambiguous
- [x] 4.5 Resolve the working directory's git branch by shelling out, tolerating non-repository directories
- [x] 4.6 Mark disappeared sessions as ended while preserving their history
- [x] 4.7 Degrade when the source is unavailable: last known data, staleness marker and an actionable reason
- [x] 4.8 Tests for the `session-registry` scenarios, including rename, sessions sharing a directory, and a missing agent tool

## 5. Usage and cost

- [x] 5.1 Declare the `UsageReader` and `HistoryStore` ports in `cost`
- [x] 5.2 Define the per-model rate table as explicit, versioned data
- [x] 5.3 Compute per-session cost with the cache breakdown kept separate from ordinary input
- [x] 5.4 Mark cost unavailable for a model with no known rate, preserving the tokens
- [x] 5.5 Implement cross-session aggregation, flagging the total as partial where applicable
- [x] 5.6 Distinguish zero usage from unknown usage across the whole data path
- [x] 5.7 Tests for the `session-cost` scenarios, including the unknown model

## 6. Persistence

- [x] 6.1 Define the SQLite schema for usage history and its location via `os.UserConfigDir()`
- [x] 6.2 Implement the `sqlite` adapter with `modernc.org/sqlite`, including initial creation and migration
- [x] 6.3 Persist accumulated usage so it survives shutdown and the disappearance of the source transcript
- [x] 6.4 Restart test: history is still available after closing and reopening
- [x] 6.5 Source-deletion test: already-accounted usage is not recomputed to zero

## 7. Composition and dashboard

- [x] 7.1 Implement the `app` composer exposing a single snapshot with session and cost already joined
- [x] 7.2 Wire the graph in `main.go` by constructor injection, with no shared dependency container
- [x] 7.3 Build the Bubble Tea skeleton with one goroutine per source and a single pump feeding typed messages to the UI loop
- [x] 7.4 Render the session table with name, status, directory, branch and cost
- [x] 7.5 Order by default with sessions waiting on the user first
- [x] 7.6 Implement automatic refresh and visible staleness signalling
- [x] 7.7 Map application error codes to their presentation in `tui`, keeping the decision out of the adapters
- [x] 7.8 Implement keyboard navigation, session detail and clean terminal restore on quit
- [x] 7.9 Add in-interface key help
- [x] 7.10 Measure widths by terminal cell width and drop columns gracefully in narrow terminals
- [x] 7.11 Handle live resize without corrupting content
- [x] 7.12 Show an explanatory empty state when there are no sessions
- [x] 7.13 Verify no operation exists that can alter a session or the repository

## 8. Wrap-up

- [x] 8.1 End-to-end verification with real parallel sessions: status, cost and freshness
- [x] 8.2 Confirm no network request originates from transcript content
- [x] 8.3 Document in the README what musem is, what it observes and how to run it
