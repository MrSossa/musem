# Launch sessions into worktrees

**Slug**: `add-session-launch`
**Created**: 2026-08-12

## Why

musem can see every agent session on the machine and say nothing to any of them.
Starting one still means leaving the dashboard, finding the repository, deciding
where to put a worktree, creating it, opening a terminal and running the agent
there — six steps outside the tool that already knows five of the six answers.

The worktree is the part worth automating rather than the terminal. Running two
agents in one checkout means they fight over the index and over each other's
edits; the discipline that avoids it is one worktree per session, and it is
exactly the discipline people skip when it costs six steps. Making it the default
is the point of this change.

This is also the change where musem stops being read-only, which was a
first-phase constraint rather than a permanent property. It bought the right
thing — observation had to be solved before acting on what is observed — and it
has now been paid for.

## What

From the dashboard the user launches a session. A form appears with the working
directory filled in, editable, and a worktree toggle that is on by default. With
the worktree on, a branch is proposed — new, named after the session — and the
user can instead pick one that already exists. The worktree's location is derived
from the repository and the branch, and shown before anything is created.

Confirming creates the worktree, starts a tmux session in it, launches the agent
there, and returns to the dashboard, where the new session appears in the
inventory like any other.

When a session ends, musem reclaims the worktree it created for it, but only if
that worktree is clean: no uncommitted changes, no untracked files, no commits
the remote has not seen, no stashes. Anything else and it stays, and the
dashboard says why.

## Scope

**In**:
- A launch form in the dashboard: editable working directory, worktree toggle on
  by default, new branch by default with the option of an existing one, and the
  derived worktree path shown before creation.
- Worktree creation through the git binary, and the checks that make it safe:
  the directory is a repository, the branch is not already checked out
  elsewhere, the destination does not exist.
- tmux as the session substrate — creating the session, launching the agent in
  it, and reporting failure without taking the dashboard down.
- Recording which worktrees musem created, so reclamation only ever touches
  those.
- Reclaiming a worktree when its session ends and the worktree is clean, and
  keeping it, with a visible reason, when it is not.

**Out**:
- Embedding the tmux pane inside musem. Launching hands off to tmux; attaching is
  the user's business for now, and embedding is a large enough piece of terminal
  work to deserve its own change.
- Sending input to a running session, stopping or restarting one. This change
  creates and reclaims; it does not drive.
- Removing a worktree that is not clean, by any path. musem never resolves that
  situation for the user.
- Creating repositories, cloning, or fetching. The repository must already exist.
- Agent tools other than Claude Code. The adapter seam stays where it is.

## Acceptance criteria

The change is done when:

- [ ] From the dashboard the user opens a launch form, edits the working
      directory, and sees the worktree toggle already on (R1, R2).
- [ ] With the toggle on, a new branch is proposed and can be replaced by an
      existing one, and the resulting worktree path is visible before anything is
      created (R3, R4).
- [ ] With the toggle off, the session starts in the given directory and no
      worktree is created (R2).
- [ ] Launching creates the worktree, starts the session in it, and the session
      appears in the inventory on the next refresh (R5, R6).
- [ ] A launch that cannot proceed — not a repository, branch already checked
      out, destination occupied, tmux or the agent missing — explains why and
      leaves nothing half-created (R7, R8).
- [ ] A session whose worktree is clean when it ends has that worktree reclaimed;
      one with any uncommitted, untracked, unpushed or stashed work keeps it, and
      the dashboard says which (R9, R10).
- [ ] musem never removes a worktree it did not create (R10).
- [ ] `make build`, `make test`, `make lint`, `make vet` and `make race` pass on
      macOS and Linux with `CGO_ENABLED=0`.

## Risks and assumptions

| Type | Detail | Mitigation |
| --- | --- | --- |
| Risk | Reclamation deletes work. It is the first destructive thing musem does, and the blast radius is a user's uncommitted code | Only worktrees musem recorded as its own are candidates; cleanliness is checked strictly and every ambiguous answer keeps the worktree. The check treats "I could not determine this" as dirty, never as clean |
| Risk | "Clean" is more subtle than `git status` — a branch with commits the remote has never seen looks clean to a naive check and is not | Cleanliness is specified as four separate conditions (R9) and tested one by one, including the case where no remote exists at all |
| Risk | A launch fails halfway, leaving a worktree with no session or a tmux session with no agent | Each step is ordered so that failure leaves the previous state intact, and what was created before the failure is undone (R8) |
| Risk | tmux is a new dependency with no code in the project yet, and its absence is a plausible state on a fresh machine | It gets an adapter of its own, named after what it wraps, and a missing tmux degrades to an explained failure rather than a crash — the same treatment the Claude CLI already gets |
| Risk | The dashboard grows a form, and forms are where a TUI's width and focus handling usually break | Reuse of the width discipline R18 already imposes, plus the launch form is subject to the same narrow-terminal tests |
| Assumption | The user wants one worktree per session, not per branch reused across sessions | The toggle exists precisely because this will not always hold; off is a first-class path, not a fallback |
| Assumption | `git worktree` is available in the git that resolves branches today | It has been in git since 2.5; the version predates every platform this project targets |
| Assumption | Reclaiming on session end is wanted in this change rather than deferred | Chosen deliberately over leaving it out of scope. If it proves noisy in practice, R9 and R10 are separable from the rest |
