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
- Run pull-request CI with read-only repository permissions and no secrets.
- Produce checksummed delivery artifacts without granting deployment authority.

## Invariants

- No credentials are committed or placed in `.env.example`.
- Phase 0 has no real credential fields and rejects live mode.
- A notification channel cannot bypass trading controls.
- CI and delivery workflows cannot place orders or deploy to an environment.
- Production deployment requires a separately approved, protected environment.

## Failure Modes

Credential leakage, unauthorized commands, dependency compromise, mutable
build inputs, log exposure, replay, and excessive workflow permissions can
cause account harm.

## Trade-offs

Strict access and rotation add operational effort but constrain blast radius.

## Unresolved Questions

Secret storage, operator identity provider, retention access, artifact signing,
production hosting, deployment approvals, and incident roles require deployment
decisions.

## Acceptance Criteria

- Repository scans find no secrets.
- Sample configuration is paper-only.
- Security-sensitive actions are designed for authentication and audit.
- GitHub workflows use least-privilege permissions and contain no credentials.
- Delivered binaries include commit metadata and SHA-256 checksums.
