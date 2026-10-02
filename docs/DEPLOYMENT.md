# Deployment

How to install and run the MobileDeck host agent. The agent is one static Go
binary. Running it natively is the supported path; Docker is optional and only
covers a subset of actions (ADR-0009, `docs/SECURITY.md` §4).

Throughout, `<config-dir>` is the per-user configuration directory
(`%APPDATA%\mobiledeck` on Windows, `$XDG_CONFIG_HOME/mobiledeck` elsewhere).
`mobiledeck` means the built binary; from a source checkout replace it with
`go run ./cmd/mobiledeck` from inside `host/`.

---

## 1. Run natively (supported)

### 1.1 Build

```sh
cd host
CGO_ENABLED=0 go build -trimpath -o mobiledeck ./cmd/mobiledeck
```

That single file is the whole product. Copy it anywhere and run it.

### 1.2 Linux

Run it inside the graphical session whose keyboard and mouse you want to drive.
The agent needs `DISPLAY` (X11) or the Wayland session, owned by the logged-in
user. Running as root is neither required nor supported (`docs/SECURITY.md` §4).

```sh
./mobiledeck run
```

Input actions over SSH or from a system service report `unsupported`, because
there is no display to type into.

### 1.3 macOS

```sh
./mobiledeck run
```

Keyboard and mouse injection requires the Accessibility permission for the
binary (or the terminal that launched it): System Settings → Privacy & Security →
Accessibility. Without it the OS refuses the injection and the host reports a
permission error naming the requirement.

### 1.4 Windows

```powershell
.\mobiledeck.exe run
```

Run it in the interactive session whose keyboard and mouse you want to control.
Input injection uses `SendInput`, which is session-aware and needs no elevation.
The first run raises a Windows Firewall prompt; allow the **Private** profile
only. To run it detached instead:

```powershell
.\mobiledeck.exe start
.\mobiledeck.exe status
.\mobiledeck.exe stop
```

`start` launches the host as a background process and records a pidfile under
`logs/`; `stop` ends it.

### 1.5 First run

`run` creates the configuration directory, a default `config.json`, a
self-signed TLS certificate under `tls/`, and seeds the example profile. It
listens on `0.0.0.0:8765` and advertises `_mobiledeck._tcp.local.` over mDNS.
`profiles/` and `logs/` are created with it; `sounds/` appears the first time the
soundboard is used, so a host that never plays a sound leaves no empty directory
behind. Pair a phone with:

```sh
./mobiledeck pair
```

`doctor` reports what this machine can and cannot do before you rely on it:

```sh
./mobiledeck doctor
```

#### The control panel

`GET /` serves the control panel to a browser on the machine that runs the host:
open `http://127.0.0.1:8765/` and the page asks for the admin key. It is a
loopback-only credential, so this is not a page for the phone on the LAN.

```sh
./mobiledeck token --open   # opens the panel with the key already in the URL
./mobiledeck token          # prints the key, to paste by hand
```

The key is generated per host process, so a restart invalidates whatever the
browser remembered — `token --open` is the quick way back in. The same page runs
as a native window with `mobiledeck gui` (Windows with CGO; see
[`GUI.md`](GUI.md)). A client that is not a browser still gets the plain-text
summary from the same address.

---

## 2. systemd user unit (Linux)

Install the agent as a **user** unit, not a system unit. Input control needs the
graphical session (`docs/SECURITY.md` §4): a system unit starts before login,
with no `DISPLAY`, no Wayland socket and no session bus, so every keyboard and
mouse action fails. A user unit starts inside your session and inherits exactly
those.

Create `~/.config/systemd/user/mobiledeck.service`:

```ini
[Unit]
Description=MobileDeck host agent
Documentation=https://github.com/mobiledeck/mobiledeck
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
# The agent must run as the logged-in user, never as root (SECURITY.md §4).
# A user unit is already unprivileged; no User= or sudo is involved.
ExecStart=%h/.local/bin/mobiledeck run --config-dir %h/.config/mobiledeck
Restart=on-failure
RestartSec=3
# A clean shutdown on SIGTERM; the agent closes sessions and removes its
# runtime file.
KillSignal=SIGTERM
TimeoutStopSec=10

[Install]
WantedBy=graphical-session.target
```

Adjust `ExecStart` to the real binary path. Then:

```sh
systemctl --user daemon-reload
systemctl --user enable --now mobiledeck
systemctl --user status mobiledeck
```

By default a user unit stops when you log out. To keep the host running across
logouts (a wall-mounted tablet that should stay reachable), enable lingering for
your account:

```sh
loginctl enable-linger "$USER"
```

Lingering keeps the user manager (and this unit) alive without a login session.
Note that the session-bound parts still require a session: with lingering and no
graphical login, the host runs and serves profiles but keyboard/mouse actions
report `unsupported` until you log into the desktop again. Follow the logs with:

```sh
journalctl --user -u mobiledeck -f
```

---

## 3. Windows

### 3.1 Start it

```powershell
.\mobiledeck.exe start
```

This is the same background mode as §1.4: a detached process with a pidfile
under `logs/`, stopped with `mobiledeck stop`.

For the desktop window instead, `.\mobiledeck.exe gui` starts the host and opens
the control panel. Closing that window does **not** stop the host: it hides the
window and leaves an icon in the notification area, so a phone keeps working
while the panel is out of the way. Quit from the icon's menu to stop it, or pass
`--no-tray` to keep the older behaviour of stopping with the window. The tray
needs a CGO build; a `CGO_ENABLED=0` binary has no icon and stops with the
window, and `gui` says so on start. `mobiledeck run` is unaffected either way —
it has no window and no tray.

### 3.2 Start at logon

A Windows **Service** cannot inject input into the interactive session: services
run in session 0, which has no desktop, and `SendInput` from there does not reach
your desktop. That is why a service is not offered. Register the agent at logon
instead, so it starts inside your interactive session.

**Task Scheduler**, via XML. Save this as `mobiledeck.xml`, adjust the paths and
user, then import it (`schtasks /Create /TN MobileDeck /XML mobiledeck.xml`):

```xml
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>MobileDeck host agent</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>DOMAIN\you</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>DOMAIN\you</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>C:\path\to\mobiledeck.exe</Command>
      <Arguments>run --config-dir "%APPDATA%\mobiledeck"</Arguments>
    </Exec>
  </Actions>
</Task>
```

`InteractiveToken` with `LeastPrivilege` is the point: the task runs as you, in
your session, without elevation. `ExecutionTimeLimit>PT0S` means "no time limit",
so the task is not killed after three days.

**Startup folder** (simpler, no XML). Put a shortcut in
`shell:startup` (open the Run box and type `shell:startup`) pointing at
`mobiledeck.exe start`. The shortcut runs at logon in your session. This uses
the detached `start` mode, so the process survives after the shortcut exits.

---

## 4. Docker (optional, subset only)

The agent is not designed to run in a container. A container has no desktop
session, so the actions that make a stream deck a stream deck do not work.
What remains is still useful as a service, and is documented here so the
limitations are explicit rather than discovered at press time.

### 4.1 What works

- Serving profiles over HTTP/WebSocket; the phone connects and renders the deck.
- Pairing and device management.
- Telemetry (`system.stats`, `cpu.*`, `mem.*`, `disk.*`, `net.*`).
- Scripts and commands that need no display (`run_script`, `run_command`),
  subject to the `scripts` scope.
- Media and volume **if** you mount the host's PulseAudio socket (see below).
- `sound.play` under the same condition: the image ships `paplay` and `aplay`, and
  the soundboard files live in the mounted `sounds/` directory. `doctor` reports
  playback as available when a player is installed, which the container has; if
  no sink is reachable, the play fails at press time with the player's error
  rather than being refused up front.

### 4.2 What does not work

- `keyboard.*` and `mouse.*`: no X11/Wayland socket, so injection is impossible.
- Anything needing the session bus or the desktop: `open_url`, `open_folder`,
  `open_terminal`, screen locking.
- `system.power` (`system.shutdown`, `system.restart`, `system.sleep`): no
  systemd PID 1 in the container, and the agent never elevates.

These actions report `unsupported`; the host stays up and everything else keeps
working.

### 4.3 Run it

The `agent` service in `docker-compose.yml` builds `host/Dockerfile` and uses
`network_mode: host` (required for mDNS discovery and LAN reachability; the
compose file explains why). Create the data directory first, as your own user,
so it is not created root-owned by the daemon and then unwritable by the
container's uid 1000:

```sh
mkdir -p data
docker compose up -d --build agent
docker compose logs -f agent
docker compose down
```

The config directory is bind-mounted `./data:/data`, so `profiles/`,
`devices.json`, `audit.jsonl` and the TLS material survive a rebuild. `./data`
is gitignored.

### 4.4 Media through a PulseAudio socket

Volume and mute need to reach the host's audio server. Mount the PulseAudio
socket (and the cookie, so the client can authenticate) into the container by
adding to the `agent` service in `docker-compose.yml`:

```yaml
    environment:
      # Point the in-container clients at the host's PulseAudio socket.
      PULSE_SERVER: unix:/run/user/1000/pulse/native
    volumes:
      - ./data:/data
      # The host user's PulseAudio socket and cookie. 1000 is the usual first
      # desktop uid; adjust it to `id -u` on the host.
      - /run/user/1000/pulse:/run/user/1000/pulse
```

Then `media.*` and `volume.*` reach the host's default sink. This is Linux-only
and requires the host user to be running a PulseAudio/PipeWire server.

### 4.5 Build the image directly

```sh
cd host
docker build --build-arg VERSION=1.2.3 -t mobiledeck/agent:1.2.3 .
```

The image runs as the unprivileged user `mobiledeck` (uid 1000). Running the
agent as root is never needed and is not supported (`docs/SECURITY.md` §4).

---

## 5. Ports, firewall, and bind address

The default bind is `0.0.0.0` on port **8765** (`config.DefaultPort`). The admin
API is not a separate port: it is the same listener, restricted to loopback
callers and gated by the admin token in `<config-dir>/runtime.json`.

Change the address or port either in `config.json`:

```json
{ "bind": "127.0.0.1", "port": 9000 }
```

or per invocation with the global flags (they must come **after** the
subcommand):

```sh
mobiledeck run --bind 127.0.0.1 --port 9000
```

`--bind 127.0.0.1` exposes the host to this machine only; a phone on the LAN
then cannot reach it (useful with `adb reverse`, see `docs/DEVELOPMENT.md` §6).

### 5.1 Windows firewall

The first run raises a prompt. Allow the **Private** network profile only. To
add the rule explicitly, in an elevated PowerShell:

```powershell
New-NetFirewallRule -DisplayName "MobileDeck" -Direction Inbound `
  -Protocol TCP -LocalPort 8765 -Action Allow -Profile Private
```

If you only ever want loopback access, deny it and bind to `127.0.0.1` instead.

### 5.2 Linux firewall

With `ufw`:

```sh
sudo ufw allow 8765/tcp
```

With `firewalld`:

```sh
sudo firewall-cmd --permanent --add-port=8765/tcp
sudo firewall-cmd --reload
```

Restrict either rule to your LAN subnet if the machine is on an untrusted
network. mDNS needs UDP 5353 to stay open on the LAN for discovery; if it is
blocked, enter the host address by hand in the client.

---

## 6. Backup and restore

Everything the host persists lives under the configuration directory
(`<config-dir>`, or `./data` for the container).

| Path | What it is | How to treat it |
|------|------------|-----------------|
| `profiles/` | Deck layouts the host serves, one directory per profile. | Back up; it is the artefact worth versioning. The repo's own `profiles/` directory holds the canonical example and is git-tracked. |
| `sounds/` | Audio files a soundboard pad may play. A file must live here to be playable, and the panel's Sounds tab drops uploads into it. | Back up if the board uses sounds; the files are not reproducible from a profile, which only references them by name. |
| `config.json` | Host name, bind, port, TLS and limits. | Back up; hand-editable. |
| `tls/` | The self-signed certificate and key. | Back up with the config if clients pin the fingerprint; otherwise a new certificate forces every client to re-verify. |
| `devices.json` | Paired device records and token **hashes** (0600). | Machine-local secret. Do not commit or share; back up only if you want pairings to survive. |
| `audit.jsonl` | Append-only audit log. | Machine-local secret. Archive if you need the history. |
| `logs/` | Rotating log files and the pidfile. | Disposable. |
| `runtime.json` | Live pid, address and admin token (0600). | Disposable; rewritten on every start and removed on exit. |

Backup is a copy of the directory:

```sh
tar czf mobiledeck-backup.tgz -C "$HOME/.config" mobiledeck
```

Restore is the reverse, into the same path, with `devices.json` and `audit.jsonl`
kept at mode 0600. A restored `devices.json` keeps existing pairings valid,
because the token hashes are unchanged; a restored `tls/` keeps the pinned
fingerprint valid, so phones do not have to re-verify. Restoring neither is
safe: clients simply re-pair with a fresh PIN.

To reset to a first-run state, stop the host and delete the directory; `run`
recreates everything. Revoke a single lost phone instead of wiping everything:

```sh
mobiledeck devices revoke <id>
```
