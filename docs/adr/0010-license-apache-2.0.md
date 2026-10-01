# ADR-0010: License Apache-2.0

Status: accepted (Milestone 1)

## Context
Requirement §41: choose MIT or Apache-2.0 and explain the consequences.

## Decision
**Apache-2.0.**

## Consequences
- Explicit patent grant (§3): contributors and users get a license to patents the
  contributors hold that are necessarily infringed by the code. MIT is silent on
  patents, which is a real risk for a project that will pull in OS-integration and
  plugin code from many authors.
- Requires preserving `NOTICE`/attribution and stating changes in modified files. This is
  the practical cost: the project must ship `THIRD_PARTY_LICENSES.md` and keep
  attribution intact.
- Compatible with the whole dependency set: Go stdlib (BSD-3), `gorilla/websocket`
  (BSD-2), `grandcat/zeroconf` (MIT), AndroidX/Compose (Apache-2.0), Kotlin (Apache-2.0),
  OkHttp (Apache-2.0), kotlinx.serialization (Apache-2.0). No GPL/AGPL dependency is
  present or permitted; `THIRD_PARTY_LICENSES.md` records this and is checked at review
  time.
- Apache-2.0 is not copyleft, so plugins and commercial forks are allowed. That is
  intended: the project's value is the protocol and the ecosystem, not license leverage.
- Cost vs MIT: longer, more formal license text, and downstream users must include the
  NOTICE file. Accepted.
