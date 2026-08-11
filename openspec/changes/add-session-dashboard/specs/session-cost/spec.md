## Purpose

Know what each session and the fleet as a whole cost, based on the real usage
the agent tool records, so that spend is a verifiable figure rather than an
estimate.

## ADDED Requirements

### Requirement: Usage derived from structured data

The system SHALL derive tokens and cost from the usage record the agent tool
writes for each response. It SHALL NOT estimate usage by counting characters nor
read it from text rendered in a pane.

#### Scenario: Usage for a session

- **WHEN** a session has produced responses with recorded usage
- **THEN** musem exposes its input, output and cache tokens exactly as recorded,
  without recomputing them

#### Scenario: Session with no recorded activity

- **WHEN** a session exists but has not produced any response yet
- **THEN** its usage is zero, distinguishable from "unknown"

### Requirement: Cache breakdown

The system SHALL account for cache-creation tokens and cache-read tokens
separately, and SHALL NOT merge them with ordinary input tokens, because they
are priced differently.

#### Scenario: Cache-heavy session

- **WHEN** a session accumulates cache reads far above its ordinary input
- **THEN** the breakdown reflects it and the computed cost applies each rate
  separately

### Requirement: Model with no known rate

When a response comes from a model whose rate musem does not know, the system
SHALL report the tokens and mark the cost of that portion as unavailable. It
SHALL NOT apply another model's rate nor silently omit the usage.

#### Scenario: A new model appears

- **WHEN** a session uses a model that is not in the rate table
- **THEN** the tokens are accounted for, the cost is marked partial, and the
  unknown model is identified

### Requirement: Usage aggregation

The system SHALL offer the aggregated usage across all known sessions in
addition to per-session figures. An aggregate that includes portions with
unavailable cost SHALL be flagged as partial.

#### Scenario: Fleet total

- **WHEN** the user asks for combined spend
- **THEN** they get the sum across all sessions, flagged if any portion could not
  be priced

### Requirement: History persistence

The system SHALL retain each session's accumulated usage such that it survives
musem shutting down and the session or its source record disappearing.

#### Scenario: musem restarts

- **WHEN** the user closes musem and opens it again
- **THEN** historical usage from earlier sessions is still available

#### Scenario: The source record disappears

- **WHEN** the transcript file the usage was derived from is deleted
- **THEN** already-accounted usage is preserved and is not recomputed to zero

### Requirement: Local processing

The system SHALL process transcripts entirely on the local machine. It SHALL NOT
transmit their content, or any fragment of it, to any remote destination.

#### Scenario: No network egress

- **WHEN** musem reads transcripts to compute usage
- **THEN** no network request originates from that content
