package com.opendvr.tv

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import com.opendvr.tv.ui.ChannelsScreen
import com.opendvr.tv.ui.GuideScreen
import com.opendvr.tv.ui.PlayerScreen
import com.opendvr.tv.ui.RecordingPlayerScreen
import com.opendvr.tv.ui.RecordingsScreen
import com.opendvr.tv.ui.SetupScreen
import com.opendvr.tv.ui.SignInScreen
import com.opendvr.tv.ui.Tab
import com.opendvr.tv.ui.Theme

/** Which screen is showing. The app is small enough for a plain state machine. */
sealed class Screen {
    data object Loading : Screen()
    data class Setup(val error: String? = null) : Screen()
    data object SignIn : Screen()
    data object Channels : Screen()
    data object Guide : Screen()
    data object Recordings : Screen()

    /** Playing a recording; back returns to the recordings list. */
    data class Playback(val id: String) : Screen()

    /** Playing a channel; back returns to the screen it was picked from. */
    data class Player(val channel: String, val back: Screen = Channels) : Screen()
}

/**
 * The screen an opendvr://open/<path> link asks for, mirroring the web
 * app's paths. Anything else, or an id with odd characters, is ignored:
 * any web page can send this link.
 */
fun linkTarget(intent: Intent?): Screen? {
    val uri = intent?.data ?: return null
    if (intent.action != Intent.ACTION_VIEW || uri.scheme != "opendvr" || uri.host != "open") return null
    val parts = uri.pathSegments.orEmpty()
    val id = parts.getOrNull(1)?.takeIf { Regex("[A-Za-z0-9._-]{1,64}").matches(it) }
    return when (parts.firstOrNull()) {
        null -> Screen.Channels
        "watch" -> id?.let { Screen.Player(it) }
        "guide" -> Screen.Guide
        "dvr" -> Screen.Recordings
        "recordings" -> id?.let { Screen.Playback(it) }
        else -> null
    }
}

class MainActivity : ComponentActivity() {
    // Set by a link; the app goes there once it is past setup and sign-in.
    private val link = mutableStateOf<Screen?>(null)

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        linkTarget(intent)?.let { link.value = it }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (savedInstanceState == null) link.value = linkTarget(intent)
        val prefs = Prefs(this)
        val autoName = deviceLabel(this)
        val api = Api(prefs, autoName)
        setContent {
            Theme {
                Box(Modifier.fillMaxSize().background(Color(0xFF0F1115))) {
                    Root(prefs, api, autoName, link)
                }
            }
        }
    }
}

@Composable
private fun Root(prefs: Prefs, api: Api, autoName: String, link: MutableState<Screen?>) {
    var screen by remember { mutableStateOf<Screen>(Screen.Loading) }

    // Decide the first screen: no server yet, sign-in needed, or straight
    // to the channel list.
    suspend fun start() {
        if (prefs.serverUrl.isEmpty()) {
            screen = Screen.Setup()
            return
        }
        screen = try {
            val me = api.me()
            if (me.enabled && me.userName == null) {
                Screen.SignIn
            } else {
                val target = link.value
                link.value = null
                if (target is Screen.Player) prefs.lastChannel = target.channel
                target ?: Screen.Channels
            }
        } catch (e: Exception) {
            Screen.Setup(error = "Cannot reach ${prefs.serverUrl}: ${e.message}")
        }
    }
    // Loading is the hub: every screen that wants a fresh decision sets it.
    LaunchedEffect(screen) { if (screen == Screen.Loading) start() }
    // A link while the app is open: decide afresh, which follows it. On the
    // setup and sign-in screens it waits until those are done.
    LaunchedEffect(link.value) {
        val s = screen
        if (link.value != null && s != Screen.Loading && s !is Screen.Setup && s != Screen.SignIn) screen = Screen.Loading
    }

    val onTab: (Tab) -> Unit = { t ->
        screen = when (t) {
            Tab.Channels -> Screen.Channels
            Tab.Guide -> Screen.Guide
            Tab.Recordings -> Screen.Recordings
            Tab.Settings -> Screen.Setup()
        }
    }

    when (val s = screen) {
        Screen.Loading -> Unit
        is Screen.Setup -> {
            val configured = prefs.serverUrl.isNotEmpty()
            if (configured) BackHandler { screen = Screen.Channels }
            SetupScreen(
                prefs, api, autoName = autoName, initialError = s.error,
                onDone = { screen = Screen.Loading }, onRestart = { screen = Screen.Loading },
                onCancel = if (configured && s.error == null) ({ screen = Screen.Channels }) else null,
            )
        }
        Screen.SignIn -> SignInScreen(api, onSignedIn = { screen = if (link.value != null) Screen.Loading else Screen.Channels }, onChangeServer = { screen = Screen.Setup() })
        Screen.Channels -> {
            // Look for a newer app build once signed in; Settings offers it.
            LaunchedEffect(Unit) { Updater.check(api) }
            ChannelsScreen(
            prefs, api,
            onPlay = { ch -> prefs.lastChannel = ch; screen = Screen.Player(ch, back = Screen.Channels) },
            onTab = onTab,
            onUnauthenticated = { screen = Screen.SignIn },
        )
        }
        Screen.Guide -> {
            BackHandler { screen = Screen.Channels }
            GuideScreen(
                prefs, api,
                onPlay = { ch -> prefs.lastChannel = ch; screen = Screen.Player(ch, back = Screen.Guide) },
                onTab = onTab,
                onUnauthenticated = { screen = Screen.SignIn },
            )
        }
        Screen.Recordings -> {
            BackHandler { screen = Screen.Channels }
            RecordingsScreen(api, onPlay = { id -> screen = Screen.Playback(id) }, onTab = onTab, onUnauthenticated = { screen = Screen.SignIn })
        }
        is Screen.Playback -> {
            BackHandler { screen = Screen.Recordings }
            RecordingPlayerScreen(api, id = s.id, onExit = { screen = Screen.Recordings })
        }
        is Screen.Player -> {
            BackHandler { screen = s.back }
            PlayerScreen(
                prefs, api, channel = s.channel,
                onChannel = { ch -> prefs.lastChannel = ch; screen = Screen.Player(ch, back = s.back) },
                onExit = { screen = s.back },
                onGuide = { screen = Screen.Guide },
                onUnauthenticated = { screen = Screen.SignIn },
            )
        }
    }
}
