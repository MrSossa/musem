# session-registry

Discovery and inventory of the live agent sessions on the machine: their stable
identity, working directory, git branch and observed status, how the inventory
refreshes, and what happens when a session disappears or a source stops being
readable.

Requirement ids are those the change that introduced them used, so the review
record stays traceable; they are not contiguous.

## R1 · Live session discovery

The system SHALL periodically discover the agent sessions active on the machine
and keep the inventory current without user intervention. Discovery SHALL rely
on the query interface the agent tool publishes, never on inspecting the process
list nor on reading rendered terminal content.

### Scenario: A new session appears

- **GIVEN** musem is open with its inventory current
- **WHEN** the user starts an agent session outside musem
- **THEN** the inventory includes it on the next refresh cycle, with its working
  directory and status

### Scenario: Refresh does not block

- **GIVEN** a discovery query already in flight
- **WHEN** that query takes longer than the refresh interval
- **THEN** the system does not queue overlapping queries and keeps serving the
  last known inventory

## R2 · Stable session identity

The system SHALL identify each session by its own stable identifier. Titles,
names and directory paths are editable by the user or by the tool itself, and
SHALL NOT be used as a key.

### Scenario: The session is renamed

- **GIVEN** a session already in the inventory with accumulated history
- **WHEN** it changes its title while musem is observing it
- **THEN** it is still treated as the same session and keeps its history

### Scenario: Two sessions share a directory

- **GIVEN** a single working directory
- **WHEN** two sessions are active in it
- **THEN** they appear as two distinct entries, each with its own status

## R3 · Observed session status

The system SHALL expose one status per session among: running (the agent is
working), waiting (it needs a user action), idle (ready for instructions), dead
(it terminated abnormally) and ended (it stopped appearing without the source
reporting a failure). Status SHALL be derived from structured data published by
the tool. When a status can only be obtained by scraping rendered text, the
system SHALL expose it as indeterminate rather than risk a wrong verdict. The
system SHALL also expose how long each session has held its current status.

### Scenario: Waiting is distinct from idle

- **GIVEN** a session the tool reports as blocked
- **WHEN** it is blocked asking the user for a confirmation
- **THEN** its status is "waiting", not "idle"

### Scenario: Ambiguous signal

- **GIVEN** a session whose only available signal is rendered text
- **WHEN** the available signals do not allow deciding the status confidently
- **THEN** the status is exposed as indeterminate, along with since when

### Scenario: Finishing is distinct from failing

- **GIVEN** a session the source has never reported as failed
- **WHEN** it stops appearing in discovery
- **THEN** its status is "ended" rather than "dead", and a death the source did
  report is preserved as such

### Scenario: The age of a status outlives the refresh that observed it

- **GIVEN** a session that has held the same status across several refreshes
- **WHEN** the user inspects it
- **THEN** the age reported is how long the status has held, not how long ago
  the last refresh ran

## R4 · Session git branch

The system SHALL resolve and expose the git branch corresponding to each
session's working directory.

### Scenario: Directory inside a repository

- **GIVEN** a session whose working directory belongs to a git repository
- **WHEN** the inventory is refreshed
- **THEN** the session carries the branch currently checked out in that directory

### Scenario: Directory outside a repository

- **GIVEN** a session working outside any git repository
- **WHEN** the inventory is refreshed
- **THEN** the session is shown without a branch, and this is not treated as an
  error

## R5 · Session end of life

When a session stops appearing in discovery, the system SHALL mark it as ended
while preserving its history, and SHALL NOT silently drop it from the registry.

### Scenario: The session disappears

- **GIVEN** a session present in the previous discovery cycle
- **WHEN** it no longer appears in the current one
- **THEN** it is marked as ended, with the timestamp it was last seen, and its
  accumulated cost remains available

## R6 · Degradation when a source is unavailable

If the discovery source is unavailable, answers in an unrecognized format, or
fails, the system SHALL keep working with the last known data, mark it as stale
and report the reason. It SHALL NOT terminate abruptly nor present old data as
if it were current.

### Scenario: The agent tool is not installed

- **GIVEN** a machine without the agent tool
- **WHEN** musem starts
- **THEN** it shows an empty inventory with an actionable explanation instead of
  failing

### Scenario: Unknown format

- **GIVEN** a discovery source that has changed its output shape
- **WHEN** it returns fields musem cannot interpret
- **THEN** what is understood is preserved, the rest is marked unknown, and the
  fact is logged once rather than repeatedly

## R19 · Foreign text carries no terminal instructions

Text the system did not author — session names, working directories, model
identifiers — SHALL be stripped of control characters before it reaches the
interface. The system SHALL NOT render a value from a foreign source in a way
that lets that source drive the terminal.

### Scenario: A crafted name

- **GIVEN** a session whose name or working directory contains escape sequences
- **WHEN** the dashboard draws it
- **THEN** the sequences are gone and the readable part of the value survives

### Scenario: A crafted identifier

- **GIVEN** a discovery record whose session identifier contains control
  characters
- **WHEN** the inventory is refreshed
- **THEN** the record is refused and counted among those that could not be read,
  rather than having its identifier rewritten

Stripping is right for text that exists to be read and wrong for a value that
exists to be matched. An identifier is the key the inventory is indexed by and
the name the usage record is looked up under, so a stripped identifier would
designate a session that does not exist and a record that cannot be found — and
two identifiers differing only in control characters would collapse into one
session, which is the confusion R2 exists to prevent.

## R20 · An unreadable record is reported, not silently dropped

When a discovery pass succeeds but cannot interpret some of the records it
found, the system SHALL report how many it dropped. A pass that could read none
of its records SHALL NOT be presented as a pass that found no sessions.

### Scenario: Every record becomes unreadable

- **GIVEN** live sessions on the machine and a source whose record shape changed
- **WHEN** a discovery pass reads none of them
- **THEN** the count of dropped records is reported, rather than an empty
  inventory in which every known session reads as having ended
