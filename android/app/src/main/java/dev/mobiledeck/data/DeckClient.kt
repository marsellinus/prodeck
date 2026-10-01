package dev.mobiledeck.data

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.decodeFromJsonElement

/**
 * A structured error the host sent in reply to a request (PROTOCOL.md §8).
 *
 * It is an exception rather than a return value because every caller in the
 * store would otherwise have to check, and forgetting the check would turn a
 * refusal into a silent no-op — the exact failure mode the host's
 * `unsupported`/`forbidden` codes exist to prevent.
 */
class ProtocolError(
    val payload: ErrorPayload,
    /** The request type that was refused, for the UI message. */
    val requestType: String,
) : Exception("${payload.code}: ${payload.message}")

/** The outcome of the handshake. */
sealed interface HandshakeResult {
    data class Ok(val welcome: WelcomePayload) : HandshakeResult

    /** The host refused the handshake, e.g. a revoked token. */
    data class Refused(val info: CloseInfo) : HandshakeResult

    /** The socket never came up. */
    data class Failed(val cause: Throwable) : HandshakeResult
}

/** Everything the host can push at a client. */
sealed interface ClientEvent {
    data class Error(val payload: ErrorPayload, val replyTo: String?) : ClientEvent

    data class ButtonState(val payload: EventButtonStatePayload) : ClientEvent

    data class Telemetry(val payload: EventTelemetryPayload) : ClientEvent

    data class ProfileChanged(val payload: EventProfileChangedPayload) : ClientEvent

    /**
     * A terminal result for a long-running action.
     *
     * The host reuses the `action.result` body shape for
     * `event.action.finished` (PROTOCOL.md §6.3), so it is decoded with the
     * same class rather than a near-identical duplicate that could drift.
     */
    data class ActionFinished(val payload: ActionResultPayload) : ClientEvent

    /** A result for a request the client sent fire-and-forget, e.g. a press. */
    data class ActionResult(val payload: ActionResultPayload) : ClientEvent

    /** A host-initiated ping, already answered by the time this is emitted. */
    data class Ping(val echo: String) : ClientEvent

    /**
     * A frame with a type this build does not know.
     *
     * PROTOCOL.md §11 makes unknown *message* types legal on the wire, so this
     * is reported rather than treated as an error; the client answers nothing
     * because an unsolicited event has no `id` to reply to.
     */
    data class Unknown(val type: String) : ClientEvent

    /** A frame that could not be parsed at all. */
    data class Malformed(val reason: String) : ClientEvent

    /** The connection ended. Terminal for this client instance. */
    data class Closed(val info: CloseInfo) : ClientEvent
}

/**
 * The protocol state machine (docs/ARCHITECTURE.md §4).
 *
 * Responsibilities, and nothing else:
 *
 *  * the `hello`/`welcome` handshake, including the 10 s first-frame deadline;
 *  * correlating replies to requests by `id` through [CompletableDeferred];
 *  * fanning unsolicited messages out as [events];
 *  * the heartbeat ping loop and the client-side idle watchdog;
 *  * translating close codes into a [CloseInfo] the store can act on.
 *
 * It deliberately holds no retry policy, no cache and no Android types: the
 * store owns reconnection, because backoff and cache preservation are
 * application concerns, and keeping them out of here is what makes this class
 * testable on the JVM.
 */
class DeckClient(
    private val transport: Transport,
    private val scope: CoroutineScope,
) {

    private val pending = HashMap<String, CompletableDeferred<Envelope>>()

    private val _events = MutableSharedFlow<ClientEvent>(
        replay = 0,
        extraBufferCapacity = 256,
        onBufferOverflow = BufferOverflow.DROP_OLDEST,
    )

    /** Unsolicited host messages. Hot: a late collector misses nothing that matters. */
    val events: SharedFlow<ClientEvent> = _events.asSharedFlow()

    private var reader: Job? = null
    private var heartbeat: Job? = null

    /**
     * The negotiated timings, set by [handshake] from the `welcome` payload so
     * the client never guesses a value the host already told it (PROTOCOL.md
     * §2.4).
     */
    private var heartbeatIntervalMs = 15_000
    private var idleTimeoutMs = 45_000

    @Volatile
    private var lastInboundAt = 0L

    /** True once the connection is terminally closed, so callers can stop. */
    @Volatile
    var closed: Boolean = false
        private set

    private var welcomeDeferred: CompletableDeferred<WelcomePayload>? = null

    /**
     * Connects and performs the handshake.
     *
     * The reader starts before `hello` is sent: the host answers `welcome` as an
     * unsolicited frame (it is not a reply to anything), so a client that
     * started reading afterwards would race the response.
     */
    suspend fun handshake(
        url: String,
        hello: HelloPayload,
        tlsFingerprint: String? = null,
    ): HandshakeResult {
        lastInboundAt = nowMs()
        when (val r = transport.connect(url, tlsFingerprint)) {
            is ConnectResult.Failed -> return HandshakeResult.Failed(r.cause)
            is ConnectResult.Closed -> return HandshakeResult.Refused(r.info)
            ConnectResult.Open -> Unit
        }

        val welcome = CompletableDeferred<WelcomePayload>()
        welcomeDeferred = welcome
        reader = scope.launch { pump() }

        // `newRequest`, not `request`: inside this class the member `request`
        // sends a frame and *waits for a correlated reply*, which is the
        // opposite of what a handshake needs. `welcome` is an unsolicited frame
        // with no `reply_to`, so routing `hello` through it would deadlock the
        // handshake until the request timeout.
        val helloFrame = ProtocolJson.encodeToString(
            Envelope.serializer(),
            newRequest(MsgType.HELLO, payload = encodeToJson(hello)),
        )
        if (!transport.send(helloFrame)) {
            close(CloseCode.MALFORMED.code, "could not send hello")
            return HandshakeResult.Failed(IllegalStateException("transport refused the hello frame"))
        }

        return try {
            // PROTOCOL.md §2.2: the host closes with 4401 if no frame arrives in
            // 10 s. Waiting a little longer than the host's own deadline lets the
            // close code win the race and gives the user a real reason.
            val w = withTimeout(HELLO_DEADLINE_MS + 2_000) { welcome.await() }
            heartbeatIntervalMs = w.heartbeatIntervalMs.coerceAtLeast(1_000)
            idleTimeoutMs = w.idleTimeoutMs.coerceAtLeast(heartbeatIntervalMs)
            startHeartbeat()
            HandshakeResult.Ok(w)
        } catch (e: TimeoutCancellationException) {
            close(CloseCode.MALFORMED.code, "no welcome within the handshake deadline")
            HandshakeResult.Failed(e)
        }
    }

    /**
     * Sends a request and waits for its reply.
     *
     * Throws [ProtocolError] when the host answers `error`, which is the
     * protocol's only way to say "I understood and refused".
     */
    suspend fun request(
        type: String,
        payload: JsonElement = EMPTY_PAYLOAD,
        timeoutMs: Long = REQUEST_TIMEOUT_MS,
    ): Envelope {
        val id = newRequestId()
        val deferred = CompletableDeferred<Envelope>()
        synchronized(pending) { pending[id] = deferred }
        try {
            val frame = ProtocolJson.encodeToString(
                Envelope.serializer(),
                newRequest(type, id = id, payload = payload),
            )
            if (!transport.send(frame)) {
                throw ProtocolError(
                    ErrorPayload(ErrorCode.UNAUTHENTICATED, "connection is closed"),
                    type,
                )
            }
            return withTimeout(timeoutMs) { deferred.await() }
        } catch (e: TimeoutCancellationException) {
            throw ProtocolError(
                ErrorPayload(ErrorCode.INTERNAL, "host did not answer $type in ${timeoutMs}ms"),
                type,
            )
        } finally {
            synchronized(pending) { pending.remove(id) }
        }
    }

    /**
     * Sends a message without waiting for a reply.
     *
     * Used for `button.press`, where the answer is an `action.result` event that
     * may legitimately never arrive (a fire-and-forget action), and for
     * `pong`, where blocking the frame loop on the host would deadlock.
     */
    fun send(type: String, payload: JsonElement = EMPTY_PAYLOAD): Boolean {
        if (closed) return false
        val frame = ProtocolJson.encodeToString(
            Envelope.serializer(),
            newRequest(type, payload = payload),
        )
        return transport.send(frame)
    }

    /** Decodes a payload body, tolerating unknown keys (PROTOCOL.md §11). */
    inline fun <reified T> decode(body: JsonElement?): T =
        ProtocolJson.decodeFromJsonElement(
            kotlinx.serialization.serializer<T>(),
            body ?: EMPTY_PAYLOAD,
        )

    /** Ends the session. Idempotent. */
    fun close(code: Int, reason: String) {
        if (closed) return
        closed = true
        heartbeat?.cancel()
        reader?.cancel()
        welcomeDeferred?.let { if (!it.isCompleted) it.cancel() }
        transport.close(code, reason)
    }

    // -- internals ----------------------------------------------------------

    /**
     * The heartbeat loop.
     *
     * The first `delay` comes *before* the first ping, which matters for more
     * than tidiness: the host has just answered `welcome`, so pinging it
     * immediately would be pure noise, and a caller that awaits the handshake
     * gets to observe its result before any further traffic is generated.
     */
    private fun startHeartbeat() {
        heartbeat?.cancel()
        heartbeat = scope.launch {
            while (isActive) {
                delay(heartbeatIntervalMs.toLong())
                if (closed) return@launch
                // The echo is carried back in `pong`, which makes a pong
                // attributable to a specific ping in a log.
                send(MsgType.PING, encodeToJson(PingPayload(echo = newRequestId())))

                // A roamed phone leaves a TCP connection that is open but dead:
                // no close frame ever arrives, so liveness has to be inferred
                // from silence. This mirrors the host's own 4408 rule and is the
                // only thing that recovers from a network that vanished.
                val silentFor = nowMs() - lastInboundAt
                if (silentFor > idleTimeoutMs) {
                    terminate(CloseInfo(CloseCode.IDLE_TIMEOUT.code, "no frames received for ${silentFor}ms"))
                    return@launch
                }
            }
        }
    }

    private suspend fun pump() {
        try {
            transport.incoming.collect { text ->
                lastInboundAt = nowMs()
                dispatch(text)
            }
        } catch (_: Throwable) {
            // The flow fails when the socket dies. `onClosed`/`onFailure` in the
            // transport already produced the authoritative close reason, so the
            // exception itself carries nothing extra.
        }
        // Reaching here means the flow completed: the socket is gone.
        if (!closed) {
            terminate(CloseInfo(CloseCode.NORMAL.code, "connection ended"))
        }
    }
    private suspend fun dispatch(text: String) {
        val env = try {
            ProtocolJson.decodeFromString(Envelope.serializer(), text)
        } catch (e: Exception) {
            _events.emit(ClientEvent.Malformed(e.message ?: "unparseable frame"))
            return
        }

        // PROTOCOL.md §1.2: a peer MUST reject an envelope whose `v` it does not
        // implement with an `error` reply and MUST NOT close the connection.
        if (env.v != PROTOCOL_VERSION) {
            val body = encodeToJson(
                ErrorPayload(
                    ErrorCode.INVALID_ARGUMENT,
                    "unsupported protocol version ${env.v}; this client speaks $PROTOCOL_VERSION",
                ),
            )
            transport.send(
                ProtocolJson.encodeToString(
                    Envelope.serializer(),
                    replyTo(env, MsgType.ERROR, body),
                ),
            )
            _events.emit(
                ClientEvent.Error(
                    ErrorPayload(ErrorCode.INVALID_ARGUMENT, "host sent protocol v${env.v}"),
                    env.id,
                ),
            )
            return
        }

        // A correlated reply wins over event fan-out: the caller that is
        // awaiting it owns the error surface for that request.
        val replyToId = env.replyTo
        if (replyToId != null) {
            val deferred = synchronized(pending) { pending.remove(replyToId) }
            if (deferred != null) {
                if (env.type == MsgType.ERROR) {
                    val err = ProtocolJson.decodeFromJsonElement(ErrorPayload.serializer(), env.payloadObject())
                    deferred.completeExceptionally(ProtocolError(err, "request"))
                } else {
                    deferred.complete(env)
                }
                return
            }
        }

        when (env.type) {
            MsgType.WELCOME -> {
                val w = ProtocolJson.decodeFromJsonElement(WelcomePayload.serializer(), env.payloadObject())
                welcomeDeferred?.complete(w)
            }

            MsgType.PING -> {
                val p = ProtocolJson.decodeFromJsonElement(PingPayload.serializer(), env.payloadObject())
                // PROTOCOL.md §2.4: a host MAY ping and the client MUST answer.
                send(MsgType.PONG, encodeToJson(PongPayload(echo = p.echo)))
                _events.emit(ClientEvent.Ping(p.echo))
            }

            MsgType.PONG -> Unit // Liveness was already recorded by the frame arriving.

            MsgType.ERROR -> {
                val err = ProtocolJson.decodeFromJsonElement(ErrorPayload.serializer(), env.payloadObject())
                _events.emit(ClientEvent.Error(err, env.replyTo))
            }

            MsgType.EVENT_BUTTON_STATE -> _events.emit(
                ClientEvent.ButtonState(
                    ProtocolJson.decodeFromJsonElement(EventButtonStatePayload.serializer(), env.payloadObject()),
                ),
            )

            MsgType.EVENT_TELEMETRY -> _events.emit(
                ClientEvent.Telemetry(
                    ProtocolJson.decodeFromJsonElement(EventTelemetryPayload.serializer(), env.payloadObject()),
                ),
            )

            MsgType.EVENT_PROFILE_CHANGED -> _events.emit(
                ClientEvent.ProfileChanged(
                    ProtocolJson.decodeFromJsonElement(EventProfileChangedPayload.serializer(), env.payloadObject()),
                ),
            )

            MsgType.EVENT_ACTION_FINISHED -> _events.emit(
                ClientEvent.ActionFinished(
                    ProtocolJson.decodeFromJsonElement(ActionResultPayload.serializer(), env.payloadObject()),
                ),
            )

            MsgType.ACTION_RESULT -> _events.emit(
                ClientEvent.ActionResult(
                    ProtocolJson.decodeFromJsonElement(ActionResultPayload.serializer(), env.payloadObject()),
                ),
            )

            else -> _events.emit(ClientEvent.Unknown(env.type))
        }
    }

    private fun terminate(info: CloseInfo) {
        if (closed) return
        closed = true
        heartbeat?.cancel()
        welcomeDeferred?.let { if (!it.isCompleted) it.completeExceptionally(ConnectionLost(info)) }
        // Fail every in-flight request immediately: a caller awaiting a reply
        // that can never arrive must not sit until its own timeout.
        val orphans = synchronized(pending) {
            val snapshot = pending.values.toList()
            pending.clear()
            snapshot
        }
        orphans.forEach { it.completeExceptionally(ConnectionLost(info)) }
        _events.tryEmit(ClientEvent.Closed(info))
        transport.close(info.code, info.reason)
    }

    companion object {
        /** PROTOCOL.md §2.2: the host gives the client 10 s to send `hello`. */
        const val HELLO_DEADLINE_MS = 10_000L

        /** How long a normal request may take before it is treated as lost. */
        const val REQUEST_TIMEOUT_MS = 20_000L
    }
}

/** The connection ended while a reply was still outstanding. */
class ConnectionLost(val info: CloseInfo) :
    Exception("connection closed: ${info.code} ${info.reason}")
