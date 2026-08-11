## Purpose

Keep a reliable inventory of the live agent sessions on the machine — what they
are, where they work and what state they are in — so the rest of musem reasons
about data rather than about what a terminal pane happens to look like.

## ADDED Requirements

### Requirement: Live session discovery

The system SHALL periodically discover the agent sessions active on the machine
and keep the inventory current without user intervention. Discovery SHALL rely
on the query interface the agent tool publishes, never on inspecting the process
list nor on reading rendered terminal content.

#### Scenario: A new session appears

- **WHEN** the user starts an agent session outside musem
- **THEN** the inventory includes it on the next refresh cycle, with its working
  directory and status

#### Scenario: Refresh does not block

- **WHEN** a discovery query takes longer than the refresh interval
- **THEN** the system does not queue overlapping queries and keeps serving the
  last known inventory

### Requirement: Stable session identity

The system SHALL identify each session by its own stable identifier. Titles,
names and directory paths are editable by the user or by the tool itself, and
SHALL NOT be used as a key.

#### Scenario: The session is renamed

- **WHEN** a session changes its title while musem is observing it
- **THEN** it is still treated as the same session and keeps its history

#### Scenario: Two sessions share a directory

- **WHEN** two sessions are active in the same working directory
- **THEN** they appear as two distinct entries, each with its own status

### Requirement: Observed session status

The system SHALL expose one status per session among: running (the agent is
working), waiting (it needs a user action), idle (ready for instructions) and
dead (it terminated abnormally). Status SHALL be derived from structured data
published by the tool. When a status can only be obtained by scraping rendered
text, the system SHALL expose it as indeterminate rather than risk a wrong
verdict.

#### Scenario: Waiting is distinct from idle

- **WHEN** a session is blocked asking the user for a confirmation
- **THEN** its status is "waiting", not "idle"

#### Scenario: Ambiguous signal

- **WHEN** the available signals do not allow deciding the status confidently
- **THEN** the status is exposed as indeterminate, along with since when

### Requirement: Session git branch

The system SHALL resolve and expose the git branch corresponding to each
session's working directory.

#### Scenario: Directory outside a repository

- **WHEN** a session's working directory does not belong to a git repository
- **THEN** the session is shown without a branch, and this is not treated as an
  error

### Requirement: Session end of life

When a session stops appearing in discovery, the system SHALL mark it as ended
while preserving its history, and SHALL NOT silently drop it from the registry.

#### Scenario: The session disappears

- **WHEN** a session present in the previous cycle no longer appears
- **THEN** it is marked as ended, with the timestamp it was last seen, and its
  accumulated cost remains available

### Requirement: Degradation when a source is unavailable

If the discovery source is unavailable, answers in an unrecognized format, or
fails, the system SHALL keep working with the last known data, mark it as stale
and report the reason. It SHALL NOT terminate abruptly nor present old data as
if it were current.

#### Scenario: The agent tool is not installed

- **WHEN** musem starts on a machine without the agent tool
- **THEN** it shows an empty inventory with an actionable explanation instead of
  failing

#### Scenario: Unknown format

- **WHEN** the source returns fields musem cannot interpret
- **THEN** what is understood is preserved, the rest is marked unknown, and the
  fact is logged once rather than repeatedly
