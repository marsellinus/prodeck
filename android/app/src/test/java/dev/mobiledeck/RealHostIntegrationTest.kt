package dev.mobiledeck

import dev.mobiledeck.data.ButtonPressPayload
import dev.mobiledeck.data.ClientEvent
import dev.mobiledeck.data.ClientInfo
import dev.mobiledeck.data.DeckClient
import dev.mobiledeck.data.HandshakeResult
import dev.mobiledeck.data.HelloPayload
import dev.mobiledeck.data.MsgType
import dev.mobiledeck.data.PressInfo
import dev.mobiledeck.data.PressKind
import dev.mobiledeck.data.Profile
import dev.mobiledeck.data.ProfileGetPayload
import dev.mobiledeck.data.ProfileGetResultPayload
import dev.mobiledeck.data.ProtocolJson
import dev.mobiledeck.data.TelemetrySubscribePayload
import dev.mobiledeck.data.WsTransport
import dev.mobiledeck.data.decodeFromJson
import dev.mobiledeck.data.encodeToJson
import dev.mobiledeck.data.payloadObject
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test
import java.net.HttpURLConnection
import java.net.URI

/**
 * End-to-end tests of the *client* stack against a **real host agent**.
 *
 * This is the strongest verification available without a device: a real
 * [WsTransport] over a real OkHttp WebSocket, the real [DeckClient] state
 * machine, the real protocol, and a real `mobiledeck` process executing real
 * actions. Everything the app does except the Compose rendering and the
 * NsdManager discovery is exercised here.
 *
 * It exists because the emulator path is unreliable: on the API 37 image
 * available here, `adb shell input` cannot drive Compose text fields, so pairing
 * cannot be completed through the UI. That gap is a harness limitation, not a
 * client limitation, and this test closes the functional half of it.
 *
 * The tests are skipped unless a host is running, so a normal `./gradlew test`
 * on a machine without one still passes. Point them at a host with:
 *
 *     MOBILEDECK_E2E_ADDR=127.0.0.1:8765 \
 *     MOBILEDECK_E2E_ADMIN_TOKEN=<from runtime.json> \
 *     ./gradlew :app:testDebugUnitTest
 *
 * Each test asks the host for its own single-use PIN through the admin API, so
 * they run independently and in any order. `scripts/e2e-host.sh` starts a
 * throwaway host and prints the two values to export.
 */
class RealHostIntegrationTest {

    private lateinit var scope: CoroutineScope
    private var client: DeckClient? = null

    private val addr: String = (System.getenv("MOBILEDECK_E2E_ADDR") ?: "127.0.0.1:8765").trim()
    /**
     * The admin token, trimmed.
     *
     * It arrives through an environment variable, and a shell that builds one
     * with `$(...)` on Windows can append a carriage return. A stray CR in a
     * header makes the Go server reject the whole request with a bare 400 before
     * it reaches a handler, which is correct behaviour and a confusing failure
     * to debug from here.
     */
    private val adminToken: String = (System.getenv("MOBILEDECK_E2E_ADMIN_TOKEN") ?: "").trim()
    private val base: String get() = "http://$addr"

    @Before
    fun setUp() {
        scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    }

    @After
    fun tearDown() {
        client?.close(1000, "test finished")
        scope.cancel()
    }

    // --- helpers ---------------------------------------------------------

    /** Skips the test when no host is reachable, so the suite still passes. */
    private fun requireHost(): Boolean {
        val reachable = try {
            val conn = URI("$base/api/v1/health").toURL().openConnection() as HttpURLConnection
            conn.connectTimeout = 1500
            conn.readTimeout = 1500
            conn.responseCode == 200
        } catch (_: Exception) {
            false
        }
        assumeTrue("no host at $base; start one with scripts/e2e-host.sh", reachable)
        return reachable
    }

    /**
     * Asks the running host for a fresh pairing PIN through its loopback-only
     * admin API.
     *
     * A PIN is single use, so each test needs its own. Fetching one here rather
     * than taking it from the environment is what lets the tests run
     * independently and in any order, and it means only the admin token has to
     * be supplied by whoever starts the host.
     */
    private fun issuePin(): String {
        assumeTrue("MOBILEDECK_E2E_ADMIN_TOKEN is not set; see the class comment", adminToken.isNotBlank())

        val conn = URI("$base/api/v1/admin/pair").toURL().openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = 3000
        conn.readTimeout = 3000
        conn.setRequestProperty("Authorization", "Bearer $adminToken")
        conn.setRequestProperty("Content-Type", "application/json")
        conn.outputStream.use { it.write("{}".toByteArray()) }

        val code = conn.responseCode
        val text = (if (code in 200..299) conn.inputStream else conn.errorStream)
            ?.bufferedReader()?.readText().orEmpty()
        assertEquals("could not obtain a PIN: $text", 200, code)

        val pin = (ProtocolJson.parseToJsonElement(text) as JsonObject)["pin"].toString().trim('"')
        assertEquals("the host returned a malformed PIN", 6, pin.length)
        return pin
    }

    /**
     * Pairs over the REST endpoint, exactly as `PairingApi` does, and returns
     * the device token.
     */
    private fun pair(deviceId: String): String {
        val pin = issuePin()

        val body = buildJsonObject {
            put("pin", pin)
            put(
                "device",
                buildJsonObject {
                    put("id", deviceId)
                    put("name", "JVM integration client")
                    put("platform", "jvm")
                    put("model", "test")
                },
            )
        }.toString()

        val conn = URI("$base/api/v1/pair").toURL().openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = 3000
        conn.readTimeout = 5000
        conn.setRequestProperty("Content-Type", "application/json")
        conn.outputStream.use { it.write(body.toByteArray()) }

        val code = conn.responseCode
        val text = (if (code in 200..299) conn.inputStream else conn.errorStream)
            ?.bufferedReader()?.readText().orEmpty()
        assertEquals("pairing failed: $text", 201, code)

        val token = JsonObject(ProtocolJson.parseToJsonElement(text) as JsonObject)
            .let { it["token"]?.let { t -> (t as kotlinx.serialization.json.JsonPrimitive).content } }
        assertNotNull("the host returned no token", token)
        return token!!
    }

    /** Connects and completes the handshake. */
    private fun connect(token: String, deviceId: String): HandshakeResult {
        val c = DeckClient(WsTransport(), scope)
        client = c
        val hello = HelloPayload(
            deviceId = deviceId,
            token = token,
            client = ClientInfo(platform = "jvm", appVersion = "test", model = "harness"),
        )
        return runBlocking { c.handshake("ws://$addr/ws", hello) }
    }

    /**
     * Performs one HTTP request over a plain socket and returns the status code.
     *
     * `HttpURLConnection` supports only GET, POST, HEAD, OPTIONS, PUT, DELETE and
     * TRACE: it rejects PATCH with "Invalid HTTP method". Writing the request by
     * hand keeps the test free of an HTTP client dependency it would otherwise
     * need for exactly one verb.
     */
    private fun httpRequest(method: String, path: String, body: String?, bearer: String?): Int {
        val host = addr.substringBefore(':')
        val port = addr.substringAfter(':').toInt()
        java.net.Socket(host, port).use { socket ->
            socket.soTimeout = 5000
            val out = socket.getOutputStream()
            val head = buildString {
                append("$method $path HTTP/1.1\r\n")
                append("Host: $addr\r\n")
                if (bearer != null) append("Authorization: Bearer $bearer\r\n")
                if (body != null) {
                    append("Content-Type: application/json\r\n")
                    append("Content-Length: ${body.toByteArray().size}\r\n")
                }
                append("Connection: close\r\n\r\n")
            }
            out.write(head.toByteArray())
            if (body != null) out.write(body.toByteArray())
            out.flush()

            val statusLine = socket.getInputStream().bufferedReader().readLine() ?: return 0
            return statusLine.split(" ").getOrNull(1)?.toIntOrNull() ?: 0
        }
    }

    // --- tests -----------------------------------------------------------

    /**
     * The host is reachable and speaks a protocol version this build implements.
     * This is the check `ConnectScreen` performs before it lets a user pair.
     */
    @Test
    fun `host info is readable and the protocol versions intersect`() {
        requireHost()

        val conn = URI("$base/api/v1/info").toURL().openConnection() as HttpURLConnection
        conn.connectTimeout = 3000
        conn.readTimeout = 3000
        assertEquals(200, conn.responseCode)

        val info = ProtocolJson.parseToJsonElement(conn.inputStream.bufferedReader().readText()) as JsonObject
        val min = (info["protocol"] as JsonObject)["min"].toString().toInt()
        val max = (info["protocol"] as JsonObject)["max"].toString().toInt()

        assertTrue("the host's protocol range $min..$max excludes this client", min <= 1 && 1 <= max)
        assertTrue("the host reported no name", (info["host_name"].toString()).length > 2)
    }

    /**
     * The full pairing handshake: REST pairing, then the WebSocket `hello`
     * exchange, ending with a `welcome` that names the host and the granted
     * scopes.
     */
    @Test
    fun `pair then handshake yields a welcome with scopes`() {
        requireHost()
        val deviceId = "jvm-e2e-" + System.nanoTime()
        val token = pair(deviceId)

        when (val r = connect(token, deviceId)) {
            is HandshakeResult.Ok -> {
                assertEquals("the welcome carries the wrong device", deviceId, r.welcome.device.id)
                assertTrue("no host name in the welcome", r.welcome.host.name.isNotBlank())
                assertTrue("the welcome carries no scopes", r.welcome.device.scopes.isNotEmpty())
                assertTrue(
                    "the default scope set must include keyboard",
                    r.welcome.device.scopes.contains("keyboard"),
                )
                assertTrue("no heartbeat interval was negotiated", r.welcome.heartbeatIntervalMs > 0)
            }
            is HandshakeResult.Refused -> throw AssertionError("the host refused the connection: ${r.info.code} ${r.info.reason}")
            is HandshakeResult.Failed -> throw AssertionError("the handshake failed", r.cause)
        }
    }

    /**
     * The path the whole project exists for: fetch the profile the host serves,
     * then press a button and receive `action.result`.
     *
     * The button is a `noop`, chosen deliberately: the assertion is about the
     * protocol and the engine round trip, and a side-effecting action would make
     * the test depend on the machine it runs on.
     */
    @Test
    fun `profile is fetched and a button press returns an action result`() {
        requireHost()
        val deviceId = "jvm-e2e-" + System.nanoTime()
        val token = pair(deviceId)

        val c = DeckClient(WsTransport(), scope)
        client = c
        runBlocking {
            val r = c.handshake(
                "ws://$addr/ws",
                HelloPayload(deviceId, token, ClientInfo(platform = "jvm", appVersion = "test")),
            )
            check(r is HandshakeResult.Ok) { "handshake failed: $r" }

            // 1. Which profiles does the host serve?
            val list = c.request(MsgType.PROFILE_LIST)
            val profiles = (list.payloadObject()["profiles"] as kotlinx.serialization.json.JsonArray)
            assertTrue("the host serves no profiles", profiles.isNotEmpty())
            val profileId = (profiles[0] as JsonObject)["id"].toString().trim('"')
            assertTrue("the profile has no id", profileId.isNotBlank())

            // 2. Fetch the document the client would render.
            val got = c.request(
                MsgType.PROFILE_GET,
                encodeToJson(ProfileGetPayload(profileId)),
            )
            val doc = decodeFromJson<ProfileGetResultPayload>(got.payloadObject())
            assertTrue("the profile has no revision", doc.revision.isNotBlank())

            // The document is opaque JSON on the wire (forward compatibility);
            // decoding it is what the UI does before rendering.
            val profile = decodeFromJson<Profile>(doc.profile!!)
            assertTrue("the profile has no pages", profile.pages.isNotEmpty())
            val rootPage = profile.rootPageOrFirst()!!
            assertTrue("the root page has no buttons", rootPage.buttons.isNotEmpty())

            // 3. Press the first button that actually has an action.
            val button = rootPage.buttons.first { it.onPress != null }
            c.send(
                MsgType.BUTTON_PRESS,
                encodeToJson(
                    ButtonPressPayload(
                        profileId = profileId,
                        pageId = rootPage.id,
                        buttonId = button.id,
                        press = PressInfo(kind = PressKind.SHORT, count = 1),
                    ),
                ),
            )

            // 4. The result arrives as an event, because a press is
            //    fire-and-forget: the host may answer with action.result or with
            //    the accepted/finished pair for a slow action.
            val result = withTimeout(10_000) {
                var seen: dev.mobiledeck.data.ActionResultPayload? = null
                while (seen == null) {
                    when (val e = c.events.first()) {
                        is ClientEvent.ActionResult -> seen = e.payload
                        is ClientEvent.ActionFinished -> seen = e.payload
                        is ClientEvent.Error -> throw AssertionError("the host refused the press: ${e.payload.message}")
                        else -> Unit
                    }
                }
                seen
            }
            assertTrue("the press failed on the host: ${result.error?.message}", result.ok)
            assertEquals("the wrong action ran", button.onPress!!.type, result.actionType)
            assertTrue("no execution id was returned", !result.executionId.isNullOrBlank())
        }
    }

    /** A request for something that does not exist must be answered, not dropped. */
    @Test
    fun `unknown profile is answered with not_found`() {
        requireHost()
        val deviceId = "jvm-e2e-" + System.nanoTime()
        val token = pair(deviceId)

        val c = DeckClient(WsTransport(), scope)
        client = c
        runBlocking {
            check(c.handshake("ws://$addr/ws", HelloPayload(deviceId, token, ClientInfo(platform = "jvm", appVersion = "test"))) is HandshakeResult.Ok)

            val error = runCatching {
                c.request(MsgType.PROFILE_GET, encodeToJson(ProfileGetPayload("definitely-not-a-profile")))
            }.exceptionOrNull()

            assertTrue("a request for a missing profile did not fail", error != null)
            assertTrue(
                "unexpected error: $error",
                error is dev.mobiledeck.data.ProtocolError &&
                    error.payload.code == dev.mobiledeck.data.ErrorCode.NOT_FOUND,
            )
        }
    }

    /** Telemetry must actually stream, since that is what drives the gauges. */
    @Test
    fun `telemetry subscription streams samples`() {
        requireHost()
        val deviceId = "jvm-e2e-" + System.nanoTime()
        val token = pair(deviceId)

        val c = DeckClient(WsTransport(), scope)
        client = c
        runBlocking {
            check(c.handshake("ws://$addr/ws", HelloPayload(deviceId, token, ClientInfo(platform = "jvm", appVersion = "test"))) is HandshakeResult.Ok)

            c.request(
                MsgType.TELEMETRY_SUBSCRIBE,
                encodeToJson(TelemetrySubscribePayload(metrics = listOf("cpu.usage"), intervalMs = 250)),
            )

            val sample = withTimeout(8_000) {
                var seen: dev.mobiledeck.data.EventTelemetryPayload? = null
                while (seen == null) {
                    val e = c.events.first()
                    if (e is ClientEvent.Telemetry) seen = e.payload
                }
                seen
            }
            assertTrue("the sample has no timestamp", sample.ts > 0)
            assertTrue("the sample does not contain cpu.usage: ${sample.values}", sample.values.containsKey("cpu.usage"))
        }
    }

    /**
     * A revoked device must be refused, which is what makes revocation mean
     * something. The device is created and revoked through the admin API, so the
     * test does not depend on a human running a CLI command mid-test.
     *
     * A revoked device is answered with 4401, not 4403: revoking *deletes* the
     * record, so the host no longer recognises the device and reports the token
     * as invalid (PROTOCOL.md §2.5). 4403 is reserved for a device that still
     * exists but has been disabled, which the next test covers.
     */
    @Test
    fun `a revoked token cannot connect`() {
        requireHost()
        assumeTrue("MOBILEDECK_E2E_ADMIN_TOKEN is not set", adminToken.isNotBlank())

        val deviceId = "jvm-e2e-revoked-" + System.nanoTime()
        val token = pair(deviceId)

        val conn = URI("$base/api/v1/admin/devices/$deviceId").toURL().openConnection() as HttpURLConnection
        conn.requestMethod = "DELETE"
        conn.setRequestProperty("Authorization", "Bearer $adminToken")
        conn.connectTimeout = 3000
        conn.readTimeout = 3000
        assertEquals("revoking failed", 200, conn.responseCode)

        when (val r = connect(token, deviceId)) {
            is HandshakeResult.Refused ->
                assertEquals("wrong close code for a revoked device", 4401, r.info.code)
            is HandshakeResult.Ok -> throw AssertionError("a revoked token was accepted")
            is HandshakeResult.Failed -> throw AssertionError("expected a refusal, got a failure", r.cause)
        }
    }

    /**
     * A disabled device is refused with 4403, which the client treats differently
     * from a revocation: the record still exists, so the UI can say "turned off
     * on your computer" rather than "pair again".
     */
    @Test
    fun `a disabled device is refused with 4403`() {
        requireHost()
        assumeTrue("MOBILEDECK_E2E_ADMIN_TOKEN is not set", adminToken.isNotBlank())

        val deviceId = "jvm-e2e-disabled-" + System.nanoTime()
        val token = pair(deviceId)

        // Disable it. HttpURLConnection refuses the PATCH method outright, so the
        // request is written by hand over a plain socket rather than pulling in
        // an HTTP client just for one verb.
        val body = """{"disabled":true}"""
        val status = httpRequest(
            method = "PATCH",
            path = "/api/v1/admin/devices/$deviceId",
            body = body,
            bearer = adminToken,
        )
        assertEquals("disabling failed", 200, status)

        when (val r = connect(token, deviceId)) {
            is HandshakeResult.Refused ->
                assertEquals("wrong close code for a disabled device", 4403, r.info.code)
            is HandshakeResult.Ok -> throw AssertionError("a disabled device was accepted")
            is HandshakeResult.Failed -> throw AssertionError("expected a refusal, got a failure", r.cause)
        }
    }

    /** A garbage token must be refused with the documented close code. */
    @Test
    fun `a bogus token is refused with 4401`() {
        requireHost()
        val deviceId = "jvm-e2e-bogus-" + System.nanoTime()

        when (val r = connect("not-a-real-token", deviceId)) {
            is HandshakeResult.Refused ->
                assertEquals("wrong close code", 4401, r.info.code)
            is HandshakeResult.Ok -> throw AssertionError("a bogus token was accepted")
            is HandshakeResult.Failed -> throw AssertionError("expected a refusal, got a failure", r.cause)
        }
    }

    /**
     * The client must survive a host ping, which a real host sends on its own
     * heartbeat. This is implicitly covered by the other tests but asserted
     * explicitly because a broken pong handling would silently drop every
     * connection after ~45 s in the field.
     */
    @Test
    fun `the connection stays alive across the host heartbeat window`() {
        requireHost()
        val deviceId = "jvm-e2e-" + System.nanoTime()
        val token = pair(deviceId)

        val c = DeckClient(WsTransport(), scope)
        client = c
        runBlocking {
            val r = c.handshake("ws://$addr/ws", HelloPayload(deviceId, token, ClientInfo(platform = "jvm", appVersion = "test")))
            check(r is HandshakeResult.Ok)
            val window = (r as HandshakeResult.Ok).welcome.heartbeatIntervalMs + 2_000

            delay(window.toLong())

            assertFalse("the client gave up during the heartbeat window", c.closed)
            // And the socket must still work.
            val list = c.request(MsgType.PROFILE_LIST)
            assertNotNull(list)
        }
    }
}
