#!/bin/sh
# Build the host agent and run it against a throwaway configuration directory.
#
# The instance is bound to 127.0.0.1 so it is not reachable from the LAN, and
# every file it writes (config, devices, audit log, TLS material, profiles,
# logs) lives under ./.devdata, which is gitignored. Delete that directory to
# reset to a first-run state.
#
# Usage:
#   scripts/dev-host.sh
#
# Extra arguments are passed through to the host before the `run` subcommand,
# so they are global flags (for example `--allow-absolute-paths`). To change the
# port or the log level, edit ./.devdata/config.json; there is no flag for
# either.
set -eu

# Resolve the repository root from this script's location, so the script works
# from any working directory.
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

host_dir="$repo_root/host"
dev_dir="$repo_root/.devdata"
binary="$host_dir/mobiledeck"

if ! command -v go >/dev/null 2>&1; then
    echo "dev-host: go is not on PATH; install Go 1.26 or newer" >&2
    exit 1
fi

mkdir -p "$dev_dir"

echo "dev-host: building host -> $binary"
(
    cd "$host_dir"
    CGO_ENABLED=0 go build -trimpath -o mobiledeck ./cmd/mobiledeck
)

echo "dev-host: config dir $dev_dir"
echo "dev-host: bind 127.0.0.1 (loopback only; not reachable from a phone)"
echo "dev-host: stop with Ctrl+C"

exec "$binary" --config-dir "$dev_dir" --bind 127.0.0.1 "$@" run
