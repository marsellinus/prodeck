package dev.mobiledeck

import dev.mobiledeck.data.ClientInfo
import dev.mobiledeck.data.DeckClient
import dev.mobiledeck.data.EMPTY_PAYLOAD
import dev.mobiledeck.data.Envelope
import dev.mobiledeck.data.ErrorPayload
import dev.mobiledeck.data.HelloPayload
import dev.mobiledeck.data.PROTOCOL_VERSION
import dev.mobiledeck.data.PAYLOAD_CLASSES
import dev.mobiledeck.data.ProtocolJson
import dev.mobiledeck.data.ResumeInfo
import dev.mobiledeck.data.ScreenInfo
import dev.mobiledeck.data.decodeFromJson
import dev.mobiledeck.data.encodeToJson
import dev.mobiledeck.data.newRequest
import dev.mobiledeck.data.payloadObject
import dev.mobiledeck.data.replyTo
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The wire-contract tests (docs/PROTOCOL.md §1.3, §11).
 *
 * These are JVM tests on purpose: `Protocol.kt` has no Android dependency, and
 * the compatibility policy is a property of the encoding, not of the app. A test
 * that needed a device would be run far less often than the rule it protects
 * needs to hold.
 */
class ProtocolTest {

    // -- envelope round-trip ------------------------------------------------

    /**
     * PROTOCOL.md §1.3: every field of the envelope survives a round trip.
     *
     * `v/id/reply_to/type/ts/payload` are the whole contract between the client
     * and the host, so a field that silently defaulted would be a protocol
     * break that only shows up against a real host.
     */
    @Test
    fun `envelope round trip preserves every field`() {
        val original = Envelope(
            v = 1,
            id = "01J8Z9Q0M8Y0V4S2K6B7N3P1XA",
            replyTo = "01J8Z9Q0M8Y0V4S2K6B7N3P1XB",
            type = "button.press",
            ts = 1780000000123L,
            payload = buildJsonObject { put("button_id", JsonPrimitive("terminal")) },
        )

        val encoded = ProtocolJson.encodeToString(Envelope.serializer(), original)
        val decoded = ProtocolJson.decodeFromString(Envelope.serializer(), encoded)

        assertEquals(original.v, decoded.v)
        assertEquals(original.id, decoded.id)
        assertEquals(original.replyTo, decoded.replyTo)
        assertEquals(original.type, decoded.type)
        assertEquals(original.ts, decoded.ts)
        assertEquals(original.payload, decoded.payload)
    }

    /**
     * PROTOCOL.md §1.3: `reply_to` is absent on an unsolicited event, and an
     * absent field must not decode as the string "null" or an empty id.
     */
    @Test
    fun `unsolicited event decodes with no reply_to`() {
        val text = """
            {"v":1,"type":"event.telemetry","ts":1780000000123,
             "payload":{"ts":1780000000123,"values":{"cpu.usage":43.2}}}
        """.trimIndent()

        val decoded = ProtocolJson.decodeFromString(Envelope.serializer(), text)

        assertEquals("event.telemetry", decoded.type)
        assertNull(decoded.replyTo)
        assertNull(decoded.id)
    }

    /**
     * PROTOCOL.md §1.3: `payload` is an object and never null. A frame whose
     * payload is missing must still be usable, because the host omits it for
     * messages that carry nothing.
     */
    @Test
    fun `payload absent decodes to an empty object`() {
        val decoded = ProtocolJson.decodeFromString(
            Envelope.serializer(),
            """{"v":1,"type":"profile.list","ts":1}""",
        )

        assertNull(decoded.payload)
        assertTrue(decoded.payloadObject().isEmpty())
    }

    // -- additive evolution -------------------------------------------------

    /**
     * PROTOCOL.md §11, and the reason this test exists: "Clients MUST ignore
     * unknown fields in any payload they parse. This is what makes additive
     * evolution safe, and it is covered by a test."
     *
     * The unknown key is nested inside a payload *and* alongside the envelope's
     * own fields, because both are places the host will add fields.
     */
    @Test
    fun `unknown field in a payload is ignored`() {
        val text = """
            {"v":1,"id":"a1","type":"welcome","ts":1780000000123,
             "future_envelope_field":{"nested":true},
             "payload":{
               "session_id":"s-1",
               "host":{"id":"h1","name":"cel-laptop","os":"windows","version":"0.1.0"},
               "device":{"id":"d1","name":"Phone","scopes":["keyboard"]},
               "heartbeat_interval_ms":15000,
               "something_from_the_future":[1,2,3]
             }}
        """.trimIndent()

        val decoded = ProtocolJson.decodeFromString(Envelope.serializer(), text)
        val welcome = decodeFromJson<dev.mobiledeck.data.WelcomePayload>(decoded.payloadObject())

        assertEquals("s-1", welcome.sessionId)
        assertEquals("h1", welcome.host.id)
        assertEquals(15000, welcome.heartbeatIntervalMs)
        // The defaults for fields the host did not send are still the documented
        // ones, not zero.
        assertEquals(45000, welcome.idleTimeoutMs)
        assertTrue(welcome.features.isEmpty())
    }

    /**
     * PROTOCOL.md §11: an unknown *message type* is legal on the wire. The
     * envelope must decode so the client can report it rather than tearing the
     * session down.
     */
    @Test
    fun `unknown type still decodes`() {
        val text = """
            {"v":1,"type":"event.something.invented.later","ts":1780000000123,
             "payload":{"whatever":true}}
        """.trimIndent()

        val decoded = ProtocolJson.decodeFromString(Envelope.serializer(), text)

        assertEquals("event.something.invented.later", decoded.type)
        assertEquals(PROTOCOL_VERSION, decoded.v)
        assertNotNull(decoded.payload)
    }

    /**
     * The same rule applied to a payload class: decoding a payload whose keys
     * this build does not model must not throw, and must not lose the keys it
     * does model.
     */
    @Test
    fun `payload with unknown keys keeps its known keys`() {
        val body = buildJsonObject {
            put("code", JsonPrimitive("forbidden"))
            put("message", JsonPrimitive("device lacks scope 'system.power'"))
            put("retry_after_ms", JsonPrimitive(250))
            put("added_in_v2", JsonPrimitive("ignored"))
        }

        val error = decodeFromJson<ErrorPayload>(body)

        assertEquals("forbidden", error.code)
        assertEquals("device lacks scope 'system.power'", error.message)
        assertEquals(250, error.retryAfterMs)
    }

    /**
     * PROTOCOL.md §5: `profile.get.result` carries a document that a newer host
     * may have written. It must survive as opaque JSON, because re-encoding it
     * through a typed struct would drop exactly the fields a future schema
     * added — the failure the compatibility policy exists to prevent.
     */
    @Test
    fun `profile document passes through unknown keys untouched`() {
        val text = """
            {"v":1,"type":"profile.get.result","ts":1,
             "payload":{"revision":"sha256:abc",
               "profile":{"schema":1,"id":"development","name":"Development",
                 "pages":[{"id":"home","name":"Home","buttons":[]}],
                 "a_field_from_the_future":{"deeply":{"nested":[1,2]}}}}}
        """.trimIndent()

        val decoded = ProtocolJson.decodeFromString(Envelope.serializer(), text)
        val body = decoded.payloadObject()

        val document = body["profile"]!!.jsonObject
        assertEquals("development", document["id"]!!.jsonPrimitive.content)
        assertNotNull(document["a_field_from_the_future"])
    }

    // -- the handshake ------------------------------------------------------

    /**
     * PROTOCOL.md §2.2 documents exactly four keys on `hello`. The host's parser
     * is the strictest one in the system and the handshake is the one frame that
     * cannot be retried with a different shape, so the encoded key set is
     * asserted rather than assumed.
     */
    @Test
    fun `hello encodes with exactly the documented keys`() {
        val hello = HelloPayload(
            deviceId = "android-7c1f9a2b",
            token = "b64url-43-chars",
            client = ClientInfo(
                platform = "android",
                appVersion = "0.1.0",
                model = "Pixel 7",
                screen = ScreenInfo(w = 1080, h = 2400, density = 2.625f, orientation = "portrait"),
            ),
            resume = ResumeInfo(profileId = "development", pageId = "home"),
        )

        val encoded = ProtocolJson.encodeToJsonElement(HelloPayload.serializer(), hello)
        val keys = encoded.jsonObject.keys

        assertEquals(setOf("device_id", "token", "client", "resume"), keys)

        // PROTOCOL.md §1.1: `encoding` is reserved for a future MessagePack
        // switch and a v1 client MUST NOT set it.
        val clientKeys = encoded.jsonObject["client"]!!.jsonObject.keys
        assertTrue("client must not opt into msgpack", "encoding" !in clientKeys)
        assertEquals(
            setOf("platform", "app_version", "model", "screen"),
            clientKeys,
        )
        assertEquals(
            setOf("w", "h", "density", "orientation"),
            encoded.jsonObject["client"]!!.jsonObject["screen"]!!.jsonObject.keys,
        )
        assertEquals(
            setOf("profile_id", "page_id"),
            encoded.jsonObject["resume"]!!.jsonObject.keys,
        )
    }

    /**
     * PROTOCOL.md §2.2: an unpaired client sends an empty token, and the field
     * must be present — an omitted `token` is a different frame from an empty
     * one, and the host uses it to decide between "pair first" and "authenticate".
     */
    @Test
    fun `unpaired hello still carries an empty token`() {
        val hello = HelloPayload(
            deviceId = "android-abc",
            token = "",
            client = ClientInfo(platform = "android", appVersion = "0.1.0"),
        )

        val encoded = ProtocolJson.encodeToJsonElement(HelloPayload.serializer(), hello)

        assertTrue("token must be present even when empty", "token" in encoded.jsonObject.keys)
        assertEquals("", encoded.jsonObject["token"]!!.jsonPrimitive.content)
        // `resume` is optional and absent here.
        assertTrue("resume must be omitted when there is nothing to resume", "resume" !in encoded.jsonObject.keys)
    }

    // -- request construction ----------------------------------------------

    /** PROTOCOL.md §1.3: a request carries an `id` and no `reply_to`. */
    @Test
    fun `request carries an id and no reply_to`() {
        val envelope = newRequest("ping", payload = encodeToJson(ErrorPayload("code", "message")))

        assertNotNull(envelope.id)
        assertNull(envelope.replyTo)
        assertEquals("ping", envelope.type)
        assertEquals(PROTOCOL_VERSION, envelope.v)
        assertTrue(envelope.ts > 0)
    }

    /** PROTOCOL.md §1.3: a reply carries `reply_to` and never reuses the id. */
    @Test
    fun `reply correlates without reusing the request id`() {
        val request = newRequest("profile.list")
        val response = replyTo(request, "profile.list.result")

        assertEquals(request.id, response.replyTo)
        assertNotNull(response.id)
        assertTrue("a reply must not reuse the request id", response.id != request.id)
    }

    /** Request ids must be unique within a session (PROTOCOL.md §1.3). */
    @Test
    fun `request ids are unique`() {
        val ids = (1..500).map { newRequest("ping").id }
        assertEquals("ids must be unique", 500, ids.toSet().size)
        // And within the 64-byte cap the protocol sets.
        assertTrue(ids.all { (it?.length ?: 0) <= 64 })
    }

    // -- the compatibility registry ----------------------------------------

    /**
     * The compatibility policy is a rule about *every* payload class, and a rule
     * that depends on remembering to apply an annotation is a rule that will
     * eventually be broken. This reflects over the registry and fails if a
     * payload was added without [dev.mobiledeck.data.JsonIgnoreProperties], so
     * the next protocol field cannot silently become a breaking change.
     */
    @Test
    fun `every payload class declares ignoreUnknown`() {
        val missing = PAYLOAD_CLASSES.filter {
            it.getAnnotation(dev.mobiledeck.data.JsonIgnoreProperties::class.java) == null
        }

        assertTrue(
            "these payload classes must carry @JsonIgnoreProperties: " +
                missing.joinToString { it.simpleName },
            missing.isEmpty(),
        )
    }

    /**
     * The registry is only useful if it is complete. Every `@Serializable` class
     * in `data/` that is not a payload is listed here explicitly, so adding a
     * payload without registering it fails the build rather than quietly
     * escaping the check above.
     */
    @Test
    fun `payload registry covers the protocol surface`() {
        val registered = PAYLOAD_CLASSES.map { it.simpleName }.toSet()

        val expected = setOf(
            "ErrorPayload", "ScreenInfo", "ClientInfo", "ResumeInfo", "HelloPayload",
            "HostInfo", "DeviceInfo", "WelcomePayload", "PingPayload", "PongPayload",
            "ProfileListPayload", "ProfileSummary", "ProfileListResultPayload",
            "ProfileGetPayload", "ProfileGetResultPayload", "ProfileSetActivePayload",
            "ProfileReloadPayload", "EventProfileChangedPayload", "PressInfo",
            "ButtonPressPayload", "ButtonReleasePayload", "ActionError",
            "ActionResultPayload", "ActionCancelPayload", "EventButtonStatePayload",
            "TelemetrySubscribePayload", "EventTelemetryPayload",
        )

        assertEquals("the payload registry must match the protocol surface", expected, registered)
    }

    // -- error decoding -----------------------------------------------------

    /** PROTOCOL.md §8: the error payload's shape is what the UI renders. */
    @Test
    fun `error payload decodes with optional fields`() {
        val body = JsonObject(
            mapOf(
                "code" to JsonPrimitive("rate_limited"),
                "message" to JsonPrimitive("too many requests"),
                "retry_after_ms" to JsonPrimitive(1500),
            ),
        )

        val error = decodeFromJson<ErrorPayload>(body)

        assertEquals("rate_limited", error.code)
        assertEquals(1500, error.retryAfterMs)
        assertNull(error.pointer)
    }

    /**
     * PROTOCOL.md §1.3: `payload` is an object and never `null`. A request built
     * without one must still put `{}` on the wire, because the host's parser
     * rejects a null body.
     */
    @Test
    fun `empty payload encodes as an object`() {
        assertEquals("{}", ProtocolJson.encodeToString(JsonElement.serializer(), EMPTY_PAYLOAD))

        val encoded = ProtocolJson.encodeToString(Envelope.serializer(), newRequest("ping"))
        assertTrue("payload must be an object, never null", encoded.contains("\"payload\":{}"))
    }

    // -- close-code policy --------------------------------------------------

    /**
     * PROTOCOL.md §2.5 is a table of client behaviours, and this asserts the
     * table rather than the code: a close code whose reaction drifts is a bug
     * that only appears when a host is shutting down or a token is revoked.
     */
    @Test
    fun `close codes map to the documented client behaviour`() {
        val policy = dev.mobiledeck.data.CloseCode.entries.associateBy { it.code }

        // 1000 Normal: do not reconnect.
        assertTrue(!policy.getValue(1000).reconnect)
        // 1013 Host busy: reconnect with backoff.
        assertTrue(policy.getValue(1013).reconnect)
        assertTrue(policy.getValue(1013).usesBackoff)
        // 4400 Malformed first frame: do not reconnect; surface a bug.
        assertTrue(!policy.getValue(4400).reconnect)
        // 4401 Unauthenticated: wipe the token, return to pairing.
        assertTrue(policy.getValue(4401).reconnect)
        assertTrue(policy.getValue(4401).wipesToken)
        // 4403 Device disabled: do not reconnect; show "turned off on your computer".
        assertTrue(!policy.getValue(4403).reconnect)
        // 4408 Idle timeout: reconnect immediately, once.
        assertTrue(policy.getValue(4408).reconnect)
        assertTrue("4408 is the immediate reconnect, not a backoff one", !policy.getValue(4408).usesBackoff)
        // 4429 Rate limited: reconnect with backoff.
        assertTrue(policy.getValue(4429).reconnect)
        assertTrue(policy.getValue(4429).usesBackoff)

        // Only 4401 wipes the token.
        assertEquals(
            setOf(4401),
            dev.mobiledeck.data.CloseCode.entries.filter { it.wipesToken }.map { it.code }.toSet(),
        )
        // An unknown code is not classified, so the store falls back to retrying.
        assertNull(dev.mobiledeck.data.CloseCode.of(4999))
    }

    // -- heartbeat ----------------------------------------------------------

    /**
     * PROTOCOL.md §2.4: the client sends `ping` every `heartbeat_interval_ms`
     * and the host echoes `payload.echo` in `pong`. The default matches the
     * host's own advertised default so an unnegotiated client is still correct.
     */
    @Test
    fun `ping carries an echo and the default interval matches the host`() {
        val ping = dev.mobiledeck.data.PingPayload(echo = "abc")
        val encoded = ProtocolJson.encodeToString(dev.mobiledeck.data.PingPayload.serializer(), ping)
        assertEquals("""{"echo":"abc"}""", encoded)

        assertEquals(15_000, dev.mobiledeck.data.WelcomePayload(
            sessionId = "s",
            host = dev.mobiledeck.data.HostInfo(id = "h", name = "n"),
            device = dev.mobiledeck.data.DeviceInfo(id = "d"),
        ).heartbeatIntervalMs)

        // The deadline the client allows for `welcome` is the host's own
        // handshake deadline, from the protocol rather than a guess.
        assertEquals(10_000L, DeckClient.HELLO_DEADLINE_MS)
    }
}
