# Architecture decision records

One file per decision. Format: context, decision, consequences (including the cost and
the rejected alternatives). Superseded records are kept, marked, and never rewritten.

| ADR | Decision | Status |
|-----|----------|--------|
| [0001](0001-host-language-go.md) | Host agent in Go | accepted |
| [0002](0002-android-kotlin-compose.md) | Android client in Kotlin + Compose | accepted |
| [0003](0003-json-envelope.md) | Single JSON envelope over one WebSocket | accepted |
| [0004](0004-pin-pairing-token-identity.md) | PIN pairing, then per-device bearer tokens | accepted |
| [0005](0005-action-registry.md) | Action registry with typed interfaces, no central switch | accepted |
| [0006](0006-tls-fingerprint-pinning.md) | Self-signed TLS + fingerprint pinning | accepted |
| [0007](0007-profile-as-directory-of-json.md) | Profile = directory of JSON, host-authoritative | accepted |
| [0008](0008-transport-abstraction.md) | Transport abstraction now, one implementation | accepted |
| [0009](0009-single-binary-no-docker-dependency.md) | Single static binary, Docker never required | accepted |
| [0010](0010-license-apache-2.0.md) | License: Apache-2.0 | accepted |
| [0011](0011-desktop-gui.md) | Desktop control panel: native window with a web view | accepted |

Adding an ADR: copy an existing file, increment the number, append to the table above.
