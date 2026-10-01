#!/usr/bin/env sh
#
# Start a throwaway MobileDeck host for the JVM integration tests, and print the
# environment variables they need.
#
# This is developer tooling, not part of the product. It exists so the Android
# client's protocol stack can be tested against a real host without touching the
# user's own configuration:
#
#   scripts/e2e-host.sh start   # run in the background, prints the exports
#   scripts/e2e-host.sh env     # print the exports for the running instance
#   scripts/e2e-host.sh stop
#
# The tests skip themselves when no host is reachable, so running
# `./gradlew test` without this still passes.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DATA="$ROOT/.devdata/e2e"
# A port of its own, so this never collides with a host the developer is already
# running on the default 8765.
PORT=${MOBILEDECK_E2E_PORT:-8766}

# Go appends no extension, but Windows needs one, so the built name is resolved
# rather than assumed: on a POSIX host it is "mobiledeck", on Windows
# "mobiledeck.exe".
if [ -f "$ROOT/.devdata/mobiledeck.exe" ] || [ "$(uname -s 2>/dev/null | cut -c1-5)" = "MINGW" ] || [ "${OS:-}" = "Windows_NT" ]; then
    BIN="$ROOT/.devdata/mobiledeck.exe"
else
    BIN="$ROOT/.devdata/mobiledeck"
fi

build() {
    if [ ! -x "$BIN" ]; then
        echo "building the host..." >&2
        (cd "$ROOT/host" && go build -o "$BIN" ./cmd/mobiledeck)
    fi
}

start() {
    build
    mkdir -p "$DATA"

    if [ ! -f "$DATA/config.json" ]; then
        "$BIN" init --config-dir "$DATA" >/dev/null
    fi

    # Bind to loopback with TLS off: the tests speak plain ws:// to 127.0.0.1, and
    # a self-signed certificate would only add a fingerprint to thread through.
    "$BIN" stop --config-dir "$DATA" >/dev/null 2>&1 || true
    rm -f "$DATA/runtime.json" "$DATA/logs/mobiledeck.pid"
    "$BIN" start --config-dir "$DATA" --bind 127.0.0.1 --port "$PORT" --no-tls >/dev/null

    sleep 1

    if [ ! -f "$DATA/runtime.json" ]; then
        echo "the host did not come up. Last lines of its log:" >&2
        tail -n 5 "$DATA/logs/mobiledeck.log" >&2 2>/dev/null || true
        echo >&2
        echo "a port conflict is the usual cause; try MOBILEDECK_E2E_PORT=8799 $0 start" >&2
        exit 1
    fi

    env_vars
}

env_vars() {
    if [ ! -f "$DATA/runtime.json" ]; then
        echo "no runtime.json: the host is not running" >&2
        exit 1
    fi
    token=$(sed -n 's/.*"admin_token": *"\([^"]*\)".*/\1/p' "$DATA/runtime.json")
    addr=$(sed -n 's/.*"addr": *"\([^"]*\)".*/\1/p' "$DATA/runtime.json")

    cat <<EOF
# Export these, then run the Android tests:
#
#   MOBILEDECK_E2E_ADDR=$addr \\
#   MOBILEDECK_E2E_ADMIN_TOKEN=$token \\
#   ./gradlew :app:testDebugUnitTest
#
export MOBILEDECK_E2E_ADDR=$addr
export MOBILEDECK_E2E_ADMIN_TOKEN=$token
EOF
}

stop() {
    build
    "$BIN" stop --config-dir "$DATA" || true
}

case "${1:-start}" in
    start) start ;;
    env)   env_vars ;;
    stop)  stop ;;
    *)     echo "usage: $0 [start|env|stop]" >&2; exit 2 ;;
esac
