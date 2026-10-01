package dev.mobiledeck.ui

import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.PressInteraction
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.AwaitPointerEventScope
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.mobiledeck.data.Button
import dev.mobiledeck.data.ButtonState
import dev.mobiledeck.data.ButtonStateValue
import dev.mobiledeck.data.Icon as ProfileIcon
import dev.mobiledeck.data.PressKind
import dev.mobiledeck.ui.theme.LocalDeckDimensions
import dev.mobiledeck.ui.theme.parseColor
import dev.mobiledeck.ui.theme.readableOn
import kotlinx.coroutines.delay
import kotlinx.coroutines.withTimeoutOrNull

/**
 * The 400 ms long-press threshold.
 *
 * PROTOCOL.md §6.1 makes this the client's decision: "The client decides `long`
 * via its own threshold (default 400 ms) and sends **one** event per physical
 * gesture; the host does not duplicate gesture detection."
 */
const val LONG_PRESS_MS = 400L

/** Default `on_hold` interval when the button does not declare `hold_repeat_ms`. */
const val DEFAULT_HOLD_REPEAT_MS = 250

/**
 * One tile of the deck.
 *
 * ## Gesture ownership
 *
 * This composable is the only place a physical gesture becomes a protocol
 * message, and it is built so that exactly one press event leaves per gesture:
 *
 *  * a release before [LONG_PRESS_MS] sends one `short`;
 *  * a hold past the threshold sends one `long`, and the release that follows
 *    sends **no** second press;
 *  * `on_hold` sends `repeat` at `hold_repeat_ms` while the finger stays down —
 *    never `short` or `long`, so a repeat cannot be mistaken for a fresh tap;
 *  * a long press on a button with no `on_long_press` still fires `on_press`
 *    once, because a button that does nothing when held a moment too long is
 *    indistinguishable from a broken one.
 *
 * ## State rendering
 *
 * The visual result merges three sources: the host's pushed
 * `event.button.state`, the local optimistic flip for toggle/radio, and the
 * profile's static `state` object. The host wins where they disagree, because
 * only the host knows what actually happened.
 */
@Composable
fun DeckButton(
    button: Button,
    /** Host-pushed or locally-flipped state, or null when there is none. */
    override: ButtonStateValue?,
    /** Telemetry value for a `state.type == "telemetry"` button. */
    telemetryValue: Double?,
    /** False while reconnecting: the tile renders but sends nothing. */
    interactive: Boolean,
    haptics: Haptics,
    onPress: (kind: String) -> Unit,
    onRelease: (heldMs: Long) -> Unit,
    modifier: Modifier = Modifier,
) {
    val dimensions = LocalDeckDimensions.current

    val background = parseColor(button.background)
        ?: parseColor(override?.color)
        ?: MaterialTheme.colorScheme.surfaceContainer
    val foreground = parseColor(button.foreground) ?: readableOn(background)

    val interactionSource = remember { MutableInteractionSource() }
    val pressed by interactionSource.collectIsPressedAsState()

    // The tile shrinks while held. For a `momentary` button this is the only
    // press feedback there is, so it has to be visible at a glance.
    val scale by animateFloatAsState(
        targetValue = if (pressed) 0.94f else 1f,
        animationSpec = spring(dampingRatio = Spring.DampingRatioMediumBouncy),
        label = "tileScale",
    )

    val active = override?.active ?: false
    val statusColor = parseColor(override?.color)

    Box(
        modifier = modifier
            .scale(scale)
            // A non-interactive tile is dimmed, not hidden: during a reconnect
            // the user must still see the layout they are coming back to.
            .alpha(if (interactive) 1f else 0.45f)
            .clip(RoundedCornerShape(dimensions.buttonRadius))
            .background(background)
            .then(
                // An "on" toggle or radio, or a host-coloured status, gets a
                // border. A border rather than a fill, because a profile whose
                // button background already matches the accent would show no
                // difference at all.
                if (active || statusColor != null) {
                    Modifier.border(
                        width = 2.dp,
                        color = statusColor ?: MaterialTheme.colorScheme.primary,
                        shape = RoundedCornerShape(dimensions.buttonRadius),
                    )
                } else {
                    Modifier
                },
            )
            .deckPressGesture(
                interactionSource = interactionSource,
                holdRepeatMs = button.holdRepeatMs.takeIf { it > 0 } ?: DEFAULT_HOLD_REPEAT_MS,
                hasOnHold = button.onHold != null,
                haptics = haptics,
                onPress = onPress,
                onRelease = onRelease,
            )
            .padding(dimensions.spacing),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
            modifier = Modifier.fillMaxSize(),
        ) {
            ButtonIcon(icon = button.icon, label = button.label, tint = foreground)

            if (button.label.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(
                    text = button.label,
                    color = foreground,
                    style = MaterialTheme.typography.labelLarge,
                    textAlign = TextAlign.Center,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }

            StateDecoration(
                button = button,
                override = override,
                telemetryValue = telemetryValue,
                tint = foreground,
            )
        }
    }
}

/**
 * The press/release state machine for one tile.
 *
 * A raw `pointerInput` block rather than `combinedClickable`, because
 * `combinedClickable` cannot express "send `repeat` every N ms while held" and
 * cannot report how long the press lasted, which `button.release` needs as
 * `held_ms` (PROTOCOL.md §6.2).
 */
private fun Modifier.deckPressGesture(
    interactionSource: MutableInteractionSource,
    holdRepeatMs: Int,
    hasOnHold: Boolean,
    haptics: Haptics,
    onPress: (String) -> Unit,
    onRelease: (heldMs: Long) -> Unit,
): Modifier = this.pointerInput(holdRepeatMs, hasOnHold) {
    awaitEachGesture {
        val down = awaitFirstDown(requireUnconsumed = false)
        val press = PressInteraction.Press(down.position)
        interactionSource.tryEmit(press)

        val startedAt = System.currentTimeMillis()
        haptics.press()

        // Wait for either the long-press threshold or the finger lifting.
        // `withTimeoutOrNull` returns null exactly when the threshold elapsed.
        val releasedEarly = withTimeoutOrNull(LONG_PRESS_MS) { awaitRelease() }

        if (releasedEarly != null) {
            interactionSource.tryEmit(PressInteraction.Release(press))
            onPress(PressKind.SHORT)
            onRelease(System.currentTimeMillis() - startedAt)
            return@awaitEachGesture
        }

        // The threshold passed: this gesture is a long press, reported once.
        haptics.toggle()
        onPress(PressKind.LONG)

        if (hasOnHold) {
            while (true) {
                val lifted = withTimeoutOrNull(holdRepeatMs.toLong()) { awaitRelease() }
                if (lifted != null) break
                onPress(PressKind.REPEAT)
            }
        } else {
            awaitRelease()
        }

        interactionSource.tryEmit(PressInteraction.Release(press))
        onRelease(System.currentTimeMillis() - startedAt)
    }
}

/**
 * Waits until every pointer is up.
 *
 * `awaitEachGesture` resets the event scope between gestures, so this is
 * always called from the start of one and terminates when that gesture ends.
 */
private suspend fun AwaitPointerEventScope.awaitRelease() {
    var event = awaitPointerEvent()
    while (event.changes.any { it.pressed }) {
        event = awaitPointerEvent()
    }
}

/**
 * The icon slot of a tile.
 *
 * See [materialIcon] for how each `icon.type` resolves and why `lucide` and
 * `image` degrade to a glyph.
 */
@Composable
private fun ButtonIcon(icon: ProfileIcon?, label: String, tint: Color) {
    val type = icon?.type?.lowercase() ?: ProfileIcon.TEXT
    val value = icon?.value.orEmpty()
    val iconColor = parseColor(icon?.color) ?: tint

    when (type) {
        ProfileIcon.MATERIAL -> {
            val vector = materialIcon(value)
            if (vector != null) {
                Icon(
                    imageVector = vector,
                    contentDescription = null,
                    tint = iconColor,
                    modifier = Modifier.size(28.dp),
                )
            } else {
                // An unrecognised Material name is a profile typo, not a crash.
                Glyph(fallbackGlyph(label), iconColor)
            }
        }

        // Lucide has no Android artefact and v1 has no icon-fetch message, so
        // both degrade to the label glyph. Documented in ui/Icons.kt.
        ProfileIcon.LUCIDE, ProfileIcon.IMAGE -> Glyph(fallbackGlyph(label), iconColor)

        ProfileIcon.EMOJI, ProfileIcon.TEXT -> Glyph(value.ifBlank { fallbackGlyph(label) }, iconColor)

        else -> Glyph(fallbackGlyph(label), iconColor)
    }
}

/** Draws an emoji or a single character at icon size. */
@Composable
private fun Glyph(text: String, color: Color) {
    Text(
        text = text,
        color = color,
        fontSize = 26.sp,
        maxLines = 1,
        overflow = TextOverflow.Clip,
        textAlign = TextAlign.Center,
    )
}

/**
 * The state-specific decoration under the label (PROTOCOL.md §5.2).
 *
 * Each kind shows the one thing the host can tell it about that kind: `status`
 * the host's label and colour, `progress` a bar, `counter` the host's number,
 * `telemetry` the bound metric, `timer` the elapsed hold. Kinds with nothing to
 * show render the sublabel rather than a placeholder.
 */
@Composable
private fun StateDecoration(
    button: Button,
    override: ButtonStateValue?,
    telemetryValue: Double?,
    tint: Color,
) {
    when (button.state.type) {
        ButtonState.STATUS -> {
            val text = override?.label ?: button.sublabel
            if (text != null) {
                Text(
                    text = text,
                    color = parseColor(override?.color) ?: tint.copy(alpha = 0.8f),
                    style = MaterialTheme.typography.labelSmall,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }

        ButtonState.PROGRESS -> {
            val progress = (override?.progress ?: override?.value)?.coerceIn(0.0, 1.0)
            if (progress != null) {
                Spacer(Modifier.height(6.dp))
                LinearProgressIndicator(
                    progress = { progress.toFloat() },
                    modifier = Modifier
                        .fillMaxWidth()
                        .height(4.dp)
                        .clip(RoundedCornerShape(2.dp)),
                    color = parseColor(override?.color) ?: MaterialTheme.colorScheme.primary,
                    trackColor = tint.copy(alpha = 0.2f),
                )
            } else {
                SubLabel(button.sublabel, tint)
            }
        }

        ButtonState.COUNTER -> {
            // The host pushes a counter as `value`. An absent value means "no
            // reading yet", which falls back to the sublabel rather than to a
            // fabricated zero.
            val counter = override?.value
            if (counter != null) {
                Text(
                    text = formatNumber(counter),
                    color = tint,
                    style = MaterialTheme.typography.labelMedium,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 1,
                )
            } else {
                SubLabel(button.sublabel, tint)
            }
        }

        ButtonState.TELEMETRY -> Text(
            text = if (telemetryValue != null) {
                formatMetric(button.state.format, telemetryValue)
            } else {
                // PROTOCOL.md §7: an unavailable metric renders as `--`, never
                // as zero, because zero is a plausible reading and would lie.
                button.state.default ?: "--"
            },
            color = tint,
            style = MaterialTheme.typography.labelMedium,
            fontWeight = FontWeight.SemiBold,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )

        ButtonState.TIMER -> {
            // The client counts up while pressed; a pushed host label (§5.2)
            // takes priority over the local count when present.
            val hostLabel = override?.label
            if (hostLabel != null) {
                Text(
                    text = hostLabel,
                    color = tint,
                    style = MaterialTheme.typography.labelMedium,
                    maxLines = 1,
                )
            } else {
                ElapsedTimer(tint)
            }
        }

        else -> SubLabel(button.sublabel, tint)
    }
}

/**
 * Counts up in whole seconds from when the tile was composed.
 *
 * Resetting on release would need the press state here, which the tile owns;
 * the counter therefore restarts whenever the tile leaves and re-enters the
 * composition, which is the behaviour a page change produces anyway.
 */
@Composable
private fun ElapsedTimer(tint: Color) {
    var elapsed by remember { mutableLongStateOf(0L) }
    LaunchedEffect(Unit) {
        while (true) {
            delay(1000)
            elapsed += 1000
        }
    }
    Text(
        text = "${elapsed / 1000}s",
        color = tint,
        style = MaterialTheme.typography.labelMedium,
    )
}

@Composable
private fun SubLabel(text: String?, tint: Color) {
    if (text.isNullOrBlank()) return
    Text(
        text = text,
        color = tint.copy(alpha = 0.75f),
        style = MaterialTheme.typography.labelSmall,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
    )
}

/**
 * Applies a profile's telemetry format string, e.g. `{value:.0f}%`.
 *
 * The spec is Python-style because that is what the host's own renderer uses
 * (PROTOCOL.md §5.2). Only the pieces a deck label needs are implemented: a
 * precision digit and surrounding literals. An unrecognised format falls back
 * to the plain number, so a typo shows a value rather than an error.
 *
 * This is parsed by hand rather than with a regular expression on purpose. The
 * previous version built `\{value(?::\.(\d+)f)?}` and threw
 * PatternSyntaxException on Android: the ICU regex engine rejects an unescaped
 * `}`, where java.util.regex tolerates it as a literal. Because the format comes
 * from a profile, that turned any telemetry button — including the one in the
 * shipped example profile — into a crash the moment the grid was drawn. Manual
 * scanning cannot fail that way, and there is nothing to get wrong.
 */
internal fun formatMetric(format: String?, value: Double): String {
    if (format.isNullOrBlank()) return formatNumber(value)

    val start = format.indexOf("{value")
    if (start < 0) return formatNumber(value)
    val end = format.indexOf('}', start)
    if (end < 0) return formatNumber(value)

    // The spec between "{value" and "}", e.g. "" or ":.0f".
    val spec = format.substring(start + "{value".length, end)

    // A precision is written as ".<digits>f"; anything else means "no precision".
    val decimals = run {
        val dot = spec.indexOf('.')
        if (dot < 0) return@run null
        val f = spec.indexOf('f', dot + 1)
        if (f <= dot + 1) return@run null
        spec.substring(dot + 1, f).toIntOrNull()?.coerceIn(0, 6)
    }

    val number = if (decimals != null) {
        String.format(java.util.Locale.US, "%.${decimals}f", value)
    } else {
        formatNumber(value)
    }
    return format.replaceRange(start, end + 1, number)
}

/** Trims a double to something a small tile can show. */
internal fun formatNumber(value: Double): String =
    if (value == value.toLong().toDouble()) value.toLong().toString() else "%.1f".format(value)
