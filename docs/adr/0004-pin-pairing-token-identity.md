# ADR-0004: PIN pairing, then per-device bearer tokens

Status: accepted (Milestone 1)

## Context
The device must pair once and never re-enter a password. Options: shared password,
mTLS client certs, OAuth-style device flow, short-lived PIN + long-lived token.

## Decision
Host displays a **6-digit PIN** (120 s TTL, single use). The client exchanges it over
`POST /api/v1/pair` for a **32-byte random token**. The host stores `sha256(token)` and
the device record; the client stores the token in Keystore-backed encrypted storage.

## Consequences
- No shared secret ever exists in a config file or a README.
- Revocation is per-device: delete the record, and the next connection gets 4403.
- The token is a bearer credential, so it is only as safe as the transport → TLS is on by
  default for LAN binds (ADR-0006).
- Rejected mTLS: the UX of installing a client cert on Android by hand is worse than a
  PIN, and self-signed CA management is a support burden for a self-hosted tool.
- Rejected long-lived password: unrotatable, shared across devices, and impossible to
  revoke per device.
