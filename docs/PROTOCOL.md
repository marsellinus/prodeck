# MobileDeck Protocol v1

Status: **frozen for Milestone 1**.
This document is the normative contract between the Android client and the host agent.
Any behaviour not described here is unspecified and must not be relied upon.

---

## 1. Design rules

1. **One transport, one envelope.** Everything the client and host exchange after the
   handshake travels as a single JSON envelope over one WebSocket text frame.
2. **Versioned.** Every envelope carries `v`. A peer MUST reject envelopes whose `v` it
   does not implement with an `error` reply and MUST NOT close the connection.
3. **Correlated.** Every request carries an `id`. Every reply carries `reply_to` equal to
   that `id`. Replies never reuse the request `id` field.
4. **Typed by string, extensible by namespace.** Message and action types are dotted
   strings (`button.press`, `obs.start_streaming`). A peer that does not know a type MUST
   answer `error{code:"unsupported"}`; it MUST NOT guess.
5. **Transport-agnostic.** The envelope is independent of whether it travelled over LAN
   WebSocket, USB, or a future transport (see ADR-0008).
6. **No secrets in URLs.** Tokens travel inside `hello` frames, never in query strings,
   because query strings are logged by intermediaries and by the host itself.

### 1.1 Encoding

Milestone 1 uses **JSON (UTF-8, text frames)**.

Rationale: the contract is inspectable with `websocat`/browser devtools, and the Android
side needs no codegen. The envelope reserves the option of MessagePack: if a future client
sets `hello.payload.encoding = "msgpack"`, the host MAY switch to binary frames for that
session only. Clients MUST ignore this field in v1.

### 1.2 Time

All timestamps are **milliseconds since the Unix epoch, UTC**, sent as integers.
`ts` is informational (sender clock). Host and client clocks are not assumed to be
synchronised; the host MUST NOT reject a message for clock skew, and MUST NOT use `ts`
for ordering. Ordering is per-connection, guaranteed by WebSocket framing.

### 1.3 Envelope

```json
{
  "v": 1,
  "id": "01J8Z9Q0M8Y0V4S2K6B7N3P1XA",
  "reply_to": "01J8Z9Q0M8Y0V4S2K6B7N3P1XB",
  "type": "button.press",
  "ts": 1780000000123,
  "payload": { }
}
```

| Field      | Type   | Required | Notes |
|------------|--------|----------|-------|
| `v`        | int    | yes      | Protocol version. Currently `1`. |
| `id`       | string | on requests | Sender-generated, unique per session. Crockford base32 ULID recommended; max 64 bytes. |
| `reply_to` | string | on replies | `id` of the message being answered. Absent on unsolicited events. |
| `type`     | string | yes      | Dotted namespace. Max 64 bytes. |
| `ts`       | int    | yes      | Sender clock, ms epoch. |
| `payload`  | object | yes      | Type-specific. MUST be an object, never `null`. |

`payload` is omitted from the examples below for brevity where irrelevant.

---

## 2. Connection lifecycle

```
client                                   host
  |  TCP + TLS (optional) + WS upgrade     |
  |--------------------------------------->|
  |  hello          ---------------------->|   (must arrive within 10 s)
  |                 <---------------------- welcome
  |  ... requests / replies / events ...    |
  |  ping (every 15 s) ------------------->|
  |                 <---------------------- pong
```

### 2.1 Endpoints

| Path | Method | Auth | Purpose |
|------|--------|------|---------|
| `/ws` | GET (upgrade) | token in `hello` | Realtime channel. |
| `/api/v1/info` | GET | none | Host identity, protocol range, TLS fingerprint, pairing state. Used to verify a discovered host. |
| `/api/v1/health` | GET | none | Liveness for the CLI and for `scripts/`. |
| `/api/v1/pair` | POST | PIN | Exchange a one-time PIN for a device token. |
| `/api/v1/admin/*` | various | admin token | Local administration (device revoke, profile CRUD). Bound to loopback by default. |

`/api/v1/info` response:

```json
{
  "host_id": "6f1c0e2a9b3d4e5f",
  "host_name": "cel-laptop",
  "os": "windows",
  "agent_version": "0.1.0",
  "protocol": { "min": 1, "max": 1 },
  "tls": { "enabled": true, "fingerprint_sha256": "ab:cd:..." },
  "pairing": { "open": true, "method": "pin", "expires_in_s": 118 },
  "profiles": { "count": 2, "revision": "sha256:5f2c..." }
}
```

### 2.2 `hello` (client → host, first frame)

```json
{
  "device_id": "android-7c1f9a2b",
  "token": "b64url-43-chars",
  "client": {
    "platform": "android",
    "app_version": "0.1.0",
    "model": "Pixel 7",
    "screen": { "w": 1080, "h": 2400, "density": 2.625, "orientation": "portrait" }
  },
  "resume": { "profile_id": "development", "page_id": "home" }
}
```

An unpaired client sends the same frame with `"token": ""` and MUST receive
`error{code:"unauthenticated"}` before it may call `/api/v1/pair`. The host MUST close
with code **4401** if the first frame is not a `hello`, or if no frame arrives in 10 s.

### 2.3 `welcome` (host → client)

```json
{
  "session_id": "s-9f2e...",
  "host": { "id": "6f1c0e2a9b3d4e5f", "name": "cel-laptop", "os": "windows", "version": "0.1.0" },
  "device": { "id": "android-7c1f9a2b", "name": "My Phone", "scopes": ["keyboard","mouse","media","apps","scripts","system.read","profiles.write"] },
  "server_time": 1780000000123,
  "heartbeat_interval_ms": 15000,
  "idle_timeout_ms": 45000,
  "features": ["telemetry", "profile.hotreload", "action.cancel"],
  "profiles_revision": "sha256:5f2c..."
}
```

`features` is the negotiation surface: a client MUST gate optional behaviour on it and
MUST tolerate unknown entries.

### 2.4 Heartbeat

Client sends `ping` every `heartbeat_interval_ms`. Host replies `pong` echoing
`payload.echo`. Host closes with **4408** after `idle_timeout_ms` without any frame.
A host MAY also send `ping`; the client MUST answer `pong`.

### 2.5 Close codes

| Code | Meaning | Client behaviour |
|------|---------|------------------|
| 1000 | Normal. | Do not reconnect. |
| 1013 | Host busy / shutting down. | Reconnect with backoff. |
| 4400 | Malformed first frame. | Do not reconnect; surface a bug. |
| 4401 | Unauthenticated / token revoked. | Wipe token, return to pairing. |
| 4403 | Device disabled by the host. | Do not reconnect; show "revoked by host". |
| 4408 | Idle timeout. | Reconnect immediately, once. |
| 4429 | Rate limited. | Reconnect with backoff, honour `Retry-After` in the close reason. |

---

## 3. Pairing

PIN-based, host-initiated. The host never accepts a device it was not told to accept.

```
host                                     client
 |  user runs `mobiledeck pair`           |
 |  -> prints PIN 482913, TTL 120 s       |
 |                                        |  GET /api/v1/info        (verify fingerprint)
 |                                        |  POST /api/v1/pair
 |  <- { pin, device:{id,name,platform,model} }
 |  validates: TTL, single use,           |
 |  constant-time compare, IP rate limit  |
 |  -> { device_id, token, scopes, host } |
 |  PIN is now dead.                      |
```

`POST /api/v1/pair` request:

```json
{
  "pin": "482913",
  "device": { "id": "android-7c1f9a2b", "name": "My Phone", "platform": "android", "model": "Pixel 7" }
}
```

Response `201`:

```json
{
  "device_id": "android-7c1f9a2b",
  "token": "kQ8v...",
  "scopes": ["keyboard","mouse","media","apps","scripts","system.read","profiles.write"],
  "host": { "id": "6f1c0e2a9b3d4e5f", "name": "cel-laptop" }
}
```

Failure `401`:

```json
{ "error": "invalid_pin", "message": "PIN is wrong, expired, or already used", "attempts_remaining": 3 }
```

Rules:

- PIN is 6 decimal digits, uniform random from a CSPRNG, TTL 120 s, **single use**.
- 5 consecutive failures for the same device id or source IP within 10 min force a new PIN.
- The host writes an audit record for every attempt (`audit.jsonl`).
- The token is 32 CSPRNG bytes, base64url (unpadded). The host stores only
  `SHA-256(token)` and compares in constant time. A token is shown exactly once.
- The host MAY require a host-side confirmation step for the *first* device
  (`pairing.require_confirm`). Milestone 1 keeps this off: the PIN itself is the
  confirmation, because only a user at the host can read it.

### 3.1 QR / mDNS-assisted pairing (reserved)

`GET /api/v1/pair/qr` returns `{ "payload": "mobiledeck://pair?h=192.168.1.10&p=8765&f=ab:cd:..&s=<one-time-secret>", "expires_in_s": 120 }`.
Reserved for Milestone 4; the client MUST NOT depend on it in M1.

---

## 4. Discovery

The client finds hosts without any internet access, in this order:

1. **mDNS / DNS-SD** — service type `_mobiledeck._tcp.local.`, TXT records
   `v=1`, `id=<host_id>`, `name=<host_name>`, `os=<windows|linux|darwin>`, `pair=<0|1>`.
2. **Manual entry** — `host:port`, always available.
3. **QR code** — reserved (see 3.1).

A discovered host is *candidate*, never *trusted*. Trust is established by the TLS
fingerprint check plus the PIN (see `docs/SECURITY.md`).

---

## 5. Profiles

A profile is a directory on the host: `profiles/<id>/profile.json` plus an `icons/`
directory. The file is the single source of truth; the client holds no authoritative
state. Profiles are meant to be committed to git.

```json
{
  "schema": 1,
  "id": "development",
  "name": "Development",
  "icon": { "type": "emoji", "value": "💻" },
  "theme": {
    "background": "#101014",
    "accent": "#4c8bf5",
    "button_background": "#1c1c22",
    "button_foreground": "#f2f2f5",
    "button_radius": 14,
    "font_scale": 1.0,
    "spacing": 8,
    "animation": "fade"
  },
  "settings": {
    "grid": { "columns": 4, "rows": 5 },
    "haptic": true,
    "nav": { "breadcrumb": true, "back_button": true, "page_tabs": true }
  },
  "root_page": "home",
  "pages": [
    {
      "id": "home",
      "name": "Home",
      "grid": { "columns": 4, "rows": 5 },
      "buttons": [
        {
          "id": "terminal",
          "label": "Terminal",
          "icon": { "type": "emoji", "value": "🖥️" },
          "background": "#202020",
          "foreground": "#ffffff",
          "cell": { "row": 0, "column": 0, "row_span": 1, "column_span": 1 },
          "state": { "type": "momentary" },
          "on_press": {
            "type": "launch_application",
            "params": {
              "target": { "windows": "wt.exe", "linux": "x-terminal-emulator", "darwin": "Terminal" }
            }
          },
          "on_long_press": null,
          "on_release": null,
          "permissions": ["apps"]
        }
      ]
    }
  ]
}
```

### 5.1 Action object

```json
{ "type": "<namespace>.<verb>", "params": { }, "on_error": "abort" }
```

`params` is validated by the action implementation, never by the core. `on_error` is
`"abort"` (default) or `"continue"` and only has meaning inside a macro.

Platform-varying parameters accept a **platform map** whose keys are `windows`, `linux`,
`darwin`; the host resolves the entry for its own platform and treats a missing key as
"this action is unsupported here".

### 5.2 Button state kinds

| `state.type` | Behaviour |
|--------------|-----------|
| `momentary`  | Visual press feedback only. |
| `toggle`     | Client flips locally, host confirms via `event.button.state`. |
| `radio`      | Exactly one button per `group` is on. |
| `status`     | Host-driven: `{ label, color, progress }`. |
| `progress`   | Host-driven 0..1 bar. |
| `counter`    | Host-driven integer. |
| `timer`      | Client counts up while pressed; host may override. |
| `telemetry`  | Bound to a host metric: `{ "metric": "cpu.usage", "format": "{value:.0f}%" }`. |

### 5.3 Messages

| Type | Dir | Payload |
|------|-----|---------|
| `profile.list` | C→H | `{}` |
| `profile.list.result` | H→C | `{ "profiles": [ { "id", "name", "icon", "pages", "revision" } ] }` |
| `profile.get` | C→H | `{ "profile_id": "development" }` |
| `profile.get.result` | H→C | `{ "profile": { ...document... }, "revision": "sha256:..." }` |
| `profile.set_active` | C→H | `{ "profile_id": "development" }` |
| `profile.reload` | C→H | `{ "profile_id": "development" }` — re-read from disk |
| `profile.export` | C→H | `{ "profile_id": "development" }` → `{ "json": "..." }` |
| `profile.import` | C→H | `{ "json": "...", "overwrite": false }` — requires `profiles.write` |
| `event.profile.changed` | H→C | `{ "profile_id", "revision" }` — hot reload; client re-fetches |

Profile documents are validated on load. A profile that fails validation is **not**
served; the host reports `invalid_profile` with the offending JSON pointer, so a broken
edit can never brick the deck.

---

## 6. Button interaction

### 6.1 `button.press` (C→H)

```json
{
  "profile_id": "development",
  "page_id": "home",
  "button_id": "terminal",
  "press": { "kind": "short", "count": 1 }
}
```

`press.kind` ∈ `short | long | repeat`. The client decides `long` via its own threshold
(default 400 ms) and sends **one** event per physical gesture; the host does not
duplicate gesture detection.

### 6.2 `button.release` (C→H)

Sent only for buttons whose `state.type` is `momentary`, `timer`, or `toggle` **and**
that declare an `on_release` action. Carries `profile_id`, `page_id`, `button_id`,
`held_ms`.

### 6.3 `action.result` (H→C)

```json
{
  "execution_id": "ex-01J8...",
  "action_id": "development/home/terminal",
  "action_type": "launch_application",
  "ok": true,
  "duration_ms": 42,
  "output": { "exit_code": 0, "stdout": "…", "stderr": "" }
}
```

`output` is action-specific and may be absent. `ok:false` carries
`"error": { "code": "not_found", "message": "…" }`.

Long-running actions (scripts, macros) reply **twice**: an immediate
`action.result` with `"accepted": true`, then a terminal
`event.action.finished` carrying `execution_id`. A client MAY send
`action.cancel { "execution_id": … }` in between; the host cancels cooperatively
(process kill, macro abort) and replies `action.result { ok:false, error.code:"cancelled" }`.

### 6.4 `event.button.state` (H→C)

```json
{
  "profile_id": "development",
  "page_id": "home",
  "button_id": "docker",
  "state": { "type": "status", "label": "12 Running", "color": "#22c55e", "progress": null }
}
```

The host pushes state only when the value actually changes, coalesced to at most
10 Hz per button.

---

## 7. Telemetry

`telemetry.subscribe` (C→H):

```json
{ "metrics": ["cpu.usage", "mem.used_pct", "disk.used_pct", "net.rx_bps", "net.tx_bps"], "interval_ms": 1000 }
```

`event.telemetry` (H→C):

```json
{ "ts": 1780000000123, "values": { "cpu.usage": 43.2, "mem.used_pct": 71.0, "net.rx_bps": 5242880 } }
```

Metric names are namespaced and plugin-extensible. The host samples at the requested
interval, clamped to `[250 ms, 60000 ms]`, and only while at least one client is
subscribed. An unavailable metric is simply absent from `values`; the client renders
`--`.

Milestone 1 metrics: `cpu.usage`, `mem.used_pct`, `mem.total_bytes`, `mem.used_bytes`,
`disk.used_pct`, `net.rx_bps`, `net.tx_bps`, `uptime_s`, `host.name`. `gpu.*` is a plugin
concern (phase 3).

---

## 8. Errors

```json
{ "reply_to": "…", "type": "error", "payload": { "code": "forbidden", "message": "device lacks scope 'system.power'" } }
```

| `code` | Meaning |
|--------|---------|
| `invalid_argument` | Payload failed validation. |
| `unauthenticated` | No valid token / revoked. |
| `forbidden` | Valid token, missing scope. |
| `not_found` | Unknown profile/page/button/action. |
| `unsupported` | Known type, not available on this host or platform. |
| `rate_limited` | Too many requests; `retry_after_ms` present. |
| `conflict` | Concurrent modification (e.g. profile import). |
| `cancelled` | Execution cancelled. |
| `action_failed` | Action ran and failed; `output` may carry stdout/stderr. |
| `invalid_profile` | Profile failed validation; `pointer` present. |
| `internal` | Bug. Reported with a correlation id in the host log. |

---

## 9. Permissions / scopes

A device token carries a scope set, fixed at pairing and editable by the host.

| Scope | Grants |
|-------|--------|
| `keyboard` | `keyboard.*` |
| `mouse` | `mouse.*` |
| `media` | `media.*`, `volume.*` |
| `apps` | `launch_application`, `open_url`, `open_folder`, `open_terminal` |
| `scripts` | `run_script`, `run_command` |
| `system.read` | telemetry |
| `system.power` | `system.shutdown`, `system.restart`, `system.sleep`, `system.lock` |
| `profiles.write` | `profile.import`, profile mutation |
| `plugins` | plugin-provided action namespaces |

The host enforces scopes **before** the action executes and rejects the whole request;
it never partially executes. High-risk scopes (`system.power`, `scripts`) are **not**
granted by default and additionally require `confirm: true` in the action params when
the profile sets `"require_confirmation": true`, which makes the client show a
confirmation sheet before sending.

---

## 10. Action catalogue (Milestone 1)

The type strings below are the wire contract: they are what a profile writes in
`"on_press": {"type": …}`. Two shapes exist and both are valid — a bare
identifier for core actions (`noop`, `macro`, `delay`, `launch_application`,
`open_url`, `open_folder`, `open_terminal`, `run_script`, `run_command`) and a
dotted namespace for grouped actions (`keyboard.shortcut`, `system.lock`) and for
plugin actions (`obs.start_streaming`). Types marked *(p)* need a scope.

### keyboard *(p: keyboard)*
| Type | Params |
|------|--------|
| `keyboard.shortcut` | `keys: ["CTRL","SHIFT","P"]` or a platform map `{"windows":[…],"linux":[…]}`; `mode: "press"\|"down"\|"up"` (default `press`); `interval_ms` |
| `keyboard.text` | `text: "hello"`, `interval_ms: 0` |
| `keyboard.key` | `key: "ENTER"`, `mode` |

### mouse *(p: mouse)*
| Type | Params |
|------|--------|
| `mouse.click` | `button: "left"\|"right"\|"middle"`, `count: 1` |
| `mouse.move` | `dx`, `dy` (relative, pixels) |
| `mouse.move_absolute` | `x`, `y` (0..1 normalised per axis) |
| `mouse.scroll` | `dx`, `dy` (notches; positive `dy` = up) |

### applications *(p: apps)*
| Type | Params |
|------|--------|
| `launch_application` | `target` (string or platform map), `args: []`, `cwd`, `env: {}`, `detach` |
| `open_url` | `url` |
| `open_folder` | `path` |
| `open_terminal` | `cwd`, `command` (optional) |

### scripts *(p: scripts)*
| Type | Params |
|------|--------|
| `run_script` | `path`, `args: []`, `cwd`, `env: {}`, `timeout_ms`, `interpreter` |
| `run_command` | `command`, `shell: "auto"\|"sh"\|"bash"\|"cmd"\|"powershell"\|"pwsh"`, `cwd`, `env`, `timeout_ms` |

`run_script` resolves `path` relative to the profile directory, then to the host
`scripts/` directory. Absolute paths outside those roots are refused unless the
host is started with `--allow-absolute-paths`. **No privilege escalation is
performed** (see `docs/SECURITY.md` §4).

### media and volume *(p: media)*
`media.play`, `media.pause`, `media.play_pause`, `media.stop`, `media.next`,
`media.previous`, `media.now_playing`, `volume.up`, `volume.down`,
`volume.mute`, `volume.set { level: 0..100 }`.

### system
`system.stats { metric }` *(p: system.read)*, `system.info` *(p: system.read)*,
`system.lock`, `system.sleep`, `system.shutdown`, `system.restart`
*(p: system.power for the last four)*. The four power verbs are refused unless
the parameters contain `"confirm": true`.

### navigation
`deck.open_page { profile_id?, page_id }`, `deck.back {}`,
`deck.change_profile { profile_id }`, `deck.notify { message, level }`.
These carry no scope: they change nothing on the host.

### flow
| Type | Params |
|------|--------|
| `delay` | `ms` (max 600000) |
| `macro` | `steps: [action…]`, `stop_on_error: true`, `step_timeout_ms`, `total_timeout_ms` |
| `noop` | `{}` |

`macro` is deliberately **not** a separate DSL: its steps are ordinary action
objects, so a macro can nest a macro, and any future action — including a plugin
action — is usable inside a macro for free.

### plugin actions
`"type": "<plugin>.<verb>"` resolves through the plugin registry (Milestone 3).
An unknown type is rejected **when the profile loads**, naming the JSON pointer,
rather than failing at press time: a button that looks configured and does
nothing is the worst possible failure mode for a deck.

---

## 11. Compatibility policy

- Additive changes (new optional fields, new message types, new action types, new
  scopes) do **not** bump `v`.
- Removing a field, changing a type, or changing the meaning of an existing field
  **does** bump `v`.
- The host advertises `protocol.min`/`protocol.max` in `/api/v1/info`; the client refuses
  to pair when the ranges do not intersect.
- Clients MUST ignore unknown fields in any payload they parse. This is what makes
  additive evolution safe, and it is covered by a test.
