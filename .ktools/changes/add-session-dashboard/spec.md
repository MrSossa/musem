# Spec delta — add-session-dashboard

**Capabilities**: `session-registry`, `session-cost`, `session-dashboard`

Every requirement carries the capability it belongs to; that is the grouping
archiving folds into `.ktools/specs/<capability>/spec.md`. The project has no
prior specs, so there is nothing modified or removed.

## ADDED Requirements

### R1 · Live session discovery

**Capability**: `session-registry`

The system SHALL periodically discover the agent sessions active on the machine
and keep the inventory current without user intervention. Discovery SHALL rely
on the query interface the agent tool publishes, never on inspecting the process
list nor on reading rendered terminal content.

#### Scenario: A new session appears

- **GIVEN** musem is open with its inventory current
- **WHEN** the user starts an agent session outside musem
- **THEN** the inventory includes it on the next refresh cycle, with its working
  directory and status

#### Scenario: Refresh does not block

- **GIVEN** a discovery query already in flight
- **WHEN** that query takes longer than the refresh interval
- **THEN** the system does not queue overlapping queries and keeps serving the
  last known inventory

### R2 · Stable session identity

**Capability**: `session-registry`

The system SHALL identify each session by its own stable identifier. Titles,
names and directory paths are editable by the user or by the tool itself, and
SHALL NOT be used as a key.

#### Scenario: The session is renamed

- **GIVEN** a session already in the inventory with accumulated history
- **WHEN** it changes its title while musem is observing it
- **THEN** it is still treated as the same session and keeps its history

#### Scenario: Two sessions share a directory

- **GIVEN** a single working directory
- **WHEN** two sessions are active in it
- **THEN** they appear as two distinct entries, each with its own status

### R3 · Observed session status

**Capability**: `session-registry`

The system SHALL expose one status per session among: running (the agent is
working), waiting (it needs a user action), idle (ready for instructions) and
dead (it terminated abnormally). Status SHALL be derived from structured data
published by the tool. When a status can only be obtained by scraping rendered
text, the system SHALL expose it as indeterminate rather than risk a wrong
verdict.

#### Scenario: Waiting is distinct from idle

- **GIVEN** a session the tool reports as blocked
- **WHEN** it is blocked asking the user for a confirmation
- **THEN** its status is "waiting", not "idle"

#### Scenario: Ambiguous signal

- **GIVEN** a session whose only available signal is rendered text
- **WHEN** the available signals do not allow deciding the status confidently
- **THEN** the status is exposed as indeterminate, along with since when

### R4 · Session git branch

**Capability**: `session-registry`

The system SHALL resolve and expose the git branch corresponding to each
session's working directory.

#### Scenario: Directory inside a repository

- **GIVEN** a session whose working directory belongs to a git repository
- **WHEN** the inventory is refreshed
- **THEN** the session carries the branch currently checked out in that directory

#### Scenario: Directory outside a repository

- **GIVEN** a session working outside any git repository
- **WHEN** the inventory is refreshed
- **THEN** the session is shown without a branch, and this is not treated as an
  error

### R5 · Session end of life

**Capability**: `session-registry`

When a session stops appearing in discovery, the system SHALL mark it as ended
while preserving its history, and SHALL NOT silently drop it from the registry.

#### Scenario: The session disappears

- **GIVEN** a session present in the previous discovery cycle
- **WHEN** it no longer appears in the current one
- **THEN** it is marked as ended, with the timestamp it was last seen, and its
  accumulated cost remains available

### R6 · Degradation when a source is unavailable

**Capability**: `session-registry`

If the discovery source is unavailable, answers in an unrecognized format, or
fails, the system SHALL keep working with the last known data, mark it as stale
and report the reason. It SHALL NOT terminate abruptly nor present old data as
if it were current.

#### Scenario: The agent tool is not installed

- **GIVEN** a machine without the agent tool
- **WHEN** musem starts
- **THEN** it shows an empty inventory with an actionable explanation instead of
  failing

#### Scenario: Unknown format

- **GIVEN** a discovery source that has changed its output shape
- **WHEN** it returns fields musem cannot interpret
- **THEN** what is understood is preserved, the rest is marked unknown, and the
  fact is logged once rather than repeatedly

### R7 · Usage derived from structured data

**Capability**: `session-cost`

The system SHALL derive tokens and cost from the usage record the agent tool
writes for each response. It SHALL NOT estimate usage by counting characters nor
read it from text rendered in a pane.

#### Scenario: Usage for a session

- **GIVEN** a session that has produced responses with recorded usage
- **WHEN** musem accounts for it
- **THEN** it exposes the input, output and cache tokens exactly as recorded,
  without recomputing them

#### Scenario: Session with no recorded activity

- **GIVEN** a session that exists but has not produced any response yet
- **WHEN** musem accounts for it
- **THEN** its usage is zero, distinguishable from "unknown"

### R8 · Cache breakdown

**Capability**: `session-cost`

The system SHALL account for cache-creation tokens and cache-read tokens
separately, and SHALL NOT merge them with ordinary input tokens, because they
are priced differently.

#### Scenario: Cache-heavy session

- **GIVEN** a session whose cache reads run far above its ordinary input
- **WHEN** its cost is computed
- **THEN** the breakdown reflects it and the computed cost applies each rate
  separately

### R9 · Model with no known rate

**Capability**: `session-cost`

When a response comes from a model whose rate musem does not know, the system
SHALL report the tokens and mark the cost of that portion as unavailable. It
SHALL NOT apply another model's rate nor silently omit the usage.

#### Scenario: A new model appears

- **GIVEN** a rate table that does not list every model in use
- **WHEN** a session uses a model absent from it
- **THEN** the tokens are accounted for, the cost is marked partial, and the
  unknown model is identified

### R10 · Usage aggregation

**Capability**: `session-cost`

The system SHALL offer the aggregated usage across all known sessions in
addition to per-session figures. An aggregate that includes portions with
unavailable cost SHALL be flagged as partial.

#### Scenario: Fleet total

- **GIVEN** several accounted sessions
- **WHEN** the user asks for combined spend
- **THEN** they get the sum across all sessions, flagged if any portion could not
  be priced

### R11 · History persistence

**Capability**: `session-cost`

The system SHALL retain each session's accumulated usage such that it survives
musem shutting down and the session or its source record disappearing.

#### Scenario: musem restarts

- **GIVEN** sessions accounted for in an earlier run
- **WHEN** the user closes musem and opens it again
- **THEN** their historical usage is still available

#### Scenario: The source record disappears

- **GIVEN** usage already derived from a transcript
- **WHEN** that transcript file is deleted
- **THEN** the already-accounted usage is preserved and is not recomputed to zero

### R12 · Local processing

**Capability**: `session-cost`

The system SHALL process transcripts entirely on the local machine. It SHALL NOT
transmit their content, or any fragment of it, to any remote destination.

#### Scenario: No network egress

- **GIVEN** transcripts containing source code and prompts
- **WHEN** musem reads them to compute usage
- **THEN** no network request originates from that content

### R13 · Fleet overview

**Capability**: `session-dashboard`

The dashboard SHALL show every known session in a single view and, for each one,
its name, status, working directory, git branch and accumulated cost.

#### Scenario: Several active sessions

- **GIVEN** several live sessions across different projects
- **WHEN** the user opens the dashboard
- **THEN** they all appear in the same view without navigating or filtering

#### Scenario: No sessions

- **GIVEN** no live session on the machine
- **WHEN** the user opens the dashboard
- **THEN** an explanatory empty state is shown, not a blank table

### R14 · Actionable sessions come first

**Capability**: `session-dashboard`

The dashboard SHALL order sessions by default so that those waiting on a user
action appear ahead of the rest.

#### Scenario: A session starts waiting

- **GIVEN** a list showing running and idle sessions
- **WHEN** one of them transitions to "waiting"
- **THEN** it moves up the order ahead of running and idle sessions

### R15 · Automatic refresh and data freshness

**Capability**: `session-dashboard`

The dashboard SHALL refresh on its own, without user action, and SHALL indicate
the age of the data when it stops being current. It SHALL NOT present a stale
value with the same appearance as a current one.

#### Scenario: Refresh in progress

- **GIVEN** the dashboard open and idle
- **WHEN** a session's status changes on the machine
- **THEN** the view reflects it without the user asking

#### Scenario: Stale data

- **GIVEN** the dashboard showing current data
- **WHEN** refresh fails or stops completing
- **THEN** the affected data is visibly marked as stale, indicating since when

### R16 · Read only

**Capability**: `session-dashboard`

Within this scope the dashboard SHALL limit itself to observing. It SHALL NOT
send input to a session, create, stop or restart one, nor modify a session's
state or the filesystem in any way.

#### Scenario: No destructive actions available

- **GIVEN** the dashboard open with live sessions
- **WHEN** the user navigates the interface
- **THEN** they find no operation that alters a session or the repository

### R17 · Keyboard navigation

**Capability**: `session-dashboard`

The dashboard SHALL be fully operable from the keyboard, including moving
through the list, inspecting a session's detail and quitting. It SHALL offer
help discoverable from within the interface.

#### Scenario: Navigate and quit

- **GIVEN** the dashboard open with several sessions
- **WHEN** the user navigates with the keyboard and asks to quit
- **THEN** the dashboard exits cleanly, restoring the terminal to its original
  state

#### Scenario: Discoverable help

- **GIVEN** a user who does not know the key bindings
- **WHEN** they ask for help inside the interface
- **THEN** the available keys are shown without leaving the dashboard

### R18 · Legibility in narrow terminals

**Capability**: `session-dashboard`

The dashboard SHALL remain legible when the terminal is narrow or gets resized,
degrading the information shown in an orderly way rather than breaking alignment
or truncating unpredictably.

#### Scenario: Narrow terminal

- **GIVEN** a terminal narrower than the full table
- **WHEN** the available width cannot fit every column
- **THEN** the lowest-priority columns are dropped and the visible ones keep
  their alignment

#### Scenario: Live resize

- **GIVEN** the dashboard rendering in an open terminal
- **WHEN** the user resizes that terminal
- **THEN** the view re-adapts without corrupting its content
