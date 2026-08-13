# session-dashboard

The view: which columns it shows, how it orders them, how often it refreshes, how
it is navigated, and how it communicates that a value is stale or unavailable.

It was read-only for its whole first phase, and no longer is — launching from the
dashboard is `session-launch`. What survives that is R31: navigating still
changes nothing, and what the dashboard may create and remove is exactly what
musem made for itself.

Requirement ids are those the change that introduced them used, so the review
record stays traceable; they are not contiguous. Where a change proposed an id
already in force elsewhere, the id it proposed is kept in parentheses.

## R13 · Fleet overview

The dashboard SHALL show every known session in a single view and, for each one,
its name, status, working directory, git branch and accumulated cost.

### Scenario: Several active sessions

- **GIVEN** several live sessions across different projects
- **WHEN** the user opens the dashboard
- **THEN** they all appear in the same view without navigating or filtering

### Scenario: No sessions

- **GIVEN** no live session on the machine
- **WHEN** the user opens the dashboard
- **THEN** an explanatory empty state is shown, not a blank table

## R14 · Actionable sessions come first

The dashboard SHALL order sessions by default so that those waiting on a user
action appear ahead of the rest.

### Scenario: A session starts waiting

- **GIVEN** a list showing running and idle sessions
- **WHEN** one of them transitions to "waiting"
- **THEN** it moves up the order ahead of running and idle sessions

## R15 · Automatic refresh and data freshness

The dashboard SHALL refresh on its own, without user action, and SHALL indicate
the age of the data when it stops being current. It SHALL NOT present a stale
value with the same appearance as a current one.

### Scenario: Refresh in progress

- **GIVEN** the dashboard open and idle
- **WHEN** a session's status changes on the machine
- **THEN** the view reflects it without the user asking

### Scenario: Stale data

- **GIVEN** the dashboard showing current data
- **WHEN** refresh fails or stops completing
- **THEN** the affected data is visibly marked as stale, indicating since when

## R31 · Destructive actions are narrow, recorded and explained (R11)

Replaces R16, whose read-only guarantee ended when launching arrived. R16 was a
first-phase constraint that forced observation to be solved before acting on what
is observed, and it did its job. What is worth keeping from it — navigation never
mutates, and nothing outside musem's own creations is ever touched — is stated
here, narrower and enforceable alongside launching.

The dashboard MAY create and remove only what this specification permits: the
worktrees it makes for the sessions it launches. Every other object the user is
observing — sessions musem did not launch, repositories, working directories,
files outside musem's own store and worktrees musem did not create — SHALL remain
untouched. No action reachable by navigating the interface SHALL modify anything;
mutation SHALL follow only from an action the user asked for explicitly.

### Scenario: Navigating changes nothing

- **GIVEN** the dashboard open with live sessions
- **WHEN** the user moves through the list, opens details and reads help
- **THEN** nothing on disk and no session has changed

### Scenario: Someone else's repository

- **GIVEN** a session observed in a repository musem never launched into
- **WHEN** that session ends, however it ends
- **THEN** that repository and its worktrees are exactly as they were

## R17 · Keyboard navigation

The dashboard SHALL be fully operable from the keyboard, including moving
through the list, inspecting a session's detail and quitting. It SHALL offer
help discoverable from within the interface.

### Scenario: Navigate and quit

- **GIVEN** the dashboard open with several sessions
- **WHEN** the user navigates with the keyboard and asks to quit
- **THEN** the dashboard exits cleanly, restoring the terminal to its original
  state

### Scenario: Discoverable help

- **GIVEN** a user who does not know the key bindings
- **WHEN** they ask for help inside the interface
- **THEN** the available keys are shown without leaving the dashboard

## R18 · Legibility in narrow terminals

The dashboard SHALL remain legible when the terminal is narrow or gets resized,
degrading the information shown in an orderly way rather than breaking alignment
or truncating unpredictably.

### Scenario: Narrow terminal

- **GIVEN** a terminal narrower than the full table
- **WHEN** the available width cannot fit every column
- **THEN** the lowest-priority columns are dropped and the visible ones keep
  their alignment

### Scenario: Live resize

- **GIVEN** the dashboard rendering in an open terminal
- **WHEN** the user resizes that terminal
- **THEN** the view re-adapts without corrupting its content
