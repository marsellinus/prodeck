# ADR-0006: Self-signed TLS with fingerprint pinning, plaintext only on loopback

Status: accepted (Milestone 1)

## Context
Home LANs are not trusted networks. A pure-WebSocket plaintext control channel lets any
LAN peer inject keystrokes. Full PKI (Let's Encrypt) needs a public name and internet.

## Decision
Generate a self-signed ECDSA P-256 certificate on first run. Advertise its SHA-256
fingerprint over mDNS TXT and `/api/v1/info`. The client pins the fingerprint at pairing
and refuses a later mismatch. Binding to loopback defaults to plaintext (no LAN exposure);
binding to a LAN address without TLS refuses to pair unless the operator passes
`--insecure-allow-plaintext`.

## Consequences
- Confidentiality and integrity on the LAN without a CA, a domain, or internet.
- Pinning defeats the rogue-mDNS-host attack (SECURITY.md T9), which a plain CA would not.
- Cost: rotating the certificate (reinstall, new machine) invalidates pins and forces
  re-pairing. Accepted, and mitigated by advertising the fingerprint before pairing.
- Cost: the client must implement a custom `X509TrustManager` comparing the pin. Small
  and well-understood.
