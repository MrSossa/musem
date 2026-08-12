# session-dashboard

The read-only view: which columns it shows, how it orders them, how often it
refreshes, how it is navigated, and how it communicates that a value is stale or
unavailable.

Requirement ids are those the change that introduced them used, so the review
record stays traceable; they are not contiguous.

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

## R16 · Read only

Within this scope the dashboard SHALL limit itself to observing. It SHALL NOT
send input to a session, create, stop or restart one, nor modify a session's
state, a repository, or any file outside musem's own history store.

The store is the one exception, and a narrow one: R11 requires history to
survive a restart, which cannot be done without writing somewhere. Everything
the user is observing stays untouched.

### Scenario: No destructive actions available

- **GIVEN** the dashboard open with live sessions
- **WHEN** the user navigates the interface
- **THEN** they find no operation that alters a session or the repository

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
