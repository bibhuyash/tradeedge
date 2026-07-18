# Phase 0: Repository Foundation

## Scope

Create the stdlib-only Go modular-monolith foundation: typed configuration, structured logging, health/readiness, graceful shutdown, domain primitives, architectural ports, and an in-memory paper broker skeleton.

## Assumptions

Go 1.23.4 is available. Readiness represents successful local initialization because no database or external integration exists yet.

## Responsibilities

- Reject all modes except `paper`.
- Define broker-neutral and notification-neutral contracts.
- Make money authoritative in integer minor units.
- Provide deterministic, context-aware paper broker behavior and unit tests.

## Invariants

- No live route, credential, network provider, or strategy implementation exists.
- Strategy packages have no broker dependency.
- The broker port is execution-owned.
- No external dependency is required.

## Failure Modes

Invalid configuration prevents startup. Context cancellation stops operations. Duplicate paper submissions return the original order; conflicting reuse is rejected.

## Trade-offs

Phase 0 deliberately omits persistence, fill simulation, orchestration, metrics backends, and real integrations to keep the milestone reviewable.

## Unresolved Questions

Persistence repositories and durable audit schema are deferred to their own milestone.

## Acceptance Criteria

- `go test ./...` and `go vet ./...` pass.
- `gofmt -l` reports no Go files.
- Local health and readiness probes work and shutdown is graceful.
- No live broker call is possible.
