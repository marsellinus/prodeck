# Security model and threat model

Scope: MobileDeck host agent and Android client, Milestone 1.

---

## 1. Assets

| Asset | Why it matters |
|-------|----------------|
| Host input control | Whoever holds it can drive keyboard/mouse of the machine. |
| Device tokens | Bearer credentials; equivalent to the paired device's powers. |
| Pairing PIN | Short-lived, but its theft is the cheapest path to a token. |
| Profile documents | May embed absolute paths, URLs, env vars, and command lines. |
| Audit log | The only record of who did what. |

## 2. Trust boundaries

```
[ untrusted LAN ] ──▶ (1) TCP/WS edge ──▶ (2) auth ──▶ (3) engine ──▶ (4) platform
                                                │
                                          (5) audit
```

1. **Network edge.** Anyone on the LAN can reach `/api/v1/info`, `/api/v1/health`, and
   attempt `/ws` and `/api/v1/pair`. Everything else requires a device token.
2. **Auth.** Token → device → scope set. Scopes are the only thing that decides whether
   an action may run.
3. **Engine.** Validates params, resolves action type, enforces path rules.
4. **Platform.** Executes. Never elevates.
5. **Audit.** Every auth decision and every privileged action is appended, regardless of
   outcome.

## 3. Threat model

Assumed attacker capabilities: on the same LAN; can sniff and inject traffic if no TLS;
can replay captured frames; can send malformed/hostile JSON; can attempt brute force;
can present itself as a discovered host (rogue mDNS responder) to a client.

Out of scope for M1: attacker with code execution on the host, physical access to the
host, a malicious Android OS, and supply-chain compromise of Go/Maven dependencies
(mitigated by pinning + `go.sum` verification, not solved).

| # | Threat | Impact | Mitigation (M1) |
|---|--------|--------|-----------------|
| T1 | Unauthenticated client sends `button.press` | Host input hijack | `/ws` requires a valid token in `hello`; close 4401 otherwise. No anonymous action path exists. |
| T2 | PIN brute force | Token theft | 6 digits, 120 s TTL, single use, per-IP and per-device attempt counters, forced PIN rotation after 5 failures, audit record per attempt. |
| T3 | Token replay from another machine | Input hijack | TLS with fingerprint pinning; token bound to `device_id`; optional `bind_ip` per device (off by default: phones roam). Documented residual risk when TLS is disabled. |
| T4 | Stolen token at rest on the phone | Input hijack | Token stored via Keystore-backed encrypted storage, never in plain prefs, never logged. |
| T5 | Malformed / oversized frame | Crash or OOM | `MaxMessageBytes` 1 MiB, envelope validated before dispatch, decode errors answered with `error{invalid_argument}` and counted; 10 malformed frames → close 4400. |
| T6 | Command injection via action params | RCE | `run_command` never interpolates into a shell string built by the host: it passes an explicit argv to the configured shell (`sh -c` only when the profile author asked for shell semantics, which is the documented behaviour of `run_command`). `run_script` executes a *path* with argv, not a string. Profile-supplied `cwd`/`env` are validated. |
| T7 | Path traversal in `run_script` | Read/execute arbitrary files | Paths are resolved and confined to the profile dir and the host `scripts/` dir; symlink escape checked with `filepath.EvalSymlinks`; absolute paths refused unless `--allow-absolute-paths`. |
| T8 | Privilege escalation | Full machine compromise | The agent never elevates. No `sudo`, no UAC prompt, no setuid helper in M1. Documented in §4. |
| T9 | Rogue host impersonating a real one | Client pairs with attacker, attacker harvests PIN or feeds hostile profile | Client shows the TLS fingerprint from mDNS TXT and verifies it on connect; PIN entry is explicit and shows the host name; client refuses plaintext pairing when `tls.enabled=true` is advertised. |
| T10 | Hostile profile document | DoS of the deck, or surprises like `rm -rf` | Strict validation on load: unknown fields rejected, grid bounds enforced, action types must exist in the registry, `require_confirmation` honoured for destructive types. A profile is code-equivalent, which is why `profiles/` is reviewed like code and exported/imported as JSON. |
| T11 | Rate abuse / accidental loops | Host DoS | Per-device token bucket (30 req/s burst 60), bounded action worker pool (default 8), queue overflow → `rate_limited`, macro step count and total duration caps. |
| T12 | Slow-loris / hung session | Resource exhaustion | Read deadline tied to heartbeat, 10 s `hello` deadline, per-session write deadline, sessions capped (`--max-clients`, default 16). |
| T13 | Log injection / secret leakage | Information disclosure | Structured logs; tokens and PINs are never logged, only `device_id` and a token fingerprint prefix. |
| T14 | Replay of a captured `pair` request | Token theft | PIN single-use; pairing response is not cacheable; audit records the source IP and user agent of the successful exchange. |

## 4. Privilege separation

The agent runs as the **logged-in user**, never as root/Administrator.

- `keyboard.*`, `mouse.*`, `media.*` need the session's input stack. On Linux this means
  X11/Wayland access as that user; on Windows it means the interactive session.
  Neither requires elevation.
- `system.shutdown` / `system.restart` are the only actions that may need privilege. M1
  behaviour: they are **refused** unless the platform reports they are permitted, and the
  error explains the requirement (`system.power` scope *and* a host-side opt-in). No
  hidden elevation, no `sudo` invocation, no saved credentials.
- A future privileged helper (systemd unit / Windows service running as SYSTEM, with a
  narrow IPC surface: only "power off"/"restart" verbs, no arguments) is the sanctioned
  path (phase 5, see ROADMAP). It is explicitly *not* a general "run anything as root"
  bridge.
- systemd: the agent installs as a **user** unit (`systemctl --user`), which is what makes
  input control work at all. Installing as a system unit is documented as unsupported for
  input actions.

## 5. Transport security

- TLS is generated on first run (self-signed, ECDSA P-256, 825-day validity, SANs for
  every local IPv4/IPv6 plus `localhost`), stored in `~/.config/mobiledeck/tls/`.
- The certificate fingerprint is advertised over mDNS and `/api/v1/info`. The Android
  client pins it at pairing time and refuses a later mismatch (this is what stops T9).
- When bound to loopback only (`--bind 127.0.0.1`), plaintext is acceptable and TLS is
  off by default; a warning is logged.
- When bound to a LAN address without TLS, the host logs a prominent warning and refuses
  to pair unless `--insecure-allow-plaintext` is passed. This is the one place where the
  user must explicitly opt out of a security control.

## 6. Credentials at rest

- Device tokens: only `sha256(token)` is stored, in `devices.json` (0600). The plaintext
  is returned exactly once, at pairing.
- Comparison uses `subtle.ConstantTimeCompare`.
- PINs are held in memory only, never written to disk, and zeroed when consumed.
- `devices.json` and `audit.jsonl` are gitignored by the shipped `.gitignore`.

## 7. Audit log

Append-only JSONL, one record per event:

```json
{"ts":1780000000123,"event":"pair.attempt","device_id":"android-7c1f","ip":"192.168.1.44","ok":false,"reason":"invalid_pin"}
{"ts":1780000000456,"event":"pair.success","device_id":"android-7c1f","ip":"192.168.1.44","scopes":["keyboard","mouse"]}
{"ts":1780000000789,"event":"action.run","device_id":"android-7c1f","type":"system.shutdown","ok":false,"reason":"forbidden"}
{"ts":1780000000999,"event":"device.revoked","device_id":"android-7c1f","by":"cli"}
```

Never contains: tokens, PINs, full command lines of `run_command` (only the first token
of argv, to avoid logging secrets embedded in arguments), or file contents.

## 8. User-facing risk summary

To be reproduced in `README.md` so it is not buried here:

> Pairing a device grants it real control of your keyboard, mouse, and (if you allow it)
> shell on this machine. Only pair devices you own, on networks you trust, and keep
> `system.power` and `scripts` scopes off unless you need them. Anyone who can read
> `~/.config/mobiledeck/devices.json` cannot use the hashes to connect, but anyone who
> can reach the host *and* has a token can drive the machine. Revoke lost devices with
> `mobiledeck devices revoke <id>`.

## 9. Security test matrix

Implemented in `host/internal/*_test.go` and `host/internal/server/security_test.go`.

| Test | Asserts |
|------|---------|
| `TestWSRejectsMissingToken` | Close 4401, no action executed. |
| `TestWSRejectsRevokedToken` | Close 4403, audit record written. |
| `TestPairBruteForceLockout` | 6th attempt rejected and PIN invalidated. |
| `TestPairPinSingleUse` | Second use of a consumed PIN fails. |
| `TestPairPinExpiry` | Expired PIN fails. |
| `TestScopeEnforcement` | `scripts` action without scope → `forbidden`, engine not invoked. |
| `TestPathTraversalRefused` | `../../etc/passwd` refused. |
| `TestOversizedFrameRejected` | >1 MiB rejected without allocation blowup. |
| `TestMalformedFramesCloseSession` | 10 bad frames → close 4400. |
| `TestRateLimit` | 61st request in a burst → `rate_limited`. |
| `TestTokenNotLogged` | Token string never appears in log output. |
| `TestActionCancelStopsExecution` | Cancelled macro stops within one step boundary. |
