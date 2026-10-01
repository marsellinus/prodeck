package dev.mobiledeck

import dev.mobiledeck.ui.formatMetric
import dev.mobiledeck.ui.formatNumber
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The telemetry format renderer.
 *
 * It exists because this code once crashed the whole app: the implementation
 * built a `Regex` from the profile's format string, and Android's ICU regex
 * engine rejects an unescaped `}` where java.util.regex tolerates it. The
 * shipped example profile uses `{value:.0f}%` on three buttons, so pairing
 * succeeded and then the grid crashed on its first draw.
 *
 * These cases pin the formats a profile can legitimately contain, and the
 * fallback that keeps a malformed one from being fatal.
 */
class MetricFormatTest {

    @Test
    fun `the example profile's format renders with no decimals`() {
        // This is the exact string in profiles/development/profile.json.
        assertEquals("43%", formatMetric("{value:.0f}%", 43.2))
        assertEquals("71%", formatMetric("{value:.0f}%", 71.0))
    }

    @Test
    fun `a precision is honoured`() {
        assertEquals("43.25", formatMetric("{value:.2f}", 43.25))
        assertEquals("43.3", formatMetric("{value:.1f}", 43.25))
        // %.0f rounds, it does not truncate.
        assertEquals("44", formatMetric("{value:.0f}", 43.9))
        assertEquals("43", formatMetric("{value:.0f}", 43.4))
    }

    @Test
    fun `a bare placeholder uses the default rendering`() {
        assertEquals("43.2", formatMetric("{value}", 43.2))
        assertEquals("43", formatMetric("{value}", 43.0))
    }

    @Test
    fun `surrounding literals are preserved`() {
        assertEquals("CPU 43%", formatMetric("CPU {value:.0f}%", 43.2))
        assertEquals("43 Mbps down", formatMetric("{value:.0f} Mbps down", 43.2))
        assertEquals("[43]", formatMetric("[{value:.0f}]", 43.2))
    }

    @Test
    fun `a missing or unusable format falls back to the plain number`() {
        assertEquals("43.2", formatMetric(null, 43.2))
        assertEquals("43.2", formatMetric("", 43.2))
        assertEquals("43.2", formatMetric("   ", 43.2))
        // No placeholder at all: the literal is not a format, so the value wins.
        assertEquals("43.2", formatMetric("no placeholder here", 43.2))
        // An unclosed brace must not be fatal.
        assertEquals("43.2", formatMetric("{value:.0f", 43.2))
    }

    @Test
    fun `an absurd precision is clamped rather than crashing`() {
        // A profile asking for 999 decimals must not allocate a huge string.
        val rendered = formatMetric("{value:.999f}", 1.0)
        assertEquals("1.000000", rendered)
    }

    @Test
    fun `a non-numeric precision falls back`() {
        assertEquals("43.2", formatMetric("{value:.xf}", 43.2))
    }

    @Test
    fun `formatNumber trims a whole number`() {
        assertEquals("43", formatNumber(43.0))
        assertEquals("43.2", formatNumber(43.2))
        assertEquals("0", formatNumber(0.0))
    }
}
