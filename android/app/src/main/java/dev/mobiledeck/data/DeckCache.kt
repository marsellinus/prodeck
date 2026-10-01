package dev.mobiledeck.data

import android.content.Context
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import java.io.File
import java.io.IOException

/**
 * The last profile document received from a host, as it arrived.
 *
 * The revision is stored with it so a reconnect can ask the host whether
 * anything changed instead of re-fetching a document it already has.
 */
@Serializable
@JsonIgnoreProperties
data class CachedProfile(
    val hostId: String,
    val profileId: String,
    val revision: String = "",
    /** The document, verbatim. See `ProfileGetResultPayload.profile`. */
    val document: JsonElement,
    val cachedAt: Long = 0L,
)

/**
 * Profile JSON on disk, so the grid renders with no network.
 *
 * docs/ARCHITECTURE.md §4: "the last profile is cached, so a dead network shows
 * a grey Reconnecting… banner over a usable, non-interactive grid instead of a
 * blank screen." The cache is therefore written *only* on a successful
 * `profile.get` and read *only* when a live fetch is impossible — it is never
 * used to make a stale decision while the host is reachable.
 *
 * Files are per host id under `filesDir/profiles/`, which is app-private
 * storage: no permission, no other app can read it.
 */
class DeckCache(context: Context) {

    private val dir = File(context.applicationContext.filesDir, "profiles")

    /** Stores the document for [hostId], replacing any previous revision. */
    fun save(hostId: String, profileId: String, revision: String, document: JsonElement) {
        if (hostId.isBlank() || profileId.isBlank()) return
        val entry = CachedProfile(
            hostId = hostId,
            profileId = profileId,
            revision = revision,
            document = document,
            cachedAt = System.currentTimeMillis(),
        )
        val payload = ProtocolJson.encodeToString(CachedProfile.serializer(), entry)
        val target = fileFor(hostId)
        try {
            if (!dir.exists() && !dir.mkdirs()) return
            // Write-then-rename: a process killed mid-write must not leave a
            // truncated document that then fails to parse on the next launch.
            val tmp = File(dir, "${target.name}.tmp")
            tmp.writeText(payload, Charsets.UTF_8)
            if (!tmp.renameTo(target)) {
                target.writeText(payload, Charsets.UTF_8)
                tmp.delete()
            }
        } catch (_: IOException) {
            // A cache that cannot be written is not a reason to fail a
            // successful fetch; the grid is live at this moment anyway.
        }
    }

    /** The cached document for [hostId], or null. */
    fun load(hostId: String): CachedProfile? {
        val file = fileFor(hostId)
        if (!file.isFile) return null
        return try {
            ProtocolJson.decodeFromString(CachedProfile.serializer(), file.readText(Charsets.UTF_8))
        } catch (_: Exception) {
            // A corrupt cache is deleted rather than retried on every launch.
            file.delete()
            null
        }
    }

    /** Drops the cache for one host. Used when the host is unpaired. */
    fun clear(hostId: String) {
        fileFor(hostId).delete()
    }

    /** Drops every cached host. */
    fun clearAll() {
        dir.listFiles()?.forEach { it.delete() }
    }

    /**
     * The cache file for a host.
     *
     * The host id comes from the network, so it is hashed rather than used as a
     * filename: `../../foo` must not be able to escape `filesDir`, and a
     * filename cannot contain a path separator.
     */
    private fun fileFor(hostId: String): File {
        val safe = sha256Hex(hostId).take(32)
        return File(dir, "$safe.json")
    }
}

private fun sha256Hex(value: String): String =
    java.security.MessageDigest.getInstance("SHA-256")
        .digest(value.toByteArray(Charsets.UTF_8))
        .joinToString("") { "%02x".format(it) }
