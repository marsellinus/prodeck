package dev.mobiledeck

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge

/**
 * The app's only activity.
 *
 * Edge-to-edge is requested explicitly rather than through a theme flag so the
 * behaviour is identical on API 26 (where the platform had no such concept) and
 * on API 35+ (where it is enforced and can no longer be opted out of). The deck
 * is a full-screen grid of tiles, so the inset handling that matters is done by
 * the composables that own the edges: the top bar applies
 * `statusBarsPadding()` and the grid applies `navigationBarsPadding()`.
 */
class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContent { App() }
    }
}
