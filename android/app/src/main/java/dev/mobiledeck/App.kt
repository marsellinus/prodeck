package dev.mobiledeck

import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import dev.mobiledeck.data.ConnectionState
import dev.mobiledeck.data.DeckStore
import dev.mobiledeck.data.DiscoveredHost
import dev.mobiledeck.ui.ConnectScreen
import dev.mobiledeck.ui.DeckScreen
import dev.mobiledeck.ui.Haptics
import dev.mobiledeck.ui.theme.DeckTheme

/**
 * The root composable.
 *
 * Screen switching is a two-way `when` on the connection state rather than a
 * navigation graph: there are exactly two screens, and a NavHost would add a
 * dependency, a back-stack model and an argument-serialisation concern for a
 * decision that is one boolean. The deck's own navigation (pages, breadcrumbs)
 * is *inside* the deck and comes from the profile, not from the app's back
 * stack — that is the point of the breadcrumb in `DeckUiState`.
 */
@Composable
fun App(store: DeckStore = viewModel()) {
    val state by store.state.collectAsStateWithLifecycle()
    val context = LocalContext.current

    // `settings.haptic` belongs to the profile, so the vibrator is rebuilt when
    // a profile changes rather than captured once at launch.
    val haptics = remember(state.profile?.settings?.haptic) {
        Haptics(context, enabled = state.profile?.settings?.haptic ?: true)
    }

    DeckTheme(theme = state.profile?.theme) {
        Surface(
            color = MaterialTheme.colorScheme.background,
            modifier = Modifier.fillMaxSize(),
        ) {
            val showDeck = state.profile != null &&
                (state.connection == ConnectionState.Connected ||
                    state.connection == ConnectionState.Reconnecting ||
                    state.connection == ConnectionState.Connecting)

            if (showDeck) {
                DeckScreen(
                    state = state,
                    haptics = haptics,
                    onPress = { buttonId, kind -> store.press(buttonId, kind) },
                    onRelease = { buttonId, heldMs -> store.release(buttonId, heldMs) },
                    onNavigate = store::navigate,
                    onBack = store::back,
                    onSwitchProfile = store::switchProfile,
                    onReconnect = store::reconnect,
                    onDisconnect = store::disconnect,
                )
            } else {
                ConnectScreen(
                    state = state,
                    onConnect = { host -> store.connect(host) },
                    onManualConnect = store::connectManual,
                    onPair = { pin ->
                        state.pairing?.host?.let { host -> store.pair(host, pin) }
                    },
                    onDismissError = store::dismissError,
                    onRefresh = store::rescan,
                )
            }
        }
    }

    // A pairing error that the user has not seen must not be lost when the
    // screen switches away from the pairing block.
    LaunchedEffect(state.error) {
        if (state.error != null) haptics.error()
    }
}
