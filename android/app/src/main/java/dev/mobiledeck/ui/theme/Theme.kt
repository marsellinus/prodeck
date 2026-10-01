package dev.mobiledeck.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.graphics.toColorInt
import dev.mobiledeck.data.Theme

/**
 * The values a profile's `theme` object contributes that Material3 has no slot
 * for: the tile radius, the grid gutter and the label scale.
 *
 * These are carried in a [staticCompositionLocalOf] rather than threaded
 * through every composable because they are read by the tile, the grid and the
 * chrome alike, and because a static local is the cheapest read for values that
 * change only when a profile is reloaded.
 */
data class DeckDimensions(
    val buttonRadius: Dp,
    val spacing: Dp,
    val fontScale: Float,
) {
    companion object {
        val Default = DeckDimensions(buttonRadius = 14.dp, spacing = 8.dp, fontScale = 1f)
    }
}

val LocalDeckDimensions = staticCompositionLocalOf { DeckDimensions.Default }

/** The resolved palette, exposed for the few places Material3 has no token for. */
val LocalDeckAccent = staticCompositionLocalOf { Color(0xFF4C8BF5) }

/**
 * Builds a Material3 scheme from a profile's `theme` object.
 *
 * Every field falls back to the [Theme] defaults when the profile omits it,
 * which is what `ApplyDefaults` on the host also guarantees — so a profile that
 * says nothing about colour still renders as a coherent deck rather than as
 * unstyled Material.
 *
 * `mode` selects the scheme family (PROTOCOL.md §5):
 *
 *  * `dark`   — the default, and the one the host's own defaults target;
 *  * `light`  — a bright scheme for a sunlit room;
 *  * `oled`   — pure black surfaces, so an AMOLED panel draws nothing for the
 *               background and the deck costs no power to display;
 *  * `custom` — the profile's colours are used verbatim, including `background`
 *               as the surface, so a designer can match a brand exactly;
 *  * absent   — follow the system setting, which is what a user expects when
 *               the profile author expressed no preference.
 */
@Composable
fun DeckTheme(
    theme: Theme?,
    content: @Composable () -> Unit,
) {
    val resolved = theme ?: Theme()
    val mode = resolved.mode?.lowercase()
        ?: if (isSystemInDarkTheme()) Theme.DARK else Theme.LIGHT

    val background = parseColor(resolved.background) ?: when (mode) {
        Theme.LIGHT -> Color(0xFFF5F5F7)
        Theme.OLED -> Color(0xFF000000)
        else -> Color(0xFF101014)
    }
    val accent = parseColor(resolved.accent) ?: Color(0xFF4C8BF5)
    val buttonBackground = parseColor(resolved.buttonBackground) ?: when (mode) {
        Theme.LIGHT -> Color(0xFFFFFFFF)
        Theme.OLED -> Color(0xFF0A0A0A)
        else -> Color(0xFF1C1C22)
    }
    val buttonForeground = parseColor(resolved.buttonForeground) ?: when (mode) {
        Theme.LIGHT -> Color(0xFF1A1A1F)
        else -> Color(0xFFF2F2F5)
    }

    val onBackground = if (isLight(background)) Color(0xFF1A1A1F) else Color(0xFFEDEDF2)
    val surfaceVariant = if (isLight(background)) Color(0xFFE6E6EC) else Color(0xFF23232B)
    val onSurfaceVariant = if (isLight(background)) Color(0xFF4A4A55) else Color(0xFFA9A9B6)
    val errorColor = if (isLight(background)) Color(0xFFB3261E) else Color(0xFFF2B8B5)

    val scheme: ColorScheme = if (isLight(background)) {
        lightColorScheme(
            primary = accent,
            onPrimary = readableOn(accent),
            primaryContainer = accent.copy(alpha = 0.18f),
            onPrimaryContainer = onBackground,
            secondary = accent,
            onSecondary = readableOn(accent),
            background = background,
            onBackground = onBackground,
            surface = background,
            onSurface = onBackground,
            surfaceVariant = surfaceVariant,
            onSurfaceVariant = onSurfaceVariant,
            surfaceContainer = buttonBackground,
            surfaceContainerHigh = surfaceVariant,
            outline = onSurfaceVariant.copy(alpha = 0.5f),
            error = errorColor,
            onError = Color.White,
            scrim = Color(0x99000000),
        )
    } else {
        darkColorScheme(
            primary = accent,
            onPrimary = readableOn(accent),
            primaryContainer = accent.copy(alpha = 0.24f),
            onPrimaryContainer = onBackground,
            secondary = accent,
            onSecondary = readableOn(accent),
            background = background,
            onBackground = onBackground,
            surface = background,
            onSurface = onBackground,
            surfaceVariant = surfaceVariant,
            onSurfaceVariant = onSurfaceVariant,
            surfaceContainer = buttonBackground,
            surfaceContainerHigh = surfaceVariant,
            outline = onSurfaceVariant.copy(alpha = 0.5f),
            error = errorColor,
            onError = Color(0xFF3B0907),
            scrim = Color(0xCC000000),
        )
    }

    // The profile's `font_scale` is a multiplier on every label in the deck, so
    // it is applied to the whole type scale rather than to individual tiles.
    val scale = resolved.fontScale.coerceIn(0.6f, 2.0f)
    val typography = if (scale == 1f) Typography() else scaledTypography(scale)

    CompositionLocalProvider(
        LocalDeckDimensions provides DeckDimensions(
            buttonRadius = resolved.buttonRadius.coerceIn(0, 48).dp,
            spacing = resolved.spacing.coerceIn(0, 32).dp,
            fontScale = scale,
        ),
        LocalDeckAccent provides accent,
    ) {
        MaterialTheme(
            colorScheme = scheme,
            typography = typography,
            content = content,
        )
    }
}

private fun scaledTypography(scale: Float): Typography {
    val base = Typography()
    fun scaled(size: androidx.compose.ui.unit.TextUnit) = (size.value * scale).sp
    return Typography(
        displayLarge = base.displayLarge.copy(fontSize = scaled(base.displayLarge.fontSize)),
        displayMedium = base.displayMedium.copy(fontSize = scaled(base.displayMedium.fontSize)),
        displaySmall = base.displaySmall.copy(fontSize = scaled(base.displaySmall.fontSize)),
        headlineLarge = base.headlineLarge.copy(fontSize = scaled(base.headlineLarge.fontSize)),
        headlineMedium = base.headlineMedium.copy(fontSize = scaled(base.headlineMedium.fontSize)),
        headlineSmall = base.headlineSmall.copy(fontSize = scaled(base.headlineSmall.fontSize)),
        titleLarge = base.titleLarge.copy(fontSize = scaled(base.titleLarge.fontSize)),
        titleMedium = base.titleMedium.copy(fontSize = scaled(base.titleMedium.fontSize)),
        titleSmall = base.titleSmall.copy(fontSize = scaled(base.titleSmall.fontSize)),
        bodyLarge = base.bodyLarge.copy(fontSize = scaled(base.bodyLarge.fontSize)),
        bodyMedium = base.bodyMedium.copy(fontSize = scaled(base.bodyMedium.fontSize)),
        bodySmall = base.bodySmall.copy(fontSize = scaled(base.bodySmall.fontSize)),
        labelLarge = base.labelLarge.copy(
            fontSize = scaled(base.labelLarge.fontSize),
            fontWeight = FontWeight.Medium,
        ),
        labelMedium = base.labelMedium.copy(fontSize = scaled(base.labelMedium.fontSize)),
        labelSmall = base.labelSmall.copy(fontSize = scaled(base.labelSmall.fontSize)),
    )
}

/**
 * Parses `#RGB`, `#RRGGBB` or `#AARRGGBB`.
 *
 * Returns null rather than throwing: a typo in a hand-edited profile must cost
 * one wrong colour, not a crash on the deck.
 */
internal fun parseColor(raw: String?): Color? {
    val value = raw?.trim()?.takeIf { it.isNotEmpty() } ?: return null
    val withHash = if (value.startsWith("#")) value else "#$value"
    return try {
        // The KTX `toColorInt` is the same parse with the exception contract
        // made explicit in its name.
        Color(withHash.toColorInt())
    } catch (_: IllegalArgumentException) {
        null
    }
}

/** Whether a colour reads as light, by its perceived luminance. */
internal fun isLight(color: Color): Boolean {
    // Rec. 709 coefficients: the eye weights green far above blue, so a plain
    // average would call mid-blue "light" and produce unreadable text on it.
    val luminance = 0.2126 * color.red + 0.7152 * color.green + 0.0722 * color.blue
    return luminance > 0.5
}

/**
 * Black or white text for a background, whichever is actually readable.
 *
 * The profile's `accent` is user-chosen and may be nearly white or nearly
 * black; picking the wrong on-colour makes a button's label invisible.
 */
internal fun readableOn(background: Color): Color =
    if (isLight(background)) Color(0xFF101014) else Color(0xFFFFFFFF)
