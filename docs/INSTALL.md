# Installing MobileDeck

This page covers installing the host agent and the Android client. The agent is
one Go binary; there is no package to install, no service account and no
elevation. For what the agent does at run time - first run, pairing, the systemd
unit in full, Windows logon, and the optional Docker container - see
[`DEPLOYMENT.md`](DEPLOYMENT.md).

There are two ways to install, and it matters which one you are using:

| Mode | Works today | Needs |
|------|-------------|-------|
| **From source** | yes | Go 1.26+ and a checkout |
| **From a release** | **no, until this repository is published** | a published release with `checksums.txt` |

The repository is not published yet: there is no remote and no release. The
release mode below is written and ready, but it will fail with an honest message
until the repository exists on a host and the release URL at the top of each
script points at it. Source mode is the path that works now.

---

## 1. Host agent

### 1.1 From source (works today)

From a checkout:

```sh
sh scripts/install.sh
```

That builds `host/`, installs the binary to `$HOME/.local/bin/mobiledeck`, and
prints the two commands to run next. It does not need the network.

Useful variants:

```sh
sh scripts/install.sh --prefix "$HOME/.local"     # install to a chosen prefix
sh scripts/install.sh --version 1.2.3             # label the build
sh scripts/install.sh --dry-run                   # show every step, change nothing
sh scripts/install.sh --service                   # also install a systemd user unit
```

On Windows, from a checkout in PowerShell:

```powershell
.\scripts\install.ps1
```

That builds `host\`, installs `mobiledeck.exe` to
`%LOCALAPPDATA%\Programs\mobiledeck`, and adds that directory to your **user**
PATH. Open a new terminal afterwards for the PATH change to take effect.

### 1.2 From a release (once published)

> **This form requires the repository to be published.** It downloads
> `releases/latest/download/mobiledeck_<os>_<arch>.tar.gz` (a `.zip` on
> Windows) from GitHub and verifies its SHA-256 against the `checksums.txt` in
> the same release. Until the repository has a remote and a release, the
> download returns 404 and the script says exactly that. Publishing is a
> two-line edit: set `REPO_OWNER` and `REPO_NAME` at the top of each script to
> the real owner and repository name.

The published one-liner form, once those two lines are set:

```sh
curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.sh | sh
```

```powershell
irm https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.ps1 | iex
```

A piped script has no location, so it cannot find a checkout and defaults to
release mode. Pass flags through the pipe explicitly:

```sh
curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.sh | sh -s -- --prefix "$HOME/.local"
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.ps1))) -Prefix "$env:LOCALAPPDATA\Programs\mobiledeck"
```

To pin a version, add `--version v1.2.3` (sh) or `-Version v1.2.3`
(PowerShell). `latest` is the default.

#### Verify what you are about to run

`curl … | sh` and `irm … | iex` hand a script you have not read straight to a
shell, which runs it with your privileges. That is a real trust decision, and
the honest answer is: read the script first.

```sh
curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.sh | less
```

```powershell
irm https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.ps1 | more
```

Both scripts are short and commented, and they are the same files in this
repository at `scripts/install.sh` and `scripts/install.ps1`. What they do, in
order: detect the OS and architecture, refuse to run as root/Administrator,
build or download the binary, verify a checksum in release mode, copy one file
into place, and print the next commands. They never write inside the repository
and never touch an existing configuration directory. If you would rather not
pipe a shell, download the script, read it, and run it:

```sh
curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/scripts/install.sh -o install.sh
less install.sh
sh install.sh
```

### 1.3 What the installer refuses to do

- It refuses to run as root, or from an elevated prompt on Windows, because the
  agent must run as the logged-in user and never elevates
  ([`SECURITY.md`](SECURITY.md) §4). `--allow-root` / `-AllowElevated` exists
  only for building a container image.
- It never writes inside the repository.
- It never touches `%APPDATA%\mobiledeck` or `~/.config/mobiledeck`. It prints
  that the directory exists and leaves it alone.
- In release mode it refuses to install an archive that is missing from
  `checksums.txt` or whose SHA-256 does not match.

### 1.4 Options

`scripts/install.sh`:

| Flag | Meaning |
|------|---------|
| `--from-source` / `--from-release` | choose the mode (default: source inside a checkout, release otherwise) |
| `--source DIR` | build from `DIR` instead of the detected checkout |
| `--version V` | pin a release tag, or set the build label reported by `mobiledeck version` in source mode |
| `--prefix DIR` | install the binary to `DIR/bin` (default `$PREFIX/bin`, else `$HOME/.local/bin`) |
| `--with-gui` | also build the desktop control panel (implies CGO; source only) |
| `--service` / `--no-service` | install and enable a systemd user unit, or not (default: not) |
| `--dry-run` | print every step without changing anything |
| `--allow-root` | allow running as root (container builds only) |
| `-h`, `--help` | usage |

`scripts/install.ps1` takes the same options in PowerShell spelling:
`-FromSource`, `-FromRelease`, `-Source`, `-Version`, `-Prefix`, `-WithGui`,
`-NoService`, `-DryRun`, `-AllowElevated`, `-Help`.

`--with-gui` needs CGO and therefore a C toolchain. On Linux that also means the
WebKitGTK development package:

```sh
sudo apt install libwebkit2gtk-4.1-dev
```

The published release archives are built with `CGO_ENABLED=0` and contain no
window; `--with-gui` is source-only for that reason, and the scripts say so.
That is a limit on the *window*, not on the panel: a CGO-free binary still
serves the same control panel to a browser at `http://127.0.0.1:8765/`, opened
with `mobiledeck token --open`. Only the native window and the notification-area
icon need CGO.

### 1.5 The systemd user unit

`--service` writes `~/.config/systemd/user/mobiledeck.service` - a **user** unit,
never a system unit, because input control needs the graphical session
([`SECURITY.md`](SECURITY.md) §4). The unit text is the one in
[`DEPLOYMENT.md`](DEPLOYMENT.md) §2, with `ExecStart` pointing at the installed
binary. The script then runs `systemctl --user daemon-reload` and
`systemctl --user enable --now mobiledeck`.

Follow it with:

```sh
journalctl --user -u mobiledeck -f
```

Windows has no user-unit equivalent, and a Windows Service cannot inject input
because services run in session 0 ([`DEPLOYMENT.md`](DEPLOYMENT.md) §3.2). The
PowerShell installer therefore installs nothing at logon and says so; use the
Task Scheduler or Startup-folder recipe in that section.

### 1.6 First run

Both installers end by printing the commands to run next:

```sh
mobiledeck run --bind 0.0.0.0
```

and, in another terminal:

```sh
mobiledeck pair
```

Then check what this machine can actually do:

```sh
mobiledeck doctor
```

`run` creates the configuration directory on first start.

The control panel is a browser page at `http://127.0.0.1:8765/`. It asks for the
admin key, and `mobiledeck token --open` opens it with the key already in the
URL, so there is nothing to copy:

```sh
mobiledeck token --open
```

---

## 2. Android client

`scripts/install-android.sh` builds the debug APK when it is missing, installs
it over USB, and prints the two ways to connect the app to the host. It needs
`adb` on `PATH` and a device that `adb devices` lists as `device` - not
`unauthorized` and not `offline`. Unlock the phone and accept the "Allow USB
debugging" prompt if it appears.

From a checkout:

```sh
sh scripts/install-android.sh
```

```sh
sh scripts/install-android.sh --build        # force a rebuild
sh scripts/install-android.sh --serial <s>   # pick one of several devices
sh scripts/install-android.sh --uninstall    # remove the app
sh scripts/install-android.sh --dry-run      # show every step, change nothing
```

There is no useful published one-liner for this script: the build needs the
Gradle wrapper in the checkout, so piping the script alone would leave it
without `android/gradlew`. Clone, then run it:

```sh
git clone https://github.com/<owner>/<repo>.git
cd <repo>
sh scripts/install-android.sh
```

To build by hand instead:

```sh
cd android
./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

### 2.1 Connecting the app

**Over USB** (no Wi-Fi needed). The script prints this:

```sh
adb reverse tcp:8765 tcp:8765
```

Then enter `127.0.0.1:8765` in the app. `adb reverse` makes the phone's own
`127.0.0.1:8765` the host's `127.0.0.1:8765`.

**Over the LAN.** Run the host bound to the LAN and read its address:

```sh
mobiledeck run --bind 0.0.0.0
mobiledeck status
```

Enter the `reachable` address from `status` (for example `192.168.1.10:8765`)
in the app.

---

## 3. Docker

Docker runs only the subset of actions that do not need the desktop session -
serving profiles, pairing, telemetry, display-less scripts. It cannot drive your
keyboard or mouse. The full detail, including the PulseAudio socket for media,
is in [`DEPLOYMENT.md`](DEPLOYMENT.md) §4.

```sh
mkdir -p data
docker compose up -d --build agent
docker compose logs -f agent
docker compose down
```

`docker-compose.yml` is in the repository, so this needs a checkout. The config
directory is bind-mounted from `./data`, which is gitignored.

---

## 4. Uninstall

### 4.1 Linux and macOS

The installer creates exactly these paths:

| Path | Created by |
|------|-----------|
| `<prefix>/bin/mobiledeck` | the install itself (default prefix: `$HOME/.local`) |
| `~/.config/systemd/user/mobiledeck.service` | only with `--service` |
| `$XDG_CONFIG_HOME/mobiledeck` (usually `~/.config/mobiledeck`) | the agent, on first run - **not** the installer |

Remove the binary:

```sh
rm -f "$HOME/.local/bin/mobiledeck"
```

If a unit was installed:

```sh
systemctl --user disable --now mobiledeck
rm -f ~/.config/systemd/user/mobiledeck.service
systemctl --user daemon-reload
```

The configuration directory holds your profiles, paired devices, TLS material,
audit log and any soundboard files. The installer never touches it; delete it
only if you want to lose that state:

```sh
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/mobiledeck"
```

### 4.2 Windows

| Path | Created by |
|------|-----------|
| `%LOCALAPPDATA%\Programs\mobiledeck\mobiledeck.exe` | the install itself (or `<prefix>` if `-Prefix` was given) |
| a `%LOCALAPPDATA%\Programs\mobiledeck` entry in your user `Path` | the installer |
| `%APPDATA%\mobiledeck` | the agent, on first run - **not** the installer |

```powershell
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\mobiledeck"

# Drop the PATH entry.
$p = [Environment]::GetEnvironmentVariable('Path', 'User')
$kept = ($p -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ine "$env:LOCALAPPDATA\Programs\mobiledeck") }) -join ';'
[Environment]::SetEnvironmentVariable('Path', $kept, 'User')
```

Open a new terminal afterwards. To also remove the configuration directory and
everything in it:

```powershell
Remove-Item -Recurse -Force "$env:APPDATA\mobiledeck"
```

If you registered a logon task, remove it too:

```powershell
schtasks /Delete /TN MobileDeck /F
```

### 4.3 Android

```sh
adb uninstall dev.mobiledeck
```

That removes the app and its stored device token. The host's record of the
device is separate: revoke it with `mobiledeck devices revoke <id>`, which is
the right move for a lost phone.

### 4.4 Docker

```sh
docker compose down
rm -rf data
```

---

## 5. Troubleshooting

**`install: refusing to run as root`** - the installer is doing its job. Run it
as your normal user. `--allow-root` exists only for container image builds.

**`Release downloads require the repository to be published`** - the repository
has no remote or no release yet. Use `--from-source` from a checkout.

**`checksum mismatch` / `is not listed in checksums.txt`** - the download does
not match the release's `checksums.txt`. The script refuses to install it. Check
the release, then retry.

**`from-source install needs Go 1.26 or newer on PATH`** - install Go, or use a
release once one exists.

**`the device is connected but not authorised`** - unlock the phone and accept
the "Allow USB debugging" prompt, then run the Android script again.

**`mobiledeck` not found after installing** - the install directory is not on
`PATH` yet. On Linux/macOS add it to your shell profile; on Windows open a new
terminal. The scripts print the exact directory.
