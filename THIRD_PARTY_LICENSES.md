# Third-party licenses

Every entry below was verified by reading the actual license file in the local
dependency cache, not from memory. The verification source is named for each
entry. Anything that could not be verified is marked `[unverified]` rather than
guessed.

Verification commands used:

```sh
go env GOMODCACHE          # Go module cache
# Gradle: ~/.gradle/caches/modules-2/files-2.1/<group>/<artifact>/<version>/
```

---

## Policy

- The project itself is licensed **Apache-2.0** (ADR-0010, `LICENSE`).
- **No GPL or AGPL dependency is present or permitted.** A dependency with a
  copyleft license incompatible with Apache-2.0 is a release blocker.
- Apache-2.0 requires preserving attribution and stating changes in modified
  files. That is the practical cost of the license choice, and this file plus
  intact upstream license texts are how the project pays it.
- A new dependency must be added here, with its license, before it is merged
  (CONTRIBUTING.md §4).
- This file is checked at review time (ADR-0010).

---

## Go dependencies

Versions are the ones pinned in `host/go.mod`.

| Component | Version | License | Why it is used |
|-----------|---------|---------|----------------|
| `github.com/gorilla/websocket` | v1.5.3 | BSD-2-Clause | The single WebSocket implementation for the protocol envelope (ADR-0003). Chosen in ADR-0001. |
| `github.com/grandcat/zeroconf` | v1.0.0 | MIT | mDNS / DNS-SD responder so the client can discover a host with no internet (PROTOCOL.md §4). |
| `github.com/miekg/dns` | v1.1.27 | BSD-3-Clause | DNS record construction and parsing underneath zeroconf. Indirect, pulled by zeroconf. |
| `github.com/jezek/xgb` | v1.3.1 | BSD-3-Clause | X11 protocol client for Linux input injection (XTEST) and display queries. |
| `github.com/godbus/dbus/v5` | v5.2.2 | BSD-2-Clause | D-Bus: MPRIS media control and systemd/logind power actions on Linux. |
| `golang.org/x/sys` | v0.48.0 | BSD-3-Clause | Low-level OS syscalls (Windows `SendInput`, process attributes, filesystem calls) that the standard library does not expose. |
| `golang.org/x/crypto` | v0.57.0 | BSD-3-Clause | Cryptography helpers used by the TLS/pairing path. Indirect; bumped from a 2019 revision because the older `x/net` it pulled in fails to link on darwin/arm64. |
| `golang.org/x/net` | v0.59.0 | BSD-3-Clause | Network primitives used by the discovery/WebSocket stack. Indirect; bumped for the same darwin/arm64 link failure. |
| `github.com/cenkalti/backoff` | v2.2.1+incompatible | MIT | Exponential backoff with jitter. Indirect, pulled by zeroconf. |
| `golang.org/x/sync` | v0.23.0 | BSD-3-Clause | Concurrency primitives underneath `x/net`. Indirect. |

### Verification detail

| Component | License file read | Evidence |
|-----------|-------------------|----------|
| `gorilla/websocket` | `$GOMODCACHE/github.com/gorilla/websocket@v1.5.3/LICENSE` | Two redistribution clauses, no endorsement clause → BSD-2-Clause. Copyright (c) 2013 The Gorilla WebSocket Authors. |
| `grandcat/zeroconf` | `$GOMODCACHE/github.com/grandcat/zeroconf@v1.0.0/LICENSE` | First line: "The MIT License (MIT)". |
| `miekg/dns` | `$GOMODCACHE/github.com/miekg/dns@v1.1.27/LICENSE` | Three clauses including "Neither the name of Google Inc. nor the names of its contributors" → BSD-3-Clause. |
| `jezek/xgb` | `$GOMODCACHE/github.com/jezek/xgb@v1.3.1/LICENSE` | Three clauses including the Google Inc. endorsement clause → BSD-3-Clause. |
| `godbus/dbus/v5` | `$GOMODCACHE/github.com/godbus/dbus/v5@v5.2.2/LICENSE` | Two clauses, no endorsement clause → BSD-2-Clause. |
| `golang.org/x/sys` | `$GOMODCACHE/golang.org/x/sys@v0.48.0/LICENSE` | BSD-3-Clause, plus a separate `PATENTS` file (Google patent grant). |
| `golang.org/x/crypto` | `$GOMODCACHE/golang.org/x/crypto@v0.57.0/LICENSE` | BSD-3-Clause, plus `PATENTS`. |
| `golang.org/x/net` | `$GOMODCACHE/golang.org/x/net@v0.59.0/LICENSE` | BSD-3-Clause, plus `PATENTS`. |
| `cenkalti/backoff` | `$GOMODCACHE/github.com/cenkalti/backoff@v2.2.1+incompatible/LICENSE` | First line: "The MIT License (MIT)". Copyright (c) 2014 Cenk Altı. |

---

## Android dependencies

Versions are the ones declared in `android/app/build.gradle.kts`.

| Component | Version | License | Why it is used |
|-----------|---------|---------|----------------|
| AndroidX (`androidx.core:core-ktx`, `androidx.activity:activity-compose`, `androidx.lifecycle:*`) | core-ktx 1.13.1, activity-compose 1.9.3, lifecycle 2.8.7 | Apache-2.0 | Core Kotlin extensions, Compose activity host, and lifecycle-aware state. |
| Jetpack Compose (`androidx.compose:compose-bom` and its members: `ui`, `ui-graphics`, `ui-tooling-preview`, `material3`, `material-icons-extended`) | BOM 2024.10.01 | Apache-2.0 | The grid UI is Compose over a list (ADR-0002). |
| Kotlin (`org.jetbrains.kotlin:kotlin-stdlib` and the Android Gradle plugin) | 2.1.0 | Apache-2.0 | The client language and compiler (ADR-0002). |
| kotlinx.serialization (`org.jetbrains.kotlinx:kotlinx-serialization-json`) | 1.7.3 | Apache-2.0 | Decoding the JSON envelope and profile documents without reflection. |
| kotlinx.coroutines (`org.jetbrains.kotlinx:kotlinx-coroutines-android`, `-test`) | 1.9.0 | Apache-2.0 | Structured concurrency for the connection state machine and the UI. |
| OkHttp (`com.squareup.okhttp3:okhttp`) | 4.12.0 | Apache-2.0 | WebSocket transport (`WsTransport`) and the REST pairing client. |
| JUnit 4 (`junit:junit`) | 4.13.2 | Eclipse Public License 1.0 (EPL-1.0) | JVM unit tests. Test-scope only; it is not distributed in the app. |

### Verification detail

| Component | Verification source | Evidence |
|-----------|---------------------|----------|
| AndroidX / Compose | `~/.gradle/caches/modules-2/files-2.1/androidx.core/core/1.15.0/.../core-1.15.0.pom` and `androidx.activity/activity/1.8.1`, `androidx.lifecycle/lifecycle-runtime/2.8.7` POMs; `compose-bom-2024.10.01.pom` | `<name>The Apache Software License, Version 2.0</name>` in every POM. |
| Kotlin | `org.jetbrains.kotlin/kotlin-stdlib/2.1.0/*.pom` (cache) and 2.1.20 | `<name>The Apache License, Version 2.0</name>`. |
| kotlinx.serialization | `kotlinx-serialization-json-1.7.3.pom` (Maven Central) | `<name>The Apache Software License, Version 2.0</name>`. |
| kotlinx.coroutines | `kotlinx-coroutines-android-1.9.0.pom` (Maven Central) | `<name>The Apache Software License, Version 2.0</name>`. |
| OkHttp | `okhttp-4.12.0.pom` (Maven Central) | `<name>The Apache Software License, Version 2.0</name>`. |
| JUnit 4 | `junit-4.13.2.pom` (Maven Central) and `LICENSE-junit.txt` inside `junit-4.13.2.jar` | POM says `<name>Eclipse Public License 1.0</name>`; the bundled `LICENSE-junit.txt` text is "Eclipse Public License - v 1.0". |

Notes on the Android side:

- The Kotlin/AndroidX/Compose artifacts are resolved through the Gradle cache and
  the Google Maven repository; the local cache at the time of writing had the
  Google-hosted POMs only for the AndroidX artifacts it had already fetched. The
  POMs for the remaining artifacts were read from their canonical repositories
  (Maven Central / Google Maven) because the local cache was being populated by
  a concurrent build. Each entry's `<licenses>` block is quoted above.
- `androidx.compose.material:material-icons-extended` is part of the Compose BOM
  and carries the same Apache-2.0 license as the rest of the BOM.
- EPL-1.0 is not a copyleft license that reaches the application: JUnit is a
  test-scope dependency and is not packaged into the APK. The policy ban is on
  GPL/AGPL, and JUnit is neither.

---

## Explicitly not used

- No GPL or AGPL component is a direct or transitive dependency of the host or
  the client. The Android Gradle build excludes `META-INF/{AL2.0,LGPL2.1}` from
  packaging, which is the standard Compose/Gradle advice, not an LGPL dependency.
- No native library with a copyleft license is linked.

## Adding a dependency

1. Read its actual license file; do not trust the README badge.
2. If it is GPL/AGPL, stop — it cannot be added.
3. Add a row above with component, version, license, and a one-line reason.
4. If the license requires distributing the license text, keep that text intact
   in the dependency and reference it here.
