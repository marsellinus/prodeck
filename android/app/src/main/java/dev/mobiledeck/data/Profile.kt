package dev.mobiledeck.data

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement

/**
 * The client-side view of a profile document (PROTOCOL.md §5).
 *
 * These classes exist only so the UI can navigate a document it received as
 * opaque JSON: the grid size, the pages, the buttons and their cells. Every
 * field has a default and every class tolerates unknown keys, because a host
 * built from a newer revision may add fields, and a profile that renders is
 * worth more than a profile that parses strictly.
 *
 * Anything the client does not model — the `params` of an action, future
 * fields — is deliberately *not* parsed. The host validates and executes
 * actions; the client only needs to know whether a button has one at all.
 */
@Serializable
@JsonIgnoreProperties
data class Profile(
    val schema: Int = 1,
    val id: String = "",
    val name: String = "",
    val icon: Icon? = null,
    val theme: Theme = Theme(),
    val settings: Settings = Settings(),
    @SerialName("root_page") val rootPage: String = "",
    val pages: List<Page> = emptyList(),
    /**
     * Inlined `image` icons, keyed by the file name a button's
     * `icon.value` names (e.g. `terminal.png`). The value is a
     * `data:image/png;base64,` URI.
     *
     * Protocol v1 has no icon-fetch message, so the only way the client can
     * ever show a host-side PNG is for the host to inline it here. The map is
     * optional and may hold only the icons the served pages actually
     * reference: a missing entry is normal, not an error, and an older host
     * that predates the field sends no map at all.
     */
    val icons: Map<String, String> = emptyMap(),
) {
    /** The page with this id, or null. */
    fun page(id: String): Page? = pages.firstOrNull { it.id == id }

    /** The data URI for an `image` icon file name, or null when not inlined. */
    fun iconDataUri(fileName: String): String? = icons[fileName]

    /** The root page, falling back to the first page for a malformed document. */
    fun rootPageOrFirst(): Page? = page(rootPage) ?: pages.firstOrNull()

    /** Effective grid for a page: the page's own, else the profile's. */
    fun gridFor(page: Page?): Grid = page?.grid ?: settings.grid
}

@Serializable
@JsonIgnoreProperties
data class Page(
    val id: String = "",
    val name: String = "",
    val icon: Icon? = null,
    val grid: Grid? = null,
    val buttons: List<Button> = emptyList(),
    val parent: String? = null,
)

@Serializable
@JsonIgnoreProperties
data class Grid(val columns: Int = 4, val rows: Int = 5) {
    /** Clamped so a malformed document cannot ask for an unbounded layout. */
    fun sanitized(): Grid = Grid(columns.coerceIn(1, MAX_COLUMNS), rows.coerceIn(1, MAX_ROWS))

    companion object {
        const val MAX_COLUMNS = 12
        const val MAX_ROWS = 16
    }
}

@Serializable
@JsonIgnoreProperties
data class Cell(
    val row: Int = 0,
    val column: Int = 0,
    @SerialName("row_span") val rowSpan: Int = 1,
    @SerialName("column_span") val columnSpan: Int = 1,
)

@Serializable
@JsonIgnoreProperties
data class Icon(
    /** emoji | material | lucide | image | text */
    val type: String = "text",
    val value: String = "",
    val color: String? = null,
) {
    companion object {
        const val EMOJI = "emoji"
        const val MATERIAL = "material"
        const val LUCIDE = "lucide"
        const val IMAGE = "image"
        const val TEXT = "text"
    }
}

/**
 * How a tile behaves over time (PROTOCOL.md §5.2).
 *
 * [metric] and [format] only carry meaning for `telemetry`; [group] only for
 * `radio`. They are kept in one class because the host does, and splitting them
 * would mean the client has to re-derive which field belongs to which kind.
 */
@Serializable
@JsonIgnoreProperties
data class ButtonState(
    val type: String = "momentary",
    val group: String? = null,
    val metric: String? = null,
    val format: String? = null,
    val default: String? = null,
    val min: Double = 0.0,
    val max: Double = 1.0,
    val color: String? = null,
) {
    companion object {
        const val MOMENTARY = "momentary"
        const val TOGGLE = "toggle"
        const val RADIO = "radio"
        const val STATUS = "status"
        const val PROGRESS = "progress"
        const val COUNTER = "counter"
        const val TIMER = "timer"
        const val TELEMETRY = "telemetry"
    }
}

@Serializable
@JsonIgnoreProperties
data class Button(
    val id: String = "",
    val label: String = "",
    val sublabel: String? = null,
    val icon: Icon? = null,
    val background: String? = null,
    val foreground: String? = null,
    val cell: Cell = Cell(),
    val state: ButtonState = ButtonState(),
    @SerialName("on_press") val onPress: Action? = null,
    @SerialName("on_long_press") val onLongPress: Action? = null,
    @SerialName("on_release") val onRelease: Action? = null,
    @SerialName("on_hold") val onHold: Action? = null,
    @SerialName("hold_repeat_ms") val holdRepeatMs: Int = 0,
    val permissions: List<String> = emptyList(),
    val hidden: Boolean = false,
) {
    /**
     * PROTOCOL.md §6.2: `button.release` is sent only for these state kinds and
     * only when the button declares an `on_release` action.
     */
    val wantsRelease: Boolean
        get() = onRelease != null &&
            (state.type == ButtonState.MOMENTARY ||
                state.type == ButtonState.TIMER ||
                state.type == ButtonState.TOGGLE)

    /** Whether this button's press should offer a confirmation sheet (§9). */
    val needsConfirmation: Boolean get() = onPress?.requireConfirmation == true
}

/**
 * One executable step.
 *
 * `params` is validated by the action implementation on the host, never here
 * (PROTOCOL.md §5.1), so it stays opaque.
 */
@Serializable
@JsonIgnoreProperties
data class Action(
    val type: String = "",
    val params: JsonElement? = null,
    @SerialName("on_error") val onError: String = "abort",
    val label: String? = null,
    @SerialName("require_confirmation") val requireConfirmation: Boolean = false,
)

@Serializable
@JsonIgnoreProperties
data class Theme(
    val background: String? = null,
    val accent: String? = null,
    @SerialName("button_background") val buttonBackground: String? = null,
    @SerialName("button_foreground") val buttonForeground: String? = null,
    @SerialName("button_radius") val buttonRadius: Int = 14,
    @SerialName("font_scale") val fontScale: Float = 1f,
    val spacing: Int = 8,
    val animation: String? = null,
    /** dark | light | oled | custom */
    val mode: String? = null,
) {
    companion object {
        const val DARK = "dark"
        const val LIGHT = "light"
        const val OLED = "oled"
        const val CUSTOM = "custom"
    }
}

@Serializable
@JsonIgnoreProperties
data class Settings(
    val grid: Grid = Grid(),
    val haptic: Boolean = true,
    val nav: Nav = Nav(),
    @SerialName("auto_return") val autoReturn: AutoReturn? = null,
)

@Serializable
@JsonIgnoreProperties
data class Nav(
    val breadcrumb: Boolean = true,
    @SerialName("back_button") val backButton: Boolean = true,
    @SerialName("page_tabs") val pageTabs: Boolean = true,
)

@Serializable
@JsonIgnoreProperties
data class AutoReturn(
    val enabled: Boolean = false,
    @SerialName("delay_ms") val delayMs: Int = 0,
)

/**
 * A button's state as pushed by the host in `event.button.state`.
 *
 * Mirrors the host's `StateVal`. Every field is optional because the host sends
 * only what changed for the state kind in question (PROTOCOL.md §6.4), and the
 * UI merges them onto what it already had.
 */
@Serializable
@JsonIgnoreProperties
data class ButtonStateValue(
    val type: String? = null,
    val label: String? = null,
    val color: String? = null,
    val progress: Double? = null,
    val value: Double? = null,
    val active: Boolean? = null,
)
