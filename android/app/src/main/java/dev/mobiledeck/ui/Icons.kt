package dev.mobiledeck.ui

import android.graphics.BitmapFactory
import android.util.Base64
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.ExitToApp
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.automirrored.filled.VolumeOff
import androidx.compose.material.icons.automirrored.filled.VolumeUp
import androidx.compose.material.icons.filled.AccessAlarm
import androidx.compose.material.icons.filled.Bolt
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.Calculate
import androidx.compose.material.icons.filled.CalendarMonth
import androidx.compose.material.icons.filled.CameraAlt
import androidx.compose.material.icons.filled.Cancel
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.Code
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.DarkMode
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Email
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.Favorite
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Gamepad
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Keyboard
import androidx.compose.material.icons.filled.LightMode
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.Map
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material.icons.filled.Mic
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.MusicNote
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Photo
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.PowerSettingsNew
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.ShoppingCart
import androidx.compose.material.icons.filled.SkipNext
import androidx.compose.material.icons.filled.SkipPrevious
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material.icons.filled.Wifi
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector

/**
 * Resolves a profile's `icon` object to something drawable.
 *
 * The host's schema allows five icon kinds (PROTOCOL.md §5): `emoji`,
 * `material`, `lucide`, `image` and `text`. They are handled as follows:
 *
 *  * `emoji` — drawn as text, which is what the platform emoji font is for.
 *    Nothing here needs a bitmap.
 *  * `material` — looked up in [MATERIAL_ICONS]. The set is closed on purpose:
 *    a profile may name any of the ~2000 Material icons, but only the ones a
 *    deck button plausibly uses are compiled in, because the alternative is a
 *    reflection lookup that R8 cannot see and that would ship every icon.
 *  * `lucide` — Lucide is a web icon set with no Android artefact, so the name
 *    is rendered as text. This is a deliberate limitation, documented rather
 *    than hidden: the fallback below makes it legible.
 *  * `image` — a file in the profile's `icons/` directory on the *host*. The
 *    protocol has no icon-fetch message in v1, so the host inlines the file it
 *    references as a `data:image/png;base64,` URI in the profile document's
 *    top-level `icons` map. [decodeDataUri] turns that URI into a bitmap. A
 *    map that is absent, empty, or missing this file — which is normal, the
 *    host only inlines what a button references — falls back like everything
 *    else.
 *  * anything unknown, or a name not in the map — falls back to the first
 *    character of the label, so a tile is never blank. A blank tile is
 *    indistinguishable from a broken one, and the user cannot tell which
 *    button is which.
 */

/** The built-in Material icon map, keyed by the lowercase name a profile uses. */
val MATERIAL_ICONS: Map<String, ImageVector> = buildMap {
    put("arrow_back", Icons.AutoMirrored.Filled.ArrowBack)
    put("back", Icons.AutoMirrored.Filled.ArrowBack)
    put("bolt", Icons.Filled.Bolt)
    put("flash", Icons.Filled.Bolt)
    put("build", Icons.Filled.Build)
    put("tools", Icons.Filled.Build)
    put("calculate", Icons.Filled.Calculate)
    put("calendar", Icons.Filled.CalendarMonth)
    put("camera", Icons.Filled.CameraAlt)
    put("cancel", Icons.Filled.Cancel)
    put("check", Icons.Filled.CheckCircle)
    put("check_circle", Icons.Filled.CheckCircle)
    put("close", Icons.Filled.Close)
    put("cloud", Icons.Filled.Cloud)
    put("code", Icons.Filled.Code)
    put("copy", Icons.Filled.ContentCopy)
    put("dark_mode", Icons.Filled.DarkMode)
    put("delete", Icons.Filled.Delete)
    put("description", Icons.Filled.Description)
    put("document", Icons.Filled.Description)
    put("download", Icons.Filled.Download)
    put("edit", Icons.Filled.Edit)
    put("email", Icons.Filled.Email)
    put("mail", Icons.Filled.Email)
    put("error", Icons.Filled.Error)
    put("exit", Icons.AutoMirrored.Filled.ExitToApp)
    put("logout", Icons.AutoMirrored.Filled.ExitToApp)
    put("favorite", Icons.Filled.Favorite)
    put("heart", Icons.Filled.Favorite)
    put("folder", Icons.Filled.Folder)
    put("gamepad", Icons.Filled.Gamepad)
    put("games", Icons.Filled.Gamepad)
    put("grid", Icons.Filled.GridView)
    put("home", Icons.Filled.Home)
    put("info", Icons.Filled.Info)
    put("keyboard", Icons.Filled.Keyboard)
    put("light_mode", Icons.Filled.LightMode)
    put("link", Icons.Filled.Link)
    put("lock", Icons.Filled.Lock)
    put("map", Icons.Filled.Map)
    put("menu", Icons.Filled.Menu)
    put("mic", Icons.Filled.Mic)
    put("more", Icons.Filled.MoreVert)
    put("more_vert", Icons.Filled.MoreVert)
    put("music", Icons.Filled.MusicNote)
    put("notifications", Icons.Filled.Notifications)
    put("pause", Icons.Filled.Pause)
    put("person", Icons.Filled.Person)
    put("photo", Icons.Filled.Photo)
    put("image", Icons.Filled.Photo)
    put("play", Icons.Filled.PlayArrow)
    put("play_arrow", Icons.Filled.PlayArrow)
    put("power", Icons.Filled.PowerSettingsNew)
    put("refresh", Icons.Filled.Refresh)
    put("reload", Icons.Filled.Refresh)
    put("search", Icons.Filled.Search)
    put("send", Icons.AutoMirrored.Filled.Send)
    put("settings", Icons.Filled.Settings)
    put("share", Icons.Filled.Share)
    put("shopping_cart", Icons.Filled.ShoppingCart)
    put("cart", Icons.Filled.ShoppingCart)
    put("skip_next", Icons.Filled.SkipNext)
    put("next", Icons.Filled.SkipNext)
    put("skip_previous", Icons.Filled.SkipPrevious)
    put("previous", Icons.Filled.SkipPrevious)
    put("star", Icons.Filled.Star)
    put("stop", Icons.Filled.Stop)
    put("terminal", Icons.Filled.Terminal)
    put("console", Icons.Filled.Terminal)
    put("timer", Icons.Filled.AccessAlarm)
    put("alarm", Icons.Filled.AccessAlarm)
    put("upload", Icons.Filled.Upload)
    put("visibility", Icons.Filled.Visibility)
    put("eye", Icons.Filled.Visibility)
    put("volume_off", Icons.AutoMirrored.Filled.VolumeOff)
    put("mute", Icons.AutoMirrored.Filled.VolumeOff)
    put("volume_up", Icons.AutoMirrored.Filled.VolumeUp)
    put("volume", Icons.AutoMirrored.Filled.VolumeUp)
    put("warning", Icons.Filled.Warning)
    put("wifi", Icons.Filled.Wifi)
    put("network", Icons.Filled.Wifi)
}

/**
 * Looks up a Material icon name.
 *
 * Accepts the several spellings a profile author might use for one icon —
 * `skip_next`, `skipNext`, `SkipNext`, `skip next` — because the name is
 * hand-typed in JSON and there is no schema to validate it against.
 */
fun materialIcon(name: String): ImageVector? {
    val key = name.trim()
        .lowercase()
        .replace(" ", "_")
        .replace("-", "_")
        .replace(Regex("([a-z0-9])([A-Z])"), "$1_$2")
    return MATERIAL_ICONS[key]
}

/**
 * The fallback glyph for a tile: the label's first character.
 *
 * Returns a question mark for an empty label rather than an empty string, so
 * the tile still reads as "something is here" instead of as a rendering bug.
 */
fun fallbackGlyph(label: String): String =
    label.trim().firstOrNull()?.uppercase() ?: "?"

/**
 * Extracts the base64 payload of a `data:image/png;base64,` URI, or null when
 * the string is not exactly that.
 *
 * Split out from [decodeDataUri] so the shape check is testable on the JVM:
 * `android.util.Base64` and `BitmapFactory` are framework classes and are not
 * on the classpath of a plain unit test. The check is strict on purpose — a
 * URI that is not a base64 PNG must fail here rather than be fed to the
 * decoder, because the placeholder is the only sane rendering for a value the
 * host should never have sent.
 */
internal fun pngBase64Payload(uri: String?): String? {
    if (uri == null) return null
    val prefix = "data:image/png;base64,"
    if (!uri.startsWith(prefix)) return null
    val payload = uri.substring(prefix.length)
    return payload.ifBlank { null }
}

/**
 * The decoded icons, keyed by URI. Bounded because a profile may reference many
 * icons and an unbounded map would hold every bitmap the user ever saw for the
 * life of the process.
 *
 * A null value is cached too: a URI that failed to decode once will fail every
 * time, and without the negative entry a bad icon would re-run Base64 and
 * BitmapFactory on every recomposition of the grid.
 */
private val iconCache = object : LinkedHashMap<String, ImageBitmap?>(CACHE_MAX, 0.75f, true) {
    override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, ImageBitmap?>?) =
        size > CACHE_MAX
}

private const val CACHE_MAX = 64

/**
 * Decodes a `data:image/png;base64,` URI into a Bitmap, cached by URI.
 * Returns null on anything malformed: an icon that cannot be decoded must
 * degrade to the existing placeholder, never crash the grid.
 *
 * `Base64.DEFAULT` rather than `NO_WRAP` because a JSON encoder may have
 * introduced newlines into the payload, and `DEFAULT` is the only flag that
 * both tolerates and skips them.
 */
fun decodeDataUri(uri: String?): ImageBitmap? {
    // `key` is non-null for the cache; a null URI is a miss by definition.
    val key = uri ?: return null
    val payload = pngBase64Payload(key) ?: return null

    synchronized(iconCache) {
        // `containsKey` distinguishes "cached miss" from "not cached": a plain
        // `get` would return null for both and never cache the negative.
        if (iconCache.containsKey(key)) return iconCache[key]
    }

    val decoded = runCatching {
        val bytes = Base64.decode(payload, Base64.DEFAULT)
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size)?.asImageBitmap()
    }.getOrNull()

    synchronized(iconCache) {
        iconCache[key] = decoded
    }
    return decoded
}

/**
 * Decodes [uri] once per value and keeps the result across recompositions.
 *
 * The decode is memoised rather than performed inline because `DeckButton`
 * recomposes on every press, state push and telemetry tick, and decoding a
 * PNG on each of those would be wasted work on the frame path.
 */
@Composable
fun rememberIconBitmap(uri: String?): ImageBitmap? = remember(uri) { decodeDataUri(uri) }
