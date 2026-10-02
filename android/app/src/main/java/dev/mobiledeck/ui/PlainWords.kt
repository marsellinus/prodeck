package dev.mobiledeck.ui

import dev.mobiledeck.data.ConnectionState

/**
 * The words a person reads for the host's own vocabulary.
 *
 * They live in one place because the same host name is rendered on more than one
 * screen — a scope in the settings sheet, a metric both on a tile and in the
 * sheet, an OS name on the connect screen and in the sheet — and two copies of a
 * translation table disagree the first time one of them gains a word.
 *
 * The scope words are deliberately the ones the desktop control panel prints for
 * the same scope. The person who paired the phone read those words in the panel,
 * and two names for one permission is how a user grants something they did not
 * mean to grant.
 *
 * A name this build does not know falls through unchanged. An unknown scope or
 * metric belongs to a plugin or a newer host, and showing the host's own name is
 * better than hiding a permission or a reading behind a placeholder.
 */
internal fun plainScopeName(scope: String): String = when (scope) {
    "keyboard" -> "press keys"
    "mouse" -> "move the mouse"
    "media" -> "control music"
    "apps" -> "open programs"
    "scripts" -> "run scripts"
    "system.read" -> "see system info"
    "system.power" -> "shut down or restart"
    "profiles.write" -> "edit boards"
    "plugins" -> "use add-ons"
    else -> scope
}

/**
 * The plain name of a telemetry metric, or the metric's own name.
 *
 * The unit is part of the name where the raw number has none: the sheet prints
 * the host's value unchanged, and a byte count or a second count with no unit is
 * a number the reader cannot interpret at all.
 */
internal fun plainMetricName(metric: String): String = when (metric) {
    "cpu.usage" -> "CPU in use"
    "mem.used_pct" -> "Memory in use"
    "mem.total_bytes" -> "Memory total (bytes)"
    "mem.used_bytes" -> "Memory used (bytes)"
    "disk.used_pct" -> "Disk in use"
    "net.rx_bps" -> "Download (bytes/s)"
    "net.tx_bps" -> "Upload (bytes/s)"
    "uptime_s" -> "Uptime (seconds)"
    "host.name" -> "Computer name"
    else -> metric
}

/** `darwin` is the kernel's name for macOS, and nobody's name for their computer. */
internal fun plainOsName(os: String): String = when (os.lowercase()) {
    "windows" -> "Windows"
    "linux" -> "Linux"
    "darwin", "macos", "mac" -> "macOS"
    else -> os
}

/**
 * The unit a metric's own name implies, or null when the name does not carry one.
 *
 * PROTOCOL.md §7 names the milestone-1 metrics with their unit in the suffix
 * (`_pct`, `_bytes`, `_bps`, `_s`), which is the only place the unit is stated:
 * `event.telemetry` sends bare numbers. A profile whose format omits the unit
 * therefore renders a number a reader cannot interpret — `mem.used_bytes` as
 * `4294967296` — so the suffix is turned back into the unit here.
 *
 * An unknown metric returns null rather than a guess. The name belongs to the
 * host or a plugin, and inventing a unit for a reading we do not understand is
 * worse than showing the bare number the profile asked for.
 */
internal fun metricUnit(metric: String?): String? = when {
    metric == null -> null
    metric.endsWith("_pct") -> "%"
    // `cpu.usage` is a percentage on all three platform implementations — the
    // two that divide busy time by total time multiply by 100, and the macOS one
    // parses `top`, which reports a percentage — but its name has no suffix to
    // say so.
    metric == "cpu.usage" -> "%"
    metric.endsWith("_bytes") -> " bytes"
    metric.endsWith("_bps") -> " B/s"
    metric.endsWith("_s") -> " s"
    else -> null
}

/** The connection state in the words the banners use, so the sheet agrees with them. */
internal fun plainConnectionName(connection: ConnectionState): String = when (connection) {
    ConnectionState.Connected -> "Connected"
    ConnectionState.Connecting -> "Connecting…"
    ConnectionState.Pairing -> "Waiting for a PIN"
    ConnectionState.Reconnecting -> "Reconnecting…"
    ConnectionState.Disconnected -> "Not connected"
}
