package com.opendvr.tv.ui

import android.net.Uri
import android.view.KeyEvent
import androidx.compose.foundation.background
import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
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
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MimeTypes
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.ui.PlayerView
import com.opendvr.tv.Api
import com.opendvr.tv.Recording
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

private fun fmtClock(sec: Double): String {
    val s = sec.toInt().coerceAtLeast(0)
    return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s / 60 % 60, s % 60) else "%d:%02d".format(s / 60, s % 60)
}

/**
 * Plays a finished recording from the server: resumes where it stopped,
 * jumps over marked commercials, saves the position as it goes and marks
 * it watched at the end. Left/right skip 15 s, up/down 60 s, OK shows
 * the bar and again opens the menu (captions, skip toggle, restart).
 */
@Composable
fun RecordingPlayerScreen(api: Api, id: String, onExit: () -> Unit) {
    val context = LocalContext.current
    val player = remember { ExoPlayer.Builder(context).build().apply { playWhenReady = true } }
    DisposableEffect(Unit) { onDispose { player.release() } }
    val scope = rememberCoroutineScope()

    var rec by remember { mutableStateOf<Recording?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var showInfo by remember { mutableStateOf(true) }
    var menuOpen by remember { mutableStateOf(false) }
    var captions by remember { mutableStateOf(false) }
    var autoSkip by remember { mutableStateOf(true) }
    var positionSec by remember { mutableStateOf(0.0) }
    var toast by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(id) {
        val r = try { api.recording(id) } catch (e: Exception) { error = e.message; return@LaunchedEffect }
        rec = r
        val url = r.playUrl ?: run { error = "This recording is not ready (${r.status})"; return@LaunchedEffect }
        val item = MediaItem.Builder().setUri(url)
        r.captionsUrl?.let { cc ->
            item.setSubtitleConfigurations(listOf(MediaItem.SubtitleConfiguration.Builder(Uri.parse(cc)).setMimeType(MimeTypes.TEXT_VTT).setLanguage("en").build()))
        }
        player.setMediaItem(item.build())
        player.prepare()
        if (r.position > 5 && r.position < r.durationSec - 30) player.seekTo((r.position * 1000).toLong())
        player.play()
        delay(4000)
        showInfo = false
    }
    LaunchedEffect(captions) {
        player.trackSelectionParameters = player.trackSelectionParameters.buildUpon()
            .setTrackTypeDisabled(C.TRACK_TYPE_TEXT, !captions)
            .setPreferredTextLanguage(if (captions) "en" else null)
            .build()
    }
    // Position, commercial skipping and the end of the programme.
    LaunchedEffect(rec, autoSkip) {
        val r = rec ?: return@LaunchedEffect
        var lastSave = 0L
        var lastSkip = -1
        while (true) {
            delay(500)
            val t = player.currentPosition / 1000.0
            positionSec = t
            if (autoSkip) {
                r.skips.forEachIndexed { i, s ->
                    if (t >= s.start && t < s.end - 0.5 && i != lastSkip) {
                        lastSkip = i
                        player.seekTo((s.end * 1000).toLong())
                        toast = "Skipped a ${((s.end - s.start) / 60).toInt()} min break"
                        scope.launch { delay(2500); toast = null }
                    }
                    if (t >= s.end && i == lastSkip) lastSkip = -1
                }
            }
            if (player.playbackState == Player.STATE_ENDED) {
                api.setWatched(r.id, true)
                onExit()
                return@LaunchedEffect
            }
            val now = System.currentTimeMillis()
            if (now - lastSave > 10_000 && player.isPlaying) {
                lastSave = now
                api.setPosition(r.id, t)
            }
        }
    }
    DisposableEffect(rec) {
        onDispose {
            val r = rec
            if (r != null && player.playbackState != Player.STATE_ENDED) {
                val t = player.currentPosition / 1000.0
                Thread { kotlinx.coroutines.runBlocking { api.setPosition(r.id, t) } }.start()
            }
        }
    }

    val focus = remember { FocusRequester() }
    LaunchedEffect(rec) { runCatching { focus.requestFocus() } }
    fun seek(ms: Long) { player.seekTo((player.currentPosition + ms).coerceAtLeast(0)); showInfo = true }

    Box(
        Modifier
            .fillMaxSize()
            .background(Color.Black)
            .focusRequester(focus)
            .focusable()
            .onKeyEvent { ev ->
                if (ev.type != KeyEventType.KeyDown || menuOpen) return@onKeyEvent false
                when (ev.key.nativeKeyCode) {
                    KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_MEDIA_REWIND -> { seek(-15_000); true }
                    KeyEvent.KEYCODE_DPAD_RIGHT, KeyEvent.KEYCODE_MEDIA_FAST_FORWARD -> { seek(30_000); true }
                    KeyEvent.KEYCODE_DPAD_UP -> { seek(60_000); true }
                    KeyEvent.KEYCODE_DPAD_DOWN -> { seek(-60_000); true }
                    KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER -> { if (showInfo) menuOpen = true else showInfo = true; true }
                    KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE -> { if (player.isPlaying) player.pause() else player.play(); showInfo = true; true }
                    KeyEvent.KEYCODE_MEDIA_PAUSE -> { player.pause(); showInfo = true; true }
                    KeyEvent.KEYCODE_MEDIA_PLAY -> { player.play(); true }
                    KeyEvent.KEYCODE_MENU -> { menuOpen = true; true }
                    KeyEvent.KEYCODE_BACK, KeyEvent.KEYCODE_ESCAPE -> { onExit(); true }
                    else -> false
                }
            },
    ) {
        AndroidView(
            factory = { ctx -> PlayerView(ctx).apply { this.player = player; useController = false; setShowBuffering(PlayerView.SHOW_BUFFERING_WHEN_PLAYING) } },
            modifier = Modifier.fillMaxSize(),
        )
        error?.let { Column(Modifier.fillMaxSize().padding(48.dp), verticalArrangement = androidx.compose.foundation.layout.Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) { Text(it, color = Err) } }
        if (rec == null && error == null) Column(Modifier.fillMaxSize(), verticalArrangement = androidx.compose.foundation.layout.Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) { CircularProgressIndicator(color = Accent) }
        toast?.let {
            Box(Modifier.fillMaxSize().padding(16.dp), contentAlignment = Alignment.TopEnd) {
                Text(it, color = Accent, modifier = Modifier.background(Color(0xE6171A21), RoundedCornerShape(8.dp)).padding(horizontal = 12.dp, vertical = 6.dp))
            }
        }
        rec?.let { r ->
            if (showInfo) {
                Box(Modifier.fillMaxSize()) {
                    Column(
                        Modifier.align(Alignment.BottomCenter).fillMaxWidth()
                            .background(Color(0xCC171A21), RoundedCornerShape(topStart = 12.dp, topEnd = 12.dp))
                            .padding(horizontal = 28.dp, vertical = 16.dp),
                    ) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(r.title, color = TextColor, fontSize = 22.sp, fontWeight = FontWeight.Bold)
                            Spacer(Modifier.width(12.dp))
                            Text(listOfNotNull(r.episodeNumber, r.episodeTitle).joinToString(" · "), color = Muted, fontSize = 16.sp)
                            Spacer(Modifier.weight(1f))
                            if (!player.isPlaying && player.playbackState == Player.STATE_READY) Tag("PAUSED", Warn)
                            if (captions) { Spacer(Modifier.width(6.dp)); Tag("CC", Accent) }
                        }
                        Spacer(Modifier.height(6.dp))
                        val dur = if (r.durationSec > 0) r.durationSec else player.duration / 1000.0
                        Box(Modifier.fillMaxWidth().height(4.dp).background(Panel2, RoundedCornerShape(2.dp))) {
                            Box(Modifier.fillMaxWidth((positionSec / dur).toFloat().coerceIn(0f, 1f)).height(4.dp).background(Accent, RoundedCornerShape(2.dp)))
                        }
                        Spacer(Modifier.height(4.dp))
                        Text("${fmtClock(positionSec)} / ${fmtClock(dur)}   ◀ ▶ 15/30 s   ▲ ▼ 1 min   OK: menu   Back: recordings", color = Muted, fontSize = 12.sp)
                    }
                }
            }
        }
    }

    if (menuOpen) {
        val r = rec
        val items = mutableListOf<Pair<String, () -> Unit>>()
        if (r?.captionsUrl != null) items.add((if (captions) "Captions: on" else "Captions: off") to { captions = !captions; menuOpen = false })
        if (r != null && r.skips.isNotEmpty()) items.add((if (autoSkip) "Skip commercials: on" else "Skip commercials: off") to { autoSkip = !autoSkip; menuOpen = false })
        items.add("Play from the start" to { player.seekTo(0); menuOpen = false })
        items.add("Mark watched and close" to { menuOpen = false; scope.launch { r?.let { api.setWatched(it.id, true) }; onExit() } })
        items.add("Back to recordings" to { menuOpen = false; onExit() })
        ActionMenu(title = r?.title ?: "Recording", items = items, onDismiss = { menuOpen = false; runCatching { focus.requestFocus() } })
    }
}
