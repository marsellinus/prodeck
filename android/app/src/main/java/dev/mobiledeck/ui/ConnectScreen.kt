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
import androidx.compose.material.icons.filled.Usb
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
import dev.mobiledeck.data.DeckStore
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
            PairingSection(pairing = pairing, busy = pairing.submitting, onPair = onPair)
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
        ConnectionState.Pairing -> "Waiting for a PIN" to MaterialTheme.colorScheme.primary
        ConnectionState.Reconnecting -> (state.status ?: "Reconnecting…") to MaterialTheme.colorScheme.error
        ConnectionState.Disconnected ->
            (state.status ?: "Not connected. Pick your computer below.") to MaterialTheme.colorScheme.onSurfaceVariant
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

/**
 * What to try, shown above the host's own sentence.
 *
 * The host's sentence is kept because it is the specific part ("PIN is wrong,
 * expired, or already used") and rewriting it here would lose that. What it
 * never says is what the person should do about it, and a card that only reports
 * a failure leaves them with a screen and no next move — so the plain sentence
 * comes first and the host's detail follows it.
 *
 * The classification is on our own messages (`PairingApi`, `CloseCode`), which
 * is why it can be this blunt: they are a closed set, and an unrecognised one
 * falls through to advice that is true of every network failure.
 */
private fun errorAdvice(message: String): String = when {
    message.contains("PIN", ignoreCase = true) || message.contains("attempt", ignoreCase = true) ->
        "Type the 6-digit PIN from the control panel on your computer. " +
            "Press “Show a PIN” there if it has expired."

    message.contains("revoked", ignoreCase = true) || message.contains("token", ignoreCase = true) ->
        "This phone has to be set up again: press “Show a PIN” on your computer, then type the new PIN below."

    message.contains("fingerprint", ignoreCase = true) || message.contains("TLS", ignoreCase = true) ->
        "Connect from the list of computers below instead of typing an address."

    message.contains("busy", ignoreCase = true) || message.contains("rate", ignoreCase = true) ->
        "Wait a minute, then try again — the computer is refusing new connections for now."

    else -> "Check the phone and the computer are on the same Wi-Fi, then try again."
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
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = errorAdvice(message),
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    style = MaterialTheme.typography.bodyMedium,
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    text = message,
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    style = MaterialTheme.typography.bodySmall,
                )
            }
            IconButton(onClick = onDismiss, modifier = Modifier.size(24.dp)) {
                Icon(
                    imageVector = Icons.Filled.Close,
                    contentDescription = "Close this message",
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
                    "On your computer, open the control panel and press “Connect a phone”, then press " +
                        "“Show a PIN”. Type that PIN here. It is good for two minutes."
                } else {
                    "Open the control panel on your computer and press “Connect a phone”, then press " +
                        "“Show a PIN”. The PIN box below comes alive the moment you do."
                },
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            // The terminal path, kept second and kept small: the panel is the way
            // in for the person this screen is for, but a user who is already in
            // a shell should not have to go looking for a window they may not
            // have open.
            Text(
                text = "Prefer a terminal? Run `mobiledeck pair` on the computer: it prints the same PIN.",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            PinEntry(
                value = pin,
                onValueChange = { raw -> pin = raw.filter { it.isDigit() }.take(6) },
                // Always editable. The field used to be disabled until the host
                // reported its pairing window open, which made it untappable: a
                // user who connected first and ran `mobiledeck pair` afterwards
                // could read the instruction and never act on it. Only the Pair
                // button is gated, because that is the action that needs a live
                // window; typing the code harms nothing and the host rejects it
                // if the window is shut.
                enabled = pairing.ready,
                focusRequester = focusRequester,
                onDone = {
                    keyboard?.hide()
                    if (pin.length == 6 && pairing.pairingOpen && !busy) onPair(pin)
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
                Text("Connect this phone")
            }

            if (pairing.ready && pairing.pairingOpen) {
                LaunchedEffect(pairing.host.key) { focusRequester.requestFocus() }
            }
        }
    }
}

/**
 * The security code, grouped for comparison against the one the computer shows.
 *
 * The comparison is the whole point of the block, so the block says so: a code
 * with no instruction to check it against anything is decoration, and a user who
 * has never been told to compare it cannot tell a real host from a rogue one
 * that answered mDNS first (docs/SECURITY.md T9). The "none" case is stated in
 * terms of what it means for the user rather than as a missing value, because
 * "none" on its own reads as something being broken.
 */
@Composable
private fun FingerprintBlock(fingerprint: String?, hostOs: String) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                text = "Security code",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            if (hostOs.isNotBlank()) {
                Spacer(Modifier.width(6.dp))
                OsBadge(hostOs)
            }
        }
        Text(
            text = fingerprint ?: "no code — this computer is not using a secure connection",
            style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
            color = if (fingerprint == null) {
                MaterialTheme.colorScheme.error
            } else {
                MaterialTheme.colorScheme.onSurface
            },
            maxLines = 3,
        )
        Text(
            text = if (fingerprint == null) {
                "Fine at home on your own Wi-Fi. On public Wi-Fi, someone nearby could read your PIN."
            } else {
                "Compare it with the code on your computer — `mobiledeck status` prints it. " +
                    "If they differ, stop: this may not be your computer."
            },
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
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
            .height(52.dp)
            // A tap anywhere in the PIN area focuses the field. The six boxes
            // below are drawn views layered over the text field, so a tap that
            // lands on a box is not guaranteed to reach the field itself; this
            // makes the whole area behave the way it looks like it should.
            .clickable(
                enabled = enabled,
                indication = null,
                interactionSource = remember { MutableInteractionSource() },
            ) {
                runCatching { focusRequester.requestFocus() }
            },
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
                text = "Your computers",
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            if (scanning) {
                CircularProgressIndicator(modifier = Modifier.size(14.dp), strokeWidth = 2.dp)
            }
            IconButton(onClick = onRefresh, enabled = !scanning) {
                Icon(imageVector = Icons.Filled.Refresh, contentDescription = "Look again")
            }
        }

        if (hosts.isEmpty()) {
            Text(
                text = "None found. Check MobileDeck is running on your computer and that both are on the " +
                    "same Wi-Fi. If that fails, type the address below instead.",
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
                                // The same word the control panel uses for the same
                                // fact, so the two screens cannot describe one host
                                // differently.
                                append("  ·  secure")
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
                    text = "PIN ready",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.primary,
                    fontWeight = FontWeight.SemiBold,
                )
            }
        }
    }
}

/** A small `Windows` / `Linux` / `macOS` badge. */
@Composable
private fun OsBadge(os: String) {
    Surface(
        color = MaterialTheme.colorScheme.surfaceVariant,
        shape = RoundedCornerShape(6.dp),
    ) {
        Text(
            // `darwin` is what the host calls macOS, and the kernel's name for a
            // computer is not the name the person reading it uses.
            text = plainOsName(os),
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp),
        )
    }
}

/**
 * Manual address entry, always available (PROTOCOL.md §4.2).
 *
 * It accepts a bare host and assumes the default port, because typing a port on
 * a phone keyboard is the part people get wrong. The label still says `host:port`
 * rather than the plain phrase: the user reads the address off another screen and
 * has to be able to tell that the two are the same kind of thing.
 */
@Composable
private fun ManualHostSection(onConnect: (String) -> Unit) {
    var value by remember { mutableStateOf("") }
    val keyboard = LocalSoftwareKeyboardController.current

    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(
            text = "Your computer's address",
            style = MaterialTheme.typography.titleSmall,
            fontWeight = FontWeight.SemiBold,
        )
        OutlinedTextField(
            value = value,
            onValueChange = { value = it.trim() },
            singleLine = true,
            label = { Text("address") },
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

        // The cable path. `adb reverse tcp:8765 tcp:8765` makes the phone's own
        // loopback reach the host's port, so no address has to be typed and the
        // traffic never touches the network. Offered as a button rather than
        // discovered, because there is nothing to discover: the address is
        // always loopback, and it simply fails if the reverse is not set up.
        OutlinedButton(
            onClick = {
                keyboard?.hide()
                onConnect("127.0.0.1:${DeckStore.DEFAULT_HOST_PORT}")
            },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Icon(
                imageVector = Icons.Filled.Usb,
                contentDescription = null,
                modifier = Modifier.size(18.dp),
            )
            Spacer(Modifier.width(8.dp))
            Text("Connect with a cable")
        }
        Text(
            text = "For a cable: run this once on your computer, then press the button above.\n" +
                "adb reverse tcp:${DeckStore.DEFAULT_HOST_PORT} tcp:${DeckStore.DEFAULT_HOST_PORT}",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}
