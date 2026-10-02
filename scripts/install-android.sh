#!/bin/sh
#
# install-android.sh - build and install the MobileDeck Android client on a
# device attached over USB.
#
# The phone must be connected and authorised: `adb devices` has to list it as
# "device", not "unauthorized" or "offline". Unlock the phone and accept the
# "Allow USB debugging" prompt if it appears.
#
# Usage: sh scripts/install-android.sh [options]
#   --build              force a rebuild even if the debug APK already exists
#   --serial SERIAL      target one device when several are attached
#   --uninstall          uninstall the app from the device and stop
#   --dry-run            print every step without changing anything
#   -h, --help           this message
#
# See docs/INSTALL.md.

set -eu

# --- repository identity -----------------------------------------------------
# Keep these in step with scripts/install.sh and scripts/install.ps1.
REPO_OWNER="mobiledeck"
REPO_NAME="mobiledeck"

APP_ID="dev.mobiledeck"
DEFAULT_PORT=8765

usage() {
    cat <<EOF
install-android.sh - build and install the MobileDeck Android client

usage: sh scripts/install-android.sh [options]

Options:
  --build              force a rebuild even if the debug APK already exists
  --serial SERIAL      target one device when several are attached
  --uninstall          uninstall the app from the device and stop
  --dry-run            print every step without changing anything
  -h, --help           this message

The script needs adb on PATH and a device that \`adb devices\` lists as
"device". It builds android/app/build/outputs/apk/debug/app-debug.apk when the
file is missing (or when --build is given) and installs it with \`adb install -r\`.
EOF
}

die() {
    printf 'install-android: %s\n' "$*" >&2
    exit 1
}

note() {
    printf '%s\n' "$*"
}

# --- arguments ---------------------------------------------------------------

build=0
uninstall=0
dry_run=0
serial=""

while [ $# -gt 0 ]; do
    case "$1" in
        --build) build=1 ;;
        --uninstall) uninstall=1 ;;
        --dry-run) dry_run=1 ;;
        --serial)
            shift
            if [ $# -eq 0 ]; then die "--serial needs a device serial"; fi
            serial=$1
            ;;
        --serial=*) serial=${1#--serial=} ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
    shift
done

# --- locations ---------------------------------------------------------------

script_dir=""
case "$0" in
    */*) script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd) || script_dir="" ;;
esac
if [ -z "$script_dir" ] || [ ! -f "$script_dir/../android/gradlew" ]; then
    die "run this from a checkout: scripts/install-android.sh needs android/gradlew next to it"
fi
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

android_dir="$repo_root/android"
apk="$android_dir/app/build/outputs/apk/debug/app-debug.apk"

if [ ! -x "$android_dir/gradlew" ] && [ -f "$android_dir/gradlew" ]; then
    chmod +x "$android_dir/gradlew"
fi

# --- adb ---------------------------------------------------------------------

if ! command -v adb >/dev/null 2>&1; then
    die "adb is not on PATH. Install the Android platform tools (https://developer.android.com/tools/releases/platform-tools) and put them on PATH."
fi

# adb_args carries an explicit -s SERIAL when one was given.
adb_args=""
if [ -n "$serial" ]; then
    adb_args="-s $serial"
fi

# run_adb runs adb with the optional -s flag and strips carriage returns, so the
# rest of the script sees POSIX line endings on Windows.
run_adb() {
    # shellcheck disable=SC2086
    adb $adb_args "$@" 2>&1 | tr -d '\r'
}

# device_lines prints one "<serial> <state>" line per attached device.
device_lines() {
    run_adb devices | awk 'NR > 1 && NF >= 2 { print $1, $2 }'
}

require_authorised_device() {
    lines=$(device_lines)

    if [ -z "$lines" ]; then
        run_adb devices >&2
        die "no device found. Connect a phone with USB debugging enabled; \`adb devices\` must list it as \"device\"."
    fi

    if [ -n "$serial" ]; then
        state=$(printf '%s\n' "$lines" | awk -v s="$serial" '$1 == s { print $2; exit }')
        if [ -z "$state" ]; then
            printf '%s\n' "$lines" >&2
            die "device $serial is not attached"
        fi
        if [ "$state" != "device" ]; then
            die "device $serial is \"$state\"; it must be \"device\""
        fi
        return 0
    fi

    ready=$(printf '%s\n' "$lines" | awk '$2 == "device" { print $1 }')
    count=$(printf '%s\n' "$ready" | awk 'NF' | wc -l | tr -d ' ')
    attached=$(printf '%s\n' "$lines" | awk 'NF' | wc -l | tr -d ' ')

    # adb without -s refuses to act when more than one device is attached, even
    # if only one of them is ready, so an explicit --serial is required then.
    if [ "$attached" -gt 1 ]; then
        printf '%s\n' "$lines" >&2
        die "several devices are attached; choose one with --serial"
    fi

    if [ "$count" -eq 1 ]; then
        return 0
    fi

    # Nothing is ready: report the actual state of what is attached.
    printf '%s\n' "$lines" >&2
    state=$(printf '%s\n' "$lines" | awk 'NR == 1 { print $2 }')
    case "$state" in
        unauthorized)
            die "the device is connected but not authorised. Unlock the phone, accept the \"Allow USB debugging\" prompt, and run this again."
            ;;
        offline)
            die "the device is listed as offline. Replug the cable, or run \`adb kill-server\`, and try again."
            ;;
        *)
            die "the device is in state \"$state\"; it must be \"device\""
            ;;
    esac
}

# --- host address ------------------------------------------------------------

find_mobiledeck() {
    if command -v mobiledeck >/dev/null 2>&1; then
        command -v mobiledeck
        return 0
    fi
    for candidate in "$repo_root/host/mobiledeck" "$repo_root/host/mobiledeck.exe" "$HOME/.local/bin/mobiledeck"; do
        if [ -x "$candidate" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

print_connection_help() {
    printf '\nconnect the app to the host, either way:\n'
    printf '\n  over USB (no Wi-Fi needed):\n'
    printf '    adb reverse tcp:%s tcp:%s\n' "$DEFAULT_PORT" "$DEFAULT_PORT"
    printf '    then enter 127.0.0.1:%s in the app\n' "$DEFAULT_PORT"
    printf '    (adb reverse makes the phone 127.0.0.1:%s the host 127.0.0.1:%s)\n' "$DEFAULT_PORT" "$DEFAULT_PORT"

    printf '\n  over the LAN:\n'
    bin=$(find_mobiledeck || true)
    if [ -n "$bin" ]; then
        printf '    %s run --bind 0.0.0.0\n' "$bin"
        printf '    then enter the address printed by:\n'
        printf '      %s status\n' "$bin"
    else
        printf '    run the host (mobiledeck run --bind 0.0.0.0), then read the\n'
        printf '    "reachable" line from \`mobiledeck status\` and enter that host:port\n'
    fi
    printf '\n'
}

# --- dry run -----------------------------------------------------------------

if [ "$dry_run" -eq 1 ]; then
    note "install-android: dry run, nothing will be changed"
    note "would check adb (found: $(command -v adb))"
    if [ -n "$serial" ]; then
        note "would target device $serial"
    else
        note "would require exactly one device listed as \"device\" by adb"
    fi
    if [ "$uninstall" -eq 1 ]; then
        if [ -n "$serial" ]; then
            note "would run: adb -s $serial uninstall $APP_ID"
        else
            note "would run: adb uninstall $APP_ID"
        fi
        exit 0
    fi
    if [ -f "$apk" ]; then
        note "APK already exists: $apk"
    else
        note "APK missing; would build it: (cd $android_dir && ./gradlew assembleDebug)"
    fi
    if [ -n "$serial" ]; then
        note "would run: adb -s $serial install -r $apk"
    else
        note "would run: adb install -r $apk"
    fi
    print_connection_help
    exit 0
fi

# --- uninstall ---------------------------------------------------------------

require_authorised_device

if [ "$uninstall" -eq 1 ]; then
    note "install-android: uninstalling $APP_ID"
    # shellcheck disable=SC2086
    if ! adb $adb_args uninstall "$APP_ID"; then
        die "adb uninstall failed"
    fi
    note "install-android: done"
    exit 0
fi

# --- build -------------------------------------------------------------------

if [ "$build" -eq 1 ] || [ ! -f "$apk" ]; then
    note "install-android: building the debug APK"
    (
        cd "$android_dir"
        ./gradlew assembleDebug
    )
else
    note "install-android: using the existing APK (pass --build to rebuild)"
fi

if [ ! -f "$apk" ]; then
    die "the build did not produce $apk"
fi

# --- install -----------------------------------------------------------------

note "install-android: installing $apk"
# shellcheck disable=SC2086
if ! adb $adb_args install -r "$apk"; then
    die "adb install failed"
fi
note "install-android: installed $APP_ID"

print_connection_help
