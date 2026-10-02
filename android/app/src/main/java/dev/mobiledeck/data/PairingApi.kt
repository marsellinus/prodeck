package dev.mobiledeck.data

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

// ---------------------------------------------------------------------------
// REST bodies (PROTOCOL.md §2.1, §3)
// ---------------------------------------------------------------------------

/** `GET /api/v1/info`. Unauthenticated by design: it is what makes pinning possible. */
@Serializable
@JsonIgnoreProperties
data class HostInfoResponse(
    @SerialName("host_id") val hostId: String = "",
    @SerialName("host_name") val hostName: String = "",
    val os: String = "",
    @SerialName("agent_version") val agentVersion: String = "",
    val protocol: ProtocolRange = ProtocolRange(),
    val tls: TlsInfo = TlsInfo(),
    val pairing: PairingInfo = PairingInfo(),
    val profiles: ProfilesSummary = ProfilesSummary(),
    val features: List<String> = emptyList(),
    @SerialName("server_time") val serverTime: Long = 0L,
    @SerialName("active_profile") val activeProfile: String? = null,
)

@Serializable
@JsonIgnoreProperties
data class ProtocolRange(val min: Int = 1, val max: Int = 1) {
    /** PROTOCOL.md §11: refuse to pair when the ranges do not intersect. */
    val intersectsV1: Boolean get() = min <= PROTOCOL_VERSION && PROTOCOL_VERSION <= max
}

@Serializable
@JsonIgnoreProperties
data class TlsInfo(
    val enabled: Boolean = false,
    @SerialName("fingerprint_sha256") val fingerprint: String = "",
    @SerialName("not_after") val notAfter: String = "",
    @SerialName("plaintext_allowed") val plaintextAllowed: Boolean = false,
)

@Serializable
@JsonIgnoreProperties
data class PairingInfo(
    val open: Boolean = false,
    val method: String = "pin",
    @SerialName("expires_in_s") val expiresInS: Int = 0,
    @SerialName("pin_length") val pinLength: Int = 6,
)

@Serializable
@JsonIgnoreProperties
data class ProfilesSummary(val count: Int = 0, val revision: String = "")

@Serializable
@JsonIgnoreProperties
data class PairRequestBody(
    val pin: String,
    val device: PairDeviceInfo,
)

@Serializable
@JsonIgnoreProperties
data class PairDeviceInfo(
    val id: String,
    val name: String,
    val platform: String = "android",
    val model: String = "",
)

/** `POST /api/v1/pair` success body. */
@Serializable
@JsonIgnoreProperties
data class PairResponse(
    @SerialName("device_id") val deviceId: String,
    val token: String,
    val scopes: List<String> = emptyList(),
    val host: HostInfo? = null,
)

/** `POST /api/v1/pair` failure body (PROTOCOL.md §3). */
@Serializable
@JsonIgnoreProperties
data class PairErrorResponse(
    val error: String = "",
    val message: String = "",
    @SerialName("attempts_remaining") val attemptsRemaining: Int? = null,
)

/** What the client learned about a host before it holds a credential. */
data class HostProbe(
    val baseUrl: String,
    val info: HostInfoResponse,
    /** The fingerprint the client is now pinned to, or null in plaintext mode. */
    val pinnedFingerprint: String?,
) {
    val isTls: Boolean get() = baseUrl.startsWith("https://")
}

/** A pairing refusal, phrased for the person holding the phone. */
class PairingException(message: String, val code: String = "", val attemptsRemaining: Int? = null) :
    Exception(message)

/**
 * The REST half of the client: `GET /api/v1/info` and `POST /api/v1/pair`.
 *
 * ## Trust model
 *
 * The host uses a self-signed certificate, so the platform trust store can
 * never validate it. Trust is instead established by pinning the SHA-256
 * fingerprint of the leaf certificate (PROTOCOL.md §3, docs/SECURITY.md T9):
 *
 *  * the fingerprint is shown to the user before pairing and comes from mDNS
 *    TXT or from `/api/v1/info` itself;
 *  * [PinnedTrustManager] accepts a chain only when the leaf's fingerprint
 *    matches the pin, compared in constant time;
 *  * a host that advertises `tls.enabled=true` is *refused* when no pin is
 *    known, because falling back to the platform trust store would silently
 *    accept any certificate and defeat the pin.
 */
class PairingApi(private val json: Json = ProtocolJson) {

    /**
     * Fetches `/api/v1/info`.
     *
     * @param pin the expected fingerprint. Required when the host serves TLS.
     * @throws PairingException when the host is unreachable, speaks an
     *   incompatible protocol, or serves TLS without a known pin.
     */
    suspend fun probe(baseUrl: String, pin: String?): HostProbe = withContext(Dispatchers.IO) {
        val normalizedPin = pin?.let { normalizeFingerprint(it) }?.takeIf { it.isNotEmpty() }
        val url = "${baseUrl.trimEnd('/')}$INFO_PATH"

        val body = try {
            get(url, normalizedPin)
        } catch (_: IOException) {
            // The transport's own text ("failed to connect to /192.168.1.10:8765")
            // is a socket error and means nothing to the reader, so it is
            // dropped. The address stays, without its scheme: it is how the user
            // tells which computer failed, and `https://` is noise on a phone
            // screen. The scheme is stripped outside the template because Kotlin
            // does not allow an escaped quote inside `${...}`.
            val shown = baseUrl.substringAfter("://")
            throw PairingException(
                "Could not reach your computer at $shown.",
                "unreachable",
            )
        }

        val info = try {
            json.decodeFromString(HostInfoResponse.serializer(), body)
        } catch (_: Exception) {
            throw PairingException(
                "Your computer answered with something this app could not read.",
                "bad_info",
            )
        }

        if (!info.protocol.intersectsV1) {
            // The two version numbers are the only concrete thing to act on —
            // they say which side is older — so they stay, spelled out rather
            // than as "1..1".
            throw PairingException(
                "This app and your computer are too different to work together: " +
                    "the app speaks version $PROTOCOL_VERSION, your computer expects " +
                    "version ${info.protocol.min} to ${info.protocol.max}. Update whichever is older.",
                "protocol_mismatch",
            )
        }

        // docs/SECURITY.md T9: a rogue host that advertises TLS but presents no
        // verifiable identity must not be able to harvest a PIN.
        if (info.tls.enabled && normalizedPin == null) {
            // The message says what went wrong and stops there: the error card
            // prints its own sentence in front of it. The word "fingerprint"
            // appears in brackets because that is what the card's advice line is
            // chosen by — a message that stops naming the case silently loses its
            // advice — and a term of art is only allowed here when it comes with
            // a word saying what it is for, which "security code" is.
            throw PairingException(
                "Your computer is using a secure connection, and this app does not have its " +
                    "security code (fingerprint) yet.",
                "no_pin",
            )
        }
        if (info.tls.enabled && normalizedPin != null) {
            val advertised = normalizeFingerprint(info.tls.fingerprint)
            if (advertised.isNotEmpty() && advertised != normalizedPin) {
                // A security refusal, not a hiccup: the code this phone holds and
                // the code this computer is presenting disagree, so the app
                // cannot tell whether this is the same machine. The two hex
                // values are deliberately not printed — they mean nothing to the
                // reader — and the wording must not soften into "try again":
                // that is exactly the case the pin exists to stop. The
                // instruction stays in the sentence rather than being left to
                // the card, because the card's generic advice for a failed
                // connection is to retry, which is the one thing the reader must
                // not do here.
                throw PairingException(
                    "This phone has a different security code (fingerprint) for your computer than " +
                        "the one it is showing now, so this may not be your computer. Do not continue " +
                        "unless you reinstalled MobileDeck on it.",
                    "fingerprint_changed",
                )
            }
        }

        HostProbe(
            baseUrl = baseUrl.trimEnd('/'),
            info = info,
            pinnedFingerprint = if (info.tls.enabled) normalizedPin else null,
        )
    }

    /**
     * Exchanges a PIN for a device token.
     *
     * The caller is responsible for having probed the host first: the pin
     * cannot be verified against anything the client did not already learn.
     */
    suspend fun pair(probe: HostProbe, pin: String, device: PairDeviceInfo): PairResponse =
        withContext(Dispatchers.IO) {
            val request = PairRequestBody(pin = pin, device = device)
            val payload = json.encodeToString(PairRequestBody.serializer(), request)
            val url = "${probe.baseUrl}$PAIR_PATH"

            val result = post(url, payload, probe.pinnedFingerprint)
            when (result.status) {
                200, 201 -> try {
                    json.decodeFromString(PairResponse.serializer(), result.body)
                } catch (_: Exception) {
                    // No "PIN" in this sentence on purpose: the error card picks
                    // its advice by matching words in the message, and the PIN
                    // advice would tell the user to retype a PIN that the
                    // computer had already accepted.
                    throw PairingException(
                        "Your computer's answer could not be read by this app. Try again.",
                        "bad_pair_reply",
                    )
                }

                401, 403 -> {
                    val err = runCatching {
                        json.decodeFromString(PairErrorResponse.serializer(), result.body)
                    }.getOrNull()
                    // The host's own sentence wins when it sent one: it knows
                    // whether the PIN was wrong, expired or already used, and it
                    // is the only side that can tell those apart. Our fallback
                    // says what went wrong and stops, because the error card
                    // prints its own line telling the user to ask for a new PIN.
                    throw PairingException(
                        err?.message?.takeIf { it.isNotBlank() } ?: "That PIN did not work.",
                        err?.error ?: "invalid_pin",
                        err?.attemptsRemaining,
                    )
                }

                429 -> throw PairingException(
                    "Too many wrong PINs, so your computer has stopped accepting them for a moment.",
                    "rate_limited",
                )

                else -> throw PairingException(
                    "Your computer refused to set up this phone.",
                    "http_${result.status}",
                )
            }
        }

    private class HttpResult(val status: Int, val body: String)

    private fun get(url: String, pin: String?): String =
        clientFor(pin).newCall(Request.Builder().url(url).get().build()).execute().use { resp ->
            if (!resp.isSuccessful) throw IOException("HTTP ${resp.code} from $url")
            resp.body?.string() ?: throw IOException("empty body from $url")
        }

    private fun post(url: String, payload: String, pin: String?): HttpResult =
        clientFor(pin).newCall(
            Request.Builder()
                .url(url)
                .post(payload.toRequestBody(JSON_MEDIA_TYPE))
                .build(),
        ).execute().use { resp ->
            HttpResult(resp.code, resp.body?.string().orEmpty())
        }

    /**
     * Builds a client that trusts exactly the pinned certificate.
     *
     * A client per call is deliberate: the pin is a per-host property, and a
     * cached client would make it possible to reuse one host's trust decision
     * for another host. The connection pool inside is short-lived and the
     * probe/pair pair is two requests.
     */
    private fun clientFor(pin: String?): OkHttpClient {
        val builder = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(15, TimeUnit.SECONDS)
            .callTimeout(30, TimeUnit.SECONDS)
            .retryOnConnectionFailure(false)

        if (pin == null) return builder.build()

        val context = SSLContext.getInstance("TLS")
        context.init(null, arrayOf(PinnedTrustManager(pin)), null)
        return builder
            .sslSocketFactory(context.socketFactory, PinnedTrustManager(pin))
            .build()
    }

    companion object {
        const val INFO_PATH = "/api/v1/info"
        const val PAIR_PATH = "/api/v1/pair"
        const val HEALTH_PATH = "/api/v1/health"

        private val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()

        /**
         * Normalises a fingerprint to bare lowercase hex.
         *
         * mDNS TXT, `/api/v1/info` and a hand-typed value can all be written as
         * `ab:cd:...`, `AB:CD:...` or `abcd...`; they must compare equal.
         */
        fun normalizeFingerprint(raw: String): String =
            raw.lowercase().filter { it.isDigit() || it in 'a'..'f' }

        /**
         * An OkHttp client for the WebSocket that trusts exactly [pin].
         *
         * The same pin the REST probe verified must be applied to the socket,
         * otherwise the control channel would be the one unprotected hop.
         * A null pin yields the plain client, which is only reachable when the
         * host itself advertised `tls.enabled=false`.
         */
        fun wsClientFor(pin: String?): OkHttpClient {
            val base = WsTransport.defaultClient()
            if (pin == null) return base
            val context = SSLContext.getInstance("TLS")
            val manager = PinnedTrustManager(pin)
            context.init(null, arrayOf<X509TrustManager>(manager), null)
            return base.newBuilder()
                .sslSocketFactory(context.socketFactory, manager)
                .build()
        }

        /**
         * The SHA-256 of a certificate's DER encoding, as `ab:cd:...`.
         *
         * Shown to the user so the value on the phone can be compared with the
         * one printed by the host, which is the whole point of pinning.
         */
        fun fingerprintOf(cert: X509Certificate): String =
            MessageDigest.getInstance("SHA-256")
                .digest(cert.encoded)
                .joinToString(":") { "%02x".format(it) }
    }
}

/**
 * Trusts a certificate chain only when the leaf's SHA-256 fingerprint matches
 * the pin.
 *
 * Two deliberate omissions:
 *
 *  * **Hostname and expiry are not checked.** The pin is an assertion about the
 *    exact public key material, which is strictly stronger than both: a
 *    certificate with the right fingerprint cannot be forged without the
 *    private key, and the operator re-pins explicitly when they rotate it.
 *    Checking the hostname on a self-signed cert would fail on the common case
 *    of an IP-addressed host with no matching SAN.
 *  * **The platform trust store is not consulted.** Falling back to it on a pin
 *    mismatch would make the pin advisory.
 */
class PinnedTrustManager(private val pin: String) : X509TrustManager {

    private val expected: ByteArray = hexToBytes(pin)

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        throw CertificateException("this trust manager is for client connections only")
    }

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull()
            ?: throw CertificateException("host presented no certificate")
        val actual = MessageDigest.getInstance("SHA-256").digest(leaf.encoded)
        // MessageDigest.isEqual is the constant-time comparison: a byte-by-byte
        // `contentEquals` would leak how many leading bytes matched.
        if (!MessageDigest.isEqual(actual, expected)) {
            throw CertificateException(
                "certificate fingerprint does not match the pinned value: got ${hexOf(actual)}",
            )
        }
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()

    private fun hexToBytes(hex: String): ByteArray {
        require(hex.length % 2 == 0) { "fingerprint must be an even number of hex digits" }
        return ByteArray(hex.length / 2) { i ->
            hex.substring(i * 2, i * 2 + 2).toInt(16).toByte()
        }
    }

    private fun hexOf(bytes: ByteArray): String = bytes.joinToString(":") { "%02x".format(it) }
}
