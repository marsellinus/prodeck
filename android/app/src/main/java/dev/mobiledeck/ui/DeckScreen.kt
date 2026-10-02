package dev.mobiledeck.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.mobiledeck.data.Button
import dev.mobiledeck.data.ButtonState
import dev.mobiledeck.data.ConnectionState
import dev.mobiledeck.data.DeckStore
import dev.mobiledeck.data.DeckUiState
import dev.mobiledeck.data.Grid
import dev.mobiledeck.data.Icon
import dev.mobiledeck.data.Page
import dev.mobiledeck.ui.theme.LocalDeckDimensions

/**
 * The deck: header, grid, page tabs, breadcrumb and the settings sheet
 * (docs/ARCHITECTURE.md §4).
 *
 * The grid is sized from `settings.grid` / `page.grid` on every frame — there is
 * no compiled-in column count. ARCHITECTURE.md §4 is explicit that the UI
 * "renders whatever profile JSON the host sent. There is no compiled-in button,
 * no compiled-in grid size, and no hardcoded host."
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DeckScreen(
    state: DeckUiState,
    haptics: Haptics,
    onPress: (buttonId: String, kind: String) -> Unit,
    onRelease: (buttonId: String, heldMs: Long) -> Unit,
    onNavigate: (pageId: String) -> Unit,
    onBack: () -> Unit,
    onSwitchProfile: (profileId: String) -> Unit,
    onReconnect: () -> Unit,
    onDisconnect: () -> Unit,
    modifier: Modifier = Modifier,
) {
    var sheetOpen by remember { mutableStateOf(false) }
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)

    Box(
        modifier = modifier
            .fillMaxSize()
            .background(MaterialTheme.colorScheme.background),
    ) {
        Column(modifier = Modifier.fillMaxSize()) {
            DeckTopBar(
                state = state,
                onBack = onBack,
                onOpenSettings = { sheetOpen = true },
            )

            ConnectionStrip(state = state, onReconnect = onReconnect)

            val profile = state.profile
            if (profile == null) {
                EmptyDeck(state = state, onReconnect = onReconnect)
            } else {
                if (profile.settings.nav.pageTabs && profile.pages.size > 1) {
                    PageTabs(
                        pages = profile.pages,
                        currentPageId = state.page?.id.orEmpty(),
                        onNavigate = onNavigate,
                    )
                }

                if (profile.settings.nav.breadcrumb && state.breadcrumbs.size > 1) {
                    Breadcrumbs(names = state.breadcrumbNames, onJump = { index ->
                        // Jumping to a crumb truncates the stack there, which is
                        // what a breadcrumb means: the pages after it are left.
                        val target = state.breadcrumbs.getOrNull(index) ?: return@Breadcrumbs
                        onNavigate(target)
                    })
                }

                ButtonGrid(
                    state = state,
                    haptics = haptics,
                    onPress = onPress,
                    onRelease = onRelease,
                    modifier = Modifier.weight(1f),
                )
            }
        }
    }

    if (sheetOpen) {
        ModalBottomSheet(
            onDismissRequest = { sheetOpen = false },
            sheetState = sheetState,
        ) {
            SettingsSheet(
                state = state,
                onSwitchProfile = onSwitchProfile,
                onReconnect = onReconnect,
                onDisconnect = {
                    sheetOpen = false
                    onDisconnect()
                },
            )
        }
    }
}

/** Profile name, the back affordance and the settings entry point. */
@Composable
private fun DeckTopBar(
    state: DeckUiState,
    onBack: () -> Unit,
    onOpenSettings: () -> Unit,
) {
    val showBack = state.profile?.settings?.nav?.backButton != false && state.breadcrumbs.size > 1

    Surface(
        color = MaterialTheme.colorScheme.background,
        modifier = Modifier
            .fillMaxWidth()
            .statusBarsPadding(),
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(56.dp)
                .padding(horizontal = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (showBack) {
                IconButton(onClick = onBack) {
                    Icon(
                        imageVector = Icons.AutoMirrored.Filled.ArrowBack,
                        contentDescription = "Back",
                        tint = MaterialTheme.colorScheme.onBackground,
                    )
                }
            } else {
                Spacer(Modifier.width(12.dp))
            }

            Text(
                text = state.profile?.name?.takeIf { it.isNotBlank() }
                    ?: state.hostName.ifBlank { "MobileDeck" },
                style = MaterialTheme.typography.titleMedium,
                fontWeight = FontWeight.SemiBold,
                color = MaterialTheme.colorScheme.onBackground,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )

            if (state.profileIsCached) {
                Text(
                    text = "saved copy",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(end = 4.dp),
                )
            }

            IconButton(onClick = onOpenSettings) {
                Icon(
                    imageVector = Icons.Filled.Settings,
                    contentDescription = "Settings",
                    tint = MaterialTheme.colorScheme.onBackground,
                )
            }
        }
    }
}

/**
 * The reconnect banner.
 *
 * ARCHITECTURE.md §4: a dead network shows a grey "Reconnecting…" banner over a
 * usable, non-interactive grid instead of a blank screen. The tiles stay visible
 * and dimmed (see [DeckButton]) rather than being replaced by a spinner.
 *
 * The banner leads with the host's own sentence when there is one, because it is
 * the specific part ("Reconnecting to study-laptop…", "Token rejected or revoked.
 * Pair again."), and adds a plain line underneath saying whether anything is
 * expected of the reader. A banner that only reports state leaves someone staring
 * at a dimmed board with no way to tell waiting from broken.
 */
@Composable
private fun ConnectionStrip(state: DeckUiState, onReconnect: () -> Unit) {
    val visible = state.connection != ConnectionState.Connected || state.profileIsCached
    if (!visible) return

    val reconnecting = state.connection == ConnectionState.Reconnecting ||
        state.connection == ConnectionState.Connecting ||
        state.connection == ConnectionState.Pairing

    // Three different situations share this strip, and they need different words:
    // a live session that is still fetching the board, a session that is retrying,
    // and no session at all. The colours are unchanged (grey while retrying, red
    // otherwise) — only the sentences are.
    val liveButCached = state.connection == ConnectionState.Connected && state.profileIsCached
    val lead = state.status ?: when {
        reconnecting -> if (state.connection == ConnectionState.Connecting) "Connecting…" else "Reconnecting…"
        liveButCached -> "Getting your board from the computer…"
        else -> "Not connected."
    }
    val advice = when {
        reconnecting -> "This keeps trying by itself."
        liveButCached -> "The board below is the copy saved on this phone."
        else -> "Press Retry, or check MobileDeck is running on your computer."
    }

    Surface(
        color = if (reconnecting) {
            MaterialTheme.colorScheme.surfaceVariant
        } else {
            MaterialTheme.colorScheme.errorContainer
        },
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (reconnecting) {
                CircularProgressIndicator(modifier = Modifier.size(14.dp), strokeWidth = 2.dp)
                Spacer(Modifier.width(10.dp))
            }
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = lead,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 2,
                )
                Text(
                    text = advice,
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 2,
                )
            }
            if (!reconnecting) {
                Text(
                    text = "Retry",
                    style = MaterialTheme.typography.labelLarge,
                    color = MaterialTheme.colorScheme.primary,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier
                        .clip(RoundedCornerShape(6.dp))
                        .padding(horizontal = 8.dp, vertical = 4.dp)
                        .clickableNoRipple(onReconnect),
                )
            }
        }
    }
}

/** A click with no ripple, for text affordances inside a status strip. */
private fun Modifier.clickableNoRipple(onClick: () -> Unit): Modifier =
    this.clickable(
        interactionSource = null,
        indication = null,
        onClick = onClick,
    )

@Composable
private fun EmptyDeck(state: DeckUiState, onReconnect: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        CircularProgressIndicator(modifier = Modifier.size(28.dp), strokeWidth = 3.dp)
        Spacer(Modifier.height(16.dp))
        Text(
            text = state.status ?: "Getting your board from the computer…",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
        )
        Spacer(Modifier.height(4.dp))
        Text(
            // This screen is what a first-time user sees before anything arrives,
            // so it names the two things that could be wrong rather than leaving
            // a spinner to explain itself.
            text = "This takes a moment the first time. If it does not finish, check MobileDeck is " +
                "running on your computer and that both are on the same Wi-Fi.",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
        )
        Spacer(Modifier.height(12.dp))
        Text(
            text = "Try again",
            style = MaterialTheme.typography.labelLarge,
            color = MaterialTheme.colorScheme.primary,
            modifier = Modifier
                .clip(RoundedCornerShape(8.dp))
                .padding(horizontal = 12.dp, vertical = 8.dp)
                .clickableNoRipple(onReconnect),
        )
    }
}

/** A horizontally scrolling tab bar of the profile's pages. */
@Composable
private fun PageTabs(pages: List<Page>, currentPageId: String, onNavigate: (String) -> Unit) {
    LazyRow(
        modifier = Modifier.fillMaxWidth(),
        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 6.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(items = pages, key = { it.id }) { page ->
            val selected = page.id == currentPageId
            Surface(
                color = if (selected) {
                    MaterialTheme.colorScheme.primary
                } else {
                    MaterialTheme.colorScheme.surfaceVariant
                },
                shape = RoundedCornerShape(8.dp),
                onClick = { onNavigate(page.id) },
            ) {
                Text(
                    text = page.name.ifBlank { page.id },
                    style = MaterialTheme.typography.labelLarge,
                    color = if (selected) {
                        MaterialTheme.colorScheme.onPrimary
                    } else {
                        MaterialTheme.colorScheme.onSurfaceVariant
                    },
                    maxLines = 1,
                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
                )
            }
        }
    }
}

/** The path from the root page to the current one. */
@Composable
private fun Breadcrumbs(names: List<String>, onJump: (Int) -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 14.dp, vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        names.forEachIndexed { index, name ->
            if (index > 0) {
                Text(
                    text = "›",
                    style = MaterialTheme.typography.labelMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 4.dp),
                )
            }
            val last = index == names.lastIndex
            Text(
                text = name,
                style = MaterialTheme.typography.labelMedium,
                fontWeight = if (last) FontWeight.SemiBold else FontWeight.Normal,
                color = if (last) {
                    MaterialTheme.colorScheme.onBackground
                } else {
                    MaterialTheme.colorScheme.primary
                },
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = if (last) {
                    Modifier
                } else {
                    Modifier
                        .clip(RoundedCornerShape(4.dp))
                        .clickableNoRipple { onJump(index) }
                },
            )
        }
    }
}

/**
 * The tile grid.
 *
 * Sized from the profile, never hardcoded: [LazyVerticalGrid] is given a fixed
 * column count from `settings.grid.columns` / `page.grid.columns`, and each
 * button is placed with [GridItemSpan] from its `cell.row_span` /
 * `cell.column_span`.
 *
 * Rows are *not* used to build the grid, because a `LazyVerticalGrid` is a
 * single flow: a button's `cell.row` is honoured by ordering the buttons by row
 * then column before placing them, which reproduces the documented layout for
 * the dense grids a deck uses (every cell filled, or filled top-left to
 * bottom-right). A sparse grid with a hole would flow around it, which is the
 * one visible difference from the host's model.
 */
@Composable
private fun ButtonGrid(
    state: DeckUiState,
    haptics: Haptics,
    onPress: (buttonId: String, kind: String) -> Unit,
    onRelease: (buttonId: String, heldMs: Long) -> Unit,
    modifier: Modifier = Modifier,
) {
    val page = state.page ?: return
    val grid = state.grid
    val dimensions = LocalDeckDimensions.current
    val gridState = rememberLazyGridState()

    // `cell.row` orders the tiles; within a row, `cell.column` does. Stable
    // sorting keeps a document's own order for buttons that declare no cell.
    val placed = remember(page) {
        page.buttons
            .filterNot { it.hidden }
            .sortedWith(compareBy({ it.cell.row }, { it.cell.column }))
    }

    val columns = grid.columns.coerceIn(1, Grid.MAX_COLUMNS)

    LazyVerticalGrid(
        columns = GridCells.Fixed(columns),
        state = gridState,
        modifier = modifier
            .fillMaxSize()
            .navigationBarsPadding(),
        contentPadding = PaddingValues(dimensions.spacing),
        horizontalArrangement = Arrangement.spacedBy(dimensions.spacing),
        verticalArrangement = Arrangement.spacedBy(dimensions.spacing),
    ) {
        items(
            items = placed,
            key = { it.id },
            span = { button ->
                val span = button.cell.columnSpan.coerceIn(1, columns)
                GridItemSpan(span)
            },
        ) { button ->
            val key = DeckStore.stateKey(
                profileId = state.profile?.id.orEmpty().ifBlank { state.activeProfileId },
                pageId = page.id,
                buttonId = button.id,
            )
            val override = state.buttonStates[key]
            val telemetry = telemetryFor(button, state)

            // Only an `image` icon has a data URI to resolve; every other kind
            // renders from its own value and must not pay for a lookup.
            val iconUri = state.profile
                ?.takeIf { button.icon?.type?.lowercase() == Icon.IMAGE }
                ?.iconDataUri(button.icon?.value.orEmpty())

            DeckButton(
                button = button,
                override = override,
                telemetryValue = telemetry,
                interactive = state.interactive,
                haptics = haptics,
                onPress = { kind -> onPress(button.id, kind) },
                onRelease = { held -> onRelease(button.id, held) },
                // The tile's height is driven by the grid: a fixed aspect ratio
                // would overflow a row on a wide screen and would not honour a
                // profile whose rows are few.
                modifier = Modifier
                    .fillMaxWidth()
                    .height(tileHeight(grid)),
                iconBitmap = rememberIconBitmap(iconUri),
            )
        }
    }
}

/**
 * A tile's height.
 *
 * Derived from the grid's row count so a 4x5 profile and a 2x2 profile both fit
 * one screen: the deck's whole point is that every button is reachable without
 * scrolling. `row_span` is not applied to the height because a
 * `LazyVerticalGrid` cannot express a row span — a spanning tile would need a
 * custom layout — and a taller tile for a 2-row button would push the rest of
 * the grid off screen. It is applied to the width, which is where it is visible.
 */
private fun tileHeight(grid: Grid): androidx.compose.ui.unit.Dp {
    // 460 dp of usable height is a conservative phone screen minus chrome, so a
    // 5-row grid gets ~92 dp tiles and a 2-row grid gets ~160 dp ones.
    val rows = grid.rows.coerceIn(1, Grid.MAX_ROWS)
    return (460 / rows).coerceIn(64, 160).dp
}

/** The telemetry value bound to a button, or null when there is none. */
private fun telemetryFor(button: Button, state: DeckUiState): Double? {
    if (button.state.type != ButtonState.TELEMETRY) return null
    val metric = button.state.metric ?: return null
    // PROTOCOL.md §7: a metric absent from `values` is unavailable, and the
    // tile renders `--`. Returning null rather than 0.0 is what keeps the UI
    // from reporting a reading the host never sent.
    return state.telemetry[metric]
}

/** The settings sheet: profile switching, reconnect and disconnect. */
@Composable
private fun SettingsSheet(
    state: DeckUiState,
    onSwitchProfile: (String) -> Unit,
    onReconnect: () -> Unit,
    onDisconnect: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .navigationBarsPadding()
            .padding(horizontal = 20.dp, vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Text(
            text = "Your computer",
            style = MaterialTheme.typography.titleSmall,
            fontWeight = FontWeight.SemiBold,
        )
        InfoRow("Name", state.hostName.ifBlank { "not known yet" })
        InfoRow("System", state.hostOs.takeIf { it.isNotBlank() }?.let(::plainOsName) ?: "not known yet")
        InfoRow("Right now", plainConnectionName(state.connection))
        // The device id is a hex string the host uses to recognise this phone;
        // it means nothing to the person holding it. The one thing about it they
        // can act on is whether this phone is paired at all, so that is the row.
        if (state.deviceId.isBlank()) {
            InfoRow("This phone", "not paired yet")
        }
        if (state.scopes.isNotEmpty()) {
            // A comma-joined list of scope ids is a permission the user cannot
            // read and therefore cannot check. The words are the panel's own, so
            // the two screens cannot describe one permission differently, and a
            // scope this build does not know still prints rather than vanishing.
            InfoRow("This phone may", state.scopes.sorted().joinToString(", ") { plainScopeName(it) })
        }

        if (state.telemetry.isNotEmpty()) {
            Spacer(Modifier.height(2.dp))
            Text(
                text = "Readings from your computer",
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
            )
            state.telemetry.entries
                .sortedBy { it.key }
                .take(6)
                .forEach { (metric, value) ->
                    InfoRow(plainMetricName(metric), formatNumber(value))
                }
        }

        if (state.profiles.isNotEmpty()) {
            Spacer(Modifier.height(2.dp))
            Text(
                text = "Boards",
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
            )
            state.profiles.forEach { summary ->
                Surface(
                    color = if (summary.id == state.activeProfileId) {
                        MaterialTheme.colorScheme.primaryContainer
                    } else {
                        MaterialTheme.colorScheme.surfaceVariant
                    },
                    shape = RoundedCornerShape(8.dp),
                    onClick = { onSwitchProfile(summary.id) },
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Row(
                        modifier = Modifier.padding(horizontal = 12.dp, vertical = 10.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(
                            text = summary.name.ifBlank { summary.id },
                            style = MaterialTheme.typography.bodyMedium,
                            modifier = Modifier.weight(1f),
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                        Text(
                            text = if (summary.pages.size == 1) "1 screen" else "${summary.pages.size} screens",
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                }
            }
        }

        Spacer(Modifier.height(4.dp))
        Surface(
            color = MaterialTheme.colorScheme.surfaceVariant,
            shape = RoundedCornerShape(8.dp),
            onClick = onReconnect,
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(
                text = "Connect again",
                style = MaterialTheme.typography.labelLarge,
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 12.dp),
            )
        }
        Surface(
            color = MaterialTheme.colorScheme.errorContainer,
            shape = RoundedCornerShape(8.dp),
            onClick = onDisconnect,
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(
                text = "Disconnect this phone",
                style = MaterialTheme.typography.labelLarge,
                color = MaterialTheme.colorScheme.onErrorContainer,
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 12.dp),
            )
        }
        Spacer(Modifier.height(12.dp))
    }
}

@Composable
private fun InfoRow(label: String, value: String) {
    Row(modifier = Modifier.fillMaxWidth()) {
        Text(
            text = label,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.width(96.dp),
        )
        Text(
            text = value,
            style = MaterialTheme.typography.bodySmall,
            modifier = Modifier.weight(1f),
        )
    }
}
