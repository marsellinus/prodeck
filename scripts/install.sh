#!/bin/sh
#
# install.sh - install the MobileDeck host agent on Linux or macOS.
#
# Two modes:
#
#   from source    needs go. Builds a checkout - this one, a directory given
#                  with --source, or a clone of the published repository - and
#                  installs the resulting binary. This works today, with no
#                  network at all when run from inside the repository.
#
#   from release   downloads the published tarball, verifies its SHA-256 against
#                  the checksums.txt in the same release, and installs it. This
#                  works once the repository is published. Until then the script
#                  says so instead of failing with a bare HTTP 404.
#
# The script never writes into the repository and never touches the agent's
# configuration directory.
#
# Usage: sh scripts/install.sh [options]
#   --from-source        build from a checkout (default when run inside one)
#   --from-release       download and verify a published release
#   --source DIR         checkout to build from
#   --version V          pin a release tag, or set the build label reported by
#                        `mobiledeck version` in source mode
#   --prefix DIR         install the binary to DIR/bin
#   --with-gui           also build the desktop control panel (CGO; source only)
#   --service            install and enable a systemd *user* unit (Linux)
#   --no-service         do not install a unit (the default)
#   --dry-run            print every step without changing anything
#   --allow-root         allow running as root (container builds only)
#   -h, --help           this message
#
# See docs/INSTALL.md.

set -eu

# --- repository identity -----------------------------------------------------
# Publishing this repository is a two-line edit: owner and name. The clone URL,
# the release URL and the unit's Documentation= are all derived from them.
REPO_OWNER="mobiledeck"
REPO_NAME="mobiledeck"

# The one place the release download URL is written. Publishing under a
# different owner or name only requires the two lines above.
RELEASE_BASE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases"

CLONE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}.git"

usage() {
    cat <<EOF
install.sh - install the MobileDeck host agent (Linux, macOS)

usage: sh scripts/install.sh [options]

Modes (the default is source inside a checkout, release otherwise):
  --from-source        build from a checkout (needs go)
  --from-release       download a published release and verify its checksum
  --source DIR         build from DIR instead of the detected checkout

Options:
  --version V          pin a release tag, or set the build label reported by
                       \`mobiledeck version\` in source mode
  --prefix DIR         install the binary to DIR/bin
                       (default: \$PREFIX/bin, else \$HOME/.local/bin)
  --with-gui           also build the desktop control panel (implies CGO; on
                       Linux this needs the WebKitGTK development package)
  --service            install and enable a systemd *user* unit (Linux only)
  --no-service         do not install a unit (the default)
  --dry-run            print every step without changing anything
  --allow-root         allow running as root (for container builds)
  -h, --help           this message

The agent runs as the logged-in user and never elevates (docs/SECURITY.md 4).
The installer refuses to run as root unless --allow-root is given.
EOF
}

die() {
    printf 'install: %s\n' "$*" >&2
    exit 1
}

note() {
    printf '%s\n' "$*"
}

# --- arguments ---------------------------------------------------------------

mode=""
version=""
prefix=""
source_dir=""
with_gui=0
service=0
dry_run=0
allow_root=0

while [ $# -gt 0 ]; do
    case "$1" in
        --from-source) mode="source" ;;
        --from-release|--release) mode="release" ;;
        --source)
            shift
            if [ $# -eq 0 ]; then die "--source needs a directory"; fi
            source_dir=$1
            ;;
        --source=*) source_dir=${1#--source=} ;;
        --version)
            shift
            if [ $# -eq 0 ]; then die "--version needs a value"; fi
            version=$1
            ;;
        --version=*) version=${1#--version=} ;;
        --prefix)
            shift
            if [ $# -eq 0 ]; then die "--prefix needs a directory"; fi
            prefix=$1
            ;;
        --prefix=*) prefix=${1#--prefix=} ;;
        --with-gui) with_gui=1 ;;
        --service) service=1 ;;
        --no-service) service=0 ;;
        --dry-run) dry_run=1 ;;
        --allow-root) allow_root=1 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
    shift
done

# --- platform ----------------------------------------------------------------

uname_s=$(uname -s)
uname_m=$(uname -m)

case "$uname_s" in
    Linux) goos=linux ;;
    Darwin) goos=darwin ;;
    # Git Bash / MSYS / Cygwin on Windows. Not the supported Windows path - that
    # is scripts/install.ps1 - but it makes this script usable from a POSIX
    # shell there, and it is how the from-source path is exercised in CI.
    MINGW*|MSYS*|CYGWIN*) goos=windows ;;
    *) die "unsupported operating system: $uname_s. On Windows use scripts/install.ps1." ;;
esac

case "$uname_m" in
    x86_64|amd64) goarch=amd64 ;;
    aarch64|arm64) goarch=arm64 ;;
    armv7l|armv7|armv6l|armv6) goarch=arm ;;
    i386|i486|i586|i686) goarch=386 ;;
    *) die "unsupported architecture: $uname_m" ;;
esac

if [ "$(id -u)" -eq 0 ] && [ "$allow_root" -eq 0 ]; then
    die "refusing to run as root. The agent must run as the logged-in user and never elevates (docs/SECURITY.md 4). Pass --allow-root only to build a container image."
fi

# --- locations ---------------------------------------------------------------

if [ -n "$prefix" ]; then
    bindir="$prefix/bin"
elif [ -n "${PREFIX:-}" ]; then
    bindir="$PREFIX/bin"
else
    bindir="$HOME/.local/bin"
fi

# A relative --prefix is resolved against the current directory: the systemd
# unit needs an absolute ExecStart, and so does the PATH hint below.
is_absolute() {
    case "$1" in
        /*) return 0 ;;          # POSIX absolute
        [A-Za-z]:[\\/]*) return 0 ;;  # Windows drive path (Git Bash, Cygwin)
    esac
    return 1
}

if ! is_absolute "$bindir"; then
    bindir="$PWD/${bindir#./}"
fi

config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/mobiledeck"
if ! is_absolute "$config_dir"; then
    config_dir="$PWD/${config_dir#./}"
fi

# A checkout is detected from this script's own location. When the script is
# piped into a shell there is no location, and release mode is used.
script_dir=""
case "$0" in
    */*) script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd) || script_dir="" ;;
esac
repo_root=""
if [ -n "$script_dir" ] && [ -f "$script_dir/../host/go.mod" ]; then
    repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
fi

if [ -n "$source_dir" ]; then
    if [ ! -f "$source_dir/host/go.mod" ]; then
        die "--source $source_dir does not look like the repository (no host/go.mod)"
    fi
    if [ -z "$mode" ]; then mode="source"; fi
fi

if [ -z "$mode" ]; then
    if [ -n "$repo_root" ]; then mode="source"; else mode="release"; fi
fi

if [ "$with_gui" -eq 1 ] && [ "$mode" = "release" ]; then
    die "--with-gui needs a source build: the published tarball is built with CGO disabled and contains no window. Use --from-source."
fi

if [ "$mode" = "release" ] && [ "$goos" = "windows" ]; then
    die "release archives for Windows are .zip and are installed by scripts/install.ps1. Use that, or --from-source here."
fi

if [ "$mode" = "source" ] && [ -z "$source_dir" ]; then
    source_dir="$repo_root"
fi

# --- version label -----------------------------------------------------------

build_version="${version:-dev}"
if [ "$mode" = "source" ] && [ -z "$version" ] && [ -n "$source_dir" ] && command -v git >/dev/null 2>&1; then
    described=$(git -C "$source_dir" describe --tags --always --dirty 2>/dev/null || true)
    if [ -n "$described" ]; then build_version=$described; fi
fi

# --- helpers -----------------------------------------------------------------

tmp=""

need_tmp() {
    if [ -z "$tmp" ]; then
        tmp=$(mktemp -d "${TMPDIR:-/tmp}/mobiledeck-install.XXXXXX")
        trap 'rm -rf "$tmp"' EXIT HUP INT TERM
    fi
}

fetch() {
    # fetch URL DEST
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        die "neither curl nor wget is on PATH; install one, or use --from-source"
    fi
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        die "no SHA-256 tool found (need sha256sum, shasum or openssl)"
    fi
}

lower() {
    tr 'A-Z' 'a-z'
}

install_binary() {
    # install_binary SRC DEST
    mkdir -p "$bindir"
    cp "$1" "$2.tmp.$$"
    chmod 0755 "$2.tmp.$$"
    mv -f "$2.tmp.$$" "$2"
}

binary_path() {
    if [ "$goos" = "windows" ]; then printf '%s' "$bindir/mobiledeck.exe"; else printf '%s' "$bindir/mobiledeck"; fi
}

install_service() {
    if [ "$goos" != "linux" ]; then
        note "install: not installing a unit: systemd is Linux-only"
        return 0
    fi
    if ! command -v systemctl >/dev/null 2>&1; then
        note "install: not installing a unit: systemctl is not on PATH"
        return 0
    fi

    unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
    unit_file="$unit_dir/mobiledeck.service"
    mkdir -p "$unit_dir"

    cat > "$unit_file" <<EOF
[Unit]
Description=MobileDeck host agent
Documentation=https://github.com/${REPO_OWNER}/${REPO_NAME}
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
# The agent must run as the logged-in user, never as root (SECURITY.md 4).
# A user unit is already unprivileged; no User= or sudo is involved.
ExecStart=$(binary_path) run --config-dir $config_dir
Restart=on-failure
RestartSec=3
# A clean shutdown on SIGTERM; the agent closes sessions and removes its
# runtime file.
KillSignal=SIGTERM
TimeoutStopSec=10

[Install]
WantedBy=graphical-session.target
EOF

    note "install: wrote $unit_file"
    systemctl --user daemon-reload || true
    if systemctl --user enable --now mobiledeck; then
        note "install: systemd user unit enabled; follow it with: journalctl --user -u mobiledeck -f"
    else
        note "install: could not enable the unit here; run: systemctl --user enable --now mobiledeck"
    fi
}

next_steps() {
    printf '\nnext:\n'
    printf '  %s run --bind 0.0.0.0\n' "$(binary_path)"
    printf '  %s pair\n' "$(binary_path)"
    printf '\n'
}

# --- dry run -----------------------------------------------------------------

note "install: $mode mode, target $goos/$goarch, binary $(binary_path)"
if [ -d "$config_dir" ]; then
    note "install: configuration directory $config_dir exists; it will not be touched"
fi

if [ "$dry_run" -eq 1 ]; then
    note "install: dry run, nothing will be changed"

    if [ "$mode" = "release" ]; then
        asset="mobiledeck_${goos}_${goarch}.tar.gz"
        if [ -n "$version" ]; then
            release_dir="$RELEASE_BASE_URL/download/$version"
        else
            release_dir="$RELEASE_BASE_URL/latest/download"
        fi
        note "would download $release_dir/$asset"
        note "would download $release_dir/checksums.txt and verify the SHA-256 of $asset"
        note "would unpack $asset and install mobiledeck to $(binary_path)"
    else
        if [ -n "$source_dir" ]; then
            note "would build $source_dir/host with CGO_ENABLED=$with_gui (version $build_version)"
        else
            note "would clone $CLONE_URL and build it with CGO_ENABLED=$with_gui (version $build_version)"
        fi
        note "would install mobiledeck to $(binary_path)"
    fi

    if [ "$service" -eq 1 ]; then
        note "would write ${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/mobiledeck.service and enable it"
    fi

    next_steps
    exit 0
fi

# --- obtain the binary -------------------------------------------------------

built=""

if [ "$mode" = "release" ]; then
    asset="mobiledeck_${goos}_${goarch}.tar.gz"
    if [ -n "$version" ]; then
        release_dir="$RELEASE_BASE_URL/download/$version"
    else
        release_dir="$RELEASE_BASE_URL/latest/download"
    fi
    asset_url="$release_dir/$asset"
    checksums_url="$release_dir/checksums.txt"

    note "install: downloading $asset_url"
    need_tmp
    fetch "$asset_url" "$tmp/$asset" || die "could not download $asset_url. Release downloads require the repository to be published; it is not yet. Use --from-source to build from a checkout."
    fetch "$checksums_url" "$tmp/checksums.txt" || die "could not download $checksums_url, so the download cannot be verified. Refusing to install an unverified binary."

    expected=$(awk -v f="$asset" '{ n=$2; sub(/^\*/, "", n); if (n == f) { print $1; exit } }' "$tmp/checksums.txt")
    if [ -z "$expected" ]; then
        die "$asset is not listed in checksums.txt; refusing to install an unverified binary"
    fi
    actual=$(sha256_of "$tmp/$asset")
    if [ "$(printf '%s' "$expected" | lower)" != "$(printf '%s' "$actual" | lower)" ]; then
        die "checksum mismatch for $asset: expected $expected, got $actual"
    fi
    note "install: checksum ok ($actual)"

    mkdir -p "$tmp/unpack"
    tar -xzf "$tmp/$asset" -C "$tmp/unpack"
    built=$(find "$tmp/unpack" -type f \( -name mobiledeck -o -name mobiledeck.exe \) | head -n 1)
    if [ -z "$built" ]; then
        die "the archive did not contain a mobiledeck binary"
    fi
else
    if [ -z "$source_dir" ]; then
        if ! command -v git >/dev/null 2>&1; then
            die "no checkout found and git is not on PATH. Run this script from inside the repository, pass --source DIR, or publish the repository and use --from-release."
        fi
        need_tmp
        if [ -n "$version" ]; then
            note "install: cloning $CLONE_URL at $version"
            git clone --depth 1 --branch "$version" "$CLONE_URL" "$tmp/src" ||
                die "could not clone $CLONE_URL. A from-source install needs a checkout: run this script from inside the repository, or pass --source DIR. If the repository is published, check the tag and your network."
        else
            note "install: cloning $CLONE_URL"
            git clone --depth 1 "$CLONE_URL" "$tmp/src" ||
                die "could not clone $CLONE_URL. A from-source install needs a checkout: run this script from inside the repository, or pass --source DIR. If the repository is published, check your network."
        fi
        source_dir="$tmp/src"
    fi

    if ! command -v go >/dev/null 2>&1; then
        die "from-source install needs Go 1.26 or newer on PATH"
    fi

    cgo=0
    if [ "$with_gui" -eq 1 ]; then
        cgo=1
        if [ "$goos" = "linux" ]; then
            note "install: --with-gui on Linux needs the WebKitGTK development package: sudo apt install libwebkit2gtk-4.1-dev"
        fi
    fi

    need_tmp
    note "install: building $source_dir/host (CGO_ENABLED=$cgo, version $build_version)"
    (
        cd "$source_dir/host"
        CGO_ENABLED=$cgo go build -trimpath -ldflags "-X main.version=$build_version" -o "$tmp/mobiledeck" ./cmd/mobiledeck
    )
    built="$tmp/mobiledeck"
    if [ "$goos" = "windows" ] && [ -f "$built.exe" ]; then
        built="$built.exe"
    fi
fi

# --- install -----------------------------------------------------------------

install_binary "$built" "$(binary_path)"
note "install: installed $(binary_path)"

if [ "$service" -eq 1 ]; then
    install_service
fi

case ":${PATH:-}:" in
    *":$bindir:"*) ;;
    *)
        if [ "${bindir#/}" != "$bindir" ]; then
            note "install: $bindir is not on PATH; add it, for example:"
            note "  echo 'export PATH=\"$bindir:\$PATH\"' >> ~/.profile"
        fi
        ;;
esac

next_steps
