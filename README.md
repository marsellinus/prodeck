# MobileDeck

Turn an Android phone into a Stream Deck for your laptop or desktop.

Your phone is the touch controller. A single Go binary on your machine is the
agent. They talk over your LAN — no cloud, no account, no subscription, no
internet connection required.

```
   ┌──────────────────┐                    ┌──────────────────────────┐
   │  Android phone   │   Wi-Fi (or USB    │  Host agent (one binary) │
   │  touch grid UI   │   tethering /      │  WebSocket server        │
   │  ┌──┬──┬──┬──┐   │   adb reverse)     │  action engine           │
   │  │  │  │  │  │   │ ◀────────────────▶ │  profile manager         │
   │  ├──┼──┼──┼──┤   │   JSON protocol    │  plugin registry (M3)    │
   │  │  │  │  │  │   │                    └────────────┬─────────────┘
   │  └──┴──┴──┴──┘   │                                 │
   └──────────────────┘              keyboard · mouse · apps · scripts
                                     media · system · OBS · anything
```

## Status

**Milestone 1 (Phase 1) is implemented and verified.** The host runs, pairs a
device, serves a profile, and executes actions; the Android client builds, pairs,
renders the grid, and presses buttons.

| Piece | State |
|-------|-------|
| Host agent (Go, Windows/Linux/macOS) | working, cross-compiles for 6 targets |
| Android client (Kotlin + Compose) | 53 tests pass, 9 against a real host; see the note below |
| Protocol v1 | frozen, documented in [`docs/PROTOCOL.md`](docs/PROTOCOL.md) |
| Pairing, tokens, scopes, rate limits, audit | working |
| Actions: keyboard, mouse, apps, scripts, media, soundboard, system, flow, navigation | 38 types |
| Profiles: load, validate, hot reload, export/import | working |
| Telemetry (CPU, RAM, disk, network) | working |
| mDNS discovery, TLS with fingerprint pinning | working |
| Desktop GUI with a layout editor | working, see [`docs/GUI.md`](docs/GUI.md) |
| Soundboard (press a tile, a sound plays on the host) | working; Windows plays wav, mp3, wma and midi, and names the formats it cannot |
| Plugins, OBS, USB transport | Phase 3–5, see [`docs/ROADMAP.md`](docs/ROADMAP.md) |

Verified on Windows 11: real `SendInput` keystrokes delivered to a real
application window, driven through the real protocol over a real WebSocket.

### What has and has not been verified on Android

Stated plainly, because "it builds" is not "it works".

**Verified against a real host.** 53 JVM tests pass, of which 9 drive the real
client stack — the real OkHttp `WsTransport`, the real `DeckClient` state
machine, the real protocol — against a real `mobiledeck` process. They cover:
reading `/api/v1/info`, pairing over REST, the `hello`/`welcome` handshake,
fetching a profile, **pressing a button and receiving `action.result`**, the
telemetry stream, `not_found` handling, a bogus token being refused with 4401, a
revoked token with 4401, a disabled device with 4403, and surviving the heartbeat
window. Start a host for them with `scripts/e2e-host.sh start`.

**Verified on a real device.** On a Xiaomi Mi A1 (LineageOS, Android 14) over
`adb reverse`, the app installs, launches with no crash, registers its mDNS
listener, accepts a typed `host:port`, reaches the host, reads its name and
identity, renders the pairing card with the fingerprint, and shows the host's
error message verbatim when a connection fails.

**Not verified.** Pressing a real button on a real device with a finger, and the
Compose rendering beyond what the UI dump shows. The blocker is `adb`: this
device's `adb shell input text` throws an internal `NullPointerException`, and
synthetic taps do not reliably move focus to a Compose text field, so pairing
cannot be completed from a script. Pair once by hand and the grid will render;
everything behind the press is covered by the JVM tests above.

Three real bugs were found by writing those tests, none of which any unit test
with a fake transport would have caught:

1. **The envelope was sent without its `v` field.** `encodeDefaults = false`
   dropped `version` because it equalled its own default, so the host rejected
   every frame at its version gate and closed the socket. The client could never
   have connected.
2. **The host's close code was discarded.** The reader ended the session with a
   hardcoded 1000, so a revoked device looked like a routine disconnect and the
   dead token was never cleared.
3. **A close during the handshake surfaced as an exception**, not as a refusal,
   so a revoked device produced a generic error instead of "pair again".

## Quick start

**Installing rather than hacking?** [`docs/INSTALL.md`](docs/INSTALL.md) is the
install page: the scripts (`sh scripts/install.sh`, `.\scripts\install.ps1`,
`sh scripts/install-android.sh`), the published one-liners, uninstall steps, and
Docker and Android notes. The manual commands below do the same thing by hand.

### 1. Build and run the host

With the desktop control panel:

```sh
cd host
go build -o mobiledeck ./cmd/mobiledeck
./mobiledeck gui
```

A window opens with the host status, a layout editor, device management and
pairing. The host starts inside it and stops when the window closes.

Or headless, for a server:

```sh
./mobiledeck run --bind 0.0.0.0
```

On first run this creates `~/.config/mobiledeck` (`%APPDATA%\mobiledeck` on
Windows), generates a TLS certificate, seeds an example profile, and starts
listening. You will see something like:

```
MobileDeck 0.1.0
  host       my-laptop (6f1c0e2a9b3d4e5f)
  listening  0.0.0.0:8765
  reachable  https://192.168.1.10:8765
  tls        on, fingerprint AB:CD:EF:...
  profiles   1 (development)
```

### 2. Install the Android app

```sh
cd android
./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

### 3. Pair once

```sh
./mobiledeck pair
```

```
  MobileDeck host: my-laptop

  PIN  4 8 2 9 1 3

  valid for 120 seconds; enter it on the phone now
```

On the phone, pick the discovered host (or type `192.168.1.10:8765`), enter the
PIN, and press **Pair**. After that the phone holds a device token and reconnects
by itself.

### 4. Use it

Tap a button. The laptop runs the action. Press the CPU tile and it shows the
real CPU load.

## Commands

```
mobiledeck gui            run the host and open the desktop control panel
mobiledeck run            run the host in the foreground
mobiledeck start|stop     run it in the background
mobiledeck status         where it listens, what is connected, whether pairing is open
mobiledeck pair           open a pairing window and print the PIN
mobiledeck devices        list / rename / enable / disable / revoke / re-scope
mobiledeck profiles       list / reload / export / import / validate
mobiledeck logs [-follow] recent log records
mobiledeck keys           every key name a profile may use
mobiledeck actions        every action type this host provides, with its required scope
mobiledeck doctor         check the environment and report what will not work
```

Run `mobiledeck <command> -h` for the flags of any command.

## Writing a profile

The easy way is the panel: `mobiledeck gui`, then the **Layout** tab. Click an
empty cell to add a button, pick an action from the host's own list, press Save,
and it appears on the phone. See [`docs/GUI.md`](docs/GUI.md).

A profile is also a directory of JSON, which is what makes a layout a reviewable
diff and lets it live in git.

```
~/.config/mobiledeck/profiles/development/profile.json
```

A button that opens a terminal:

```json
{
  "id": "terminal",
  "label": "Terminal",
  "icon": { "type": "emoji", "value": "🖥️" },
  "cell": { "row": 0, "column": 0 },
  "state": { "type": "momentary" },
  "on_press": { "type": "open_terminal", "params": {} }
}
```

A button that works on both Windows and Linux:

```json
{
  "id": "editor",
  "label": "Editor",
  "cell": { "row": 0, "column": 1 },
  "on_press": {
    "type": "launch_application",
    "params": {
      "target": { "windows": "notepad.exe", "linux": "gedit", "darwin": "TextEdit" }
    }
  }
}
```

A macro that types something and presses Enter:

```json
{
  "id": "macro",
  "label": "Type + Enter",
  "cell": { "row": 0, "column": 2 },
  "on_press": {
    "type": "macro",
    "params": {
      "steps": [
        { "type": "delay", "params": { "ms": 100 } },
        { "type": "keyboard.text", "params": { "text": "hello", "interval_ms": 20 } },
        { "type": "keyboard.key", "params": { "key": "ENTER" } }
      ]
    }
  }
}
```

A tile bound to a live metric:

```json
{
  "id": "cpu",
  "label": "CPU",
  "cell": { "row": 3, "column": 0 },
  "state": { "type": "telemetry", "metric": "cpu.usage", "format": "{value:.0f}%" },
  "on_press": { "type": "noop", "params": {} }
}
```

Save the file and the running host picks it up within two seconds; connected
phones refresh. `mobiledeck profiles validate <file>` checks one before you save
it, and a profile that fails validation is never served — the previous good
layout keeps working.

The complete example lives in [`profiles/development/profile.json`](profiles/development/profile.json).
The full schema and the action catalogue are in [`docs/PROTOCOL.md`](docs/PROTOCOL.md).

## Security, in plain language

Pairing a device grants it **real control of your keyboard, mouse, and — if you
allow it — a shell on this machine.** Treat a paired phone like a logged-in
session.

- Pairing is a short-lived single-use PIN; the phone then holds a random token.
  The host stores only its SHA-256 hash, and a leaked `devices.json` cannot be
  used to connect.
- TLS is on by default with a self-signed certificate, and the phone pins its
  fingerprint, so a rogue host on the LAN cannot impersonate yours. Plaintext on
  a LAN bind is refused unless you pass `--insecure-allow-plaintext`.
- Each device has scopes. `scripts` and `system.power` are **not** granted by
  default, because they mean "run anything" and "power off the machine".
  Grant them with `mobiledeck devices scopes <id> keyboard mouse scripts`.
- Shutdown and restart additionally require an explicit confirmation in the
  action parameters, so a misconfigured button cannot power off your machine on
  a stray tap.
- The agent never runs as root and never elevates. There is no `sudo`, no UAC
  prompt, and no privileged helper.
- Everything is audited to `audit.jsonl`, and the log never contains a token or a
  PIN.

Revoke a lost phone with `mobiledeck devices revoke <id>`; it is disconnected
immediately and its token stops working.

The full threat model is in [`docs/SECURITY.md`](docs/SECURITY.md).

## No internet, no cloud, no Docker

The agent is one binary. It embeds the HTTP server, the WebSocket endpoint, the
mDNS responder and the action engine. Core functionality needs nothing but a LAN:

- No account, no sign-in, no telemetry to anyone but your own phone.
- No Docker required. `docker-compose.yml` exists for documentation and
  reproducible builds, and for an *optional* agent container that covers only the
  actions that do not need the desktop session. The agent is never designed to
  run in a container for input control: a container cannot drive your desktop
  session's keyboard without more privilege than running it natively
  (`docs/DEPLOYMENT.md` §4).
- Internet is only ever useful for updates and reading these docs.

## Layout

```
host/                       the agent (Go)
  cmd/mobiledeck/           the binary
  internal/proto/           the wire protocol, pure data
  internal/profile/         profile schema and validation
  internal/profiles/        loading, watching, revisions
  internal/auth/            pairing, tokens, scopes, rate limits, audit
  internal/engine/          action registry, macro engine, cancellation
  internal/platform/        the only place that knows an OS exists
  internal/server/          HTTP + WebSocket edge, mDNS
  internal/telemetry/       metric sampling
  internal/cli/             the command line
android/                    the client (Kotlin + Compose)
profiles/                   deck layouts, meant to be committed
docs/                       protocol, architecture, security, ADRs
scripts/                    developer tooling
```

The layering rule that matters: **nothing above `internal/platform` may know
which operating system it runs on.** Porting to a new OS means adding files under
`internal/platform/` and changing nothing else.

## Documentation

| Document | Contents |
|----------|----------|
| [`docs/PROTOCOL.md`](docs/PROTOCOL.md) | The frozen wire contract: envelopes, pairing, every message, every action |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Layering, packages, concurrency, failure behaviour |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Threat model, privilege separation, security tests |
| [`docs/adr/`](docs/adr/) | Why each significant decision was made, and what it cost |
| [`docs/PLUGIN_DEVELOPMENT.md`](docs/PLUGIN_DEVELOPMENT.md) | Writing a plugin (Phase 3) |
| [`docs/GUI.md`](docs/GUI.md) | The desktop control panel and the layout editor |
| [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) | Building and running from source |
| [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) | Installing and running: native, systemd user unit, Windows logon, optional Docker |
| [`docs/INSTALL.md`](docs/INSTALL.md) | The install scripts, the published one-liners, uninstall steps |
| [`docs/MULTI_DEVICE.md`](docs/MULTI_DEVICE.md) | Several phones at once: per-device scopes, revoke vs disable, worked example |
| [`docs/ROADMAP.md`](docs/ROADMAP.md) | The five phases and what each one owes |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | The rules enforced at review |

## Development

```sh
cd host
go vet ./...
go test ./...
go build ./...
```

```sh
cd android
./gradlew assembleDebug testDebugUnitTest
```

The client's integration tests need a host. Start a throwaway one and export what
it prints:

```sh
sh scripts/e2e-host.sh start
```

The end-to-end test that proves keystrokes really reach an application needs a
desktop session and steals keyboard focus, so it is opt-in:

```sh
cd scripts/smokeinput && go build -o ../../.devdata/smokeinput.exe .
cd ../../host
MOBILEDECK_E2E_INPUT=1 go test ./internal/server -run TestEndToEndReal -v
```

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md).

Apache-2.0 rather than MIT for one reason: it grants patent rights explicitly,
which matters for a project that integrates with many vendors' software. The cost
is that downstream users must preserve attribution. It is not copyleft, so forks
and commercial use are allowed — the project's value is the protocol and the
ecosystem, not license leverage.
