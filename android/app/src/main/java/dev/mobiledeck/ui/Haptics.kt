package dev.mobiledeck.ui

import android.content.Context
import android.os.Build
import android.os.VibrationEffect
import android.os.Vibrator
import android.os.VibratorManager

/**
 * Haptic feedback for the deck.
 *
 * ## Why not `View.performHapticFeedback`
 *
 * The Compose `LocalHapticFeedback` path routes through the view's
 * `HapticFeedbackConstants`, which the platform maps to whatever the OEM chose
 * and which silently does nothing when the system-level touch feedback toggle is
 * off. A deck is used without looking at the screen, so the feel of a press is
 * functional, not decorative, and it needs a waveform this code controls.
 *
 * ## Gating
 *
 * `settings.haptic` (PROTOCOL.md §5) is the profile author's decision, not the
 * user's: a profile that drives a quiet-room presentation can turn feedback off
 * for all its buttons. When it is false, [Haptics] performs no vibration at all
 * rather than relying on the system setting, so the behaviour is identical on
 * every device.
 *
 * The amplitude is deliberately low (the `DEFAULT_AMPLITUDE` sentinel, or a
 * small explicit value) because a deck press happens hundreds of times a
 * session; a strong motor is fatiguing and drains the battery.
 */
class Haptics(context: Context, private val enabled: Boolean) {

    private val vibrator: Vibrator? = resolveVibrator(context)

    /** Whether this device can vibrate at all. */
    val available: Boolean get() = vibrator?.hasVibrator() == true

    /** A short tap. Used on every press. */
    fun press() = buzz(PRESS_MS, AMPLITUDE_LIGHT)

    /** A firmer tap, for a toggle or radio changing state. */
    fun toggle() = buzz(TOGGLE_MS, AMPLITUDE_MEDIUM)

    /** A confirmation: two short pulses. */
    fun success() = buzzPattern(longArrayOf(0, 20, 60, 20), intArrayOf(0, AMPLITUDE_MEDIUM, 0, AMPLITUDE_MEDIUM))

    /** A refusal: a longer, heavier pulse. */
    fun error() = buzz(ERROR_MS, AMPLITUDE_HEAVY)

    private fun buzz(durationMs: Long, amplitude: Int) {
        if (!enabled) return
        val v = vibrator ?: return
        if (!v.hasVibrator()) return
        // `VibrationEffect` is API 26 and minSdk is 26, so there is no legacy
        // branch to keep: the deprecated one-shot `vibrate(long)` overload
        // cannot be reached on any device this app installs on.
        runCatching {
            v.vibrate(VibrationEffect.createOneShot(durationMs, amplitude))
        }
    }

    private fun buzzPattern(timings: LongArray, amplitudes: IntArray) {
        if (!enabled) return
        val v = vibrator ?: return
        if (!v.hasVibrator()) return
        runCatching {
            v.vibrate(VibrationEffect.createWaveform(timings, amplitudes, -1))
        }
    }

    companion object {
        private const val PRESS_MS = 12L
        private const val TOGGLE_MS = 18L
        private const val ERROR_MS = 45L

        /**
         * `VibrationEffect.DEFAULT_AMPLITUDE` means "let the platform pick".
         * The explicit values below are fractions of the motor's range.
         */
        private const val AMPLITUDE_LIGHT = 60
        private const val AMPLITUDE_MEDIUM = 120
        private const val AMPLITUDE_HEAVY = 220

        /**
         * The default vibrator.
         *
         * `VibratorManager` is API 31; on 26..30 the service is obtained the
         * old way, which is why this one branch remains.
         */
        private fun resolveVibrator(context: Context): Vibrator? =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                val manager = context.getSystemService(Context.VIBRATOR_MANAGER_SERVICE) as? VibratorManager
                manager?.defaultVibrator
            } else {
                @Suppress("DEPRECATION")
                context.getSystemService(Context.VIBRATOR_SERVICE) as? Vibrator
            }
    }
}
