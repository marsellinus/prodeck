# ADR-0003: JSON envelope over one WebSocket, not REST + WS + gRPC

Status: accepted (Milestone 1)

## Context
Candidates: (a) REST for commands + WS for events, (b) gRPC/protobuf streaming,
(c) a single typed envelope over one WebSocket, (d) raw OSC/other.

## Decision
(c). One WebSocket, one envelope shape, `type`-tagged payloads, `id`/`reply_to`
correlation. REST is kept only for `/info`, `/health`, `/pair` and local admin.

## Consequences
- One connection means one lifecycle to get right: handshake, heartbeat, backoff,
  revocation. Three transports would mean three failure modes.
- Correlation via `reply_to` gives request/response semantics on top of a stream without
  inventing a second protocol.
- Human-inspectable: `websocat` is a complete debugging tool, and the docs' examples are
  literal wire traffic. This matters more than bytes for a tool whose whole point is
  user-authored configuration.
- Cost: no schema codegen, no compile-time type checking across the wire. Mitigated by
  the version gate, strict validation, and the rule that unknown fields are ignored.
- gRPC-Web in browsers/Android is awkward and protobuf hurts hand-editing profiles.
