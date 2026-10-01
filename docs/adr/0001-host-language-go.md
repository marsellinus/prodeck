# ADR-0001: Host agent in Go

Status: accepted (Milestone 1)

## Context
The host must be a single lightweight cross-platform binary that controls keyboard,
mouse, media, launches processes, and speaks WebSocket + mDNS, on Linux/Windows/macOS,
without requiring a runtime, a Docker daemon, or an installer framework.

## Decision
Implement the host in **Go** (stdlib `net/http` + `github.com/gorilla/websocket` +
`github.com/grandcat/zeroconf`).

## Consequences
- `GOOS=windows|linux|darwin go build` produces a static binary; `CGO_ENABLED=0` keeps
  cross-compilation trivial and makes the binary self-contained.
- Rich, mature OS-input and mDNS libraries already exist in Go.
- Goroutines map directly onto "one goroutine per session", which is the concurrency
  model we want.
- Cost: no compile-time exhaustive enums, weaker type-level guarantees than Rust. We
  compensate with table-driven tests and explicit validation at the protocol boundary.
- Rust was the alternative and would have been a fine choice (stronger types, no GC).
  Go wins on time-to-working-MVP and on cross-compilation ergonomics; the extra safety
  Rust buys is at the layer where we instead put a validation pass.
