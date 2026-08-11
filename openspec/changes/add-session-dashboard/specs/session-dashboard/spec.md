## Purpose

Answer "what is happening across my sessions right now?" at a glance,
prioritising whatever demands the user's attention and never hiding when a value
stopped being trustworthy.

## ADDED Requirements

### Requirement: Fleet overview

The dashboard SHALL show every known session in a single view and, for each one,
its name, status, working directory, git branch and accumulated cost.

#### Scenario: Several active sessions

- **WHEN** there are several live sessions across different projects
- **THEN** they all appear in the same view without navigating or filtering

#### Scenario: No sessions

- **WHEN** there is no live session
- **THEN** an explanatory empty state is shown, not a blank table

### Requirement: Actionable sessions come first

The dashboard SHALL order sessions by default so that those waiting on a user
action appear ahead of the rest.

#### Scenario: A session starts waiting

- **WHEN** a session transitions to "waiting"
- **THEN** it moves up the order ahead of running and idle sessions

### Requirement: Automatic refresh and data freshness

The dashboard SHALL refresh on its own, without user action, and SHALL indicate
the age of the data when it stops being current. It SHALL NOT present a stale
value with the same appearance as a current one.

#### Scenario: Refresh in progress

- **WHEN** a session's status changes on the machine
- **THEN** the view reflects it without the user asking

#### Scenario: Stale data

- **WHEN** refresh fails or stops completing
- **THEN** the affected data is visibly marked as stale, indicating since when

### Requirement: Read only

Within this scope the dashboard SHALL limit itself to observing. It SHALL NOT
send input to a session, create, stop or restart one, nor modify a session's
state or the filesystem in any way.

#### Scenario: No destructive actions available

- **WHEN** the user navigates the interface
- **THEN** they find no operation that alters a session or the repository

### Requirement: Keyboard navigation

The dashboard SHALL be fully operable from the keyboard, including moving
through the list, inspecting a session's detail and quitting. It SHALL offer
help discoverable from within the interface.

#### Scenario: Navigate and quit

- **WHEN** the user navigates with the keyboard and asks to quit
- **THEN** the dashboard exits cleanly, restoring the terminal to its original
  state

#### Scenario: Discoverable help

- **WHEN** the user asks for help inside the interface
- **THEN** the available keys are shown without leaving the dashboard

### Requirement: Legibility in narrow terminals

The dashboard SHALL remain legible when the terminal is narrow or gets resized,
degrading the information shown in an orderly way rather than breaking alignment
or truncating unpredictably.

#### Scenario: Narrow terminal

- **WHEN** the available width cannot fit every column
- **THEN** the lowest-priority columns are dropped and the visible ones keep
  their alignment

#### Scenario: Live resize

- **WHEN** the user resizes the terminal while musem is open
- **THEN** the view re-adapts without corrupting its content
