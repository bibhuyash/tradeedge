# Security

## Scope

Security covers secrets, identities, network boundaries, operator controls, dependencies, telemetry, and incident response.

## Assumptions

Production outbound traffic will originate from an approved static public IP. Credentials will be supplied by a secrets system, never source control.

## Responsibilities

- Apply least privilege and separate paper from future live credentials.
- Redact logs and audit access.
- Authenticate Telegram operators and protect commands against replay.
- Patch dependencies and maintain a reviewable software supply chain.

## Invariants

- No credentials are committed or placed in `.env.example`.
- Phase 0 has no real credential fields and rejects live mode.
- A notification channel cannot bypass trading controls.

## Failure Modes

Credential leakage, unauthorized commands, dependency compromise, log exposure, replay, and excessive permissions can cause account harm.

## Trade-offs

Strict access and rotation add operational effort but constrain blast radius.

## Unresolved Questions

Secret storage, operator identity provider, retention access, and incident roles require deployment decisions.

## Acceptance Criteria

- Repository scans find no secrets.
- Sample configuration is paper-only.
- Security-sensitive actions are designed for authentication and audit.
