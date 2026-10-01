package dev.mobiledeck.data

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

/**
 * The MobileDeck v1 wire protocol (docs/PROTOCOL.md).
 *
 * This file is pure data. It performs no I/O and holds no mutable state, so the
 * same shapes are usable from the JVM unit tests, the transport and the UI.
 *
 * Every payload class carries [JsonIgnoreProperties]. That is not decoration:
 * PROTOCOL.md §11 promises additive evolution, so a host built from a newer
 * revision may send fields this build has never heard of, and dropping the
 * frame over one of them would be a compatibility break.
 */

/** Protocol version this build speaks. */
const val PROTOCOL_VERSION = 1

// ---------------------------------------------------------------------------
// Message types (PROTOCOL.md §2.3, §5.3, §6, §7)
// ---------------------------------------------------------------------------

object MsgType {
    const val HELLO = "hello"
    const val WELCOME = "welcome"
    const val PING = "ping"
    const val PONG = "pong"
    const val ERROR = "error"

    const val PROFILE_LIST = "profile.list"
    const val PROFILE_LIST_RESULT = "profile.list.result"
    const val PROFILE_GET = "profile.get"
    const val PROFILE_GET_RESULT = "profile.get.result"
    const val PROFILE_SET_ACTIVE = "profile.set_active"
    const val PROFILE_RELOAD = "profile.reload"
    const val PROFILE_EXPORT = "profile.export"
    const val PROFILE_EXPORT_RESULT = "profile.export.result"
    const val PROFILE_IMPORT = "profile.import"

    const val BUTTON_PRESS = "button.press"
    const val BUTTON_RELEASE = "button.release"
    const val ACTION_RESULT = "action.result"
    const val ACTION_CANCEL = "action.cancel"

    const val EVENT_ACTION_FINISHED = "event.action.finished"
    const val EVENT_BUTTON_STATE = "event.button.state"
    const val EVENT_PROFILE_CHANGED = "event.profile.changed"
    const val EVENT_TELEMETRY = "event.telemetry"

    const val TELEMETRY_SUBSCRIBE = "telemetry.subscribe"
}

/** Error codes carried in an `error` payload (PROTOCOL.md §8). */
object ErrorCode {
    const val INVALID_ARGUMENT = "invalid_argument"
    const val UNAUTHENTICATED = "unauthenticated"
    const val FORBIDDEN = "forbidden"
    const val NOT_FOUND = "not_found"
    const val UNSUPPORTED = "unsupported"
    const val RATE_LIMITED = "rate_limited"
    const val CONFLICT = "conflict"
    const val CANCELLED = "cancelled"
    const val ACTION_FAILED = "action_failed"
    const val INVALID_PROFILE = "invalid_profile"
    const val INTERNAL = "internal"
}

/**
 * WebSocket close codes and what the client is required to do about them
 * (PROTOCOL.md §2.5). Keeping the reaction next to the code means the policy is
 * stated once and cannot drift between the transport and the store.
 */
enum class CloseCode(val code: Int) {
    NORMAL(1000),
    TRY_AGAIN_LATER(1013),
    MALFORMED(4400),
    UNAUTHENTICATED(4401),
    DEVICE_DISABLED(4403),
    IDLE_TIMEOUT(4408),
    RATE_LIMITED(4429),
    ;

    /** Whether the client should try to re-establish the socket at all. */
    val reconnect: Boolean
        get() = when (this) {
            NORMAL, MALFORMED, DEVICE_DISABLED -> false
            TRY_AGAIN_LATER, UNAUTHENTICATED, IDLE_TIMEOUT, RATE_LIMITED -> true
        }

    /** Whether the stored token must be discarded before reconnecting. */
    val wipesToken: Boolean get() = this == UNAUTHENTICATED

    /**
     * Whether the client should re-apply the normal backoff schedule rather
     * than the "immediately, once" rule for 4408.
     */
    val usesBackoff: Boolean
        get() = this == TRY_AGAIN_LATER || this == RATE_LIMITED || this == UNAUTHENTICATED

    /** Human-readable reason surfaced in the connection banner. */
    val explanation: String
        get() = when (this) {
            NORMAL -> "Host closed the session."
            TRY_AGAIN_LATER -> "Host is busy or shutting down."
            MALFORMED -> "The client sent a malformed handshake. This is a bug."
            UNAUTHENTICATED -> "Token rejected or revoked. Pair again."
            DEVICE_DISABLED -> "This device was revoked by the host."
            IDLE_TIMEOUT -> "Idle timeout; reconnecting."
            RATE_LIMITED -> "Rate limited by the host."
        }

    companion object {
        /** Maps a raw close code, or null for a code this build does not know. */
        fun of(code: Int): CloseCode? = entries.firstOrNull { it.code == code }
    }
}

// ---------------------------------------------------------------------------
// Envelope (PROTOCOL.md §1.3)
// ---------------------------------------------------------------------------

/**
 * The single frame shape for every message in both directions.
 *
 * `payload` is a [JsonElement] rather than a typed field because the envelope
 * is shared by every message type; decoding it eagerly into a `JsonObject`
 * keeps unknown payload keys intact, which matters for `profile.get.result`
 * where the document body is deliberately passed through opaquely.
 */
@Serializable
@JsonIgnoreProperties
data class Envelope(
    val v: Int = PROTOCOL_VERSION,
    val id: String? = null,
    @SerialName("reply_to") val replyTo: String? = null,
    val type: String,
    val ts: Long = 0L,
    val payload: JsonElement? = null,
)

/** The body of an `error` message (PROTOCOL.md §8). */
@Serializable
@JsonIgnoreProperties
data class ErrorPayload(
    val code: String,
    val message: String = "",
    val pointer: String? = null,
    @SerialName("retry_after_ms") val retryAfterMs: Int? = null,
)

// ---------------------------------------------------------------------------
// Handshake (PROTOCOL.md §2.2, §2.3)
// ---------------------------------------------------------------------------

/**
 * Screen geometry the host uses to warn about an oversized grid.
 *
 * None of these fields has a default. The codec drops fields equal to their
 * default (`encodeDefaults = false`, so an optional field that is absent stays
 * absent), and a handshake that omits its own screen geometry would leave the
 * host unable to tell the user that their profile will not fit.
 */
@Serializable
@JsonIgnoreProperties
data class ScreenInfo(
    val w: Int,
    val h: Int,
    val density: Float,
    val orientation: String,
)

/** Identifies the connecting application. */
@Serializable
@JsonIgnoreProperties
data class ClientInfo(
    /**
     * Always `android`. It has no default on purpose: the codec is configured
     * with `encodeDefaults = false`, so a defaulted field would be dropped from
     * the frame and the host would see a client that never said what it is.
     */
    val platform: String,
    @SerialName("app_version") val appVersion: String,
    val model: String = "",
    val screen: ScreenInfo? = null,
    /**
     * PROTOCOL.md §1.1: reserved for a future MessagePack switch. A v1 client
     * MUST NOT set it, so it is deliberately absent from what we send.
     */
    val encoding: String? = null,
)

/** Lets a reconnecting client land back where it was. */
@Serializable
@JsonIgnoreProperties
data class ResumeInfo(
    @SerialName("profile_id") val profileId: String? = null,
    @SerialName("page_id") val pageId: String? = null,
)

/**
 * The client's first frame.
 *
 * The documented keys are exactly `device_id`, `token`, `client` and `resume`;
 * the test asserts that, because the handshake is the one place where an extra
 * field would be visible to the host's strictest parser.
 */
@Serializable
@JsonIgnoreProperties
data class HelloPayload(
    @SerialName("device_id") val deviceId: String,
    val token: String,
    val client: ClientInfo,
    val resume: ResumeInfo? = null,
)

/** Identifies the host. */
@Serializable
@JsonIgnoreProperties
data class HostInfo(
    val id: String,
    val name: String,
    val os: String = "",
    val version: String = "",
)

/** Identifies the authenticated device and what it may do. */
@Serializable
@JsonIgnoreProperties
data class DeviceInfo(
    val id: String,
    val name: String = "",
    val scopes: List<String> = emptyList(),
)

/** The host's answer to `hello`. */
@Serializable
@JsonIgnoreProperties
data class WelcomePayload(
    @SerialName("session_id") val sessionId: String,
    val host: HostInfo,
    val device: DeviceInfo,
    @SerialName("server_time") val serverTime: Long = 0L,
    @SerialName("heartbeat_interval_ms") val heartbeatIntervalMs: Int = 15000,
    @SerialName("idle_timeout_ms") val idleTimeoutMs: Int = 45000,
    /**
     * The negotiation surface. A client MUST gate optional behaviour on this
     * and MUST tolerate entries it does not recognise (PROTOCOL.md §2.3), which
     * is why it is a plain list of strings and never an enum.
     */
    val features: List<String> = emptyList(),
    @SerialName("profiles_revision") val profilesRevision: String = "",
) {
    /** Whether the host advertised an optional feature. */
    fun supports(feature: String): Boolean = features.contains(feature)
}

/** Heartbeat body; `echo` is copied back by `pong` (PROTOCOL.md §2.4). */
@Serializable
@JsonIgnoreProperties
data class PingPayload(val echo: String = "")

@Serializable
@JsonIgnoreProperties
data class PongPayload(val echo: String = "")

// ---------------------------------------------------------------------------
// Profiles (PROTOCOL.md §5)
// ---------------------------------------------------------------------------

@Serializable
@JsonIgnoreProperties
class ProfileListPayload

/** One entry in the profile list. */
@Serializable
@JsonIgnoreProperties
data class ProfileSummary(
    val id: String,
    val name: String,
    val icon: JsonElement? = null,
    val pages: List<String> = emptyList(),
    val revision: String = "",
)

@Serializable
@JsonIgnoreProperties
data class ProfileListResultPayload(
    val profiles: List<ProfileSummary> = emptyList(),
    val active: String? = null,
)

@Serializable
@JsonIgnoreProperties
data class ProfileGetPayload(@SerialName("profile_id") val profileId: String)

/**
 * One profile document.
 *
 * `profile` stays a [JsonElement] on purpose. The document is rendered from
 * whatever the host sent; re-encoding it through a typed struct here would
 * silently drop the fields this build does not know about, which is exactly the
 * forward-compatibility the protocol promises.
 */
@Serializable
@JsonIgnoreProperties
data class ProfileGetResultPayload(
    val profile: JsonElement? = null,
    val revision: String = "",
)

@Serializable
@JsonIgnoreProperties
data class ProfileSetActivePayload(@SerialName("profile_id") val profileId: String)

@Serializable
@JsonIgnoreProperties
data class ProfileReloadPayload(@SerialName("profile_id") val profileId: String)

@Serializable
@JsonIgnoreProperties
data class EventProfileChangedPayload(
    @SerialName("profile_id") val profileId: String,
    val revision: String = "",
)

// ---------------------------------------------------------------------------
// Buttons (PROTOCOL.md §6)
// ---------------------------------------------------------------------------

/** `press.kind` values. */
object PressKind {
    const val SHORT = "short"
    const val LONG = "long"
    const val REPEAT = "repeat"
}

/**
 * Distinguishes a tap from a long press.
 *
 * The client owns the 400 ms threshold; the host never re-derives the gesture,
 * because two implementations of one threshold would eventually disagree
 * (PROTOCOL.md §6.1).
 */
@Serializable
@JsonIgnoreProperties
data class PressInfo(val kind: String = PressKind.SHORT, val count: Int = 1)

@Serializable
@JsonIgnoreProperties
data class ButtonPressPayload(
    @SerialName("profile_id") val profileId: String,
    @SerialName("page_id") val pageId: String,
    @SerialName("button_id") val buttonId: String,
    val press: PressInfo = PressInfo(),
)

@Serializable
@JsonIgnoreProperties
data class ButtonReleasePayload(
    @SerialName("profile_id") val profileId: String,
    @SerialName("page_id") val pageId: String,
    @SerialName("button_id") val buttonId: String,
    @SerialName("held_ms") val heldMs: Long,
)

/** Action-specific failure detail. */
@Serializable
@JsonIgnoreProperties
data class ActionError(
    val code: String,
    val message: String = "",
)

/** The outcome of an action (PROTOCOL.md §6.3). */
@Serializable
@JsonIgnoreProperties
data class ActionResultPayload(
    @SerialName("execution_id") val executionId: String? = null,
    @SerialName("action_id") val actionId: String? = null,
    @SerialName("action_type") val actionType: String? = null,
    val ok: Boolean = false,
    @SerialName("duration_ms") val durationMs: Long = 0L,
    /** Action-specific and frequently absent, so it stays opaque. */
    val output: JsonElement? = null,
    val error: ActionError? = null,
    /**
     * Long-running actions reply twice: an immediate `accepted:true`, then a
     * terminal `event.action.finished` (PROTOCOL.md §6.3).
     */
    val accepted: Boolean? = null,
)

@Serializable
@JsonIgnoreProperties
data class ActionCancelPayload(@SerialName("execution_id") val executionId: String)

/**
 * A host-pushed state change for one button.
 *
 * `state` stays a [JsonElement]: the host sends only the fields that changed
 * for the state kind in question (PROTOCOL.md §6.4), and the client merges them
 * onto whatever it already has.
 */
@Serializable
@JsonIgnoreProperties
data class EventButtonStatePayload(
    @SerialName("profile_id") val profileId: String,
    @SerialName("page_id") val pageId: String,
    @SerialName("button_id") val buttonId: String,
    val state: JsonElement? = null,
)

// ---------------------------------------------------------------------------
// Telemetry (PROTOCOL.md §7)
// ---------------------------------------------------------------------------

/** Interval bounds the host clamps to; mirrored so the UI cannot ask for less. */
const val TELEMETRY_MIN_INTERVAL_MS = 250
const val TELEMETRY_MAX_INTERVAL_MS = 60000

@Serializable
@JsonIgnoreProperties
data class TelemetrySubscribePayload(
    val metrics: List<String>,
    @SerialName("interval_ms") val intervalMs: Int = 1000,
)

@Serializable
@JsonIgnoreProperties
data class EventTelemetryPayload(
    val ts: Long = 0L,
    val values: Map<String, Double> = emptyMap(),
)

// ---------------------------------------------------------------------------
// Codec
// ---------------------------------------------------------------------------

/**
 * The one JSON codec for every frame.
 *
 * `ignoreUnknownKeys` is set globally as well as per class: the annotation
 * states the intent per payload, this makes it true for the envelope and for
 * the nested documents that are not modelled as Kotlin classes at all.
 * `encodeDefaults` is false so a payload on the wire carries only the fields
 * the protocol documents as meaningful, and `explicitNulls` is false so an
 * absent optional field is absent rather than an explicit `null`.
 */
val ProtocolJson: Json = Json {
    ignoreUnknownKeys = true
    isLenient = false
    encodeDefaults = false
    explicitNulls = false
    allowStructuredMapKeys = false
    coerceInputValues = false
}

/** The payload classes the compatibility policy applies to. */
internal val PAYLOAD_CLASSES: List<Class<*>> = listOf(
    ErrorPayload::class.java,
    ScreenInfo::class.java,
    ClientInfo::class.java,
    ResumeInfo::class.java,
    HelloPayload::class.java,
    HostInfo::class.java,
    DeviceInfo::class.java,
    WelcomePayload::class.java,
    PingPayload::class.java,
    PongPayload::class.java,
    ProfileListPayload::class.java,
    ProfileSummary::class.java,
    ProfileListResultPayload::class.java,
    ProfileGetPayload::class.java,
    ProfileGetResultPayload::class.java,
    ProfileSetActivePayload::class.java,
    ProfileReloadPayload::class.java,
    EventProfileChangedPayload::class.java,
    PressInfo::class.java,
    ButtonPressPayload::class.java,
    ButtonReleasePayload::class.java,
    ActionError::class.java,
    ActionResultPayload::class.java,
    ActionCancelPayload::class.java,
    EventButtonStatePayload::class.java,
    TelemetrySubscribePayload::class.java,
    EventTelemetryPayload::class.java,
)

/** Current wall-clock time in epoch milliseconds, the unit of `ts`. */
internal fun nowMs(): Long = System.currentTimeMillis()

private var idCounter = 0L
private val idLock = Any()

/**
 * Generates a request id.
 *
 * PROTOCOL.md §1.3 only requires uniqueness within a session and a 64-byte
 * cap; a Crockford base32 ULID is *recommended*, not required. A monotonic
 * counter mixed with the clock is unique per process and readable in a log,
 * which is what the field is actually for.
 */
fun newRequestId(): String {
    val n = synchronized(idLock) { idCounter++ }
    return "a-${nowMs().toString(36)}-${n.toString(36)}"
}

/** The payload every message without one carries: `{}`, never `null`. */
val EMPTY_PAYLOAD: JsonElement = JsonObject(emptyMap())

/**
 * Encodes a payload class to a [JsonElement].
 *
 * The reified overload of `Json.encodeToJsonElement` is a top-level extension,
 * so an unimported call silently resolves to the two-argument member function
 * and fails with a confusing error. Naming it once here keeps every call site
 * on the inferred-strategy version.
 */
inline fun <reified T> encodeToJson(value: T): JsonElement =
    ProtocolJson.encodeToJsonElement(kotlinx.serialization.serializer<T>(), value)

/** Decodes a payload body, tolerating unknown keys (PROTOCOL.md §11). */
inline fun <reified T> decodeFromJson(element: JsonElement?): T =
    ProtocolJson.decodeFromJsonElement(
        kotlinx.serialization.serializer<T>(),
        element ?: EMPTY_PAYLOAD,
    )

/**
 * Builds an outbound request envelope.
 *
 * PROTOCOL.md §1.1 forbids the `msgpack` opt-in in v1, so `encoding` is never
 * set on the client side. PROTOCOL.md §1.3 requires `payload` to be an object
 * and never `null`, so an omitted payload becomes `{}` rather than a null.
 */
fun newRequest(type: String, id: String = newRequestId(), payload: JsonElement = EMPTY_PAYLOAD): Envelope =
    Envelope(v = PROTOCOL_VERSION, id = id, replyTo = null, type = type, ts = nowMs(), payload = payload)

/** Builds an outbound reply to an inbound request. */
fun replyTo(request: Envelope, type: String, payload: JsonElement = EMPTY_PAYLOAD): Envelope =
    Envelope(v = PROTOCOL_VERSION, id = newRequestId(), replyTo = request.id, type = type, ts = nowMs(), payload = payload)

/**
 * The envelope's payload as an object, tolerating an absent or non-object body.
 *
 * The protocol says `payload` is always an object, but a buggy or hostile peer
 * can still send a bare string, and a crash in the frame loop is a much worse
 * failure than treating the body as empty.
 */
fun Envelope.payloadObject(): JsonObject =
    payload as? JsonObject ?: EMPTY_PAYLOAD as JsonObject
