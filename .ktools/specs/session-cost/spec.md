# session-cost

Token and cost accounting per session, derived from the structured data the
agent tool records, with the cache breakdown kept separate, cross-session
aggregation and history that survives both a restart and the disappearance of
the record it came from.

Requirement ids are those the change that introduced them used, so the review
record stays traceable; they are not contiguous.

## R7 · Usage derived from structured data

The system SHALL derive tokens and cost from the usage record the agent tool
writes for each response. It SHALL NOT estimate usage by counting characters nor
read it from text rendered in a pane.

### Scenario: Usage for a session

- **GIVEN** a session that has produced responses with recorded usage
- **WHEN** musem accounts for it
- **THEN** it exposes the input, output and cache tokens exactly as recorded,
  without recomputing them

### Scenario: Session with no recorded activity

- **GIVEN** a session that exists but has not produced any response yet
- **WHEN** musem accounts for it
- **THEN** its usage is zero, distinguishable from "unknown"

## R8 · Cache breakdown

The system SHALL account for cache-creation tokens and cache-read tokens
separately, and SHALL NOT merge them with ordinary input tokens, because they
are priced differently.

### Scenario: Cache-heavy session

- **GIVEN** a session whose cache reads run far above its ordinary input
- **WHEN** its cost is computed
- **THEN** the breakdown reflects it and the computed cost applies each rate
  separately

## R9 · Model with no known rate

When a response comes from a model whose rate musem does not know, the system
SHALL report the tokens and mark the cost of that portion as unavailable. It
SHALL NOT apply another model's rate nor silently omit the usage.

### Scenario: A new model appears

- **GIVEN** a rate table that does not list every model in use
- **WHEN** a session uses a model absent from it
- **THEN** the tokens are accounted for, the cost is marked partial, and the
  unknown model is identified

## R10 · Usage aggregation

The system SHALL offer the aggregated usage across all known sessions in
addition to per-session figures. An aggregate that includes portions with
unavailable cost SHALL be flagged as partial.

### Scenario: Fleet total

- **GIVEN** several accounted sessions
- **WHEN** the user asks for combined spend
- **THEN** they get the sum across all sessions, flagged if any portion could not
  be priced

## R11 · History persistence

The system SHALL retain each session's accumulated usage such that it survives
musem shutting down and the session or its source record disappearing.

### Scenario: musem restarts

- **GIVEN** sessions accounted for in an earlier run
- **WHEN** the user closes musem and opens it again
- **THEN** their historical usage is still available

### Scenario: The source record disappears

- **GIVEN** usage already derived from a transcript
- **WHEN** that transcript file is deleted
- **THEN** the already-accounted usage is preserved and is not recomputed to zero

## R12 · Local processing

The system SHALL process transcripts entirely on the local machine. It SHALL NOT
transmit their content, or any fragment of it, to any remote destination.

### Scenario: No network egress

- **GIVEN** transcripts containing source code and prompts
- **WHEN** musem reads them to compute usage
- **THEN** no network request originates from that content
