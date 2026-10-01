package dev.mobiledeck.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.ErrorOutline
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Wifi
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.mobiledeck.data.ConnectionState
import dev.mobiledeck.data.DeckUiState
import dev.mobiledeck.data.DiscoveredHost
import dev.mobiledeck.data.PairingUiState
import kotlinx.coroutines.delay

/**
 * The connect screen: discovery, manual entry, PIN entry and the pairing
 * fingerprint (docs/ARCHITECTURE.md §4).
 *
 * The fingerprint is shown, not hidden: it is the only thing that lets a user
 * tell a real host from a rogue one that answered mDNS first
 * (docs/SECURITY.md T9), and hiding it would make pinning a formality.
 */
@Composable
fun ConnectScreen(
    state: DeckUiState,
    onConnect: (DiscoveredHost) -> Unit,
    onManualConnect: (String) -> Unit,
    onPair: (String) -> Unit,
    onDismissError: () -> Unit,
    onRefresh: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .imePadding()
            .verticalScroll(rememberScrollState())
            .padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text(
            text = "MobileDeck",
            style = MaterialTheme.typography.headlineMedium,
            fontWeight = FontWeight.Bold,
            color = MaterialTheme.colorScheme.onBackground,
        )

        StatusBanner(state)

        state.error?.let { message ->
            ErrorCard(message = message, onDismiss = onDismissError)
        }

        val pairing = state.pairing
        if (pairing != null) {
            PairingSection(pairing = pairing, busy = state.connection == ConnectionState.Pairing, onPair = onPair)
        }

        DiscoveredHostsSection(
            hosts = state.hosts,
            scanning = state.scanning,
            onConnect = onConnect,
            onRefresh = onRefresh,
        )

        ManualHostSection(onConnect = onManualConnect)
    }
}

/** The connection status, as a banner rather than a blocking dialog. */
@Composable
private fun StatusBanner(state: DeckUiState) {
    val (label, color) = when (state.connection) {
        ConnectionState.Connected -> "Connected to ${state.hostName}" to MaterialTheme.colorScheme.primary
        ConnectionState.Connecting -> "Connecting…" to MaterialTheme.colorScheme.primary
        ConnectionState.Pairing -> "Pairing…" to MaterialTheme.colorScheme.primary
        ConnectionState.Reconnecting -> (state.status ?: "Reconnecting…") to MaterialTheme.colorScheme.error
        ConnectionState.Disconnected -> (state.status ?: "Not connected") to MaterialTheme.colorScheme.onSurfaceVariant
    }

    Surface(
        color = color.copy(alpha = 0.12f),
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (state.connection == ConnectionState.Connecting ||
                state.connection == ConnectionState.Pairing ||
                state.connection == ConnectionState.Reconnecting
            ) {
                CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp, color = color)
                Spacer(Modifier.width(10.dp))
            }
            Text(
                text = label,
                color = color,
                style = MaterialTheme.typography.bodyMedium,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

@Composable
private fun ErrorCard(message: String, onDismiss: () -> Unit) {
    Card(
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.errorContainer),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier.padding(14.dp),
            verticalAlignment = Alignment.Top,
        ) {
            Icon(
                imageVector = Icons.Filled.ErrorOutline,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.onErrorContainer,
                modifier = Modifier.size(20.dp),
            )
            Spacer(Modifier.width(10.dp))
            Text(
                text = message,
                color = MaterialTheme.colorScheme.onErrorContainer,
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.weight(1f),
            )
            IconButton(onClick = onDismiss, modifier = Modifier.size(24.dp)) {
                Icon(
                    imageVector = Icons.Filled.Close,
                    contentDescription = "Dismiss",
                    tint = MaterialTheme.colorScheme.onErrorContainer,
                )
            }
        }
    }
}

/**
 * The pairing block: host name, the fingerprint to compare, and the PIN boxes.
 *
 * The PIN is only accepted once the probe has confirmed the host is open for
 * pairing, so the user is never asked to type a PIN into a host that will
 * reject it.
 */
@Composable
private fun PairingSection(pairing: PairingUiState, busy: Boolean, onPair: (String) -> Unit) {
    var pin by remember(pairing.host.key) { mutableStateOf("") }
    val focusRequester = remember { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current

    // Focus the PIN field as soon as it becomes usable.
    //
    // Without this the field is only reachable by tapping it exactly: the six
    // boxes the user sees are drawn views, not the text field, so a tap that
    // lands on a box does not always forward focus. A user who has just opened
    // this card wants to type the code, so focusing it for them is both the
    // expected behaviour and the only reliable way in.
    //
    // requestFocus throws when the node is not attached yet, which is the normal
    // case on the first composition, so it is retried rather than attempted
    // once: a single failed attempt would leave the field unfocused for the rest
    // of the screen's life because the effect's keys never change again.
    LaunchedEffect(pairing.ready, pairing.pairingOpen, busy) {
        if (!(pairing.ready && pairing.pairingOpen && !busy)) return@LaunchedEffect
        repeat(20) {
            val focused = runCatching { focusRequester.requestFocus() }.isSuccess
            if (focused) return@LaunchedEffect
            delay(100)
        }
    }

    Card(modifier = Modifier.fillMaxWidth()) {
        Column(
            modifier = Modifier.padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = pairing.hostName.ifBlank { pairing.host.authority },
                style = MaterialTheme.typography.titleMedium,
                fontWeight = FontWeight.SemiBold,
            )

            FingerprintBlock(fingerprint = pairing.fingerprint, hostOs = pairing.host.os)

            Text(
                text = if (pairing.pairingOpen) {
                    "Run `mobiledeck pair` on the host and type the 6-digit PIN it prints."
                } else {
                    "This host is not accepting pairing right now."
                },
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            PinEntry(
                value = pin,
                onValueChange = { raw -> pin = raw.filter { it.isDigit() }.take(6) },
                enabled = pairing.ready && pairing.pairingOpen && !busy,
                focusRequester = focusRequester,
                onDone = {
                    keyboard?.hide()
                    if (pin.length == 6) onPair(pin)
                },
            )

            Button(
                onClick = {
                    keyboard?.hide()
                    onPair(pin)
                },
                enabled = pairing.ready && pairing.pairingOpen && pin.length == 6 && !busy,
                modifier = Modifier.fillMaxWidth(),
            ) {
                if (busy) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(16.dp),
                        strokeWidth = 2.dp,
                        color = MaterialTheme.colorScheme.onPrimary,
                    )
                    Spacer(Modifier.width(8.dp))
                }
                Text("Pair")
            }

            if (pairing.ready && pairing.pairingOpen) {
                LaunchedEffect(pairing.host.key) { focusRequester.requestFocus() }
            }
        }
    }
}

/** The SHA-256 fingerprint, grouped for comparison against the host's output. */
@Composable
private fun FingerprintBlock(fingerprint: String?, hostOs: String) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                text = "TLS fingerprint",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            if (hostOs.isNotBlank()) {
                Spacer(Modifier.width(6.dp))
                OsBadge(hostOs)
            }
        }
        Text(
            text = fingerprint ?: "none — the host runs without TLS",
            style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
            color = if (fingerprint == null) {
                MaterialTheme.colorScheme.error
            } else {
                MaterialTheme.colorScheme.onSurface
            },
            maxLines = 3,
        )
    }
}

/**
 * Six PIN boxes.
 *
 * One transparent [BasicTextField] drives six drawn boxes rather than six real
 * fields: six fields would need focus juggling and would fight the keyboard,
 * and the PIN is a fixed-length code where the value matters far more than the
 * editing. The field itself has no decoration, so the boxes *are* the input.
 */
@Composable
private fun PinEntry(
    value: String,
    onValueChange: (String) -> Unit,
    enabled: Boolean,
    focusRequester: FocusRequester,
    onDone: () -> Unit,
) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .height(52.dp),
    ) {
        Row(
            modifier = Modifier.fillMaxSize(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            repeat(6) { index ->
                val filled = index < value.length
                val isCursor = index == value.length && enabled
                Box(
                    modifier = Modifier
                        .weight(1f)
                        .fillMaxSize()
                        .clip(RoundedCornerShape(10.dp))
                        .background(
                            when {
                                filled -> MaterialTheme.colorScheme.primaryContainer
                                isCursor -> MaterialTheme.colorScheme.surfaceVariant
                                else -> MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f)
                            },
                        )
                        .then(
                            if (isCursor) {
                                Modifier.border(
                                    width = 1.dp,
                                    color = MaterialTheme.colorScheme.primary,
                                    shape = RoundedCornerShape(10.dp),
                                )
                            } else {
                                Modifier
                            },
                        ),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = if (filled) value[index].toString() else "",
                        style = MaterialTheme.typography.headlineSmall,
                        fontWeight = FontWeight.SemiBold,
                        color = MaterialTheme.colorScheme.onSurface,
                    )
                }
            }
        }

        // The real input, invisible and covering the boxes: tapping anywhere on
        // them focuses it and raises the numeric keyboard.
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            enabled = enabled,
            singleLine = true,
            textStyle = LocalTextStyle.current.copy(color = Color.Transparent, fontSize = 1.sp),
            cursorBrush = SolidColor(Color.Transparent),
            keyboardOptions = KeyboardOptions(
                keyboardType = KeyboardType.NumberPassword,
                imeAction = ImeAction.Done,
            ),
            keyboardActions = KeyboardActions(onDone = { onDone() }),
            modifier = Modifier
                .fillMaxSize()
                .focusRequester(focusRequester)
                .clickable(enabled = enabled, indication = null, interactionSource = remember { MutableInteractionSource() }) {
                    // An explicit tap handler, because a click on the drawn boxes
                    // above does not reliably reach the field: they are siblings
                    // drawn over it, so the tap is consumed by whichever box it
                    // lands on. Requesting focus here makes a tap anywhere on the
                    // PIN area behave the way it looks like it should.
                    runCatching { focusRequester.requestFocus() }
                },
        )
    }
}

@Composable
private fun DiscoveredHostsSection(
    hosts: List<DiscoveredHost>,
    scanning: Boolean,
    onConnect: (DiscoveredHost) -> Unit,
    onRefresh: () -> Unit,
) {
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(
                imageVector = Icons.Filled.Wifi,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.size(18.dp),
            )
            Spacer(Modifier.width(8.dp))
            Text(
                text = "Discovered hosts",
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            if (scanning) {
                CircularProgressIndicator(modifier = Modifier.size(14.dp), strokeWidth = 2.dp)
            }
            IconButton(onClick = onRefresh, enabled = !scanning) {
                Icon(imageVector = Icons.Filled.Refresh, contentDescription = "Rescan")
            }
        }

        if (hosts.isEmpty()) {
            Text(
                text = "No hosts discovered. mDNS needs the phone and the host on the same network; " +
                    "the manual field below always works.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        } else {
            // A plain Column, not a LazyColumn: this list sits inside a
            // `verticalScroll` parent, and a lazy list would be measured with an
            // infinite maximum height and throw. The candidate set is a handful
            // of hosts on one LAN, so laziness would buy nothing anyway.
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                hosts.forEach { host ->
                    HostRow(host = host, onClick = { onConnect(host) })
                }
            }
        }
    }
}

@Composable
private fun HostRow(host: DiscoveredHost, onClick: () -> Unit) {
    Card(
        onClick = onClick,
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier.padding(14.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(
                imageVector = Icons.Filled.Computer,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
                modifier = Modifier.size(24.dp),
            )
            Spacer(Modifier.width(12.dp))
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = host.name,
                    style = MaterialTheme.typography.bodyLarge,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    text = buildAnnotatedString {
                        append(host.authority)
                        if (host.fingerprint.isNotEmpty()) {
                            withStyle(SpanStyle(color = MaterialTheme.colorScheme.primary)) {
                                append("  · pinned")
                            }
                        }
                    },
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            if (host.os.isNotBlank()) OsBadge(host.os)
            if (host.pairingOpen) {
                Spacer(Modifier.width(6.dp))
                Text(
                    text = "PIN",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.primary,
                    fontWeight = FontWeight.SemiBold,
                )
            }
        }
    }
}

/** A small `windows` / `linux` / `darwin` badge. */
@Composable
private fun OsBadge(os: String) {
    Surface(
        color = MaterialTheme.colorScheme.surfaceVariant,
        shape = RoundedCornerShape(6.dp),
    ) {
        Text(
            text = os,
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp),
        )
    }
}

/**
 * Manual `host:port` entry, always available (PROTOCOL.md §4.2).
 *
 * It accepts a bare host and assumes the default port, because typing a port on
 * a phone keyboard is the part people get wrong.
 */
@Composable
private fun ManualHostSection(onConnect: (String) -> Unit) {
    var value by remember { mutableStateOf("") }
    val keyboard = LocalSoftwareKeyboardController.current

    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(
            text = "Manual host",
            style = MaterialTheme.typography.titleSmall,
            fontWeight = FontWeight.SemiBold,
        )
        OutlinedTextField(
            value = value,
            onValueChange = { value = it.trim() },
            singleLine = true,
            label = { Text("host:port") },
            placeholder = { Text("192.168.1.10:8765") },
            keyboardOptions = KeyboardOptions(
                keyboardType = KeyboardType.Uri,
                imeAction = ImeAction.Go,
            ),
            keyboardActions = KeyboardActions(
                onGo = {
                    keyboard?.hide()
                    if (value.isNotBlank()) onConnect(value)
                },
            ),
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedButton(
            onClick = {
                keyboard?.hide()
                onConnect(value)
            },
            enabled = value.isNotBlank(),
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text("Connect")
        }
    }
}
