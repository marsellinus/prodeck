# ADR-0008: Transport abstraction now, one implementation (LAN WebSocket)

Status: accepted (Milestone 1)

## Context
Requirement §13 asks for Wi-Fi/USB/tethering support but explicitly warns against a
half-built USB implementation. ADB-based USB is slow and requires `adb` on the host; USB
tethering is just IP-over-USB and needs no new transport at all.

## Decision
Define `Transport` on both sides (open/send/close, message framing independent). Ship
exactly one implementation: LAN WebSocket. Add the interface because it costs ~40 lines
and it is the seam that keeps USB from becoming a refactor.

## Consequences
- USB tethering works **today** with zero extra code: the phone is reachable at an IP, so
  it is the same transport. This is the honest 90% of the USB requirement.
- ADB port-forwarding (`adb reverse tcp:8765 tcp:8765`) also works today and is the
  recommended cable path; documented in DEVELOPMENT.md.
- A true native USB transport (AOA/ADB protocol) is deferred to phase 5 rather than
  shipped as a stub, per the explicit instruction in the requirements.
