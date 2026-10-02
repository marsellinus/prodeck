# Roadmap

Five phases. Each phase has deliverables, the single acceptance criterion that
makes it "done", and what it explicitly defers. Nothing is "done" because a
build succeeds: it is done when the criterion below is observable.

Terminology: **Phase 1** is the project's Milestone 1. The ADRs in `docs/adr/`
carry their milestone in the status line: `0001`–`0010` are Milestone 1,
`0011` is Milestone 2, and `0012`–`0013` are Milestone 3.

---

## Phase 1 — Android UI → WebSocket → host agent → keyboard shortcut

**Status: implemented.**

The vertical slice: a phone renders a host-supplied grid, pairs with a PIN, and a
button press injects a keyboard shortcut on the host. Reconnect and profiles are
part of the slice, not follow-ups.

### Deliverables (artefacts that exist)

- `docs/PROTOCOL.md` — the frozen v1 wire contract.
- `docs/ARCHITECTURE.md` — the system shape, layering rules, and the list of
  what is deliberately absent.
- `docs/SECURITY.md` — assets, trust boundaries, the T1–T14 threat table, and
  the security test matrix.
- `docs/adr/0001`–`0013` — host language, Android stack, JSON envelope, PIN
  pairing, action registry, TLS pinning, profile-as-directory, transport
  abstraction, single binary, license, desktop control panel, soundboard,
  tray and browser panel.
- `host/internal/proto` — envelope, message-type constants, error codes, the
  version gate, ULID generation.
- `host/internal/profile` — the profile/page/button/action model with strict
  validation and JSON-pointer errors.
- `host/internal/platform` — `Input`, `Launcher`, `Shell`, `Media`, `Sound`,
  `Power`, `Metrics` interfaces; the canonical key table; `CommandContext`
  process-group handling; the Windows SendInput implementation.
- `host/internal/engine` — the action registry, scope enforcement, the bounded
  worker pool, cancellation, per-button state, and event fan-out.
- `host/internal/auth` — PIN lifecycle, token issue/verify, device records,
  scopes, rate limiting, audit log.
- `host/internal/config` — the hand-editable, range-checked configuration.
- `host/internal/store` — atomic writes, file locking, pidfile, path resolution.
- `host/internal/logging` — structured `log/slog` setup with a ring buffer.
- `android/` — Kotlin + Compose client: grid, connect screen, pairing, the
  `DeckClient` protocol state machine, `DeckStore`, and offline caching.
- `profiles/development/` — the canonical example profile.

### Acceptance criterion

A phone that has never seen the host discovers it (mDNS or manual entry), pairs
with the PIN printed by `mobiledeck pair`, receives the profile, and pressing a
button injects the shortcut on the host. Killing and restarting the host leaves
the phone showing the cached grid with a "Reconnecting…" banner and it recovers
without re-pairing. Revoking the device returns it to pairing and never loops.

### Explicitly deferred

- No USB transport. `Transport` exists on both sides with exactly one
  implementation (LAN WebSocket), per ADR-0008.
- No plugin system.
- No telemetry history, no cloud, no auto-update.

*(The visual layout editor was deferred here and shipped in Phase 4; see below.)*

---

## Phase 2 — Mouse, launching, scripts, macros, pages, folders

**Status: core actions shipped in Milestone 1; the phase is the deepening of
that surface.**

Milestone 1 already registers `mouse.click`, `mouse.move`,
`mouse.move_absolute`, `mouse.scroll`, `launch_application`, `open_url`,
`open_folder`, `open_terminal`, `run_script`, `run_command`, `macro`, `delay`,
and the navigation actions `deck.open_page`, `deck.back`, `deck.change_profile`,
`deck.notify` (PROTOCOL.md §10).

### Deliverables

- Mouse control on all three platforms, including absolute positioning.
- Application launching with argv, working directory, and environment.
- URL and folder opening, and terminal emulators with a working directory.
- `run_script` and `run_command` with confinement, timeout, capture, and
  process-group teardown.
- `macro` as an ordinary action whose steps are action objects, with nesting,
  per-step timeout, and cancellation.
- Multi-page decks with folders, breadcrumbs, and back navigation.
- Auto-return to the root page after an action.

### Acceptance criterion

A profile with a mouse-pad page, a folder two levels deep, and a macro that
types text and launches an application runs end to end on Windows, Linux, and
macOS, and cancelling a running macro or script leaves no orphaned child
processes behind.

### Explicitly deferred

- Gamepad / MIDI input.
- Per-action scheduling and triggers (time, hotkey).
- Cross-device macros (a macro that spans two hosts).

---

## Phase 3 — Plugins, OBS, media, system monitoring, status buttons

**Status: media, volume, telemetry and the host-driven button states shipped in
Milestone 1; the plugin runtime and the plugin catalogue are Phase 3.**

Already shipped in the core: `media.play`, `media.pause`, `media.play_pause`,
`media.next`, `media.previous`, `media.stop`, `volume.up`, `volume.down`,
`volume.mute`, `volume.set`, `sound.play` and the soundboard around it (ADR-0012),
`system.stats`, the `cpu.*`/`mem.*`/`disk.*`/`net.*` telemetry metrics, and the
`status`, `toggle`, `radio`, `progress`, `counter`, and `telemetry` button states.

### Deliverables

- The plugin runtime: manifest loading, action registration through
  `engine.Registry`, panic recovery at the plugin boundary, and the `plugins`
  scope. The interface is frozen in `docs/PLUGIN_DEVELOPMENT.md`.
- The plugin catalogue built on it: OBS (scene and streaming control), media
  players (VLC, Spotify), Docker, and `gpu.*` telemetry.
- Status buttons driven by plugin state via `event.button.state`.
- An out-of-process plugin host for plugins not compiled into the binary.

### Acceptance criterion

A plugin can be dropped into `plugins/` as a directory with a `plugin.json`,
its actions validate and run, its button state reaches the client, and a panic
inside it marks it unhealthy while the host stays up and other actions keep
working. No core file is modified to add it.

### Explicitly deferred

- A plugin marketplace or registry (Phase 5, optional).
- Plugin signing and sandboxing beyond process-group isolation.
- Hot-reloading a plugin without restarting the host.

---

## Phase 4 — Visual layout editor, themes, import/export, multiple devices, advanced permissions

**Status: partly shipped.** The visual layout editor landed in Milestone 2 as the
desktop control panel (`docs/GUI.md`, ADR-0011) and it runs in a browser as well as
in a window (ADR-0013). Multiple paired devices were done in Milestone 1. Theme
editing in the editor and the advanced-permission flow are still to come.

### Deliverables

- A visual layout editor that edits the same `profile.json` files through the
  admin API, never a second storage format (ADR-0007).
  **Done** (Milestone 2): the panel's Board tab edits the grid the phone shows,
  at its real size — add, edit, delete, drag to move or swap — and writes through
  the admin API. Documented in [`GUI.md`](GUI.md).
- Theme editing with live preview; the theme model already exists in
  `internal/profile`. **Not yet**: the panel switches its own light/dark
  appearance but does not edit a profile's theme.
- Profile import/export as JSON, including the `profiles.write` scope path.
  **CLI only** (`mobiledeck profiles export` / `import`); not in the panel.
- Multiple paired devices with per-device scopes and names.
  **Done** (Milestone 1): several devices connect at once, each with its own
  record, token and scope set; `mobiledeck devices` lists, renames, enables,
  disables, revokes and re-scopes them; `session.max_clients` (default 16) caps
  concurrent sessions and refuses past it with HTTP 503 before the upgrade.
  Documented in [`MULTI_DEVICE.md`](MULTI_DEVICE.md).
- Advanced permission editing: per-device scope changes, `bind_ip`, and the
  `require_confirmation` flow surfaced in the client. The panel's Phones tab
  edits per-device scopes and turns a device on or off; revoking, renaming,
  `bind_ip` and `require_confirmation` are still CLI and profile-level.
- QR-assisted pairing (`GET /api/v1/pair/qr` is reserved in PROTOCOL.md §3.1).

### Acceptance criterion

A user creates a page, adds a button, picks an icon and a theme, and presses it
on the phone, without opening a text editor — and the result is a `profile.json`
that `git diff` renders readably.

### Explicitly deferred

- A hosted/shared profile gallery.
- Collaborative editing.

---

## Phase 5 — Hardening, packaging, and the optional set

**Status: not started.** Everything in the optional set is marked optional and
the project is fully usable without it.

### Deliverables

- **Native USB transport (AOA / ADB protocol).** Today USB works through
  `adb reverse` and USB tethering on the existing transport (ADR-0008). A true
  native transport is the deferred work.
- **Privileged helper.** A systemd unit / Windows service running as SYSTEM with
  a narrow IPC surface accepting only "power off" and "restart" verbs and no
  arguments, so `system.power` stops being refused on hosts that want it. It is
  explicitly not a general "run anything as root" bridge (SECURITY.md §4).
- **Flatpak packaging** for Linux desktop distribution.
- **Auto-update** for the host binary.
- **Packaging**: `.deb`, `.tar.gz`, and an installer, per ADR-0009.
  **Docker packaging done** (Milestone 1): `host/Dockerfile` is a multi-stage
  build producing a static binary on `debian:bookworm-slim`, running as the
  unprivileged user `mobiledeck`; the `agent` service in `docker-compose.yml`
  runs it with `network_mode: host`. It covers only the subset of actions that
  do not need the desktop session. Documented in
  [`DEPLOYMENT.md`](DEPLOYMENT.md) §4. The native packages (`.deb`, `.tar.gz`,
  installer) remain deferred.
- **Plugin marketplace / repository** — **optional**. Without it, plugins are
  directories copied into `plugins/`.
- **Cloud sync** — **optional, and never required**. The host must work with no
  internet access at all; cloud sync can never become a dependency of pairing,
  profiles, or actions (ARCHITECTURE.md §7).

### Acceptance criterion

The host installs from a package on a clean machine, runs as the logged-in user,
controls input, and survives a reboot as a user unit; the optional components
are absent and nothing breaks.

### Explicitly deferred

- Anything requiring a hosted service to be present for core functionality.
- Multi-host orchestration.
- Non-Android clients.
