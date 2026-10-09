package com.opendvr.tv.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
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
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.opendvr.tv.Api
import com.opendvr.tv.Recording
import com.opendvr.tv.UnauthenticatedException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.text.DateFormat
import java.util.Date

private fun fmtWhen(unix: Long): String = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(unix * 1000))
private fun fmtMin(sec: Double): String {
    val m = (sec / 60).toInt()
    return if (m >= 60) "${m / 60}h ${m % 60}m" else "${m}m"
}

/**
 * The library: what to continue, what is up next, and every recording by
 * show. OK on a recording opens its menu (play, resume, mark watched,
 * delete). Recordings are files on the server; the TV only plays them.
 */
@Composable
fun RecordingsScreen(api: Api, onPlay: (String) -> Unit, onTab: (Tab) -> Unit, onUnauthenticated: () -> Unit) {
    var all by remember { mutableStateOf<List<Recording>>(emptyList()) }
    var user by remember { mutableStateOf<String?>(null) }
    var canChange by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var menu by remember { mutableStateOf<Recording?>(null) }
    var reload by remember { mutableStateOf(0) }
    val scope = rememberCoroutineScope()

    LaunchedEffect(reload) {
        try {
            api.me().let { user = it.userName; canChange = !it.restricted }
        } catch (e: UnauthenticatedException) {
            onUnauthenticated()
            return@LaunchedEffect
        } catch (_: Exception) {
        }
        while (true) {
            try {
                all = api.recordings()
                error = null
            } catch (e: UnauthenticatedException) {
                onUnauthenticated()
                return@LaunchedEffect
            } catch (e: Exception) {
                error = e.message
            }
            delay(15_000)
        }
    }

    val ready = all.filter { it.status == "ready" }
    val continuing = ready.filter { !it.watched && it.position > 60 && it.durationSec > 0 && it.position < it.durationSec - 60 }
    val upNext = run {
        val seen = HashSet<String>()
        ready.sortedBy { it.airing }.filter { r -> !r.watched && r !in continuing && seen.add(r.title) }.take(12)
    }
    val byShow = all.filter { it.status != "scheduled" && it.status != "conflict" }
        .groupBy { it.title }.toSortedMap(String.CASE_INSENSITIVE_ORDER)
        .mapValues { (_, v) -> v.sortedByDescending { it.airing } }

    val first = remember { FocusRequester() }
    var placed by remember { mutableStateOf(false) }
    LaunchedEffect(all) {
        if (!placed && all.isNotEmpty()) {
            placed = true
            withFrameNanos { }
            runCatching { first.requestFocus() }
        }
    }
    var firstGiven = false
    fun firstMod(): Modifier = if (!firstGiven) { firstGiven = true; Modifier.focusRequester(first) } else Modifier

    Column(Modifier.fillMaxSize().padding(horizontal = 40.dp, vertical = 24.dp)) {
        TopBar(Tab.Recordings, user, onTab)
        Spacer(Modifier.height(12.dp))
        error?.let { Text(it, color = Err) }
        if (all.isEmpty() && error == null) Text("Nothing recorded yet. Pick a programme in the guide and press OK to record it.", color = Muted)

        LazyColumn(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            if (continuing.isNotEmpty()) {
                item { Text("Continue watching", color = TextColor, fontSize = 18.sp, fontWeight = FontWeight.SemiBold) }
                item {
                    LazyRow(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        items(continuing, key = { "c" + it.id }) { r -> Shelf(r, firstMod(), progress = (r.position / r.durationSec).toFloat()) { menu = r } }
                    }
                }
            }
            if (upNext.isNotEmpty()) {
                item { Text("Up next", color = TextColor, fontSize = 18.sp, fontWeight = FontWeight.SemiBold) }
                item {
                    LazyRow(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        items(upNext, key = { "u" + it.id }) { r -> Shelf(r, firstMod()) { menu = r } }
                    }
                }
            }
            byShow.forEach { (title, recs) ->
                item { Text("$title  ·  ${recs.size}", color = Muted, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.padding(top = 6.dp)) }
                items(recs, key = { it.id }) { r -> RowItem(r, firstMod()) { menu = r } }
            }
            item { Hints("▲ ▼ ◀ ▶ move", "OK: play and more", "Back: channels") }
        }
    }

    menu?.let { r ->
        val items = mutableListOf<Pair<String, () -> Unit>>()
        if (r.status == "ready") {
            if (r.position > 60 && r.position < r.durationSec - 60) items.add("Resume from ${fmtMin(r.position)}" to { menu = null; onPlay(r.id) })
            items.add((if (r.position > 60) "Play from the start" else "Play") to {
                menu = null
                scope.launch { api.setPosition(r.id, 0.0); onPlay(r.id) }
            })
            items.add((if (r.watched) "Mark as not watched" else "Mark as watched") to {
                menu = null
                scope.launch { api.setWatched(r.id, !r.watched); reload++ }
            })
        }
        if (canChange) {
            items.add((if (r.status == "recording") "Stop and discard" else "Delete") to {
                menu = null
                scope.launch { runCatching { api.deleteRecording(r.id) }; reload++ }
            })
        }
        items.add("Close" to { menu = null })
        ActionMenu(
            title = r.title,
            subtitle = listOfNotNull(r.episodeNumber, r.episodeTitle, fmtWhen(r.airing), "${r.channel} ${r.channelName ?: ""}".trim(),
                if (r.status != "ready") r.status else null, r.error).joinToString(" · "),
            items = items, onDismiss = { menu = null },
        )
    }
}

@Composable
private fun Shelf(r: Recording, modifier: Modifier, progress: Float = 0f, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Column(
        modifier
            .width(220.dp)
            .onFocusChanged { focused = it.isFocused }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(10.dp))
            .background(if (focused) FocusFill else Panel, RoundedCornerShape(10.dp))
            .clickable(onClick = onClick)
            .padding(12.dp),
    ) {
        Text(r.title, color = TextColor, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Text(r.episodeTitle ?: r.episodeNumber ?: fmtWhen(r.airing), color = Muted, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (progress > 0f) {
            Spacer(Modifier.height(8.dp))
            Box(Modifier.fillMaxWidth().height(3.dp).background(Panel2)) {
                Box(Modifier.fillMaxWidth(progress.coerceIn(0f, 1f)).height(3.dp).background(Accent))
            }
            Text("${fmtMin(r.durationSec - r.position)} left", color = Muted, fontSize = 11.sp)
        } else {
            Text(fmtMin(r.durationSec), color = Muted, fontSize = 11.sp)
        }
    }
}

@Composable
private fun RowItem(r: Recording, modifier: Modifier, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Row(
        modifier
            .fillMaxWidth()
            .onFocusChanged { focused = it.isFocused }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(8.dp))
            .background(if (focused) FocusFill else Panel, RoundedCornerShape(8.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 14.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                listOfNotNull(r.episodeNumber, r.episodeTitle).joinToString(" · ").ifEmpty { fmtWhen(r.airing) },
                color = TextColor, fontSize = 15.sp, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            Text(
                listOfNotNull(fmtWhen(r.airing), "${r.channel} ${r.channelName ?: ""}".trim(), if (r.durationSec > 0) fmtMin(r.durationSec) else null,
                    if (r.cut) "ads removed" else if (r.skips.isNotEmpty()) "ads marked" else null, if (r.captions) "CC" else null, r.error).joinToString(" · "),
                color = Muted, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
        Spacer(Modifier.width(10.dp))
        when {
            r.status == "recording" -> Tag("REC", Err)
            r.status == "processing" -> Tag("PROCESSING", Warn)
            r.status == "failed" -> Tag("FAILED", Err)
            r.watched -> Tag("WATCHED", Muted)
            r.position > 60 -> Tag("${(r.position / r.durationSec * 100).toInt()}%", Accent)
        }
    }
}
