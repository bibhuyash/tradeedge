# Phase 8 M3 real-market SHADOW qualification

## Safety boundary

This workflow is SHADOW-only. It must produce zero orders, fills, authoritative
positions, or Zerodha mutations. NIFTY and BANKNIFTY evidence is independent.
The checked-in configurations are disabled and do not authorize a session.

## Activation prerequisites

Pin the exact reviewed commit and require passing GitHub CI, fresh read-only
Zerodha preflight, checksum-pinned current mappings and runtime bundle, healthy
Telegram delivery, an authorization manifest, and explicit operator approval.
Never place, modify, or cancel an order during this runbook.

## Normal session

After the existing safe authentication and authorization process has produced
the approved environment, start the normal Day-0 service:

```text
docker compose --env-file .env up -d tradeedge-day0
```

Verify health/readiness and the GET-only endpoints:

```text
GET /api/v1/qualification/strategies
GET /api/v1/qualification/strategies/EMA_REFERENCE_V1/NIFTY
GET /api/v1/qualification/strategies/EMA_REFERENCE_V1/BANKNIFTY
GET /api/v1/qualification/signals/recent?limit=20
GET /api/v1/qualification/scorecards
```

Confirm Telegram messages say `Broker Order: NONE`, checkpoint generations are
mode `SHADOW`, and broker mutation counters remain zero. Stop collection on
stale/ambiguous mapping, CAS restriction, market close, kill switch, circuit
breaker, checkpoint failure, or readiness loss. Missing horizons must be typed
unavailable rather than reconstructed.

## Manual work remaining

Zerodha authentication, authorization-manifest approval, current instrument
mapping review, and session activation remain manual. M3 contains deterministic
replay evidence only; no real-market session is claimed. Any later PAPER-capital
decision requires multi-session evidence and a separate explicit approval.
