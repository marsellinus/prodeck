package dev.mobiledeck.data

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.merge
import java.util.concurrent.ConcurrentHashMap

/**
 * A host the client could connect to.
 *
 * It is a *candidate*, never a trust decision: PROTOCOL.md §4 is explicit that
 * trust comes from the TLS fingerprint plus the PIN, so nothing here is acted
 * on without a probe.
 */
data class DiscoveredHost(
    /** Stable identity for list diffing: the mDNS service name, or the manual address. */
    val key: String,
    val name: String,
    val host: String,
    val port: Int,
    val os: String = "",
    val hostId: String = "",
    val protocolVersion: Int = 0,
    /** True when the TXT record says the host is currently accepting pairing. */
    val pairingOpen: Boolean = false,
    /** SHA-256 fingerprint from the TXT record, if the host advertises one. */
    val fingerprint: String = "",
    val manual: Boolean = false,
) {
    /** `host:port`, with IPv6 literals bracketed so the URL parses. */
    val authority: String
        get() = if (host.contains(':') && !host.startsWith("[")) "[$host]:$port" else "$host:$port"

    /**
     * The URL to probe. Scheme is decided by [fingerprint]: a host that
     * advertises one is speaking TLS, which is the only evidence available
     * before `/api/v1/info` answers.
     */
    fun baseUrl(fingerprintKnown: Boolean = fingerprint.isNotEmpty()): String =
        if (fingerprintKnown) "https://$authority" else "http://$authority"
}

/**
 * Finds hosts on the LAN.
 *
 * An interface because discovery is the one part of the client that is
 * inherently platform- and permission-bound, and because a test wants to feed
 * the store a fixed list without an Android runtime.
 */
interface Discovery {
    /** Emits the current candidate set, and again whenever it changes. */
    fun start(): Flow<List<DiscoveredHost>>

    /** Stops browsing. Idempotent. */
    fun stop() {}
}

/**
 * mDNS/DNS-SD discovery via [NsdManager] (PROTOCOL.md §4).
 *
 * The TXT records the host publishes are `v`, `id`, `name`, `os`, `pair` and
 * `fp`; they are read into [DiscoveredHost] so the connect screen can show an
 * OS badge, the pairing state and the fingerprint without a round trip.
 *
 * `NsdManager` only reports service *resolution* asynchronously and forbids
 * resolving a service that is already being resolved, so resolutions are
 * tracked by name and never issued twice concurrently.
 */
class NsdDiscovery(context: Context) : Discovery {

    private val nsd = context.applicationContext.getSystemService(Context.NSD_SERVICE) as? NsdManager

    private val hosts = ConcurrentHashMap<String, DiscoveredHost>()
    private val resolving = ConcurrentHashMap.newKeySet<String>()

    override fun start(): Flow<List<DiscoveredHost>> = callbackFlow {
        val manager = nsd
        if (manager == null) {
            // A device without NSD still has manual entry, so this is a
            // degraded list rather than a failure.
            trySend(emptyList())
            awaitClose { }
            return@callbackFlow
        }

        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) = Unit

            override fun onServiceFound(service: NsdServiceInfo) {
                val name = service.serviceName ?: return
                // `resolveService` fails with FAILURE_ALREADY_ACTIVE if called
                // twice for one service, and the framework re-announces a
                // service on every TXT change, so de-duplicate here.
                if (!resolving.add(name)) return
                @Suppress("DEPRECATION")
                manager.resolveService(
                    service,
                    object : NsdManager.ResolveListener {
                        override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
                            resolving.remove(name)
                        }

                        override fun onServiceResolved(serviceInfo: NsdServiceInfo) {
                            resolving.remove(name)
                            val host = serviceInfo.host?.hostAddress ?: return
                            val txt = serviceInfo.attributes.orEmpty()
                                .mapValues { (_, v) -> v?.toString(Charsets.UTF_8).orEmpty() }
                            hosts[name] = DiscoveredHost(
                                key = name,
                                name = txt["name"]?.takeIf { it.isNotBlank() } ?: name,
                                host = host,
                                port = serviceInfo.port,
                                os = txt["os"].orEmpty(),
                                hostId = txt["id"].orEmpty(),
                                protocolVersion = txt["v"]?.toIntOrNull() ?: 0,
                                pairingOpen = txt["pair"] == "1",
                                fingerprint = txt["fp"].orEmpty(),
                            )
                            trySend(hosts.values.toList())
                        }
                    },
                )
            }

            override fun onServiceLost(service: NsdServiceInfo) {
                val name = service.serviceName ?: return
                resolving.remove(name)
                hosts.remove(name)
                trySend(hosts.values.toList())
            }

            override fun onDiscoveryStopped(serviceType: String) = Unit

            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                // Nothing useful to say beyond the code, and the manual field
                // remains the fallback path.
                trySend(hosts.values.toList())
                close()
            }

            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {
                close()
            }
        }

        trySend(hosts.values.toList())
        manager.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener)

        awaitClose {
            runCatching { manager.stopServiceDiscovery(listener) }
        }
    }

    override fun stop() {
        resolving.clear()
    }

    companion object {
        /** The service type from PROTOCOL.md §4. */
        const val SERVICE_TYPE = "_mobiledeck._tcp."
    }
}

/**
 * The always-available manual entry (PROTOCOL.md §4.2).
 *
 * It emits the address the user typed, once there is one, so it appears in the
 * same candidate list as the mDNS results and the connect screen has no special
 * case for it. Before the user types anything it emits nothing, because an
 * empty candidate is not a host.
 */
class ManualDiscovery : Discovery {

    private val host = MutableStateFlow<DiscoveredHost?>(null)

    /** Offers a hand-typed `host:port` as a candidate. */
    fun offer(candidate: DiscoveredHost) {
        host.value = candidate
    }

    /** Withdraws the manual candidate. */
    fun clear() {
        host.value = null
    }

    override fun start(): Flow<List<DiscoveredHost>> = host.map { listOfNotNull(it) }
}

/**
 * Merges several [Discovery] sources into one list, de-duplicated by key.
 *
 * mDNS and the manual entry are combined here rather than in the UI, so the
 * screen renders one list and cannot disagree with itself about which hosts
 * exist.
 */
class CompositeDiscovery(private val sources: List<Discovery>) : Discovery {

    override fun start(): Flow<List<DiscoveredHost>> = merge(
        *sources.map { it.start() }.toTypedArray(),
    ).let { merged ->
        flow {
            val seen = LinkedHashMap<String, DiscoveredHost>()
            merged.collect { batch ->
                // A source that reports an empty list (the manual entry before
                // anything is typed, or an mDNS restart) must not erase what the
                // others already found.
                if (batch.isEmpty()) return@collect
                batch.forEach { seen[it.key] = it }
                emit(seen.values.toList())
            }
        }
    }

    override fun stop() {
        sources.forEach { it.stop() }
    }
}
