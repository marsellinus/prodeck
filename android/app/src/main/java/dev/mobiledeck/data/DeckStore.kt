package dev.mobiledeck.data

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Job
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.jsonPrimitive
import kotlin.math.min
import kotlin.random.Random

/** Where the connection currently is (docs/ARCHITECTURE.md §4). */
enum class ConnectionState {
    Disconnected,
    Connecting,

    /** Probing a host and waiting for a PIN. */
    Pairing,
    Connected,

    /** Was connected, is retrying with backoff; the cached grid stays visible. */
    Reconnecting,
}

/** The connect screen's view of a host that is being paired with. */
data class PairingUiState(
    val host: DiscoveredHost,
    val hostName: String = "",
    val fingerprint: String? = null,
    val pairingOpen: Boolean = false,
    val expiresInS: Int = 0,
    /** True once the probe succeeded and a PIN can be entered. */
    val ready: Boolean = false,
    /**
     * True while a pairing request is in flight.
     *
     * This is deliberately separate from [DeckUiState.connection]: the screen
     * sits in [ConnectionState.Pairing] for as long as it is waiting for a PIN,
     * so deriving "busy" from that state left the PIN field and the Pair button
     * disabled for the whole life of the screen — the user could read the
     * instruction and never act on it.
     */
    val submitting: Boolean = false,
)

/**
 * Everything the UI renders.
 *
 * One immutable value so a recomposition can never observe a half-updated
 * combination of connection, profile and page — which is what makes the
 * "cached grid under a reconnecting banner" state expressible at all.
 */
data class DeckUiState(
    val connection: ConnectionState = ConnectionState.Disconnected,
    /** Non-fatal status, e.g. the reason for the current reconnect. */
    val status: String? = null,
    /** A failure the user must see and dismiss. */
    val error: String? = null,

    val hostId: String = "",
    val hostName: String = "",
    val hostOs: String = "",
    val deviceId: String = "",
    val scopes: Set<String> = emptySet(),
    val features: Set<String> = emptySet(),

    val profiles: List<ProfileSummary> = emptyList(),
    val activeProfileId: String = "",

    /** The active profile document, live or from cache. */
    val profile: Profile? = null,

    /** True when [profile] came from disk because the host is unreachable. */
    val profileIsCached: Boolean = false,

    val currentPageId: String = "",
    /** Page ids from the root down to the current page, for the breadcrumb. */
    val breadcrumbs: List<String> = emptyList(),

    /** Visual overrides pushed by `event.button.state`, keyed `profile/page/button`. */
    val buttonStates: Map<String, ButtonStateValue> = emptyMap(),

    /** Latest `event.telemetry` values; a missing metric renders as `--`. */
    val telemetry: Map<String, Double> = emptyMap(),

    val hosts: List<DiscoveredHost> = emptyList(),
    val pairing: PairingUiState? = null,
    val scanning: Boolean = false,
) {
    /** Whether intents that talk to the host should be offered. */
    val interactive: Boolean get() = connection == ConnectionState.Connected

    /** The page currently shown, resolved against the loaded document. */
    val page: Page? get() = profile?.page(currentPageId) ?: profile?.rootPageOrFirst()

    /** Effective grid for the current page (never hardcoded — ARCHITECTURE.md §4). */
    val grid: Grid get() = (profile?.gridFor(page) ?: Grid()).sanitized()

    val breadcrumbNames: List<String>
        get() = breadcrumbs.mapNotNull { id -> profile?.page(id)?.name ?: id.takeIf { it.isNotBlank() } }

    /** Whether this device holds a scope the host enforces per action. */
    fun hasScope(scope: String): Boolean = scopes.contains(scope)
}

/**
 * The single source of truth for the UI (docs/ARCHITECTURE.md §4).
 *
 * It owns four things and nothing else:
 *
 *  1. **The session loop** — connect, handshake, run, and reconnect with
 *     exponential backoff. The loop lives here rather than in `DeckClient`
 *     because backoff, cache preservation and token invalidation are
 *     application policy, and a transport-level retry would hide them from the
 *     UI that has to explain them.
 *  2. **Reconciliation** — turning protocol replies and events into the profile
 *     document, the page stack and the per-button overrides the UI renders.
 *  3. **Intents** — `press`, `release`, `navigate`, `back`, `switchProfile`,
 *     `reconnect`, `pair`, `disconnect`.
 *  4. **The offline story** — the cached document is loaded whenever a session
 *     ends, and is *never* discarded by a reconnect.
 */
class DeckStore(application: Application) : AndroidViewModel(application) {

    private val secureStore = SecureStore(application)
    private val cache = DeckCache(application)
    private val pairingApi = PairingApi()

    /**
     * mDNS and manual entry are merged into one candidate list, so the connect
     * screen renders a single list and the two paths cannot disagree.
     */
    private val manualDiscovery = ManualDiscovery()
    private val discovery: Discovery = CompositeDiscovery(
        listOf(NsdDiscovery(application), manualDiscovery),
    )

    private val _state = MutableStateFlow(DeckUiState())
    val state: StateFlow<DeckUiState> = _state.asStateFlow()

    /** The credentials in force, or null when unpaired. Never logged. */
    private var bundle: TokenBundle? = null

    private var sessionJob: Job? = null
    private var discoveryJob: Job? = null

    /** Attempt counter for the backoff schedule; reset by `welcome`. */
    private var attempt = 0

    /**
     * The credential a reconnecting client needs, remembered separately from
     * [bundle] so that a 4401 can clear the token without losing the host
     * identity the user has to re-pair against.
     */
    private var lastHost: DiscoveredHost? = null

    /** Set when a 4408 requires exactly one immediate reconnect (§2.5). */
    private var idleReconnectUsed = false

    /** The metrics the host is currently asked for, so a re-subscribe is a no-op. */
    private var subscribedMetrics: Set<String> = emptySet()

    init {
        // A previously paired device reconnects on launch without a tap; that
        // is the whole point of storing the token. With one slot per host this
        // picks the most recently written one, which is the host the user was
        // last on.
        secureStore.load()?.let { stored ->
            bundle = stored
            _state.update {
                it.copy(
                    hostId = stored.hostId,
                    hostName = stored.hostName,
                    deviceId = stored.deviceId,
                    scopes = stored.scopes.toSet(),
                )
            }
            lastHost = DiscoveredHost(
                key = stored.hostId,
                name = stored.hostName,
                host = stored.baseUrl.substringAfter("://").substringBeforeLast(':'),
                port = stored.baseUrl.substringAfterLast(':').toIntOrNull() ?: 0,
                hostId = stored.hostId,
                fingerprint = stored.fingerprint.orEmpty(),
            )
            loadCachedProfile(stored.hostId)
            connect(lastHost!!)
        }
        startDiscovery()
    }

    // -----------------------------------------------------------------------
    // Discovery
    // -----------------------------------------------------------------------

    private fun startDiscovery() {
        if (discoveryJob != null) return
        discoveryJob = viewModelScope.launch {
            _state.update { it.copy(scanning = true) }
            discovery.start()
                .catch { _ -> _state.update { s -> s.copy(scanning = false) } }
                .collect { hosts -> _state.update { it.copy(hosts = hosts) } }
        }
    }

    /**
     * Restarts the browse.
     *
     * mDNS on Android goes quiet after a network change and does not recover on
     * its own, so an explicit rescan is the only way back without a restart.
     */
    fun rescan() {
        discoveryJob?.cancel()
        discoveryJob = null
        discovery.stop()
        startDiscovery()
    }

    // -----------------------------------------------------------------------
    // Intents: connection
    // -----------------------------------------------------------------------

    /**
     * Connects to a host, pairing first when no token is held for it.
     *
     * @param pin the PIN shown on the host. When null and the host is unpaired,
     *   the store stops at [ConnectionState.Pairing] and the UI collects one.
     */
    fun connect(host: DiscoveredHost, pin: String? = null) {
        lastHost = host
        sessionJob?.cancel()
        sessionJob = viewModelScope.launch {
            val existing = bundle?.takeIf { it.hostId == host.hostId || it.baseUrl == host.baseUrl() }
            if (existing != null) {
                runSession(host, existing)
                return@launch
            }
            if (pin == null) {
                awaitPin(host)
                return@launch
            }
            pairThenRun(host, pin)
        }
    }

    /** Retries the last host immediately, skipping the backoff wait. */
    fun reconnect() {
        val host = lastHost ?: return
        attempt = 0
        idleReconnectUsed = false
        sessionJob?.cancel()
        sessionJob = viewModelScope.launch { runSession(host, bundle) }
    }

    /**
     * Connects to a hand-typed `host:port` (PROTOCOL.md §4.2).
     *
     * The candidate is offered to [ManualDiscovery] first so it appears in the
     * list the user is looking at; a host that vanishes from the list while
     * being connected to would be confusing.
     */
    fun connectManual(address: String, defaultPort: Int = DEFAULT_HOST_PORT) {
        val candidate = parseManualHost(address, defaultPort) ?: run {
            // The typed address is not echoed: it is still in the field the user
            // just used, and the example is what they need to compare it with.
            _state.update {
                it.copy(error = "That address does not look right. Write it like 192.168.1.10 or 192.168.1.10:8765.")
            }
            return
        }
        manualDiscovery.offer(candidate)
        connect(candidate)
    }

    /** Ends the session. The token and the cached profile are both kept. */
    fun disconnect() {
        sessionJob?.cancel()
        sessionJob = null
        attempt = 0
        _state.update {
            it.copy(
                connection = ConnectionState.Disconnected,
                status = null,
                pairing = null,
            )
        }
    }

    /** Forgets the host entirely: token, cache and all. */
    fun forgetHost() {
        val id = bundle?.hostId ?: _state.value.hostId
        disconnect()
        if (id.isNotBlank()) {
            cache.clear(id)
            secureStore.forget(id)
        } else {
            secureStore.clear()
        }
        bundle = null
        lastHost = null
        _state.update {
            DeckUiState(hosts = it.hosts, scanning = it.scanning)
        }
    }

    fun dismissError() = _state.update { it.copy(error = null) }

    // -----------------------------------------------------------------------
    // Intents: pairing
    // -----------------------------------------------------------------------

    /**
     * Exchanges a PIN for a token and connects (PROTOCOL.md §3).
     *
     * The probe runs first, so the fingerprint the user was shown is the one the
     * pairing request is pinned to. Pairing against an unverified certificate
     * would hand the PIN to whatever answered on that address.
     */
    fun pair(host: DiscoveredHost, pin: String) {
        lastHost = host
        sessionJob?.cancel()
        sessionJob = viewModelScope.launch { pairThenRun(host, pin) }
    }

    /**
     * Probes the host and keeps the PIN field usable.
     *
     * The probe runs first, so the fingerprint the user was shown is the one the
     * pairing request is pinned to: pairing against an unverified certificate
     * would hand the PIN to whatever answered on that address.
     *
     * It then repeats while the host reports pairing closed. A single probe was a
     * real bug: the natural order for a user is to connect first and press
     * "Show a PIN" on the computer afterwards, and with one probe the PIN field
     * stayed disabled for the life of the screen — the user could see the
     * instruction to open the control panel but could never act on it. Polling
     * also picks up the PIN's expiry, so the field disables itself again when the
     * window closes instead of accepting a code the host will reject.
     */
    private suspend fun awaitPin(host: DiscoveredHost) {
        _state.update {
            it.copy(
                connection = ConnectionState.Pairing,
                status = null,
                error = null,
                pairing = PairingUiState(host = host, submitting = false),
            )
        }

        while (currentCoroutineContext().isActive) {
            val probe = try {
                pairingApi.probe(host.baseUrl(), host.fingerprint.takeIf { it.isNotBlank() })
            } catch (e: PairingException) {
                _state.update {
                    it.copy(
                        connection = ConnectionState.Disconnected,
                        pairing = null,
                        error = e.message,
                    )
                }
                return
            }

            val open = probe.info.pairing.open
            val name = probe.info.hostName.ifBlank { host.name }
            _state.update {
                it.copy(
                    pairing = PairingUiState(
                        host = host,
                        hostName = name,
                        fingerprint = probe.pinnedFingerprint ?: probe.info.tls.fingerprint.takeIf { f -> f.isNotBlank() },
                        pairingOpen = open,
                        expiresInS = probe.info.pairing.expiresInS,
                        ready = true,
                    ),
                    // The banner is a status line, not an instruction: the
                    // pairing card right below it already says what to press on
                    // the computer, and repeating that here would print the same
                    // sentence twice on one screen.
                    status = if (open) {
                        "Ready for the PIN from $name"
                    } else {
                        "Waiting for you to press “Show a PIN” on $name"
                    },
                )
            }

            // Faster while the user is waiting on a window they just opened, and
            // slow enough afterwards that an idle screen costs nothing.
            delay(if (open) PAIRING_POLL_OPEN_MS else PAIRING_POLL_CLOSED_MS)
        }
    }

    private suspend fun pairThenRun(host: DiscoveredHost, pin: String) {
        _state.update {
            it.copy(
                connection = ConnectionState.Pairing,
                error = null,
                pairing = (it.pairing ?: PairingUiState(host = host)).copy(submitting = true),
            )
        }
        val probe = try {
            pairingApi.probe(host.baseUrl(), host.fingerprint.takeIf { it.isNotBlank() })
        } catch (e: PairingException) {
            failPairing(e.message ?: "Could not reach your computer.")
            return
        }

        val device = PairDeviceInfo(
            id = deviceId(),
            name = deviceName(),
            platform = "android",
            model = android.os.Build.MODEL.orEmpty(),
        )

        val paired = try {
            pairingApi.pair(probe, pin, device)
        } catch (e: PairingException) {
            // "attempts left" is the count the host gave, and it is worth the
            // space: it is the difference between trying the next digit and
            // walking to the computer.
            val suffix = e.attemptsRemaining?.let {
                if (it == 1) " One try left." else " $it tries left."
            }.orEmpty()
            failPairing((e.message ?: "That PIN did not work.") + suffix)
            return
        }

        val fresh = TokenBundle(
            hostId = paired.host?.id ?: probe.info.hostId,
            hostName = paired.host?.name ?: probe.info.hostName,
            baseUrl = probe.baseUrl,
            deviceId = paired.deviceId.ifBlank { device.id },
            deviceName = device.name,
            token = paired.token,
            scopes = paired.scopes,
            fingerprint = probe.pinnedFingerprint,
        )
        secureStore.save(fresh)
        bundle = fresh
        _state.update {
            it.copy(
                pairing = null,
                status = null,
                hostId = fresh.hostId,
                hostName = fresh.hostName,
                deviceId = fresh.deviceId,
                scopes = fresh.scopes.toSet(),
            )
        }
        runSession(host, fresh)
    }

    private fun failPairing(message: String) {
        _state.update {
            it.copy(
                connection = ConnectionState.Disconnected,
                status = null,
                error = message,
                // Clear the in-flight flag so a wrong PIN can be corrected and
                // retried without leaving the screen: the field must come back.
                pairing = it.pairing?.copy(submitting = false),
            )
        }
    }

    // -----------------------------------------------------------------------
    // The session loop
    // -----------------------------------------------------------------------

    /**
     * Connects, runs until the socket ends, then decides what to do about it.
     *
     * The loop is deliberately in one function so the policy is readable end to
     * end: which close codes retry, which wipe the token, and the one place
     * where the backoff counter is reset.
     *
     * Everything lives inside one [coroutineScope], so cancelling the session
     * job (a disconnect, a profile switch, `onCleared`) tears down the reader
     * and the event collector with it rather than leaving them attached to a
     * socket nobody owns.
     */
    private suspend fun runSession(host: DiscoveredHost, credentials: TokenBundle?) = coroutineScope {
        val url = wsUrl(host, credentials)
        val isFirstAttempt = attempt == 0

        _state.update {
            it.copy(
                connection = if (isFirstAttempt) ConnectionState.Connecting else ConnectionState.Reconnecting,
                status = if (isFirstAttempt) null else "Reconnecting to ${it.hostName.ifBlank { host.name }}…",
                error = null,
            )
        }

        val transport = WsTransport(PairingApi.wsClientFor(credentials?.fingerprint))
        val client = DeckClient(transport, this)
        activeClient = client

        val hello = HelloPayload(
            deviceId = credentials?.deviceId ?: deviceId(),
            // An unpaired client sends an empty token and expects
            // `error{unauthenticated}` before it may pair (§2.2).
            token = credentials?.token.orEmpty(),
            client = ClientInfo(
                platform = "android",
                appVersion = appVersion(),
                model = android.os.Build.MODEL.orEmpty(),
                screen = screenInfo(),
            ),
            resume = resumeInfo(),
        )

        // Subscribed *before* the handshake: the host may push a state or
        // telemetry frame the instant it answers `welcome`, and a shared flow
        // with no subscriber at that moment would drop it.
        val closed = CompletableDeferred<CloseInfo>()
        val eventsJob = launch {
            client.events.collect { event ->
                if (event is ClientEvent.Closed) {
                    closed.complete(event.info)
                } else {
                    handleEvent(event)
                }
            }
            // The flow completed without a Closed event (the client was closed
            // by us, or the socket vanished mid-read).
            closed.complete(CloseInfo(CloseCode.NORMAL.code, "connection ended"))
        }

        val outcome = try {
            client.handshake(url, hello, credentials?.fingerprint)
        } catch (e: Exception) {
            HandshakeResult.Failed(e)
        }

        when (outcome) {
            is HandshakeResult.Failed -> {
                eventsJob.cancel()
                client.close(CloseCode.NORMAL.code, "handshake failed")
                onSessionEnded(CloseInfo(0, outcome.cause.message ?: "connection failed"))
                return@coroutineScope
            }

            is HandshakeResult.Refused -> {
                eventsJob.cancel()
                onSessionEnded(outcome.info)
                return@coroutineScope
            }

            is HandshakeResult.Ok -> {
                // `welcome` is the reset point: the host answered, so whatever
                // went wrong before is over and the next failure starts from
                // 500 ms again.
                attempt = 0
                idleReconnectUsed = false
                onWelcome(outcome.welcome, credentials, host)
            }
        }

        // Run until the socket dies.
        val info = closed.await()
        eventsJob.cancel()
        activeClient = null
        onSessionEnded(info)
    }

    private suspend fun onWelcome(welcome: WelcomePayload, credentials: TokenBundle?, host: DiscoveredHost) {
        _state.update {
            it.copy(
                connection = ConnectionState.Connected,
                status = null,
                error = null,
                hostId = welcome.host.id.ifBlank { credentials?.hostId.orEmpty() },
                hostName = welcome.host.name.ifBlank { credentials?.hostName.orEmpty() },
                hostOs = welcome.host.os,
                deviceId = welcome.device.id,
                scopes = welcome.device.scopes.toSet(),
                features = welcome.features.toSet(),
            )
        }

        // Re-persist if the host renamed itself; the token itself is unchanged.
        val refreshed = credentials?.copy(
            hostId = welcome.host.id.ifBlank { credentials.hostId },
            hostName = welcome.host.name.ifBlank { credentials.hostName },
        )
        if (refreshed != null && refreshed != credentials) {
            secureStore.save(refreshed)
            bundle = refreshed
        }

        refreshProfiles()
    }

    private suspend fun refreshProfiles() {
        val client = activeClient ?: return
        val listResult = try {
            client.request(
                MsgType.PROFILE_LIST,
                encodeToJson(ProfileListPayload()),
            )
        } catch (_: Exception) {
            return
        }

        val list = try {
            ProtocolJson.decodeFromJsonElement(
                ProfileListResultPayload.serializer(),
                listResult.payloadObject(),
            )
        } catch (_: Exception) {
            return
        }

        val activeId = list.active?.takeIf { it.isNotBlank() }
            ?: _state.value.activeProfileId.ifBlank { list.profiles.firstOrNull()?.id.orEmpty() }

        _state.update {
            it.copy(
                profiles = list.profiles,
                activeProfileId = activeId,
            )
        }

        if (activeId.isNotBlank()) loadProfile(activeId)
    }

    /** Fetches one profile document, caches it and re-subscribes telemetry. */
    suspend fun loadProfile(profileId: String) {
        val client = activeClient ?: return
        val result = try {
            client.request(
                MsgType.PROFILE_GET,
                encodeToJson(ProfileGetPayload(profileId)),
            )
        } catch (e: Exception) {
            // A ProtocolError's own message is "code: message", which is the
            // shape a developer wants in a log. The code is not something a
            // reader can act on, so the host's sentence is shown on its own.
            // `internal` is excluded because the client builds those sentences
            // itself ("host did not answer profile.get in 20000ms"), and a bug
            // report is not something to put in front of a user; they get the
            // plain sentence instead. Anything that is not a ProtocolError at
            // all — a socket failure, a lost connection — is exception text and
            // never reaches the card either.
            val protocol = e as? ProtocolError
            val shown = protocol?.payload?.message
                ?.takeIf { it.isNotBlank() && protocol.payload.code != ErrorCode.INTERNAL }
                ?: "Could not get your board from the computer."
            _state.update { it.copy(error = shown) }
            return
        }

        val body = try {
            ProtocolJson.decodeFromJsonElement(ProfileGetResultPayload.serializer(), result.payloadObject())
        } catch (_: Exception) {
            return
        }
        val document = body.profile ?: return
        val profile = try {
            ProtocolJson.decodeFromJsonElement(Profile.serializer(), document)
        } catch (_: Exception) {
            return
        }

        val hostId = _state.value.hostId
        if (hostId.isNotBlank()) cache.save(hostId, profileId, body.revision, document)

        _state.update { s ->
            // Landing on a page that no longer exists (a hot reload deleted it)
            // must not leave the grid pointing at nothing.
            val pageId = s.currentPageId.takeIf { id -> profile.page(id) != null }
                ?: profile.rootPageOrFirst()?.id.orEmpty()
            s.copy(
                profile = profile,
                profileIsCached = false,
                activeProfileId = profileId,
                currentPageId = pageId,
                breadcrumbs = listOf(pageId).filter { it.isNotBlank() },
            )
        }

        subscribeTelemetryFor(profile)
    }

    /**
     * Ends a session and decides what happens next.
     *
     * The cached profile is loaded first and unconditionally: whatever the close
     * reason, the user keeps a usable grid, and the reconnect happens *under*
     * it rather than instead of it.
     */
    private fun onSessionEnded(info: CloseInfo) {
        activeClient = null
        val hostId = _state.value.hostId
        if (hostId.isNotBlank()) loadCachedProfile(hostId)

        val known = info.known
        if (known == null) {
            // A code this build does not classify, or a transport failure.
            // Retrying is the safer default: refusing would strand a user whose
            // host simply restarted.
            scheduleReconnect(info, retry = true, immediate = false)
            return
        }

        when (known) {
            CloseCode.NORMAL -> _state.update {
                it.copy(
                    connection = ConnectionState.Disconnected,
                    // The strip below this adds what to do about it, so this
                    // line says only what happened.
                    status = "Disconnected.",
                )
            }

            CloseCode.MALFORMED, CloseCode.DEVICE_DISABLED -> _state.update {
                it.copy(
                    connection = ConnectionState.Disconnected,
                    status = known.explanation,
                    error = if (known == CloseCode.DEVICE_DISABLED) known.explanation else it.error,
                )
            }

            CloseCode.UNAUTHENTICATED -> {
                // §2.5: wipe the token and return to pairing. The host identity
                // and the cached profile stay, so re-pairing is one PIN away.
                //
                // Only this host's token is dropped. Clearing every slot would
                // make a revocation on one machine silently sign the user out of
                // all the others.
                val id = bundle?.hostId ?: _state.value.hostId
                if (id.isNotBlank()) secureStore.forget(id) else secureStore.clear()
                bundle = null
                _state.update {
                    it.copy(
                        connection = ConnectionState.Disconnected,
                        status = known.explanation,
                        error = known.explanation,
                        scopes = emptySet(),
                        pairing = lastHost?.let { h -> PairingUiState(host = h) },
                    )
                }
            }

            CloseCode.IDLE_TIMEOUT -> {
                // "Reconnect immediately, once." A second 4408 in a row means
                // something is actually wrong, so it falls back to backoff
                // rather than hammering the host.
                if (!idleReconnectUsed) {
                    idleReconnectUsed = true
                    scheduleReconnect(info, retry = true, immediate = true)
                } else {
                    scheduleReconnect(info, retry = true, immediate = false)
                }
            }

            CloseCode.RATE_LIMITED, CloseCode.TRY_AGAIN_LATER ->
                scheduleReconnect(info, retry = true, immediate = false)
        }
    }

    private fun scheduleReconnect(info: CloseInfo, retry: Boolean, immediate: Boolean) {
        if (!retry) return
        val host = lastHost
        if (host == null) {
            _state.update { it.copy(connection = ConnectionState.Disconnected, status = info.known?.explanation) }
            return
        }

        _state.update {
            // The explanation is already a full sentence about the drop, and the
            // strip's own line says that a retry is happening; appending
            // "Reconnecting…" here would say it a second time and push the host
            // name off the end of a two-line banner. An unclassified drop gets
            // the bare word, because there is nothing specific to report.
            it.copy(
                connection = ConnectionState.Reconnecting,
                status = info.known?.explanation ?: "Connection lost. Reconnecting…",
            )
        }

        sessionJob = viewModelScope.launch {
            if (!immediate) delay(backoffDelayMs(attempt))
            attempt++
            runSession(host, bundle)
        }
    }

    private fun loadCachedProfile(hostId: String) {
        val cached = cache.load(hostId) ?: return
        val profile = try {
            ProtocolJson.decodeFromJsonElement(Profile.serializer(), cached.document)
        } catch (_: Exception) {
            return
        }
        _state.update { s ->
            // Only ever *fill in* the document; a live document that is already
            // loaded is strictly better than the cache and is never replaced.
            if (s.profile != null && !s.profileIsCached) return@update s
            val pageId = s.currentPageId.takeIf { profile.page(it) != null }
                ?: profile.rootPageOrFirst()?.id.orEmpty()
            s.copy(
                profile = profile,
                profileIsCached = true,
                activeProfileId = s.activeProfileId.ifBlank { cached.profileId },
                currentPageId = pageId,
                breadcrumbs = listOf(pageId).filter { it.isNotBlank() },
            )
        }
    }

    // -----------------------------------------------------------------------
    // Intents: buttons and navigation
    // -----------------------------------------------------------------------

    /**
     * Sends a press.
     *
     * The client owns gesture detection (PROTOCOL.md §6.1): the UI decides
     * `short` from `long` at 400 ms and sends exactly one press per gesture, and
     * `repeat` for `on_hold`. Nothing here re-derives the threshold.
     */
    fun press(buttonId: String, kind: String = PressKind.SHORT, count: Int = 1) {
        val s = _state.value
        val profile = s.profile ?: return
        val page = s.page ?: return
        val button = page.buttons.firstOrNull { it.id == buttonId } ?: return

        // Navigation lives in the client: the host cannot tell this phone which
        // page it is looking at, because there is no such event in the protocol.
        if (kind != PressKind.REPEAT) applyLocalNavigation(button, page, profile)

        // Optimistic local flip for toggle/radio, so the tile responds at touch
        // speed; `event.button.state` confirms or corrects it (§5.2).
        if (kind == PressKind.SHORT) applyOptimisticState(button, page, profile)

        val client = activeClient
        if (client == null || !s.interactive) return

        val payload = ButtonPressPayload(
            profileId = profile.id.ifBlank { s.activeProfileId },
            pageId = page.id,
            buttonId = buttonId,
            press = PressInfo(kind = kind, count = count),
        )
        client.send(MsgType.BUTTON_PRESS, encodeToJson(payload))
    }

    /**
     * Sends a release.
     *
     * PROTOCOL.md §6.2 restricts this to momentary, timer and toggle buttons
     * that declare an `on_release` action; sending it for anything else would be
     * a message the host has no handler for.
     */
    fun release(buttonId: String, heldMs: Long) {
        val s = _state.value
        val profile = s.profile ?: return
        val page = s.page ?: return
        val button = page.buttons.firstOrNull { it.id == buttonId } ?: return
        if (!button.wantsRelease) return

        val client = activeClient ?: return
        if (!s.interactive) return

        val payload = ButtonReleasePayload(
            profileId = profile.id.ifBlank { s.activeProfileId },
            pageId = page.id,
            buttonId = buttonId,
            heldMs = heldMs,
        )
        client.send(MsgType.BUTTON_RELEASE, encodeToJson(payload))
    }

    /** Navigates to a page by id. No-op for an unknown page. */
    fun navigate(pageId: String) {
        _state.update { s ->
            val profile = s.profile ?: return@update s
            if (profile.page(pageId) == null) return@update s
            if (pageId == s.currentPageId) return@update s
            val current = s.currentPageId
            val stack = if (current.isBlank()) listOf(pageId) else s.breadcrumbs + pageId
            s.copy(currentPageId = pageId, breadcrumbs = stack)
        }
    }

    /** Pops one page off the breadcrumb stack. No-op at the root. */
    fun back() {
        _state.update { s ->
            if (s.breadcrumbs.size <= 1) return@update s
            val stack = s.breadcrumbs.dropLast(1)
            s.copy(currentPageId = stack.last(), breadcrumbs = stack)
        }
    }

    /** Switches the active profile, on the host and locally. */
    fun switchProfile(profileId: String) {
        if (profileId.isBlank()) return
        _state.update { it.copy(activeProfileId = profileId) }
        val client = activeClient ?: return
        viewModelScope.launch {
            try {
                client.request(
                    MsgType.PROFILE_SET_ACTIVE,
                    encodeToJson(ProfileSetActivePayload(profileId)),
                )
            } catch (_: Exception) {
                // The host refuses an unknown profile; the list came from the
                // host so this is a race, and the reload below is authoritative.
            }
            loadProfile(profileId)
        }
    }

    /** Asks the host to re-read a profile from disk (`profile.reload`). */
    fun reloadProfile(profileId: String) {
        val client = activeClient ?: return
        viewModelScope.launch {
            try {
                client.request(
                    MsgType.PROFILE_RELOAD,
                    encodeToJson(ProfileReloadPayload(profileId)),
                )
            } catch (_: Exception) {
                return@launch
            }
            loadProfile(profileId)
        }
    }

    /** Cancels a running execution (§6.3). */
    fun cancelExecution(executionId: String) {
        val client = activeClient ?: return
        client.send(
            MsgType.ACTION_CANCEL,
            encodeToJson(ActionCancelPayload(executionId)),
        )
    }

    // -----------------------------------------------------------------------
    // Event handling
    // -----------------------------------------------------------------------

    private suspend fun handleEvent(event: ClientEvent) {
        when (event) {
            is ClientEvent.ButtonState -> applyRemoteState(event.payload)

            is ClientEvent.Telemetry -> _state.update {
                // §7: an unavailable metric is absent, not zero. The UI
                // renders absence as `--`, so nothing is invented here.
                it.copy(telemetry = it.telemetry + event.payload.values)
            }

            is ClientEvent.ProfileChanged -> {
                val s = _state.value
                if (event.payload.profileId == s.activeProfileId) loadProfile(s.activeProfileId)
                refreshProfilesQuietly()
            }

            is ClientEvent.ActionResult -> {
                val err = event.payload.error
                if (err != null) _state.update { it.copy(error = err.message.ifBlank { err.code }) }
            }

            is ClientEvent.ActionFinished -> {
                val err = event.payload.error
                if (err != null) _state.update { it.copy(error = err.message.ifBlank { err.code }) }
            }

            is ClientEvent.Error -> _state.update {
                // An `error` with no `reply_to` is about the session rather
                // than one request; both are worth showing.
                it.copy(error = event.payload.message.ifBlank { event.payload.code })
            }

            // "Host" and "frame" are both developer words. This is the deck's
            // error card, and the one thing the reader can act on is that the
            // app and the computer disagreed about a message.
            is ClientEvent.Malformed -> _state.update {
                it.copy(error = "This app and your computer could not understand each other.")
            }

            // Handled by the session loop, which owns reconnection.
            is ClientEvent.Closed, is ClientEvent.Ping, is ClientEvent.Unknown -> Unit
        }
    }

    private suspend fun refreshProfilesQuietly() {
        val client = activeClient ?: return
        try {
            val result = client.request(
                MsgType.PROFILE_LIST,
                encodeToJson(ProfileListPayload()),
            )
            val list = ProtocolJson.decodeFromJsonElement(
                ProfileListResultPayload.serializer(),
                result.payloadObject(),
            )
            _state.update { it.copy(profiles = list.profiles) }
        } catch (_: Exception) {
            // The next reconnect re-fetches anyway.
        }
    }

    /**
     * Merges a host-pushed state change onto what the tile already shows.
     *
     * The host sends only the changed fields (§6.4), so a merge is required: a
     * status update that carries a new label but no colour must not blank the
     * colour.
     */
    private fun applyRemoteState(payload: EventButtonStatePayload) {
        val incoming = parseStateValue(payload.state) ?: return
        val key = stateKey(payload.profileId, payload.pageId, payload.buttonId)
        _state.update { s ->
            val previous = s.buttonStates[key]
            s.copy(buttonStates = s.buttonStates + (key to merge(previous, incoming)))
        }
    }

    private fun applyOptimisticState(button: Button, page: Page, profile: Profile) {
        val key = stateKey(profile.id.ifBlank { _state.value.activeProfileId }, page.id, button.id)
        when (button.state.type) {
            ButtonState.TOGGLE -> _state.update { s ->
                val previous = s.buttonStates[key]
                val next = !(previous?.active ?: false)
                s.copy(buttonStates = s.buttonStates + (key to (previous ?: ButtonStateValue()).copy(active = next)))
            }

            ButtonState.RADIO -> {
                val group = button.state.group ?: return
                // Exactly one button per group is on (§5.2), so the whole group
                // is rewritten rather than just the pressed member. The group is
                // resolved through the page's own button list, which is the only
                // place the group membership is declared.
                val members = page.buttons
                    .filter { it.state.type == ButtonState.RADIO && it.state.group == group }
                    .map { stateKey(profile.id.ifBlank { _state.value.activeProfileId }, page.id, it.id) }
                _state.update { s ->
                    val cleared = s.buttonStates.toMutableMap()
                    members.forEach { memberKey ->
                        val previous = cleared[memberKey] ?: ButtonStateValue()
                        cleared[memberKey] = previous.copy(active = memberKey == key)
                    }
                    s.copy(buttonStates = cleared)
                }
            }

            else -> Unit
        }
    }

    /**
     * Applies the client-side `deck.*` navigation namespace (§10).
     *
     * These actions are still sent to the host so its session state agrees with
     * what the user sees — the host uses that for `resume` and for its own
     * hot-reload reconciliation.
     */
    private fun applyLocalNavigation(button: Button, page: Page, profile: Profile) {
        val action = button.onPress ?: return
        val params = action.params as? JsonObject
        when (action.type) {
            "deck.open_page" -> {
                val target = params?.get("page_id")?.jsonPrimitive?.contentOrNull
                val targetProfile = params?.get("profile_id")?.jsonPrimitive?.contentOrNull
                if (!targetProfile.isNullOrBlank() && targetProfile != profile.id) {
                    switchProfile(targetProfile)
                } else if (!target.isNullOrBlank()) {
                    navigate(target)
                }
            }

            "deck.back" -> back()

            "deck.change_profile" -> {
                val target = params?.get("profile_id")?.jsonPrimitive?.contentOrNull
                if (!target.isNullOrBlank()) switchProfile(target)
            }

            "deck.notify" -> {
                val message = params?.get("message")?.jsonPrimitive?.contentOrNull
                if (!message.isNullOrBlank()) _state.update { it.copy(status = message) }
            }

            else -> Unit
        }
    }

    // -----------------------------------------------------------------------
    // Telemetry
    // -----------------------------------------------------------------------

    /**
     * Subscribes to exactly the metrics the profile binds plus host health.
     *
     * §7: the host samples only while at least one client is subscribed, so an
     * unnecessary metric costs the host a sample per interval. The profile's
     * `telemetry` buttons are the real requirement; the baseline set exists
     * because the settings sheet shows host health, and it is the same list
     * Milestone 1 documents.
     */
    private suspend fun subscribeTelemetryFor(profile: Profile) {
        val client = activeClient ?: return
        if (!_state.value.features.contains(FEATURE_TELEMETRY)) return

        val referenced = profile.pages
            .flatMap { it.buttons }
            .mapNotNull { it.state.metric?.takeIf { m -> m.isNotBlank() } }
            .toSet()
        val wanted = referenced + BASELINE_METRICS
        if (wanted == subscribedMetrics) return

        val payload = TelemetrySubscribePayload(
            metrics = wanted.toList(),
            intervalMs = TELEMETRY_INTERVAL_MS,
        )
        try {
            client.send(MsgType.TELEMETRY_SUBSCRIBE, encodeToJson(payload))
            subscribedMetrics = wanted
        } catch (_: Exception) {
            // A lost subscription is re-established by the next reconnect.
        }
    }

    // -----------------------------------------------------------------------
    // Helpers
    // -----------------------------------------------------------------------

    /** The live client, tracked separately from the session job. */
    private var activeClient: DeckClient? = null

    private fun wsUrl(host: DiscoveredHost, credentials: TokenBundle?): String {
        // The scheme follows the credential's own base URL rather than the
        // candidate's advertised fingerprint: what matters is which client the
        // transport was built with, and only the credential knows that.
        val base = credentials?.baseUrl ?: host.baseUrl()
        val scheme = if (base.startsWith("https://")) "wss" else "ws"
        val authority = base.substringAfter("://").trimEnd('/')
        return "$scheme://$authority/ws"
    }

    private fun resumeInfo(): ResumeInfo? {
        val s = _state.value
        if (s.activeProfileId.isBlank() && s.currentPageId.isBlank()) return null
        return ResumeInfo(
            profileId = s.activeProfileId.takeIf { it.isNotBlank() },
            pageId = s.currentPageId.takeIf { it.isNotBlank() },
        )
    }

    private fun screenInfo(): ScreenInfo {
        val metrics = getApplication<Application>().resources.displayMetrics
        val orientation = if (metrics.widthPixels <= metrics.heightPixels) "portrait" else "landscape"
        return ScreenInfo(
            w = metrics.widthPixels,
            h = metrics.heightPixels,
            density = metrics.density,
            orientation = orientation,
        )
    }

    private fun deviceId(): String {
        // The token is bound to the device id the host recorded (§2.2), so this
        // must be stable across launches. It is derived from ANDROID_ID rather
        // than a random value persisted separately, so a cleared cache still
        // produces the same id and the stored token keeps working.
        val androidId = runCatching {
            android.provider.Settings.Secure.getString(
                getApplication<Application>().contentResolver,
                android.provider.Settings.Secure.ANDROID_ID,
            )
        }.getOrNull()
        return "android-" + (androidId ?: "unknown").take(16)
    }

    private fun deviceName(): String =
        android.os.Build.MODEL?.takeIf { it.isNotBlank() } ?: "Android device"

    private fun appVersion(): String = runCatching {
        getApplication<Application>().packageManager
            .getPackageInfo(getApplication<Application>().packageName, 0)
            .versionName
    }.getOrNull() ?: "0.1.0"

    override fun onCleared() {
        super.onCleared()
        activeClient?.close(CloseCode.NORMAL.code, "client shutting down")
        activeClient = null
        discoveryJob?.cancel()
        discovery.stop()
    }

    companion object {
        /** 500 ms floor for the backoff schedule. */
        const val BASE_BACKOFF_MS = 500L

        /** 30 s ceiling for the backoff schedule. */
        const val MAX_BACKOFF_MS = 30_000L

        /** The 400 ms long-press threshold the client owns (§6.1). */
        const val LONG_PRESS_MS = 400L

        /** Default `on_hold` repeat interval when the button does not say. */
        const val DEFAULT_HOLD_REPEAT_MS = 250

        const val TELEMETRY_INTERVAL_MS = 1000

        /**
         * How often to re-probe the host while the user is on the pairing screen.
         *
         * Faster once the window is open, so a code that was just issued is
         * accepted immediately; slower while it is closed, because the common
         * case is a user staring at the screen deciding to press "Show a PIN" on
         * the computer, and a tight loop there would be pure noise against the
         * host.
         */
        const val PAIRING_POLL_OPEN_MS = 2_000L
        const val PAIRING_POLL_CLOSED_MS = 4_000L

        /** §2.3: gate optional behaviour on this feature. */
        const val FEATURE_TELEMETRY = "telemetry"

        /**
         * Milestone 1 metrics (PROTOCOL.md §7). Subscribed in addition to the
         * metrics the profile references so the settings sheet can show host
         * health without a second subscription.
         */
        val BASELINE_METRICS = setOf("cpu.usage", "mem.used_pct", "disk.used_pct", "uptime_s")

        /**
         * The backoff window for an attempt: `500ms * 2^n`, capped at 30 s.
         *
         * Deterministic and separate from the jitter so the schedule can be
         * asserted directly. The window is the promise about how hard the client
         * may hit a host, and a promise only verified by reading the code is not
         * verified at all.
         *
         * The shift is clamped at 16 because `1L shl 63` is negative: an attempt
         * counter that grew unbounded would overflow into a *negative* window
         * before `min` ever got a chance to cap it.
         */
        fun backoffWindowMs(attempt: Int): Long {
            val safeAttempt = attempt.coerceIn(0, 16)
            return min(BASE_BACKOFF_MS shl safeAttempt, MAX_BACKOFF_MS)
        }

        /**
         * Full-jitter backoff: a uniform draw from `[0, window]`.
         *
         * Full jitter rather than the window itself is what stops a fleet of
         * phones on one Wi-Fi network from reconnecting in lockstep after the
         * host restarts and knocking it over again. The draw can legitimately be
         * zero — that is the whole point of full jitter — so callers must not
         * treat a zero delay as a bug.
         */
        fun backoffDelayMs(attempt: Int, random: Random = Random.Default): Long =
            random.nextLong(backoffWindowMs(attempt) + 1)

        /** The default host port, used when the user types a bare host. */
        const val DEFAULT_HOST_PORT = 8765

        /** The override key for one button. */
        fun stateKey(profileId: String, pageId: String, buttonId: String): String =
            "$profileId/$pageId/$buttonId"
    }
}

/**
 * Parses a hand-typed `host:port` into a candidate, or null when it is empty.
 *
 * A bare host takes the protocol's standard port, because typing a port on a
 * phone keyboard is the part people get wrong and the manual path exists
 * precisely for the case where mDNS failed. An *explicit* port that is not a
 * valid port number is an error rather than a fallback: silently connecting to
 * 8765 when the user typed 876X would send them to the wrong host, or to
 * nothing, with no explanation.
 *
 * An IPv6 literal is bracketed and contains colons, so only the colon after the
 * closing bracket separates the port.
 */
internal fun parseManualHost(address: String, defaultPort: Int): DiscoveredHost? {
    val trimmed = address.trim()
        .removePrefix("http://")
        .removePrefix("https://")
        .trimEnd('/')
    if (trimmed.isBlank()) return null

    val host: String
    val port: Int
    if (trimmed.startsWith("[")) {
        val close = trimmed.indexOf(']')
        if (close <= 0) return null
        host = trimmed.substring(1, close)
        val rest = trimmed.substring(close + 1)
        port = when {
            rest.isEmpty() -> defaultPort
            rest.startsWith(":") -> rest.removePrefix(":").toIntOrNull() ?: return null
            else -> return null
        }
    } else {
        val parts = trimmed.split(':')
        if (parts.size > 2) return null
        host = parts[0]
        port = if (parts.size == 2) parts[1].toIntOrNull() ?: return null else defaultPort
    }

    if (host.isBlank()) return null
    if (port !in 1..65535) return null
    return DiscoveredHost(
        key = "manual:$host:$port",
        name = host,
        host = host,
        port = port,
        manual = true,
    )
}

/** Parses a `state` object, returning null when it carries nothing usable. */
internal fun parseStateValue(element: JsonElement?): ButtonStateValue? {
    val obj = element as? JsonObject ?: return null
    return ButtonStateValue(
        type = obj["type"]?.jsonPrimitive?.contentOrNull,
        label = obj["label"]?.jsonPrimitive?.contentOrNull,
        color = obj["color"]?.jsonPrimitive?.contentOrNull,
        progress = obj["progress"]?.jsonPrimitive?.doubleOrNull,
        value = obj["value"]?.jsonPrimitive?.doubleOrNull,
        active = obj["active"]?.jsonPrimitive?.booleanOrNull,
    )
}

/** Overlays the non-null fields of [incoming] onto [previous]. */
internal fun merge(previous: ButtonStateValue?, incoming: ButtonStateValue): ButtonStateValue {
    if (previous == null) return incoming
    return ButtonStateValue(
        type = incoming.type ?: previous.type,
        label = incoming.label ?: previous.label,
        color = incoming.color ?: previous.color,
        progress = incoming.progress ?: previous.progress,
        value = incoming.value ?: previous.value,
        active = incoming.active ?: previous.active,
    )
}
