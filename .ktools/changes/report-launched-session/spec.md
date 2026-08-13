# Spec delta — report-launched-session

**Capability**: `session-launch`

Requirement ids continue the project-wide numbering; R31 is the highest in force.

## ADDED Requirements

### R32 · A launch says what it started

**Capability**: `session-launch`

When a launch succeeds, the system SHALL report what it started: the name the
session is hosted under, the directory it is working in, the branch when the
launch created one, and how the user gets a terminal in it. It SHALL NOT return
to the dashboard without saying anything.

#### Scenario: A session in a worktree

- **GIVEN** a confirmed launch with the worktree enabled
- **WHEN** it succeeds
- **THEN** the user is shown the session's name, the worktree it is working in,
  the branch it is on, and the command that attaches to it

#### Scenario: A session without a worktree

- **GIVEN** a confirmed launch with the worktree disabled
- **WHEN** it succeeds
- **THEN** the user is shown the session's name, the directory as they gave it,
  and the command that attaches to it, with no worktree named

#### Scenario: A launch that failed

- **GIVEN** a launch that could not be completed
- **WHEN** the failure is reported
- **THEN** nothing is announced as started, because nothing was

### R33 · The report stands until the session is in the inventory

**Capability**: `session-launch`

The report SHALL remain visible while the session it names is absent from the
inventory, and SHALL say that the session has not appeared yet. It SHALL be
withdrawn, without the user acting, once the session is observed.

A session can be absent for a while and be perfectly healthy: discovery runs on
an interval, and an agent that asks for confirmation before it starts is running
and waiting rather than failing. The report exists so that interval, and that
wait, are legible instead of looking like nothing having happened.

#### Scenario: The session appears

- **GIVEN** a reported launch whose session is not yet in the inventory
- **WHEN** a discovery pass observes it
- **THEN** the report is withdrawn and the session is a row like any other

#### Scenario: The session has not appeared yet

- **GIVEN** a reported launch whose session is not yet in the inventory
- **WHEN** the user reads the dashboard
- **THEN** the report says the session has not appeared yet, and still says how
  to reach it

#### Scenario: The agent is waiting to be let in

- **GIVEN** a launch whose agent is waiting for the user to confirm access before
  it starts
- **WHEN** the user reads the dashboard
- **THEN** the session reads as started and waiting, with the way to reach it,
  rather than as missing

#### Scenario: More launches than there is room for

- **GIVEN** several launches reported and none of their sessions observed yet
- **WHEN** the dashboard is drawn
- **THEN** the ones that do not fit are accounted for by a count rather than
  drawn, and the dashboard stays within the terminal

## MODIFIED Requirements

### R25 · Launching starts an observable session (R5)

**Capability**: `session-launch`

A confirmed launch SHALL create the worktree when enabled, start the agent
session in the resulting directory, and the session SHALL then be discoverable by
the same inventory that observes every other session, under the identifier the
launch started it with.

Discovery observes a session once the agent is running. An agent that asks the
user to confirm access before it starts is hosted and waiting, not lost: it holds
its place until the question is answered and is then observed like any other.

#### Scenario: The session joins the inventory

- **GIVEN** a confirmed launch whose agent started unattended
- **WHEN** the next discovery cycle runs
- **THEN** the new session appears with its working directory and its branch

#### Scenario: The agent asks before it starts

- **GIVEN** a launch whose agent asks the user to confirm access to the directory
- **WHEN** nobody has answered it yet
- **THEN** the session is hosted and waiting rather than lost, and is observed
  under the identifier the launch chose once the question is answered

#### Scenario: The session survives musem

- **GIVEN** a session launched from musem
- **WHEN** musem is closed
- **THEN** the session keeps running and is still there when musem is reopened

**Before**: a confirmed launch was said to appear in the inventory on the next
discovery cycle, with no account of an agent that asks before it starts — which
made a session that was running and waiting indistinguishable, in the
specification, from one that had failed to start at all.
