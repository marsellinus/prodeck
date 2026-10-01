# ADR-0009: Single static binary; Docker optional and never required

Status: accepted (Milestone 1)

## Context
Requirement §35: Docker may be used for development, docs, testing, and optional server
components, but the host agent must run directly via `./mobiledeck`.

## Decision
One Go binary with `cli`, `server`, `engine`, and `platform` linked in. Docker appears
only in `docker-compose.yml` for documentation/dev tooling, never as a runtime
dependency of the agent.

## Consequences
- Deployment is `scp` + `chmod +x`, or a `.deb`/`.tar.gz`/installer (phase 5 packaging).
- No container networking/device-access problem for keyboard and mouse control — a
  container cannot drive the host's X11 session without `--privileged` and socket mounts,
  which would be worse than not using Docker at all.
- Cost: the binary embeds everything, so plugin distribution must be files + a documented
  interface rather than container images. That is the M3 design anyway.
