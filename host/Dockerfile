# syntax=docker/dockerfile:1
#
# MobileDeck host agent, container image.
#
# The agent NEVER requires Docker (ADR-0009). Running it natively is the
# supported path for input control; see docs/DEPLOYMENT.md for what a container
# can and cannot do. This image exists so the subset of the host that does not
# touch the desktop session can be deployed like any other service.

# ---------------------------------------------------------------------------
# Builder
# ---------------------------------------------------------------------------
FROM golang:1.26-bookworm AS builder

# The release version stamped into the binary. Override with
#   docker build --build-arg VERSION=1.2.3 ...
ARG VERSION=dev

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 keeps the result a static binary (ADR-0001); GOOS=linux fixes
# the target even when the build runs on another OS.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o /out/mobiledeck ./cmd/mobiledeck

# ---------------------------------------------------------------------------
# Runtime
# ---------------------------------------------------------------------------
# debian:bookworm-slim, not scratch: the agent shells out to desktop and session
# tools at action time, and it needs a CA bundle for outbound TLS. A scratch
# image would make every one of those actions fail with "not found".
FROM debian:bookworm-slim

# Runtime packages, and why each is present. All of them are OPTIONAL: the agent
# starts and serves profiles without them, and only the actions that use them
# report `unsupported` when the tool is missing.
#
#   ca-certificates   TLS trust store; without it any outbound HTTPS from an
#                     action fails. Required, not optional.
#   curl              only for the HEALTHCHECK below; the agent itself never
#                     calls it.
#   xdg-utils         provides xdg-open, used by open_url and open_folder.
#   pulseaudio-utils  provides pactl, the preferred volume/mute tool.
#   wireplumber       provides wpctl, the PipeWire volume/mute tool.
#   alsa-utils        provides amixer, the fallback volume tool.
#   systemd           provides systemctl and loginctl, used by system.power and
#                     system.lock. Inside a container there is normally no
#                     systemd PID 1 and no session bus, so these actions report
#                     `unsupported` unless the container is given host
#                     integration; the binaries are installed so the failure is
#                     a clear "no permission / not running" rather than
#                     "command not found".
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      xdg-utils \
      pulseaudio-utils \
      wireplumber \
      alsa-utils \
      systemd \
 && rm -rf /var/lib/apt/lists/*

# The static binary from the builder stage. Nothing else from the build stage
# ships.
COPY --from=builder /out/mobiledeck /usr/local/bin/mobiledeck

# The agent MUST NOT run as root (docs/SECURITY.md §4). It never elevates and
# never needs to; running it as root would only widen the blast radius of a
# compromised token. uid/gid 1000 matches the usual first desktop user, which
# makes bind-mounting a config directory owned by that user work unchanged.
RUN groupadd --gid 1000 mobiledeck \
 && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin mobiledeck

# Input control needs the desktop session (X11/Wayland, and on Linux the session
# bus). A container has neither, so keyboard.*, mouse.* and anything else that
# reaches into X11 will report `unsupported`. The container is therefore only
# useful for the subset of actions that do not touch the session: profile
# serving, telemetry, and scripts that need no display. See docs/DEPLOYMENT.md.
# Conventional location marker for operators. The binary does NOT read this
# variable: the ENTRYPOINT passes --config-dir /data explicitly, and the
# variable is set only so an operator's own tooling can discover the mount.
ENV MOBILEDECK_CONFIG_DIR=/data

RUN mkdir -p /data && chown mobiledeck:mobiledeck /data
VOLUME /data

EXPOSE 8765

# The health endpoint is unauthenticated (docs/PROTOCOL.md §2.1). Try plain HTTP
# first, then the pinned-self-signed HTTPS path, so the check works whether the
# host was started with TLS on or off.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD-SHELL curl -fsS http://127.0.0.1:8765/api/v1/health >/dev/null 2>&1 \
   || curl -fsS -k https://127.0.0.1:8765/api/v1/health >/dev/null 2>&1 \
   || exit 1

USER mobiledeck
WORKDIR /data

ENTRYPOINT ["/usr/local/bin/mobiledeck", "run", "--config-dir", "/data", "--bind", "0.0.0.0"]
