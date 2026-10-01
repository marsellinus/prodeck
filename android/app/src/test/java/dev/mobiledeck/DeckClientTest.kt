package dev.mobiledeck

import dev.mobiledeck.data.CloseCode
import dev.mobiledeck.data.DeckStore
import dev.mobiledeck.data.CloseInfo
import dev.mobiledeck.data.ClientEvent
import dev.mobiledeck.data.ConnectResult
import dev.mobiledeck.data.DeckClient
import dev.mobiledeck.data.EMPTY_PAYLOAD
import dev.mobiledeck.data.Envelope
import dev.mobiledeck.data.ErrorCode
import dev.mobiledeck.data.HandshakeResult
import dev.mobiledeck.data.HelloPayload
import dev.mobiledeck.data.ClientInfo
import dev.mobiledeck.data.MsgType
import dev.mobiledeck.data.PROTOCOL_VERSION
import dev.mobiledeck.data.ProtocolError
import dev.mobiledeck.data.ProtocolJson
import dev.mobiledeck.data.Transport
import dev.mobiledeck.data.decodeFromJson
import dev.mobiledeck.data.encodeToJson
import dev.mobiledeck.data.newRequest
import dev.mobiledeck.data.parseManualHost
import dev.mobiledeck.data.payloadObject
import kotlinx.serialization.json.jsonPrimitive
import dev.mobiledeck.data.replyTo
import kotlinx.coroutines.async
import kotlinx.coroutines.delay
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.consumeAsFlow
import kotlinx.coroutines.yield
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * An in-memory [Transport] that lets a test drive both directions of the
 * protocol.
 *
 * It exists so the state machine can be exercised on the JVM: the parts of
 * `DeckClient` that matter — request correlation, the heartbeat, close-code
 * translation, unknown-type tolerance — are pure protocol behaviour and would be
 * painful and slow to test through a real socket.
 */
private class FakeTransport : Transport {

    // A synchronized list: the client appends from its own coroutine while the
    // test reads from the test thread, and a plain ArrayList throws
    // ConcurrentModificationException when those overlap.
    val sent: MutableList<String> = java.util.Collections.synchronizedList(mutableListOf())

    private val frames = Channel<String>(Channel.UNLIMITED)

    /** Set to make `send` report a dead socket. */
    var dead: Boolean = false

    var closedWith: Pair<Int, String>? = null

    override suspend fun connect(url: String, tlsFingerprint: String?): ConnectResult = ConnectResult.Open

    override fun send(text: String): Boolean {
        if (dead) return false
        sent += text
        return true
    }

    override val incoming: Flow<String> = frames.consumeAsFlow()

    /**
     * How the connection ended, as a real transport would report it.
     *
     * Settable so a test can reproduce a host close code; the default is null,
     * which is what a transport reports while the socket is still open.
     */
    override var closeInfo: CloseInfo? = null

    override fun close(code: Int, reason: String) {
        closedWith = code to reason
        if (closeInfo == null) closeInfo = CloseInfo(code, reason, byClient = true)
        frames.close()
    }

    /** Delivers a frame as if the host had sent it. */
    fun deliver(text: String) {
        frames.trySend(text)
    }

    /** The last envelope sent, decoded. */
    fun lastSent(): Envelope = ProtocolJson.decodeFromString(Envelope.serializer(), sent.last())

    /** Every sent envelope whose type matches. */
    fun sentOfType(type: String): List<Envelope> =
        sent.map { ProtocolJson.decodeFromString(Envelope.serializer(), it) }.filter { it.type == type }

}

private fun hello() = HelloPayload(
    deviceId = "android-test",
    token = "tok",
    client = ClientInfo(platform = "android", appVersion = "0.1.0"),
)

/** A minimal `welcome` frame, as the host would send it. */
private fun welcomeFrame(
    heartbeatMs: Int = 15_000,
    idleMs: Int = 45_000,
    features: String = "\"telemetry\"",
) = """
    {"v":1,"type":"welcome","ts":1,"payload":{
      "session_id":"s-1",
      "host":{"id":"h1","name":"cel-laptop","os":"windows","version":"0.1.0"},
      "device":{"id":"android-test","name":"Phone","scopes":["keyboard"]},
      "server_time":1,
      "heartbeat_interval_ms":$heartbeatMs,
      "idle_timeout_ms":$idleMs,
      "features":[$features],
      "profiles_revision":"sha256:x"}}
""".trimIndent()

/**
 * `DeckClient` against a fake transport.
 *
 * docs/ARCHITECTURE.md §4 requires `DeckClient` to be JVM-testable: it must not
 * touch Android UI classes. These tests are the proof, and they cover the parts
 * of the protocol a UI test cannot reach — a revoked token, an unknown message
 * type, a request whose reply never comes.
 */
class DeckClientTest {

    @Test
    fun `handshake sends hello first and completes on welcome`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)

        val handshake = async { client.handshake("ws://host/ws", hello()) }
        // PROTOCOL.md §2.2: `hello` is the first frame, before the host answers.
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        assertEquals(MsgType.HELLO, transport.lastSent().type)

        transport.deliver(welcomeFrame())

        val outcome = withTimeout(2_000) { handshake.await() }
        assertTrue("handshake must succeed", outcome is HandshakeResult.Ok)
        val welcome = (outcome as HandshakeResult.Ok).welcome
        assertEquals("h1", welcome.host.id)
        assertEquals(15_000, welcome.heartbeatIntervalMs)
        assertEquals(setOf("telemetry"), welcome.features.toSet())
        assertTrue("the negotiated feature must be readable", welcome.supports("telemetry"))
        assertFalse(welcome.supports("invented.feature"))

        // The hello body carries the documented field.
        val body = transport.sentOfType(MsgType.HELLO).single().payloadObject()
        assertEquals("android-test", body["device_id"]!!.jsonPrimitive.content)

        client.close(1000, "test")
        scope.cancel()
    }

    /**
     * The heartbeat interval comes from `welcome`, not from a compiled-in
     * guess (PROTOCOL.md §2.4).
     *
     * The test waits out a short negotiated interval rather than the 15 s
     * default, so a ping that appears is evidence the *negotiated* value was
     * used — a client that ignored `welcome` would still be waiting.
     */
    @Test
    fun `welcome supplies the heartbeat interval`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        // The protocol's floor for the interval is what the host advertises;
        // 1 s is the smallest value the client will honour.
        transport.deliver(welcomeFrame(heartbeatMs = 1_000, idleMs = 30_000))

        withTimeout(5_000) {
            while (transport.sentOfType(MsgType.PING).isEmpty()) delay(20)
        }

        val ping = transport.sentOfType(MsgType.PING).first()
        // The echo makes a pong attributable to a specific ping in a log.
        assertNotNull(ping.payloadObject()["echo"])

        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /** PROTOCOL.md §2.4: a host-initiated ping must be answered with a pong. */
    @Test
    fun `host ping is answered with a pong carrying the same echo`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)
        val events = mutableListOf<ClientEvent>()
        val collector = scope.launch { client.events.collect { events += it } }

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        transport.deliver(
            """{"v":1,"type":"ping","ts":1,"payload":{"echo":"abc123"}}""",
        )

        withTimeout(2_000) {
            while (transport.sentOfType(MsgType.PONG).isEmpty()) delay(10)
        }
        val pong = transport.sentOfType(MsgType.PONG).single()
        assertEquals("abc123", pong.payloadObject()["echo"]!!.toString().trim('"'))

        collector.cancel()
        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /** PROTOCOL.md §1.3: a reply is matched to its request by `reply_to`. */
    @Test
    fun `request is correlated by reply_to`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        // Wait for the handshake to finish before issuing a request: a request
        // sent before `welcome` would be answered by a host that has not yet
        // bound the session.
        withTimeout(2_000) { while (!job.isCompleted) yield() }

        val pending = scope.launch {
            val reply = client.request(MsgType.PROFILE_LIST, EMPTY_PAYLOAD)
            assertEquals(MsgType.PROFILE_LIST_RESULT, reply.type)
        }

        withTimeout(2_000) {
            while (transport.sentOfType(MsgType.PROFILE_LIST).isEmpty()) delay(5)
        }
        val request = transport.sentOfType(MsgType.PROFILE_LIST).last()
        transport.deliver(
            """{"v":1,"id":"r1","reply_to":"${request.id}","type":"profile.list.result","ts":1,
                "payload":{"profiles":[],"active":null}}""",
        )

        pending.join()
        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /**
     * PROTOCOL.md §8: an `error` reply is surfaced as [ProtocolError] rather
     * than as an empty success, which is what would otherwise let a refused
     * action look like it worked.
     */
    @Test
    fun `error reply fails the request`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        // Wait for the handshake to finish before issuing a request: a request
        // sent before `welcome` would be answered by a host that has not yet
        // bound the session.
        withTimeout(2_000) { while (!job.isCompleted) yield() }

        var failure: ProtocolError? = null
        val pending = scope.launch {
            try {
                client.request(MsgType.PROFILE_GET, encodeToJson(dev.mobiledeck.data.ProfileGetPayload("nope")))
            } catch (e: ProtocolError) {
                failure = e
            }
        }

        withTimeout(2_000) {
            while (transport.sentOfType(MsgType.PROFILE_GET).isEmpty()) delay(5)
        }
        val request = transport.sentOfType(MsgType.PROFILE_GET).last()
        transport.deliver(
            """{"v":1,"id":"r1","reply_to":"${request.id}","type":"error","ts":1,
                "payload":{"code":"not_found","message":"unknown profile"}}""",
        )

        pending.join()
        assertNotNull("a refusal must not look like success", failure)
        assertEquals(ErrorCode.NOT_FOUND, failure!!.payload.code)
        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /**
     * PROTOCOL.md §11: an unknown message type must be reported, not fatal.
     * Tearing the session down over one would make additive evolution a
     * breaking change, which is the opposite of the documented policy.
     */
    @Test
    fun `unknown type is reported and the session survives`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)
        val events = mutableListOf<ClientEvent>()
        val collector = scope.launch { client.events.collect { events += it } }

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        transport.deliver("""{"v":1,"type":"event.invented.later","ts":1,"payload":{}}""")

        withTimeout(2_000) {
            while (events.none { it is ClientEvent.Unknown }) delay(5)
        }

        assertEquals(
            "event.invented.later",
            events.filterIsInstance<ClientEvent.Unknown>().single().type,
        )
        assertFalse("an unknown type must not close the session", client.closed)

        collector.cancel()
        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /**
     * PROTOCOL.md §1.1/§1.2: a peer MUST reject an envelope whose `v` it does
     * not implement with an `error` reply and MUST NOT close the connection.
     */
    @Test
    fun `unsupported protocol version is answered with an error and keeps the socket`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        transport.deliver("""{"v":99,"id":"x1","type":"welcome","ts":1,"payload":{}}""")

        withTimeout(2_000) {
            while (transport.sentOfType(MsgType.ERROR).isEmpty()) delay(5)
        }
        val error = transport.sentOfType(MsgType.ERROR).single()
        assertEquals("x1", error.replyTo)
        assertFalse("the connection must survive a version mismatch", client.closed)

        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /**
     * A `pong` is liveness evidence and nothing else; it must not be mistaken
     * for an event the UI has to render.
     */
    @Test
    fun `pong is consumed silently`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)
        val events = mutableListOf<ClientEvent>()
        val collector = scope.launch { client.events.collect { events += it } }

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        transport.deliver("""{"v":1,"type":"pong","ts":1,"payload":{"echo":"x"}}""")
        delay(50)

        assertTrue("a pong is not an event", events.none { it is ClientEvent.Unknown })

        collector.cancel()
        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /**
     * A frame that cannot be parsed at all must be reported rather than
     * crashing the reader, because the reader dying is indistinguishable from
     * the host dying and would trigger a pointless reconnect.
     */
    @Test
    fun `malformed frame is reported and does not kill the reader`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)
        val events = mutableListOf<ClientEvent>()
        val collector = scope.launch { client.events.collect { events += it } }

        val job = scope.launch { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.deliver(welcomeFrame())
        transport.deliver("{not json at all")

        withTimeout(2_000) {
            while (events.none { it is ClientEvent.Malformed }) delay(5)
        }
        assertFalse(client.closed)

        collector.cancel()
        job.cancel()
        client.close(1000, "test")
        scope.cancel()
    }

    /** A send on a closed transport reports failure instead of throwing. */
    @Test
    fun `send reports failure on a dead socket`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)
        transport.dead = true

        assertFalse(client.send(MsgType.PING))
    }

    /**
     * A host close code must reach the caller unchanged.
     *
     * This is a regression test for a real bug: the reader used to end the
     * session with a hardcoded 1000, which threw away the host's code. A revoked
     * device (4403) then looked like an ordinary disconnect, so the store would
     * keep the dead token and retry forever instead of sending the user back to
     * pairing.
     */
    @Test
    fun `the host close code survives to the caller`() = runBlocking {
        for (code in listOf(4401, 4403, 4408, 4429)) {
            val transport = FakeTransport()
            val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
            val client = DeckClient(transport, scope)
            val events = mutableListOf<ClientEvent>()
            val collector = scope.launch { client.events.collect { events += it } }

            val job = scope.launch { client.handshake("ws://host/ws", hello()) }
            withTimeout(1_000) {
                while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
            }
            transport.deliver(welcomeFrame())

            // The host closes with its own code, as the transport would report.
            transport.closeInfo = CloseInfo(code, "host said so")
            transport.close(code, "host said so")

            withTimeout(2_000) {
                while (events.none { it is ClientEvent.Closed }) delay(5)
            }
            val closed = events.filterIsInstance<ClientEvent.Closed>().single()
            assertEquals("close code $code was lost", code, closed.info.code)

            collector.cancel()
            job.cancel()
            scope.cancel()
        }
    }

    /**
     * A close during the handshake must be reported as a refusal carrying the
     * host's code, not as a generic failure: that code is what tells the user
     * their device was revoked.
     */
    @Test
    fun `a close during the handshake is a refusal with the host code`() = runBlocking {
        val transport = FakeTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val client = DeckClient(transport, scope)

        val job = async { client.handshake("ws://host/ws", hello()) }
        withTimeout(1_000) {
            while (transport.sentOfType(MsgType.HELLO).isEmpty()) yield()
        }
        transport.closeInfo = CloseInfo(4401, "token revoked")
        transport.close(4401, "token revoked")

        val result = withTimeout(2_000) { job.await() }
        assertTrue("expected a refusal, got $result", result is HandshakeResult.Refused)
        assertEquals(4401, (result as HandshakeResult.Refused).info.code)

        job.cancel()
        scope.cancel()
    }

}

/**
 * The manual `host:port` parser (PROTOCOL.md §4.2).
 *
 * It is the one place a user's typing becomes a URL, so the awkward cases —
 * a bare host, an explicit port, an IPv6 literal — are covered rather than
 * assumed.
 */
class ManualHostTest {

    @Test
    fun `bare host takes the default port`() {
        val host = parseManualHost("192.168.1.10", 8765)!!
        assertEquals("192.168.1.10", host.host)
        assertEquals(8765, host.port)
        assertEquals("192.168.1.10:8765", host.authority)
        assertTrue(host.manual)
    }

    @Test
    fun `explicit port is honoured`() {
        val host = parseManualHost("cel-laptop:9000", 8765)!!
        assertEquals("cel-laptop", host.host)
        assertEquals(9000, host.port)
    }

    @Test
    fun `a scheme and a trailing slash are stripped`() {
        assertEquals(8765, parseManualHost("http://host/", 8765)!!.port)
        assertEquals(8765, parseManualHost("https://host", 8765)!!.port)
    }

    /** An IPv6 literal contains colons, so only the bracket's colon is the port. */
    @Test
    fun `ipv6 literal is bracketed in the authority`() {
        val host = parseManualHost("[fe80::1]:8765", 8765)!!
        assertEquals("fe80::1", host.host)
        assertEquals(8765, host.port)
        assertEquals("[fe80::1]:8765", host.authority)
    }

    @Test
    fun `blank and unusable input is rejected`() {
        assertNull(parseManualHost("", 8765))
        assertNull(parseManualHost("   ", 8765))
        assertNull(parseManualHost("host:not-a-port", 8765))
        assertNull(parseManualHost("host:99999", 8765))
        assertNull(parseManualHost("a:b:c", 8765))
    }

    /**
     * A host that advertises a fingerprint is speaking TLS, and the base URL
     * must say so: probing an https host over http would fail with a confusing
     * "connection reset" instead of using the pin.
     */
    @Test
    fun `fingerprint selects https`() {
        val plain = dev.mobiledeck.data.DiscoveredHost("k", "n", "10.0.0.1", 8765)
        assertEquals("http://10.0.0.1:8765", plain.baseUrl())

        val pinned = plain.copy(fingerprint = "ab:cd")
        assertEquals("https://10.0.0.1:8765", pinned.baseUrl())
    }
}

/** Guards against a silent protocol-version drift in the client. */
class ProtocolVersionTest {

    @Test
    fun `client speaks v1`() {
        assertEquals(1, PROTOCOL_VERSION)
        assertEquals(1, newRequest("ping").v)
        assertEquals(1, replyTo(newRequest("ping"), "pong").v)
    }

    /** Every documented close code must be classified, or its policy is lost. */
    @Test
    fun `every documented close code is classified`() {
        listOf(1000, 1013, 4400, 4401, 4403, 4408, 4429).forEach { code ->
            assertNotNull("close code $code must be classified", CloseCode.of(code))
        }
    }

    /** An unclassified close reports a reason rather than an empty string. */
    @Test
    fun `close info explains a known code`() {
        val info = CloseInfo(4403, "revoked")
        assertTrue(info.known!!.explanation.contains("revoked"))
    }
}

/**
 * The reconnection schedule.
 *
 * It is asserted because it is a promise about how hard the client is allowed to
 * hit a host: a bug that made the cap unbounded would turn a host restart into a
 * self-inflicted denial of service, and that is not visible by reading the call
 * site.
 */
class BackoffTest {

    /** The schedule doubles from the 500 ms floor. */
    @Test
    fun `the backoff window doubles from the floor`() {
        assertEquals(500L, DeckStore.backoffWindowMs(0))
        assertEquals(1_000L, DeckStore.backoffWindowMs(1))
        assertEquals(2_000L, DeckStore.backoffWindowMs(2))
        assertEquals(4_000L, DeckStore.backoffWindowMs(3))
        assertEquals(8_000L, DeckStore.backoffWindowMs(4))
        assertEquals(16_000L, DeckStore.backoffWindowMs(5))
    }

    /** The window is capped at 30 s and never grows past it. */
    @Test
    fun `the backoff window is capped at 30 seconds`() {
        for (attempt in 0..64) {
            val window = DeckStore.backoffWindowMs(attempt)
            assertTrue("attempt $attempt produced a ${window}ms window", window in 500..30_000)
        }
        assertEquals(30_000L, DeckStore.backoffWindowMs(6))
        assertEquals(30_000L, DeckStore.backoffWindowMs(30))
    }

    /**
     * An absurd attempt count must not overflow into a negative window: a
     * negative delay would make `delay` throw or return immediately, and the
     * client would spin on a host that is down.
     */
    @Test
    fun `an absurd attempt count still yields a sane window`() {
        listOf(17, 63, 64, 1_000, Int.MAX_VALUE).forEach { attempt ->
            val window = DeckStore.backoffWindowMs(attempt)
            assertTrue("attempt $attempt produced a ${window}ms window", window in 500..30_000)
        }
    }

    /** Every jittered delay stays inside its window, including zero. */
    @Test
    fun `jitter stays within the window`() {
        val random = kotlin.random.Random(7)
        for (attempt in 0..40) {
            val window = DeckStore.backoffWindowMs(attempt)
            repeat(50) {
                val delay = DeckStore.backoffDelayMs(attempt, random)
                assertTrue("delay $delay outside [0, $window]", delay in 0..window)
            }
        }
    }

    /**
     * Full jitter must actually jitter. If every draw returned the window the
     * client would be a thundering herd after a host restart, which is the
     * failure the jitter exists to prevent.
     */
    @Test
    fun `full jitter produces a spread of delays`() {
        val random = kotlin.random.Random(11)
        val draws = (1..200).map { DeckStore.backoffDelayMs(5, random) }
        assertTrue("jitter must produce more than one value", draws.toSet().size > 20)
        assertTrue("jitter must sometimes be far below the window", draws.min() < 16_000 / 2)
    }
}
