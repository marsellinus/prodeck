# Architecture

Status: Milestone 1 implemented, plus the desktop control panel (ADR-0011,
Milestone 2) and the soundboard, tray and browser panel (ADR-0012, ADR-0013,
Milestone 3). See `docs/adr/` for the decisions behind them.

---

## 1. Shape of the system

```
┌──────────────────────────────┐
│ Android client               │
│  Compose grid UI             │
│  DeckStore (in-memory)       │
│  DeckClient (protocol)       │
│  Transport: LAN WebSocket    │
└──────────────┬───────────────┘
               │  JSON envelope over WS  (docs/PROTOCOL.md)
               │  mDNS discovery + REST pairing
┌──────────────▼───────────────┐
│ Host agent (single Go binary)│
│  internal/server   HTTP+WS   │
│  internal/auth     pairing   │
│  internal/profiles registry  │
│  internal/profile  documents │
│  internal/engine   actions   │
│  internal/platform adapters  │
│  internal/gui      panel     │
│  internal/tray     icon      │
│  internal/sounds   files     │
│  internal/telemetry metrics  │
│  internal/store    disk      │
└──────────────┬───────────────┘
               │  platform adapters
   ┌───────────┼───────────┬────────────┐
   ▼           ▼           ▼            ▼
 keyboard    mouse      launcher      scripts
 (SendInput) (SendInput) (exec/open)  (exec)

 media       sound       power       metrics
 (MPRIS/…)   (default    (logind/    (host
             output)     shutdown)   stats)
```

The host is **one process**, not a set of daemons. It embeds an HTTP server, a
WebSocket endpoint, an mDNS responder, and a scheduler. There is no message broker, no
database, and no Docker requirement: `./mobiledeck run` must work on a bare machine
(user requirement §35).

## 2. Layering rules

The host follows a strict dependency direction. Anything violating it is a bug.

```
cli  →  server  →  engine  →  platform
         │           │
         ├── auth    ├── profile
         ├── store   ├── telemetry
         ├── proto   └── plugin (M3)
         └── gui     (the panel at GET /; no CGO, no window)
```

- `internal/proto` — pure data. No I/O, no globals. Depends on nothing but stdlib.
- `internal/profile` — pure data + validation. No I/O beyond `Load`/`Save` via an
  injected `fs.FS`, so it is unit-testable without touching disk.
- `internal/platform` — where the host's OS capabilities live. Interfaces declared in
  `platform/api.go`, implementations in per-OS files behind build tags. The window
  and tray (`internal/gui`, `internal/tray`) and process management
  (`internal/cli`'s `detach_*`/`proc_*` and `openInBrowser`) also carry
  build-tagged per-OS files, each paired with a counterpart for the platforms it
  does not support; `CONTRIBUTING.md` §2 lists the exceptions.
- `internal/engine` — orchestration. Talks to `platform` through interfaces and to
  plugins through a registry. **Never** imports an OS package, never touches `os/exec`
  directly (that is `platform.Shell`).
- `internal/server` — HTTP/WS edge. Owns no domain logic; it validates envelopes,
  checks scopes, and calls the engine.
- `internal/gui` — the panel document and, on Windows with CGO, the native window
  around it. `internal/server` imports it to serve the same page to a browser at
  `GET /`; the package does not import `server` back (the window is a client of the
  admin API, like the CLI).
- `internal/cli` — argument parsing and process lifecycle only.

Consequence: porting to macOS means adding files under `internal/platform/`, plus a
build-tagged counterpart in the UI packages if the port needs a window or a tray.
Consequence: adding an action means touching one registry, not the server.

## 3. Host packages

| Package | Responsibility | Key types |
|---------|----------------|-----------|
| `internal/proto` | Envelope, message-type constants, action-type constants, error codes, encode/decode + version gate | `Envelope`, `ErrorPayload` |
| `internal/profile` | Profile/page/button/action model, JSON schema validation, revisions, hot reload | `Profile`, `Page`, `Button`, `Action`, `Theme` |
| `internal/profiles` | The host-authoritative profile registry: discovery, validation on load, file watching, revisioning. Distinct from `internal/profile`, which is the document model | `Registry`, `Entry` |
| `internal/auth` | Pairing PIN lifecycle, token issue/verify (SHA-256 + constant time), device records, scopes, per-device rate limiting, audit log | `Manager`, `Device`, `Scope` |
| `internal/engine` | Action dispatch, macro execution, cancellation, per-button state, event fan-out | `Engine`, `Registry`, `Execution` |
| `internal/platform` | OS abstraction for input, launching, shell, media, sound playback, power, metrics | `Input`, `Launcher`, `Shell`, `Media`, `Sound`, `Power`, `Metrics` |
| `internal/sounds` | What a sound file is: accepted extensions, MIME types, name sanitising, format sniffing | `MaxFileBytes` |
| `internal/server` | HTTP routes, WS upgrade, session pump, mDNS advertising, admin API, the panel at `GET /` | `Server`, `Session` |
| `internal/gui` | The control panel: the native window (CGO, Windows) and the browser-served page. `panel.go` has no build constraints, so a CGO-free binary still serves the page | `Options`, `Run` |
| `internal/tray` | The notification-area icon that keeps the host running when the window is closed. Windows + CGO only; a no-op elsewhere | `Options`, `Callbacks` |
| `internal/telemetry` | Metric registry, sampling loop, subscriber fan-out | `Collector` |
| `internal/store` | On-disk layout, atomic writes, file locking, log rotation | `Store` |
| `internal/app` | Assembles a runnable host from a configuration, so the CLI and the GUI wire it identically | `Host` |
| `internal/cli` | Subcommands, service install helpers, log tailing | `Run` |
| `internal/logging` | Structured `log/slog` setup, human + JSON sinks, in-memory ring for `mobiledeck logs` | `Setup`, `Ring` |

### 3.1 Action registry, not a switch

An action is:

```go
type Action interface {
    Type() string                       // "keyboard.shortcut"
    Scope() auth.Scope                  // auth.ScopeKeyboard
    Validate(params json.RawMessage) error
    Run(ctx context.Context, req Request) (Result, error)
}
```

Actions are registered into `engine.Registry` at start-up. `Registry.Lookup("docker.start")`
first checks core actions, then the plugin registry. A core action never contains
plugin-specific logic and a plugin never contains a `switch` over action names — this is
the rule that keeps the plugin system from degenerating into one big `if/else`
(user requirement §40).

Adding a **plugin-provided** action in M3 means implementing `Action` in a plugin
package and calling `registry.Register`. No core file changes.

### 3.2 Macro is a normal action

`macro` is registered like any other action and its `steps` are ordinary action objects.
That single decision buys: nesting, cancellation, per-step timeouts, and forward
compatibility with every action added later, without a macro DSL. Cancellation flows
from `context.Context`; every action MUST honour `ctx.Done()`.

### 3.3 Concurrency model

- One goroutine per WebSocket session; reads are serialised per session.
- The engine executes actions on a bounded worker pool
  (`engine.max_concurrent_actions`, default 8; a config field, not a flag). Queue
  overflow replies `rate_limited` rather than blocking the reader.
- Each execution has an `execution_id`; a per-device map holds cancel functions.
- Session state (active profile, subscriptions) lives in the `Session`, guarded by a
  mutex; the engine never reaches into a session.
- Event fan-out goes through a per-session buffered channel. A slow client is dropped
  from the fan-out (and only the fan-out) after its buffer fills, so one bad phone
  cannot stall the host.

## 4. Android app

Single module, MVVM-ish, no DI framework (Koin/Hilt would be ceremony at this size).

```
MainActivity
  └── AppNavHost          connect → deck
        ├── ConnectScreen   discovery list, manual host, PIN entry, status
        └── DeckScreen      header, grid, page tabs, breadcrumb, settings sheet

data/
  DeckClient       protocol state machine: hello/welcome, request map, events
  Transport        interface; WsTransport (OkHttp) is the only M1 impl
  Discovery        interface; NsdDiscovery (NsdManager) + manual
  PairingApi       REST client for /info and /pair
  DeckStore        StateFlow<DeckUiState> — the single source of truth for the UI
  SecureStore      EncryptedSharedPreferences/Keystore for the token
  DeckCache        profile JSON on disk, so the grid survives a dead network
ui/                pure Compose, no networking, reads DeckStore only
```

Rules:

- Compose code never constructs an HTTP client; it calls `DeckStore` intents.
- `DeckClient` never touches Android UI classes; it is testable on the JVM.
- The UI renders whatever profile JSON the host sent. There is **no** compiled-in
  button, no compiled-in grid size, and no hardcoded host (user requirement §40).
  Adding a button on the host must never require an app release.
- Offline behaviour: the last profile is cached, so a dead network shows a grey
  "Reconnecting…" banner over a usable, non-interactive grid instead of a blank screen.

## 5. Data on disk

```
~/.config/mobiledeck/            (Linux, XDG)     %APPDATA%\MobileDeck\   (Windows)
├── config.json            host settings: port, bind, TLS, name, timeouts
├── devices.json           device records: id, name, sha256(token), scopes, timestamps
├── audit.jsonl            append-only security log
├── profiles/              git-friendly, one dir per profile
│   └── development/
│       ├── profile.json
│       └── icons/
├── sounds/                audio files a soundboard pad may play
└── logs/
    ├── mobiledeck.log     rotated
    └── mobiledeck.pid     pidfile for start/stop/status
```

`profiles/` is the git-tracked artefact (user requirement §24). `devices.json` and
`audit.jsonl` are machine-local secrets and MUST be gitignored.

## 6. Failure behaviour

| Failure | Behaviour |
|---------|-----------|
| Host killed | Client keeps cached grid, banner "Reconnecting…", exponential backoff 0.5→30 s with jitter. |
| Network changed | Backoff resets on a successful `welcome`; discovery re-runs. |
| Token revoked | Close 4401 → client wipes token, returns to pairing. Never loops. |
| Invalid profile on disk | That profile is not served; error names the JSON pointer. Other profiles keep working. |
| Plugin panics | Recovered at the plugin boundary, plugin marked unhealthy, host stays up. |
| Slow client | Dropped from event fan-out only; session stays alive. |
| Port already bound | Start-up fails with a clear message naming the port and the owning pid if discoverable. |

## 7. What is deliberately absent

Recorded so nobody mistakes omission for oversight. Each has an owner phase in
`docs/ROADMAP.md`.

- TLS is **implemented but optional** (`tls.enabled`, self-signed, fingerprint-pinned).
  Default on for LAN, off for loopback-only.
- No USB transport: the `Transport` abstraction exists on both sides, with one
  implementation (LAN WS). A half-built USB path would be worse than none (§13).
- A visual layout editor **exists** now, in the desktop control panel
  (`internal/gui`, ADR-0011). The profile JSON remains a supported editor: the host
  validates and hot reloads, which is what makes hand-editing safe, and the panel
  writes the same files rather than a second storage format.
- No plugin marketplace, no auto-update, no cloud anything (§37, §38 phase 5).
