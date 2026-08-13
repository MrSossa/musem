# Spec delta — add-session-launch

**Capabilities**: `session-launch` (new), `session-dashboard` (modified)

`session-launch` is where creating and reclaiming live. `session-dashboard` is
touched only to retire R16, whose read-only guarantee this change deliberately
ends.

## ADDED Requirements

### R1 · Launching is reachable from the dashboard

**Capability**: `session-launch`

The system SHALL let the user start a new agent session from within the
dashboard, without leaving it, and SHALL show what will be created before
creating it.

#### Scenario: Opening the form

- **GIVEN** the dashboard is open
- **WHEN** the user asks to launch a session
- **THEN** a form appears with the working directory filled in and editable

#### Scenario: Nothing happens until it is confirmed

- **GIVEN** the launch form open with values filled in
- **WHEN** the user abandons it instead of confirming
- **THEN** no worktree, branch or session has been created

### R2 · Working directory and worktree are the user's to choose

**Capability**: `session-launch`

The form SHALL offer an editable working directory and a worktree toggle. The
toggle SHALL default to enabled. With it disabled the session SHALL start
directly in the given directory and no worktree SHALL be created.

#### Scenario: The default is a worktree

- **GIVEN** the user opens the launch form
- **WHEN** they have changed nothing
- **THEN** the worktree toggle is already enabled

#### Scenario: Launching without a worktree

- **GIVEN** the launch form with the worktree toggle disabled
- **WHEN** the user confirms
- **THEN** the session starts in the directory as given, and no worktree exists
  that did not exist before

#### Scenario: A directory that is not a repository

- **GIVEN** the worktree toggle enabled
- **WHEN** the given directory is not inside a git repository
- **THEN** the form says so and refuses to launch until the toggle is disabled or
  the directory is changed

### R3 · The branch is proposed and can be replaced

**Capability**: `session-launch`

With the worktree enabled, the system SHALL propose a new branch name and SHALL
let the user replace it, either by editing the name or by choosing a branch that
already exists.

#### Scenario: The proposed branch

- **GIVEN** the launch form with the worktree enabled
- **WHEN** the user has not chosen otherwise
- **THEN** a new branch name is proposed, and confirming creates that branch

#### Scenario: Reusing an existing branch

- **GIVEN** a repository with branches that already exist
- **WHEN** the user picks one instead of the proposed name
- **THEN** the worktree checks out that branch rather than creating one

#### Scenario: A branch already checked out elsewhere

- **GIVEN** a branch that another worktree already has checked out
- **WHEN** the user picks it
- **THEN** the form says so and refuses to launch, because git permits one
  checkout of a branch at a time

### R4 · The destination is derived and visible

**Capability**: `session-launch`

The system SHALL derive the worktree's location from the repository and the
branch, and SHALL show it before creation. A destination that already exists
SHALL NOT be written into.

#### Scenario: Seeing where it will go

- **GIVEN** the launch form with a repository and a branch
- **WHEN** the user reads the form
- **THEN** the path the worktree will occupy is shown

#### Scenario: Occupied destination

- **GIVEN** a derived destination that already exists on disk
- **WHEN** the user confirms
- **THEN** the launch is refused with the path named, and what is there is left
  untouched

### R5 · Launching starts an observable session

**Capability**: `session-launch`

A confirmed launch SHALL create the worktree when enabled, start the agent
session in the resulting directory, and the session SHALL then be discoverable by
the same inventory that observes every other session.

#### Scenario: The session joins the inventory

- **GIVEN** a confirmed launch
- **WHEN** the next discovery cycle runs
- **THEN** the new session appears with its working directory and its branch

#### Scenario: The session survives musem

- **GIVEN** a session launched from musem
- **WHEN** musem is closed
- **THEN** the session keeps running and is still there when musem is reopened

### R6 · The dashboard stays usable while a launch is in flight

**Capability**: `session-launch`

Creating a worktree and starting a session SHALL NOT freeze the interface, and
the user SHALL be able to tell that the launch is in progress.

#### Scenario: A slow creation

- **GIVEN** a repository large enough that creating a worktree takes seconds
- **WHEN** the user confirms the launch
- **THEN** the dashboard keeps refreshing and drawing, and shows the launch as in
  progress

### R7 · A launch that cannot proceed explains itself

**Capability**: `session-launch`

When a launch cannot be completed — the substrate or the agent tool is missing,
git refuses, the destination is occupied — the system SHALL report which step
failed and why, and SHALL remain usable.

#### Scenario: No session substrate

- **GIVEN** a machine without the tooling musem uses to host a session
- **WHEN** the user confirms a launch
- **THEN** the failure names what is missing, and the dashboard stays up

### R8 · A failed launch leaves nothing half-created

**Capability**: `session-launch`

If a launch fails after something has already been created, the system SHALL undo
what it created, and SHALL NOT leave a worktree without a session or a session
without its agent.

#### Scenario: The session fails after the worktree exists

- **GIVEN** a launch whose worktree was created
- **WHEN** starting the session then fails
- **THEN** the worktree musem just created is removed, and the repository is left
  as it was before the launch

#### Scenario: Undo that cannot complete

- **GIVEN** a failed launch whose cleanup also fails
- **WHEN** the system gives up
- **THEN** it reports what remains on disk and where, rather than claiming a
  clean rollback

### R9 · A worktree is reclaimed only when it is clean

**Capability**: `session-launch`

When a session ends, the system SHALL remove the worktree it created for that
session only if the worktree is clean. Clean means all of: no uncommitted
changes, no untracked files, no commits absent from the branch's remote, and no
stashed entries. If any condition is unmet, or cannot be determined, the worktree
SHALL be kept.

#### Scenario: Clean worktree

- **GIVEN** a session whose worktree has everything committed and pushed
- **WHEN** the session ends
- **THEN** the worktree is removed

#### Scenario: Uncommitted work

- **GIVEN** a session whose worktree has modified or untracked files
- **WHEN** the session ends
- **THEN** the worktree is kept, and the dashboard says why it was kept

#### Scenario: Committed but never pushed

- **GIVEN** a worktree whose working tree is clean but whose branch has commits
  the remote does not have
- **WHEN** the session ends
- **THEN** the worktree is kept: a commit that exists in one place only is work
  that deleting would destroy

#### Scenario: No remote at all

- **GIVEN** a worktree on a branch that tracks no remote
- **WHEN** the session ends
- **THEN** the worktree is kept, because there is nowhere its commits could have
  been preserved

#### Scenario: Cleanliness cannot be established

- **GIVEN** a worktree whose state git cannot report
- **WHEN** the session ends
- **THEN** the worktree is kept, and the failure to determine its state is
  reported rather than treated as clean

### R10 · Only what musem created is ever removed

**Capability**: `session-launch`

The system SHALL record which worktrees it created and SHALL only ever consider
those for removal. A worktree musem did not create SHALL NOT be removed under any
condition, however clean it is.

#### Scenario: A worktree musem did not create

- **GIVEN** a session working in a worktree the user made by hand
- **WHEN** that session ends and the worktree is clean
- **THEN** the worktree is left alone

#### Scenario: The record survives a restart

- **GIVEN** worktrees created by musem in an earlier run
- **WHEN** musem is closed and reopened and one of those sessions ends
- **THEN** it is still recognised as musem's own and handled by R9

## MODIFIED Requirements

### R11 · Destructive actions are narrow, recorded and explained

**Capability**: `session-dashboard`

Replaces R16, whose read-only guarantee this change ends.

The dashboard MAY create and remove only what this specification permits: the
worktrees it makes for the sessions it launches. Every other object the user is
observing — sessions musem did not launch, repositories, working directories,
files outside musem's own store and worktrees musem did not create — SHALL remain
untouched. No action reachable by navigating the interface SHALL modify anything;
mutation SHALL follow only from an action the user asked for explicitly.

#### Scenario: Navigating changes nothing

- **GIVEN** the dashboard open with live sessions
- **WHEN** the user moves through the list, opens details and reads help
- **THEN** nothing on disk and no session has changed

#### Scenario: Someone else's repository

- **GIVEN** a session observed in a repository musem never launched into
- **WHEN** that session ends, however it ends
- **THEN** that repository and its worktrees are exactly as they were

## REMOVED Requirements

### R16 · Read only

**Capability**: `session-dashboard`

Removed because this change is the one that ends it. R16 was a first-phase
constraint that forced observation to be solved before acting on what is
observed, and it did its job. What is worth keeping from it — navigation never
mutates, and nothing outside musem's own creations is ever touched — is now
stated as R11, which is narrower and enforceable alongside launching.
