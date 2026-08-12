# Tasks — add-session-launch

Scaffolding tasks that advance no single requirement are marked `infra`.

Ordered so the write path is built from the bottom up: the adapters that can fail
come first, the sequence that has to undo them second, the form last. Nothing in
the interface can launch anything until the rollback it depends on is tested.

## 1. Domain

- [ ] 1.1 Define the launch request and its outcome in the root package, with the worktree toggle defaulting to enabled — `musem.go` · R1, R2
  - Verification: a test asserting the zero value of a launch request has the worktree enabled, so the default cannot be lost in the form
- [ ] 1.2 Define the cleanliness verdict as a closed set — clean, dirty with a named reason, undetermined — so no caller can read "undetermined" as "clean" — `musem.go` · R9
  - Verification: a test asserting the zero value is not clean
- [ ] 1.3 Define the record of a worktree musem created, keyed by session — `musem.go` · R10
  - Verification: `go test .`

## 2. git worktrees

- [ ] 2.1 Create a worktree for a new branch, and for an existing one — `internal/git` · R3, R4
  - Verification: tests against a fake git for both, asserting the argv git receives
- [ ] 2.2 Report that a directory is not a repository, and that a branch is already checked out elsewhere — `internal/git` · R2, R3
  - Verification: tests for scenarios "A directory that is not a repository" of R2 and "A branch already checked out elsewhere" of R3
- [ ] 2.3 List the branches that already exist, for the form to offer — `internal/git` · R3
  - Verification: test for scenario "Reusing an existing branch" of R3
- [ ] 2.4 Refuse a destination that already exists, without writing into it — `internal/git` · R4
  - Verification: test for scenario "Occupied destination" of R4
- [ ] 2.5 Remove a worktree — `internal/git` · R8, R9
  - Verification: `go test ./internal/git`
- [ ] 2.6 Answer each of R9's four cleanliness conditions separately, and report undetermined when git cannot say — `internal/git` · R9
  - Verification: one test per condition, including "No remote at all" and "Cleanliness cannot be established"

## 3. tmux adapter

- [ ] 3.1 Create the `tmux` adapter over `execx`: start a detached session running a command in a directory — `internal/tmux` · R5
  - Verification: test against a fake tmux asserting the argv, plus archtest confirming the new adapter imports no other adapter
- [ ] 3.2 Report a missing tmux as unavailable rather than as a generic failure — `internal/tmux` · R7
  - Verification: test for scenario "No session substrate" of R7
- [ ] 3.3 Report whether a session exists, so a launch can tell it succeeded — `internal/tmux` · R5, R8
  - Verification: `go test ./internal/tmux`

## 4. Agent start

- [ ] 4.1 Extend the `claude` adapter to produce the command that starts an agent in a directory — `internal/claude` · R5
  - Verification: `go test ./internal/claude`
- [ ] 4.2 Report a missing agent tool as unavailable, reusing the treatment discovery already gives it — `internal/claude` · R7
  - Verification: test for scenario "No session substrate" of R7 applied to the agent tool

## 5. Ownership record

- [ ] 5.1 Add the worktree-ownership table by migration, leaving an older store readable — `internal/sqlite` · R10
  - Verification: test that an existing database migrates forward and that the empty table means musem owns nothing
- [ ] 5.2 Record a created worktree and read it back by session — `internal/sqlite` · R10
  - Verification: test for scenario "The record survives a restart" of R10
- [ ] 5.3 Forget a worktree once it has been removed — `internal/sqlite` · R9
  - Verification: `go test ./internal/sqlite`

## 6. The launch sequence

- [ ] 6.1 Declare in `launch` the interfaces it needs from git, tmux and the agent, defined by what the package needs — `internal/launch` · R5
  - Verification: archtest confirms `launch` imports no adapter
- [ ] 6.2 Order the steps so each failure leaves the previous state intact, and record the worktree before the session is started — `internal/launch` · R5, R8, R10
  - Verification: happy-path test with doubles
- [ ] 6.3 Undo what was created when a later step fails — `internal/launch` · R8
  - Verification: test for scenario "The session fails after the worktree exists" of R8, with a failure injected at each step
- [ ] 6.4 Report a cleanup that itself failed, naming what remains on disk — `internal/launch` · R8
  - Verification: test for scenario "Undo that cannot complete" of R8
- [ ] 6.5 Validate a prospective launch without performing it, for the form to consult — `internal/launch` · R2, R3, R4
  - Verification: `go test ./internal/launch`
- [ ] 6.6 Derive the worktree destination from repository and branch — `internal/launch` · R4
  - Verification: test for scenario "Seeing where it will go" of R4

## 7. Reclamation

- [ ] 7.1 Reclaim on the registry's end-of-session transition rather than by polling — `internal/launch` · R9
  - Verification: test asserting a session ended by a degraded discovery pass is never a reclamation candidate
- [ ] 7.2 Remove a clean worktree and keep a dirty one with its reason — `internal/launch` · R9
  - Verification: tests for scenarios "Clean worktree", "Uncommitted work" and "Committed but never pushed" of R9
- [ ] 7.3 Refuse to remove a worktree with no ownership record — `internal/launch` · R10
  - Verification: test for scenario "A worktree musem did not create" of R10
- [ ] 7.4 Surface why a worktree was kept — `internal/app`, `internal/tui` · R9
  - Verification: test that the reason reaches the view

## 8. The launch form

- [ ] 8.1 Open the form from the dashboard and abandon it without creating anything — `internal/tui` · R1
  - Verification: tests for both scenarios of R1
- [ ] 8.2 Editable working directory and worktree toggle enabled by default — `internal/tui` · R2
  - Verification: tests for scenarios "The default is a worktree" and "Launching without a worktree" of R2
- [ ] 8.3 Propose a branch name and allow picking an existing one — `internal/tui` · R3
  - Verification: tests for scenarios "The proposed branch" and "Reusing an existing branch" of R3
- [ ] 8.4 Show the derived destination, and the refusals from validation, inside the form — `internal/tui` · R2, R3, R4
  - Verification: tests for the refusal scenarios of R2, R3 and R4
- [ ] 8.5 Run the launch off the UI goroutine and report progress and failure as typed messages — `internal/tui` · R6, R7
  - Verification: test for scenario "A slow creation" of R6, plus `make race`
- [ ] 8.6 Keep the form legible in a narrow terminal, under the discipline R18 already imposes — `internal/tui` · R6
  - Verification: narrow-terminal test for the form

## 9. Boundaries

- [ ] 9.1 Extend archtest to the new packages: `tmux` is an adapter, `launch` is orchestration, and the `tui` rule forbidding `os` and `os/exec` still holds — `internal/archtest` · infra
  - Verification: each new rule made to fail by injecting the violation it forbids, then restored
- [ ] 9.2 Assert that nothing outside musem's own creations is ever removed — `internal/launch` · R11
  - Verification: test for scenario "Someone else's repository" of R11
- [ ] 9.3 Assert that no key reachable by navigation mutates anything — `internal/tui` · R11
  - Verification: test for scenario "Navigating changes nothing" of R11, replacing the R16 test it supersedes

## 10. Wrap-up

- [ ] 10.1 End-to-end with a real repository: launch with and without a worktree, confirm discovery and branch, close musem and confirm the session survives · R5
  - Verification: manual run
- [ ] 10.2 End-to-end reclamation: end sessions with clean, dirty and unpushed worktrees and confirm which survive · R9
  - Verification: manual run
- [ ] 10.3 Document launching in the README, including that tmux is required for it · infra
  - Verification: a reader can launch a session from the README alone

## Final validation

- [ ] Every requirement in the spec has code and a test
- [ ] `make vet`
- [ ] `make lint`
- [ ] `make test`
- [ ] `make race`
- [ ] `make build`
