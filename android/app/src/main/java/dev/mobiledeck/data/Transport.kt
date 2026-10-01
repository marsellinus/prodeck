package dev.mobiledeck.data

import kotlinx.coroutines.CancellableContinuation
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.consumeAsFlow
import kotlinx.coroutines.suspendCancellableCoroutine
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/** Why a connection ended, in terms the state machine can act on. */
data class CloseInfo(
    val code: Int,
    val reason: String,
    /** True when the client itself initiated the close. */
    val byClient: Boolean = false,
) {
    /** The known close code, or null for a code this build does not classify. */
    val known: CloseCode? get() = CloseCode.of(code)
}

/** The result of a successful `connect`. */
sealed interface ConnectResult {
    /** The socket is open; `hello` may now be sent. */
    object Open : ConnectResult

    /** The peer closed during the handshake; carry its code to the caller. */
    data class Closed(val info: CloseInfo) : ConnectResult

    /** The connection failed before it was established. */
    data class Failed(val cause: Throwable) : ConnectResult
}

/**
 * A duplex text-frame channel to a host.
 *
 * The interface exists so `DeckClient` is testable against an in-memory pipe
 * without a socket, and so a future USB transport (ADR-0008) is a new
 * implementation rather than a rewrite of the state machine.
 */
interface Transport {
    /** Opens the socket. Returns when the handshake completes or fails. */
    suspend fun connect(url: String, tlsFingerprint: String?): ConnectResult

    /**
     * Sends one text frame.
     *
     * Returns false when the socket is gone, which is the caller's signal to
     * stop trusting the connection rather than a reason to throw.
     */
    fun send(text: String): Boolean

    /** Frames received from the host, in arrival order. Completes on close. */
    val incoming: Flow<String>

    /**
     * How the connection ended, once it has.
     *
     * The transport records this because only it sees the WebSocket close frame,
     * and the close code is what decides whether the client reconnects, wipes
     * its token, or stops for good (PROTOCOL.md §2.5). A state machine that
     * invents its own code cannot tell a revocation from a dropped network.
     *
     * Null until the connection has ended.
     */
    val closeInfo: CloseInfo?

    /** Closes the socket, if it is still open. Idempotent. */
    fun close(code: Int, reason: String)
}

/**
 * OkHttp WebSocket transport.
 *
 * OkHttp's listener callbacks arrive on its own dispatcher thread, so they are
 * bridged into a coroutine [Channel]. The channel is buffered and
 * drop-oldest: a stalled consumer must not be able to grow an unbounded queue
 * of frames, and the newest telemetry sample is more useful than the oldest.
 */
class WsTransport(
    private val client: OkHttpClient = defaultClient(),
) : Transport {

    private var socket: WebSocket? = null
    private var handshake: CancellableContinuation<ConnectResult>? = null

    /**
     * How the connection ended.
     *
     * OkHttp reports the host's close frame in `onClosing`/`onClosed`, and that
     * code is the only signal that separates a revocation from a dropped
     * network. It is kept here so `DeckClient` can act on the real reason
     * instead of guessing one.
     */
    @Volatile
    override var closeInfo: CloseInfo? = null
        private set

    /**
     * One channel per connection. A fresh transport is created per attempt by
     * the store, so the channel's lifetime is the connection's lifetime and no
     * frame can leak from one session into the next.
     */
    private val frames = Channel<String>(capacity = 256, onBufferOverflow = BufferOverflow.DROP_OLDEST)

    override val incoming: Flow<String> = frames.consumeAsFlow()

    override suspend fun connect(url: String, tlsFingerprint: String?): ConnectResult {
        val request = Request.Builder()
            .url(url)
            .header("Sec-WebSocket-Protocol", "mobiledeck.v1")
            .build()

        return suspendCancellableCoroutine { cont ->
            handshake = cont
            val ws = client.newWebSocket(request, Listener())
            socket = ws
            cont.invokeOnCancellation {
                handshake = null
                ws.cancel()
            }
        }
    }

    override fun send(text: String): Boolean = socket?.send(text) ?: false

    override fun close(code: Int, reason: String) {
        val ws = socket
        socket = null
        // 1000 is "normal closure"; anything the caller passes must be a valid
        // WebSocket code or OkHttp throws, so out-of-range values degrade to
        // the normal code rather than crashing the disconnect path.
        val safeCode = if (code in 1000..4999) code else 1000
        ws?.close(safeCode, reason.take(120))
    }

    private fun settle(result: ConnectResult) {
        val cont = handshake
        handshake = null
        if (cont != null && cont.isActive) cont.resume(result)
    }

    private inner class Listener : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            settle(ConnectResult.Open)
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            // `trySend` never blocks the OkHttp thread; a full buffer drops the
            // oldest frame, which is the behaviour the channel was built for.
            frames.trySend(text)
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            // Record the host's code before acknowledging: `onClosing` is where
            // the peer's reason is delivered, and `onClosed` that follows may
            // report the code this side echoed back.
            if (closeInfo == null) closeInfo = CloseInfo(code, reason)
            webSocket.close(1000, null)
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            socket = null
            if (closeInfo == null) closeInfo = CloseInfo(code, reason)
            settle(ConnectResult.Closed(closeInfo!!))
            frames.close()
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            socket = null
            // A transport failure has no peer close code, so it is reported as an
            // abnormal closure rather than left null: the caller must still learn
            // that the connection ended.
            if (closeInfo == null) closeInfo = CloseInfo(1006, t.message ?: "connection failed")
            settle(ConnectResult.Failed(t))
            frames.close(IOException("websocket failed", t))
        }
    }

    companion object {
        /**
         * The shared OkHttp client.
         *
         * No read timeout: a WebSocket is idle between heartbeats by design and
         * a read timeout would tear it down every `heartbeat_interval_ms`.
         * Liveness is enforced by the protocol's ping/pong instead, which is
         * the mechanism that can actually distinguish "idle" from "dead".
         */
        fun defaultClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .writeTimeout(10, TimeUnit.SECONDS)
            .pingInterval(0, TimeUnit.MILLISECONDS)
            .retryOnConnectionFailure(false)
            .build()
    }
}
