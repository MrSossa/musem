# session-launch

Starting a session from inside musem, and taking back what starting one created:
the launch form and what it will and will not accept, the worktree derived for a
session, the ordering that keeps a failed launch from leaving anything behind,
and the conditions under which a worktree is reclaimed when its session ends.

This is where musem stops being read-only. What it may create and remove is
deliberately narrow — the worktrees it makes for the sessions it launches, and
nothing else — and the record of what it created is the precondition for
removing any of it.

Requirement ids are those the change that introduced them used, so the review
record stays traceable; they are not contiguous. Where a change proposed an id
already in force elsewhere, the id it proposed is kept in parentheses.

## R21 · Launching is reachable from the dashboard (R1)

The system SHALL let the user start a new agent session from within the
dashboard, without leaving it, and SHALL show what will be created before
creating it.

### Scenario: Opening the form

- **GIVEN** the dashboard is open
- **WHEN** the user asks to launch a session
- **THEN** a form appears with the working directory filled in and editable

### Scenario: Nothing happens until it is confirmed

- **GIVEN** the launch form open with values filled in
- **WHEN** the user abandons it instead of confirming
- **THEN** no worktree, branch or session has been created

## R22 · Working directory and worktree are the user's to choose (R2)

The form SHALL offer an editable working directory and a worktree toggle. The
toggle SHALL default to enabled. With it disabled the session SHALL start
directly in the given directory and no worktree SHALL be created.

### Scenario: The default is a worktree

- **GIVEN** the user opens the launch form
- **WHEN** they have changed nothing
- **THEN** the worktree toggle is already enabled

### Scenario: Launching without a worktree

- **GIVEN** the launch form with the worktree toggle disabled
- **WHEN** the user confirms
- **THEN** the session starts in the directory as given, and no worktree exists
  that did not exist before

### Scenario: A directory that is not a repository

- **GIVEN** the worktree toggle enabled
- **WHEN** the given directory is not inside a git repository
- **THEN** the form says so and refuses to launch until the toggle is disabled or
  the directory is changed

## R23 · The branch is proposed and can be replaced (R3)

With the worktree enabled, the system SHALL propose a new branch name and SHALL
let the user replace it, either by editing the name or by choosing a branch that
already exists.

### Scenario: The proposed branch

- **GIVEN** the launch form with the worktree enabled
- **WHEN** the user has not chosen otherwise
- **THEN** a new branch name is proposed, and confirming creates that branch

### Scenario: Reusing an existing branch

- **GIVEN** a repository with branches that already exist
- **WHEN** the user picks one instead of the proposed name
- **THEN** the worktree checks out that branch rather than creating one

### Scenario: A branch already checked out elsewhere

- **GIVEN** a branch that another worktree already has checked out
- **WHEN** the user picks it
- **THEN** the form says so and refuses to launch, because git permits one
  checkout of a branch at a time

## R24 · The destination is derived and visible (R4)

The system SHALL derive the worktree's location from the repository and the
branch, and SHALL show it before creation. A destination that already exists
SHALL NOT be written into.

### Scenario: Seeing where it will go

- **GIVEN** the launch form with a repository and a branch
- **WHEN** the user reads the form
- **THEN** the path the worktree will occupy is shown

### Scenario: Occupied destination

- **GIVEN** a derived destination that already exists on disk
- **WHEN** the user confirms
- **THEN** the launch is refused with the path named, and what is there is left
  untouched

## R25 · Launching starts an observable session (R5)

A confirmed launch SHALL create the worktree when enabled, start the agent
session in the resulting directory, and the session SHALL then be discoverable by
the same inventory that observes every other session, under the identifier the
launch started it with.

Discovery observes a session once the agent is running. An agent that asks the
user to confirm access before it starts is hosted and waiting, not lost: it holds
its place until the question is answered and is then observed like any other.

### Scenario: The session joins the inventory

- **GIVEN** a confirmed launch whose agent started unattended
- **WHEN** the next discovery cycle runs
- **THEN** the new session appears with its working directory and its branch

### Scenario: The agent asks before it starts

- **GIVEN** a launch whose agent asks the user to confirm access to the directory
- **WHEN** nobody has answered it yet
- **THEN** the session is hosted and waiting rather than lost, and is observed
  under the identifier the launch chose once the question is answered

### Scenario: The session survives musem

- **GIVEN** a session launched from musem
- **WHEN** musem is closed
- **THEN** the session keeps running and is still there when musem is reopened

## R26 · The dashboard stays usable while a launch is in flight (R6)

Creating a worktree and starting a session SHALL NOT freeze the interface, and
the user SHALL be able to tell that the launch is in progress.

### Scenario: A slow creation

- **GIVEN** a repository large enough that creating a worktree takes seconds
- **WHEN** the user confirms the launch
- **THEN** the dashboard keeps refreshing and drawing, and shows the launch as in
  progress

## R27 · A launch that cannot proceed explains itself (R7)

When a launch cannot be completed — the substrate or the agent tool is missing,
git refuses, the destination is occupied — the system SHALL report which step
failed and why, and SHALL remain usable.

### Scenario: No session substrate

- **GIVEN** a machine without the tooling musem uses to host a session
- **WHEN** the user confirms a launch
- **THEN** the failure names what is missing, and the dashboard stays up

## R28 · A failed launch leaves nothing half-created (R8)

If a launch fails after something has already been created, the system SHALL undo
what it created, and SHALL NOT leave a worktree without a session or a session
without its agent.

### Scenario: The session fails after the worktree exists

- **GIVEN** a launch whose worktree was created
- **WHEN** starting the session then fails
- **THEN** the worktree musem just created is removed, and the repository is left
  as it was before the launch

### Scenario: Undo that cannot complete

- **GIVEN** a failed launch whose cleanup also fails
- **WHEN** the system gives up
- **THEN** it reports what remains on disk and where, rather than claiming a
  clean rollback

## R29 · A worktree is reclaimed only when it is clean (R9)

When a session ends, the system SHALL remove the worktree it created for that
session only if the worktree is clean. Clean means all of: no uncommitted
changes, no untracked files, no commits absent from the branch's remote, and no
stashed entries. If any condition is unmet, or cannot be determined, the worktree
SHALL be kept.

### Scenario: Clean worktree

- **GIVEN** a session whose worktree has everything committed and pushed
- **WHEN** the session ends
- **THEN** the worktree is removed

### Scenario: Uncommitted work

- **GIVEN** a session whose worktree has modified or untracked files
- **WHEN** the session ends
- **THEN** the worktree is kept, and the dashboard says why it was kept

### Scenario: Committed but never pushed

- **GIVEN** a worktree whose working tree is clean but whose branch has commits
  the remote does not have
- **WHEN** the session ends
- **THEN** the worktree is kept: a commit that exists in one place only is work
  that deleting would destroy

### Scenario: No remote at all

- **GIVEN** a worktree on a branch that tracks no remote
- **WHEN** the session ends
- **THEN** the worktree is kept, because there is nowhere its commits could have
  been preserved

### Scenario: Cleanliness cannot be established

- **GIVEN** a worktree whose state git cannot report
- **WHEN** the session ends
- **THEN** the worktree is kept, and the failure to determine its state is
  reported rather than treated as clean

## R30 · Only what musem created is ever removed (R10)

The system SHALL record which worktrees it created and SHALL only ever consider
those for removal. A worktree musem did not create SHALL NOT be removed under any
condition, however clean it is.

### Scenario: A worktree musem did not create

- **GIVEN** a session working in a worktree the user made by hand
- **WHEN** that session ends and the worktree is clean
- **THEN** the worktree is left alone

### Scenario: The record survives a restart

- **GIVEN** worktrees created by musem in an earlier run
- **WHEN** musem is closed and reopened and one of those sessions ends
- **THEN** it is still recognised as musem's own and handled by R29

## R32 · A launch says what it started

When a launch succeeds, the system SHALL report what it started: the name the
session is hosted under, the directory it is working in, the branch when the
launch created one, and how the user gets a terminal in it. It SHALL NOT return
to the dashboard without saying anything.

### Scenario: A session in a worktree

- **GIVEN** a confirmed launch with the worktree enabled
- **WHEN** it succeeds
- **THEN** the user is shown the session's name, the worktree it is working in,
  the branch it is on, and the command that attaches to it

### Scenario: A session without a worktree

- **GIVEN** a confirmed launch with the worktree disabled
- **WHEN** it succeeds
- **THEN** the user is shown the session's name, the directory as they gave it,
  and the command that attaches to it, with no worktree named

### Scenario: A launch that failed

- **GIVEN** a launch that could not be completed
- **WHEN** the failure is reported
- **THEN** nothing is announced as started, because nothing was

## R33 · The report stands until the session is in the inventory

The report SHALL remain visible while the session it names is absent from the
inventory, and SHALL say that the session has not appeared yet. It SHALL be
withdrawn, without the user acting, once the session is observed.

A session can be absent for a while and be perfectly healthy: discovery runs on
an interval, and an agent that asks for confirmation before it starts is running
and waiting rather than failing. The report exists so that interval, and that
wait, are legible instead of looking like nothing having happened.

### Scenario: The session appears

- **GIVEN** a reported launch whose session is not yet in the inventory
- **WHEN** a discovery pass observes it
- **THEN** the report is withdrawn and the session is a row like any other

### Scenario: The session has not appeared yet

- **GIVEN** a reported launch whose session is not yet in the inventory
- **WHEN** the user reads the dashboard
- **THEN** the report says the session has not appeared yet, and still says how
  to reach it

### Scenario: The agent is waiting to be let in

- **GIVEN** a launch whose agent is waiting for the user to confirm access before
  it starts
- **WHEN** the user reads the dashboard
- **THEN** the session reads as started and waiting, with the way to reach it,
  rather than as missing

### Scenario: More launches than there is room for

- **GIVEN** several launches reported and none of their sessions observed yet
- **WHEN** the dashboard is drawn
- **THEN** the ones that do not fit are accounted for by a count rather than
  drawn, and the dashboard stays within the terminal
