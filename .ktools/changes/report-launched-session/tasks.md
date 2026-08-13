# Tasks — report-launched-session

Ordered so the state exists before anything draws it, and the drawing exists
before the rules about how much of it fits.

## 1. Keep what a launch produced

- [x] 1.1 Keep the outcome of a successful launch in the model instead of dropping it, and stop closing the form on nothing — `internal/tui/launch.go` · R32
  - Verification: a test driving `LaunchedMsg` with a successful outcome and asserting the model holds it afterwards
- [x] 1.2 Drop a reported launch once its session id appears among the snapshot's rows — `internal/tui/tui.go` · R33
  - Verification: test for scenario "The session appears" of R33
- [x] 1.3 Report nothing as started when the launch failed — `internal/tui/launch.go` · R32
  - Verification: test for scenario "A launch that failed" of R32

## 2. Draw it

- [x] 2.1 Render the report in the header beside the kept-worktree notices: session name, directory, branch, and the command that attaches — `internal/tui/tui.go` · R32
  - Verification: tests for scenarios "A session in a worktree" and "A session without a worktree" of R32
- [x] 2.2 Say that the session has not appeared yet, so a waiting agent reads as waiting rather than as missing — `internal/tui/tui.go` · R33
  - Verification: tests for scenarios "The session has not appeared yet" and "The agent is waiting to be let in" of R33
- [x] 2.3 Bound how many reports are drawn and count the rest, under the rule the notices beside them already follow — `internal/tui/tui.go` · R33
  - Verification: test for scenario "More launches than there is room for" of R33

## 3. Keep the header honest

- [x] 3.1 Confirm the report is clipped and padded like every other header line, and that a long path or branch cannot wrap — `internal/tui/tui.go` · R32, R33
  - Verification: the existing narrow-terminal tests, extended to a dashboard carrying a report with a long worktree path

## 4. The specification

- [x] 4.1 Correct R25 in the living spec: the inventory scenario, the agent that asks before it starts, and the identifier it is then observed under — `.ktools/specs/session-launch/spec.md` · R25
  - Verification: a reader of the living spec can tell a session waiting on a confirmation from one that failed to start
- [x] 4.2 Say in the README what a launch reports and how to attach to what it started — `README.md` · infra
  - Verification: a reader who launches a session knows what to expect on screen and what to do if it does not appear

## Final validation

- [x] Every requirement in the spec has code and a test
- [x] `make vet`
- [x] `make lint`
- [x] `make test`
- [x] `make race`
- [x] `make build`
