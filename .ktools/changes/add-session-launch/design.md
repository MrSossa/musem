# Technical design — add-session-launch

## Technical context

The observation half is built and in force under `.ktools/specs/`. This change
adds the first write path, which touches every layer:

```
musem.go                   domain: gains the launch request and its outcome
internal/
├── claude/                adapter: knows how to start the agent, not only read it
├── git/                   adapter: today one question (Branch); gains worktrees
├── tmux/                  adapter: NEW, the session substrate
├── sqlite/                adapter: gains the record of worktrees musem created
├── safetext/, execx/      helpers: reused as they are
├── registry/, cost/       orchestration: unchanged
├── launch/                orchestration: NEW, owns the sequence and its rollback
├── app/                   composer: exposes launching to the view
└── tui/                   the launch form
```

Three constraints from the existing code shape everything below.

`internal/tui` may not import `os` or `os/exec`: `TestDashboardHasNoMutatingOperations`
parses the package's sources and fails on either, plus on calls named `Create`,
`Remove`, `RemoveAll`, `WriteFile` or `Kill`. That test was written for R16, and
R16 is being removed — but the rule it enforces is worth keeping for a reason
that survives R16: the view must not be able to touch the disk directly, whatever
it is allowed to cause indirectly.

`internal/archtest` asserts that adapters import neither each other nor
orchestration, that the root package imports only the standard library, and that
nothing in the graph can reach the network. A new adapter and a new orchestration
package must fit that, not amend it.

Every event source runs in its own goroutine and reaches the model through
`Program.Send`; nothing touches the model outside `Update`. A launch takes
seconds, so it is an event source like any other.

## Decisions

### D1 · tmux gets its own adapter, named after what it wraps

**Chosen**: `internal/tmux`, using `execx` exactly as `claude` and `git` do, with
one job — create a detached session running a command in a directory, and report
whether it exists.

**Rejected alternatives**: starting the agent as a bare child process of musem.
It would die with musem, and R5 requires the opposite. Also rejected: putting
tmux calls inside `claude`, which would make one adapter wrap two foreign things
and break the naming rule that makes "does this belong here?" answer itself.

**Consequences**: a third shelling adapter, and the `execx` extraction pays off a
second time. tmux becomes a hard runtime dependency of launching — absent tmux,
launching fails and observing still works, which is the degradation R7 asks for.

**Requirements it supports**: R5, R7

### D2 · The git adapter grows worktree operations rather than a second adapter

**Chosen**: `internal/git` gains worktree creation, listing, removal and the
cleanliness query, beside the branch resolution it already does.

**Rejected alternatives**: a separate `worktree` adapter. It wraps the same
foreign thing — the git binary — and splitting by verb rather than by wrapped
system would put two adapters in a position to disagree about the same tool.

**Consequences**: `git` stops being a one-question adapter and becomes the
largest. That is the right trade against two packages shelling out to the same
binary with two notions of how to parse it.

**Requirements it supports**: R2, R3, R4, R8, R9

### D3 · Cleanliness is four questions, and any unanswered one means dirty

**Chosen**: R9's four conditions are checked separately — working tree, untracked
files, unpushed commits, stashes — and the result is a value that distinguishes
clean, dirty-for-a-named-reason, and undetermined. Undetermined is treated as
dirty at the call site.

**Rejected alternatives**: `git status --porcelain` alone, which answers two of
the four and silently reports clean for a branch whose commits exist nowhere
else. That is the failure that loses work, and it is the one a naive check
invites.

**Consequences**: reclamation is conservative by construction; worktrees will
sometimes survive that could have gone. That asymmetry is deliberate — a worktree
kept costs disk, a worktree removed costs work.

**Requirements it supports**: R9

### D4 · Only worktrees musem recorded are candidates, and the record is persisted

**Chosen**: a table in the existing SQLite store, written when a worktree is
created and read before any removal, keyed by the session it was made for. It is
a fact about what musem did, so it has to outlive the process that did it.

**Rejected alternatives**: inferring ownership from the path shape, which makes a
naming convention load-bearing for a destructive decision — rename a directory
and musem either forgets its own worktree or adopts somebody else's. Also
rejected: keeping the record in memory, which would make every restart a licence
to forget, and R10 requires the opposite.

**Consequences**: a schema migration, which the store already supports and tests.
Removal has a hard precondition that a bug in path handling cannot bypass.

**Requirements it supports**: R9, R10

### D5 · A `launch` orchestration package owns the sequence and its rollback

**Chosen**: `internal/launch` composes the git, tmux and claude adapters, ordering
the steps so each one's failure leaves the previous state intact, and undoing what
it created when a later step fails.

**Rejected alternatives**: driving the sequence from `app`, which is a composer
and holds no behaviour of its own; or from `tui`, which would put a multi-step
process with rollback inside the view. Both would also force the view to know
which adapter fails how.

**Consequences**: a second orchestration package beside `registry` and `cost`,
declaring the interfaces it needs the same way they do. Rollback lives in one
readable place instead of being spread across the call sites that can fail.

**Requirements it supports**: R5, R7, R8

### D6 · The form collects, the pump launches

**Chosen**: `tui` owns the form's state and validation and emits a request; the
launch runs in its own goroutine and reports back through `Program.Send` as a
typed message, like every other event source.

**Rejected alternatives**: launching synchronously from `Update`, which freezes
the interface for the length of a `git worktree add` and violates R6. Also
rejected: relaxing the archtest rule so the view can shell out — the rule is why
the view cannot grow a write path by accident, and the indirection it forces
costs one message type.

**Consequences**: the launch is observable as in-progress, failed or done, which
is what R6 needs anyway. The view keeps its property of being unable to touch
anything directly.

**Requirements it supports**: R1, R6, R7

### D7 · Validation that needs the disk happens before confirmation, not inside it

**Chosen**: the checks R2, R3 and R4 impose — is this a repository, is the branch
checked out elsewhere, is the destination occupied — run as the form is filled in
and their answers are shown there, rather than surfacing as a failed launch.

**Rejected alternatives**: validating only on confirm. It is simpler and it makes
every mistake cost a round trip through a destructive operation that then has to
roll back.

**Consequences**: the form talks to the launch package while it is open, so those
queries must be cheap and cancellable. It also means a launch that passed
validation can still fail, because the disk can change in between — which is why
R8 exists regardless.

**Requirements it supports**: R2, R3, R4

### D8 · Reclamation is triggered by the registry's existing end-of-session signal

**Chosen**: R5 of `session-registry` already marks a session ended exactly once,
and only on a discovery pass that could read everything it found. Reclamation
listens to that transition rather than polling for dead sessions.

**Rejected alternatives**: a timer sweeping worktrees. It would re-derive a fact
the registry already establishes, and would have to reimplement the degradation
guard that keeps an unreadable pass from looking like a machine with nothing
running — the exact bug the last review round closed.

**Consequences**: reclamation inherits that guard for free: a session that only
appears to have ended, because a discovery pass could not read its record, is
never a candidate for having its worktree deleted. This is load-bearing and worth
stating, because it is the path by which a parsing change could otherwise have
deleted a running session's work.

**Requirements it supports**: R9, R10

## Impact

| Area | Impact |
| --- | --- |
| Data schema | One new table for worktrees musem created, added by the migration mechanism `internal/sqlite` already has and tests. Backward compatible: an older store migrates forward, and the table being empty means musem owns nothing and removes nothing |
| Public API | None. musem is a binary, and the CLI gains no flag |
| Security | The first write path. Mitigated by D4 (removal needs a recorded creation), D3 (undetermined is dirty) and R11 (nothing outside musem's own creations). Paths entered by the user reach git as argv, never a shell, as `git.go` already does for the directory it passes after `-C`. Branch names supplied by the user are foreign text on the way back out and go through `safetext` like every other rendered value |
| Performance | Worktree creation is seconds on a large repository and runs off the UI goroutine (D6). The form's validation queries (D7) run per keystroke at worst and must be debounced or cancelled, or they will fight the refresh loop for the git binary |
| Dependencies | tmux, at runtime, for launching only. No new Go module: the form uses `bubbles/textinput`, already an indirect dependency of the TUI stack, and everything else shells out through `execx` |

## Test plan

- **Architecture tests** (`internal/archtest`): the new `tmux` adapter imports no
  other adapter and no orchestration; `launch` imports no adapter; the root
  package still imports only the standard library; nothing reaches the network.
  The `tui` rule forbidding `os` and `os/exec` stays, and is what proves D6.
- **Adapter tests with fake binaries** (`internal/git`, `internal/tmux`): the
  pattern `git_test.go` already uses — a script standing in for the real binary —
  extended to worktree creation, removal and each cleanliness condition. The four
  conditions of R9 get one test each, including the no-remote case and the case
  where git fails to answer.
- **Launch sequence tests** (`internal/launch`) with doubles for git, tmux and
  the agent: the happy path, and a failure injected at each step asserting both
  that the earlier steps were undone and that a cleanup which itself fails is
  reported rather than swallowed (R8).
- **Ownership tests** (`internal/sqlite`, `internal/launch`): a worktree musem
  did not record is never a removal candidate, and the record survives a restart
  (R10).
- **TUI tests**: the toggle defaults to enabled, the form validates and refuses,
  the derived path is shown, abandoning creates nothing, a launch in flight does
  not block rendering, and the form stays legible in a narrow terminal under the
  same discipline R18 imposes.
- **Manual end-to-end**: launch into a real repository with and without a
  worktree, confirm the session is discovered with its branch, close musem and
  confirm the session survives, then end sessions with clean and dirty worktrees
  and confirm which are reclaimed.

## Open questions

- Where derived worktrees live by default — a sibling of the repository, or a
  configured root. Sibling is the assumption; making it configurable later is
  additive and changes no requirement.
- Whether reclamation should also offer to delete the branch it created when the
  branch has no commits of its own. Deliberately out of this change: it is a
  second destructive decision and deserves its own evidence.
