# Say what a launch started

**Slug**: `report-launched-session`
**Created**: 2026-08-13

## Why

Launching works and says nothing. The form takes the confirmation, creates the
worktree, starts the session, writes the ownership record — and then closes,
leaving the user on a dashboard that looks exactly as it did before they pressed
`n`. Everything went right and the screen is the evidence that nothing happened.

The gap has two causes and both are ordinary. Discovery runs on its own interval,
so even a perfect launch takes a few seconds to show up. And when the worktree
lands somewhere the agent tool has not been trusted with, the CLI opens with a
confirmation prompt inside the tmux pane and waits there — verified to wait
indefinitely without expiring, and to register under the session id musem chose
the moment it is answered. In both cases the session is running and healthy; it
simply is not in the inventory yet, and musem gives no sign that it exists or
that it is waiting for anything.

The worst version of this is the second one, because the user has no way to guess
it. Nothing on screen mentions a tmux session, so nothing suggests there is a
pane to attach to, so the question sits unanswered for as long as it takes them
to work it out from first principles.

## What

After a launch succeeds, musem says what it started: the session's tmux name, the
directory it is working in, the branch when there is one, and the command that
gets the user a terminal in it. That statement stays on the dashboard until the
session turns up in the inventory, and says plainly that it has not turned up
yet — so a session waiting on a confirmation reads as waiting rather than as
missing.

The living spec is corrected in the same change. R25 currently promises that a
confirmed launch appears in the inventory on the next discovery cycle. That is
true only when the agent starts unattended, and the specification should say what
the system does rather than what was hoped for it.

## Scope

**In**:
- A statement, after a successful launch, naming the tmux session, the working
  directory, the branch and how to attach.
- That statement persisting until the session is observed in the inventory, and
  saying while it persists that the session has not appeared yet.
- Correcting R25 so the inventory scenario matches observed behaviour, and adding
  the case where the agent is waiting to be let in.

**Out**:
- Answering the agent tool's confirmation prompt on the user's behalf, by writing
  its configuration or any other route. That was considered and rejected: it
  means musem deciding trust for the user, in another tool's file, which that
  tool rewrites while sessions are live. The prompt waits indefinitely, so the
  cost of leaving it alone is a few seconds of the user's attention.
- Attaching to the session from inside musem, or embedding its pane. Launching
  hands off to tmux; attaching stays the user's business, as it was when
  launching was proposed.
- Reading the agent tool's configuration to predict whether a prompt will appear.
  It would let the notice be more specific, and it makes musem depend on the
  private layout of a file that belongs to somebody else. The notice is worded to
  be true either way instead.
- Any change to what a launch creates, or to reclamation.

## Acceptance criteria

The change is done when:

- [ ] After a successful launch the user is shown the tmux session name, the
      working directory, the branch when there is one, and the command that
      attaches to it (R32).
- [ ] A launch with the worktree toggle off is reported the same way, naming the
      directory it started in and no worktree (R32).
- [ ] The statement stays on screen while the session is absent from the
      inventory, and says it has not appeared yet (R33).
- [ ] The statement goes once the session appears in the inventory, without the
      user doing anything (R33).
- [ ] A session whose agent is waiting on a confirmation reads as waiting, with
      the way to reach it, rather than as missing (R33).
- [ ] R25 in `.ktools/specs/session-launch/` describes what the system does,
      including the case where the agent asks before it starts (R25).
- [ ] `make build`, `make test`, `make lint`, `make vet` and `make race` pass.

## Risks and assumptions

| Type | Detail | Mitigation |
| --- | --- | --- |
| Risk | The notice occupies header rows the table is budgeted against, and a wrapped line costs the fleet total — the failure the header discipline already exists to prevent | It is drawn under the same rules as the kept-worktree notices beside it: clipped to the terminal width, bounded in number, counted past the bound. Covered by the narrow-terminal tests |
| Risk | A launch that never appears pins a line forever | That is the intended reading: a launch that never showed up is exactly what the user needs telling about. Several are bounded by the same count-past-the-bound rule as the notices beside them |
| Risk | The notice and the session both appear, briefly showing the same session twice | The notice clears on the first snapshot containing the session, so the overlap is one frame at most, and it says "waiting" rather than repeating the row |
| Assumption | The session id musem launched with is the one discovery reports, so the notice can tell whether its session has arrived | Verified by hand on 2026-08-13: a session started with `--session-id`, including one that first waited on a confirmation prompt, registered under exactly that id |
| Assumption | A user who is told the tmux session name can attach to it | `tmux attach -t <name>` is the whole of it, and the README documents it since the launch change |
