package com.opendvr.tv.ui

import android.view.KeyEvent
import androidx.compose.foundation.background
import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.nativeKeyCode
import androidx.compose.ui.input.key.onKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.compose.runtime.rememberCoroutineScope
import kotlinx.coroutines.launch
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.ui.PlayerView
import com.opendvr.tv.Api
import com.opendvr.tv.Channel
import com.opendvr.tv.NowNext
import com.opendvr.tv.Prefs
import com.opendvr.tv.TunerUse
import com.opendvr.tv.TunersBusyException
import com.opendvr.tv.UnauthenticatedException
import kotlinx.coroutines.delay
import java.text.DateFormat
import java.util.Date

private fun fmtTime(unix: Long): String = DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(unix * 1000))

private sealed class PlayState {
    data object Tuning : PlayState()
    data object Playing : PlayState()
    data class Busy(val tuners: List<TunerUse>) : PlayState()
    data class Error(val message: String) : PlayState()
}

/**
 * Full-screen playback. Up/down change channel, left/right skip 30 s in
 * the live buffer, OK shows the info bar, play/pause pauses (the server
 * keeps the buffer, so rewind works), menu or guide opens the guide, back
 * returns to where the channel was picked. While a message is up (busy,
 * error) those keys are left to its buttons so the remote can choose one.
 * A heartbeat every five seconds keeps the viewer handle alive; leaving
 * frees the tuner within seconds.
 */
@Composable
fun PlayerScreen(
    prefs: Prefs, api: Api, channel: String,
    onChannel: (String) -> Unit, onExit: () -> Unit, onGuide: () -> Unit, onUnauthenticated: () -> Unit,
) {
    val context = LocalContext.current
    val player = remember { ExoPlayer.Builder(context).build().apply { playWhenReady = true } }
    DisposableEffect(Unit) { onDispose { player.release() } }

    var state by remember { mutableStateOf<PlayState>(PlayState.Tuning) }
    var channels by remember { mutableStateOf<List<Channel>>(emptyList()) }
    var onNow by remember { mutableStateOf<NowNext?>(null) }
    var showInfo by remember { mutableStateOf(true) }
    var menuOpen by remember { mutableStateOf(false) }
    var captions by remember { mutableStateOf(false) }
    var canRecord by remember { mutableStateOf(true) }
    var notice by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    var attempt by remember { mutableStateOf(0) }
    var viewerId by remember { mutableStateOf("") }

    LaunchedEffect(Unit) {
        runCatching { channels = api.channels().filter { !it.drm } }
        runCatching { canRecord = !api.me().restricted }
    }
    // Captions are CEA-608 inside the video; ExoPlayer exposes them as a
    // text track that is off until asked for.
    LaunchedEffect(captions) {
        player.trackSelectionParameters = player.trackSelectionParameters.buildUpon()
            .setTrackTypeDisabled(C.TRACK_TYPE_TEXT, !captions)
            .setSelectUndeterminedTextLanguage(captions)
            .build()
    }

    // Tune. Re-runs on channel change or retry.
    LaunchedEffect(channel, attempt) {
        state = PlayState.Tuning
        showInfo = true
        runCatching { onNow = api.guideNow()[channel] }
        try {
            val w = api.watch(channel)
            viewerId = w.viewerId
            player.setMediaItem(MediaItem.fromUri(w.playlist))
            player.prepare()
            player.play()
            state = PlayState.Playing
            delay(4000)
            showInfo = false
        } catch (e: TunersBusyException) {
            state = PlayState.Busy(e.tuners)
        } catch (e: UnauthenticatedException) {
            onUnauthenticated()
        } catch (e: Exception) {
            state = PlayState.Error(e.message ?: "Unable to tune")
        }
    }

    // Heartbeat while on this channel; leave when it changes or we exit.
    DisposableEffect(channel) {
        onDispose {
            val id = viewerId
            if (id.isNotEmpty()) {
                // Fire and forget on a background thread; the composable is gone.
                Thread { kotlinx.coroutines.runBlocking { api.leave(id) } }.start()
            }
        }
    }
    LaunchedEffect(channel) {
        while (true) {
            delay(5000)
            val id = viewerId
            if (id.isNotEmpty()) {
                val ok = runCatching { api.heartbeat(id, !player.isPlaying) }.getOrDefault(true)
                if (!ok) runCatching { viewerId = api.watch(channel).viewerId }
            }
        }
    }

    val idx = channels.indexOfFirst { it.number == channel }
    val prev = channels.getOrNull(idx - 1)
    val next = channels.getOrNull(idx + 1)
    val focus = remember { FocusRequester() }
    // The surface holds focus while tuning and playing; a message's first
    // button takes it otherwise (see Menu).
    LaunchedEffect(state) {
        if (state is PlayState.Playing || state is PlayState.Tuning) runCatching { focus.requestFocus() }
    }
    fun seek(ms: Long) {
        player.seekTo(maxOf(0, player.currentPosition + ms))
        showInfo = true
    }

    Box(
        Modifier
            .fillMaxSize()
            .background(Color.Black)
            .focusRequester(focus)
            .focusable()
            .onKeyEvent { ev ->
                if (ev.type != KeyEventType.KeyDown) return@onKeyEvent false
                if (menuOpen) return@onKeyEvent false
                val playing = state is PlayState.Playing
                when (ev.key.nativeKeyCode) {
                    // Navigation keys belong to the message's buttons when one is showing.
                    KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_CHANNEL_UP -> if (playing) { next?.let { onChannel(it.number) }; true } else false
                    KeyEvent.KEYCODE_DPAD_DOWN, KeyEvent.KEYCODE_CHANNEL_DOWN -> if (playing) { prev?.let { onChannel(it.number) }; true } else false
                    KeyEvent.KEYCODE_DPAD_LEFT -> if (playing) { seek(-30_000); true } else false
                    KeyEvent.KEYCODE_DPAD_RIGHT -> if (playing) { seek(30_000); true } else false
                    KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER -> if (playing) { if (showInfo) menuOpen = true else showInfo = true; true } else false
                    KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE -> { if (player.isPlaying) player.pause() else player.play(); showInfo = true; true }
                    KeyEvent.KEYCODE_MEDIA_PAUSE -> { player.pause(); showInfo = true; true }
                    KeyEvent.KEYCODE_MEDIA_PLAY -> { player.play(); true }
                    KeyEvent.KEYCODE_MEDIA_REWIND -> { seek(-30_000); true }
                    KeyEvent.KEYCODE_MEDIA_FAST_FORWARD -> { seek(30_000); true }
                    KeyEvent.KEYCODE_GUIDE, KeyEvent.KEYCODE_MENU -> { onGuide(); true }
                    KeyEvent.KEYCODE_BACK, KeyEvent.KEYCODE_ESCAPE -> { onExit(); true }
                    else -> false
                }
            },
    ) {
        AndroidView(
            factory = { ctx ->
                PlayerView(ctx).apply {
                    this.player = player
                    useController = false
                    setShowBuffering(PlayerView.SHOW_BUFFERING_WHEN_PLAYING)
                }
            },
            modifier = Modifier.fillMaxSize(),
        )

        when (val s = state) {
            PlayState.Tuning -> Menu(emptyList()) {
                CircularProgressIndicator(color = Accent)
                Spacer(Modifier.height(12.dp))
                Text("Tuning $channel…", color = TextColor)
            }
            is PlayState.Error -> Menu(listOf<Pair<String, () -> Unit>>("Try again" to { attempt++ }, "Channels" to onExit)) {
                Text("Unable to play $channel", color = TextColor, fontSize = 24.sp, fontWeight = FontWeight.SemiBold)
                Text(s.message, color = Err, textAlign = TextAlign.Center)
            }
            is PlayState.Busy -> {
                val shared = s.tuners.filter { it.shared }
                val external = s.tuners.filter { !it.shared }
                val items: List<Pair<String, () -> Unit>> = shared.map { t -> "Join ${t.channel} ${t.name ?: ""}".trim() to { onChannel(t.channel) } } +
                    listOf<Pair<String, () -> Unit>>("Try $channel again" to { attempt++ }, "Channels" to onExit)
                Menu(items) {
                    Text("All tuners are busy", color = TextColor, fontSize = 24.sp, fontWeight = FontWeight.SemiBold)
                    if (shared.isNotEmpty()) {
                        Text("These channels are already streaming and can be joined without a tuner.", color = Muted, textAlign = TextAlign.Center)
                    }
                    if (external.isNotEmpty()) {
                        Text(
                            "In use elsewhere: " + external.joinToString { "${it.channel}${it.name?.let { n -> " $n" } ?: ""}${it.client?.let { c -> " ($c)" } ?: ""}" },
                            color = Muted, fontSize = 13.sp, textAlign = TextAlign.Center,
                        )
                    }
                }
            }
            PlayState.Playing -> if (showInfo) {
                InfoBar(channel, channels.getOrNull(idx)?.name ?: "", onNow, paused = !player.isPlaying && player.playbackState == Player.STATE_READY, prev, next, notice)
            }
        }
    }

    if (menuOpen) {
        val items = mutableListOf<Pair<String, () -> Unit>>()
        if (canRecord) {
            items.add("Record the rest of this programme" to {
                menuOpen = false
                scope.launch {
                    notice = try { val r = api.recordNow(channel); "Recording ${r.title} until ${fmtTime(r.end)}" } catch (e: Exception) { e.message }
                    showInfo = true
                }
            })
        }
        items.add((if (captions) "Captions: on" else "Captions: off") to { captions = !captions; menuOpen = false })
        items.add("Guide" to { menuOpen = false; onGuide() })
        items.add("Channels" to { menuOpen = false; onExit() })
        ActionMenu(
            title = "$channel ${channels.getOrNull(idx)?.name ?: ""}",
            subtitle = onNow?.now?.title, items = items,
            onDismiss = { menuOpen = false; runCatching { focus.requestFocus() } },
        )
    }
}

/**
 * A message over the video with a column of choices. The first choice
 * takes focus, so the remote can pick one straight away; up/down move
 * between them and OK selects.
 */
@Composable
private fun Menu(items: List<Pair<String, () -> Unit>>, header: @Composable () -> Unit) {
    val first = remember { FocusRequester() }
    LaunchedEffect(items.size) {
        if (items.isNotEmpty()) {
            withFrameNanos { }
            runCatching { first.requestFocus() }
        }
    }
    Column(
        Modifier.fillMaxSize().background(Color(0x8C000000)).padding(48.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Column(Modifier.widthIn(max = 560.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) {
            header()
            Spacer(Modifier.height(4.dp))
            items.forEachIndexed { i, (label, action) ->
                TvButton(label, modifier = Modifier.fillMaxWidth().then(if (i == 0) Modifier.focusRequester(first) else Modifier), onClick = action)
            }
        }
    }
}

@Composable
private fun InfoBar(channel: String, name: String, onNow: NowNext?, paused: Boolean, prev: Channel?, next: Channel?, notice: String? = null) {
    Box(Modifier.fillMaxSize()) {
        Column(
            Modifier
                .align(Alignment.BottomCenter)
                .fillMaxWidth()
                .background(Color(0xCC171A21), RoundedCornerShape(topStart = 12.dp, topEnd = 12.dp))
                .padding(horizontal = 28.dp, vertical = 16.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(channel, color = Accent, fontSize = 26.sp, fontWeight = FontWeight.Bold)
                Spacer(Modifier.width(12.dp))
                Text(name, color = TextColor, fontSize = 20.sp)
                Spacer(Modifier.weight(1f))
                if (paused) Tag("PAUSED", Warn) else Tag("LIVE", Accent)
            }
            onNow?.now?.let { p ->
                Spacer(Modifier.height(4.dp))
                Text(
                    "${p.title}${p.episodeTitle?.let { " · $it" } ?: ""} · ${fmtTime(p.start)} – ${fmtTime(p.end)}" +
                        (onNow.next?.let { " · Next: ${it.title}" } ?: ""),
                    color = Muted, fontSize = 14.sp,
                )
            }
            notice?.let {
                Spacer(Modifier.height(4.dp))
                Text(it, color = Accent, fontSize = 13.sp)
            }
            Spacer(Modifier.height(6.dp))
            Text(
                listOfNotNull(
                    next?.let { "▲ ${it.number}" }, prev?.let { "▼ ${it.number}" },
                    "◀ ▶ 30 s", "OK: info, again: record and more", "Back: channels",
                ).joinToString("   "),
                color = Muted, fontSize = 12.sp,
            )
        }
    }
}
