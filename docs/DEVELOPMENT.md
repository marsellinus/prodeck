# Development

Building and running MobileDeck from source.

---

## 1. Repository layout

```
.
├── docs/                 protocol, architecture, security, ADRs
├── host/                 Go host agent (module github.com/mobiledeck/mobiledeck/host)
├── android/              Kotlin + Compose client
├── profiles/             git-tracked deck layouts (one directory per profile)
├── examples/profiles/    documentation for the profile format
└── scripts/              developer scripts
```

Repository state: `host/internal/` contains `app`, `auth`, `cli`, `config`,
`engine`, `gui`, `icons`, `logging`, `platform`, `profile`, `profiles`, `proto`,
`server`, `sounds`, `store`, `telemetry`, `tlsutil`, and `tray`. The
`cmd/mobiledeck` entrypoint wires them together. `docs/ARCHITECTURE.md` §3
describes what each package is for.

---

## 2. Host agent (Go)

Requires Go 1.26 or newer. The module is `github.com/mobiledeck/mobiledeck/host`.

```sh
cd host
go build ./...          # compile every package
go test ./...           # unit tests
go run ./cmd/mobiledeck run
```

`go run ./cmd/mobiledeck run` starts the host, opens the WebSocket listener on
port **8765**, advertises `_mobiledeck._tcp.local.` over mDNS, and generates a
self-signed TLS certificate on first run. Stop it with Ctrl+C.

Build a release binary:

```sh
cd host
CGO_ENABLED=0 go build -trimpath -o mobiledeck ./cmd/mobiledeck
```

`CGO_ENABLED=0` keeps cross-compilation working
(`GOOS=windows|linux|darwin go build`), per ADR-0001. Such a binary has no native
window and no notification-area icon; it still serves the control panel to a
browser, so `mobiledeck run` plus `mobiledeck token --open` is a complete panel
with no C toolchain at all.

### 2.1 Docker

The agent never requires Docker; running the binary natively is the supported
path. There is an optional container image for the subset of actions that do not
touch the desktop session (`host/Dockerfile`, the `agent` service in
`docker-compose.yml`). Building, running, and the exact list of what does and
does not work in a container are in
[`docs/DEPLOYMENT.md`](DEPLOYMENT.md) §4.

### 2.2 Multi-device

Several phones may connect at once, each with its own device record, token and
scope set. Per-device scopes, the concurrent-client cap, revoke vs disable, and
a worked two-phone example are in
[`docs/MULTI_DEVICE.md`](MULTI_DEVICE.md).

---

## 3. Android client (Kotlin + Compose)

Requires a JDK (17, 21 tested) and the Android SDK with `compileSdk 36`. The
Gradle wrapper pins its own JDK through `org.gradle.java.home` in
`android/gradle.properties`, so `JAVA_HOME` is not consulted.

```sh
cd android
./gradlew assembleDebug
```

On Windows use `gradlew.bat`. Install the debug APK on a connected device or
emulator:

```sh
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

`minSdk 26` (Android 8.0) and `targetSdk 36` per ADR-0002.

---

## 4. Running the host

### 4.1 Linux

```sh
cd host
go run ./cmd/mobiledeck run
```

Input actions need access to the session's input stack: an X11 or Wayland
socket owned by the logged-in user. Running as root is neither required nor
supported (SECURITY.md §4). On a headless machine without a display,
`keyboard.*` and `mouse.*` report `unsupported` rather than failing silently.

The control panel does not need a display: `GET /` serves it to a browser, so
`http://127.0.0.1:8765/` with the key from `go run ./cmd/mobiledeck token` is a
usable panel on a headless box. The native window (`gui`) is the part that needs
a desktop session and CGO; see [`GUI.md`](GUI.md).

### 4.2 Windows

```powershell
cd host
go run ./cmd/mobiledeck run
```

Run it in the interactive session whose keyboard and mouse you want to control.
Input injection uses `SendInput`, which is session-aware and requires no
elevation. The first run may raise a Windows Firewall prompt; allow the
**Private** network profile only.

### 4.3 macOS

```sh
cd host
go run ./cmd/mobiledeck run
```

Keyboard and mouse injection requires the Accessibility permission for the
terminal (or binary) running the host, granted in
System Settings → Privacy & Security → Accessibility. Without it the OS refuses
the injection and the host reports a permission error naming the requirement.

### 4.4 Custom configuration directory

The host stores `config.json`, `devices.json`, `audit.jsonl`, `profiles/`,
`sounds/`, `logs/`, and `tls/` under the per-user config directory
(`%APPDATA%\mobiledeck` on Windows, `$XDG_CONFIG_HOME/mobiledeck` elsewhere).
Override it for a throwaway instance:

```sh
go run ./cmd/mobiledeck run --config-dir ./.devdata
```

`scripts/dev-host.sh` and `scripts/dev-host.ps1` do exactly this, with
`--bind 127.0.0.1` so the instance is not reachable from the LAN.

---

## 5. CLI surface

```
mobiledeck gui       Start the host and open the desktop control panel.
mobiledeck run       Start the host in the foreground.
mobiledeck start     Start the host in the background (pidfile in logs/).
mobiledeck stop      Stop the background host.
mobiledeck restart   Stop then start.
mobiledeck status    Report whether the host is running and on which port.
mobiledeck token     Print the key the browser panel asks for; `--open` opens the panel with it.
mobiledeck pair      Print a one-time PIN for pairing a new device.
mobiledeck devices   List paired devices; `devices revoke <id>` removes one.
mobiledeck profiles  List profiles; subcommands manage profile files.
mobiledeck logs      Tail the host log ring buffer / log file.
mobiledeck keys      List the canonical key names accepted by profiles.
mobiledeck actions   List every action type this host provides.
mobiledeck doctor    Check the environment and report what will not work.
mobiledeck init      Create a default configuration without starting anything.
mobiledeck version   Print the agent version and protocol range.
```

Flags are accepted after the subcommand. The documented ones are:

| Flag | Effect |
|------|--------|
| `--config-dir <path>` | Use this directory instead of the per-user config directory. |
| `--config <path>` | Use this configuration file (default `<config-dir>/config.json`). |
| `--bind <addr>` | Listen address. `0.0.0.0` (default) exposes the LAN; `127.0.0.1` is loopback only. |
| `--port <n>` | TCP port to listen on (default 8765; `0` asks the OS for a free one). |
| `--name <name>` | Host name shown to clients during discovery and pairing. |
| `--log-level <level>` | `debug`, `info`, `warn` or `error`. |
| `--log-format <fmt>` | `text` or `json`. |
| `--no-tls` | Disable TLS (only sensible when bound to loopback). |
| `--insecure-allow-plaintext` | Permit pairing over an unencrypted LAN socket. Off by default; see SECURITY.md §5. |
| `--allow-absolute-paths` | Let `run_script`/`open_folder` reference paths outside the profile and `scripts/` directories. Off by default. |

Two commands add a flag of their own:

| Command flag | Effect |
|--------------|--------|
| `gui --no-tray` | Close the window and stop the host, instead of leaving it running in the notification area. |
| `token --open` | Open the control panel in a browser with the key already in the URL fragment, so it does not have to be pasted. |

Concurrency limits are not flags: set `session.max_clients` (default 16) and
`engine.max_concurrent_actions` (default 8) in `config.json`.

`mobiledeck keys` is backed by `platform.KeyNames()`, so it always lists exactly
the keys the key table accepts.

---

## 6. Connecting over USB

There is no separate USB transport in Milestone 1 (ADR-0008). Two paths work
today with the existing LAN WebSocket transport:

**ADB port forwarding** (cable, USB debugging enabled):

```sh
adb reverse tcp:8765 tcp:8765
```

The phone's `127.0.0.1:8765` is then the host's `127.0.0.1:8765`. Use this
against a host bound to loopback.

**USB tethering** (no `adb` needed): enable USB tethering on the phone; the
host and phone are on a private IP link and the normal LAN discovery /
manual-entry path applies.

A native USB transport (AOA / ADB protocol) is a Phase 5 item and is not
implemented.

---

## 7. Writing a profile by hand

A profile is a directory `<profiles_dir>/<id>/` containing `profile.json` and an
`icons/` directory (ADR-0007). Add it under the config directory's `profiles/`
and reload with `profile.reload` or restart the host.

Minimal profile:

```json
{
  "schema": 1,
  "id": "demo",
  "name": "Demo",
  "icon": { "type": "emoji", "value": "🖥️" },
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
      "buttons": [
        {
          "id": "terminal",
          "label": "Terminal",
          "icon": { "type": "emoji", "value": "⌨️" },
          "cell": { "row": 0, "column": 0, "row_span": 1, "column_span": 1 },
          "state": { "type": "momentary" },
          "on_press": {
            "type": "launch_application",
            "params": {
              "target": {
                "windows": "wt.exe",
                "linux": "x-terminal-emulator",
                "darwin": "Terminal"
              }
            }
          },
          "permissions": ["apps"]
        }
      ]
    }
  ]
}
```

Rules that matter when hand-editing:

- Unknown fields are rejected. A typo is an error naming the JSON pointer, never
  a silently ignored setting.
- Every action type must be registered on this host, otherwise the profile is
  refused at load time.
- Grid bounds: `columns`/`rows` are capped at 16 each; a page holds at most 512
  buttons (see `internal/profile`).
- `target` may be a plain string or a platform map. A platform map with no entry
  for the running OS makes the action unsupported here, which is deliberate.
- The full document reference is `docs/PROTOCOL.md` §5; the canonical example is
  `profiles/development/profile.json`.

Once the plugin runtime lands (Milestone 3), plugin-provided action types are
validated the same way. See `docs/PLUGIN_DEVELOPMENT.md`.

---

## 8. Tests

### 8.1 Host

```sh
cd host
go test ./...                 # all packages
go test ./internal/engine/... # one package
go vet ./...
```

The security test matrix (`docs/SECURITY.md` §9) lives in
`host/internal/*_test.go` — `internal/server/server_test.go` and
`internal/auth/auth_test.go` carry most of it. Key
invariants covered there: 4401/4403 close codes, PIN lockout, PIN single use and
expiry, scope enforcement, path traversal refusal, oversized-frame rejection,
rate limiting, and that tokens never reach the log.

### 8.2 Android

```sh
cd android
./gradlew test                       # JVM unit tests (DeckClient, DeckStore)
./gradlew connectedDebugAndroidTest  # instrumented tests on a device/emulator
```

The JVM tests are the ones that matter most: `DeckClient` is deliberately free
of Android UI classes so the protocol state machine is testable without a
device. `app/src/test` holds them (`DeckClientTest`, `ProtocolTest`,
`MetricFormatTest`, `IconDataUriTest`, and `RealHostIntegrationTest`, which is
skipped unless a host is reachable). There is no `app/src/androidTest` yet, so
`connectedDebugAndroidTest` has nothing to run; the JVM tests are the acceptance
command for a PR.

---

## 9. Emulator end-to-end test

The emulator is a separate virtual machine: its `localhost` is the emulator, not
the host. Reach the host through the emulator's alias for the host loopback,
`10.0.2.2`.

1. Start the host bound so the emulator can reach it:
   `go run ./cmd/mobiledeck run` (default `0.0.0.0:8765`).
2. Start an emulator (API 26+) and install the debug APK.
3. In the app, enter the host manually as `10.0.2.2:8765` when mDNS discovery
   does not surface the host (the emulator's multicast support is limited).
4. Pair with the PIN printed by `mobiledeck pair`.
5. Press a button and confirm the action lands on the host (a `keyboard.text`
   into a host editor is the quickest end-to-end proof).

The instrumented test suite is run with
`./gradlew connectedDebugAndroidTest`; it depends on a running emulator or
device.

---

## 10. Troubleshooting

**Port already in use.** The host exits naming the port and, when discoverable,
the owning pid. Find it with `lsof -i :8765` / `ss -ltnp` on Unix,
`netstat -ano | findstr :8765` on Windows. Either stop the other process or
change `port` in `config.json`.

**No X11 display.** `keyboard.*` / `mouse.*` return `unsupported`. The host must
run inside the graphical session whose input you want to drive; `DISPLAY` (and
`XAUTHORITY`, when applicable) must be set for that session. Running over SSH
does not give you the session's display.

**Windows firewall prompt.** Windows asks once per binary/network class. Allow
the Private profile; deny it if you only ever want loopback access, and bind to
`127.0.0.1` instead.

**Emulator cannot reach the host.** `localhost` inside the emulator is the
emulator itself. Use `10.0.2.2` for the host machine's loopback, or the host's
LAN address if it is bound to `0.0.0.0`.

**mDNS not visible.** Some networks (guest Wi-Fi, VLANs, APs with client
isolation) drop multicast. Enter the host manually: `host-address:8765`, and
verify the TLS fingerprint against `/api/v1/info` before pairing.

**Revoked token.** The host closes with 4403; the client wipes the token and
returns to pairing. Re-pair with `mobiledeck pair` and the new PIN. The device
record was deleted, so the old token cannot be re-admitted.

**Profile rejected with a pointer.** The error names the offending JSON pointer,
e.g. `/pages/0/buttons/2/on_press/params`. Fix that field; the previously
working profile keeps serving until the new one validates.
