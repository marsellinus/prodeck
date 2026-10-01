# ADR-0002: Android client in Kotlin + Jetpack Compose

Status: accepted (Milestone 1)

## Context
The client is a touch grid that renders a host-supplied layout, with discovery, pairing,
and a resilient realtime connection.

## Decision
**Kotlin + Jetpack Compose (Material 3)**, single module, `minSdk 26` (Android 8.0),
`targetSdk 36`.

## Consequences
- No bridge, no JS runtime, no second toolchain: one language, one build.
- Compose makes "render an arbitrary grid from data" a `LazyVerticalGrid` over a list —
  no XML layouts, no view recycling bugs, no per-device layout files.
- `minSdk 26` gives `NsdManager`, `Vibrator` with `VibrationEffect`, and Keystore-backed
  encrypted storage without compat shims. Android 8+ covers the stated requirement.
- Cost: Android-only. Accepted — the requirement names Android specifically, and a
  cross-platform UI framework would add a bridge process and a second debug surface for
  a screen that is fundamentally a grid of rectangles.
- Rejected: Flutter/React Native (extra runtime + platform channels for NsdManager and
  Vibration), and a WebView client (worse touch latency, no native haptics/discovery).
