package com.opendvr.tv.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
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
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.opendvr.tv.Api
import com.opendvr.tv.Channel
import com.opendvr.tv.Prefs
import com.opendvr.tv.Program
import com.opendvr.tv.UnauthenticatedException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import androidx.compose.runtime.rememberCoroutineScope
import java.text.DateFormat
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

private const val WINDOW_SEC = 3 * 3600L // what one screen shows
private const val STEP_SEC = 90 * 60L // how far a page turn moves
private const val FETCH_SEC = 12 * 3600L // how much is asked for at once
private val LABEL_W = 124.dp
private val ROW_H = 56.dp

private fun nowSec() = System.currentTimeMillis() / 1000
private fun halfHourFloor(t: Long) = t - t % 1800
private fun fmtTime(unix: Long): String = DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(unix * 1000))
private fun fmtDay(unix: Long): String = SimpleDateFormat("EEE d MMM", Locale.getDefault()).format(Date(unix * 1000))

/** One box in a guide row: a programme clipped to the window, or a gap. */
private data class Cell(val start: Long, val end: Long, val program: Program?)

/** Programmes overlapping [from, to), clipped, with gaps filled so every row is navigable. */
private fun cells(programs: List<Program>, from: Long, to: Long): List<Cell> {
    val out = ArrayList<Cell>()
    var cursor = from
    for (p in programs.sortedBy { it.start }) {
        val s = maxOf(p.start, from)
        val e = minOf(p.end, to)
        if (e - s < 60) continue
        if (s - cursor >= 60) out.add(Cell(cursor, s, null))
        out.add(Cell(s, e, p))
        cursor = e
    }
    if (to - cursor >= 60) out.add(Cell(cursor, to, null))
    return out
}

/**
 * The programme guide: a row per channel, three hours across, paged with
 * the D-pad. Up/down move between channels, left/right along the row;
 * pressing right past the last programme (or left past the channel name)
 * turns the page. OK plays the channel. The focused programme's details
 * show above the grid.
 */
@Composable
fun GuideScreen(prefs: Prefs, api: Api, onPlay: (String) -> Unit, onTab: (Tab) -> Unit, onUnauthenticated: () -> Unit) {
    var menu by remember { mutableStateOf<Pair<Channel, Program?>?>(null) }
    var canRecord by remember { mutableStateOf(true) }
    var notice by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    val minFrom = remember { halfHourFloor(nowSec()) - 1800 }
    var from by remember { mutableStateOf(halfHourFloor(nowSec())) }
    val to = from + WINDOW_SEC
    var lineup by remember { mutableStateOf<List<Channel>>(emptyList()) }
    var listings by remember { mutableStateOf<Map<String, List<Program>>>(emptyMap()) }
    var loaded by remember { mutableStateOf(0L to 0L) }
    var live by remember { mutableStateOf<Set<String>>(emptySet()) }
    var user by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var now by remember { mutableStateOf(nowSec()) }
    var focused by remember { mutableStateOf<Pair<Channel, Program?>?>(null) }
    // Where focus should land next: a channel and a moment on its row.
    var focusTarget by remember { mutableStateOf<Pair<String, Long>?>(null) }
    var placed by remember { mutableStateOf(false) }
    val listState = rememberLazyListState()

    LaunchedEffect(Unit) {
        try {
            lineup = api.channels().filter { !it.drm }
            api.me().let { user = it.userName; canRecord = !it.restricted }
        } catch (e: UnauthenticatedException) {
            onUnauthenticated()
            return@LaunchedEffect
        } catch (e: Exception) {
            error = e.message
        }
        while (true) {
            runCatching { live = api.streams().map { it.channel }.toSet() }
            now = nowSec()
            delay(10_000)
        }
    }

    // Listings come twelve hours at a time and are fetched again when the
    // window leaves what is held.
    LaunchedEffect(from) {
        if (from >= loaded.first && to <= loaded.second) return@LaunchedEffect
        val f = from
        try {
            listings = api.guide(f, f + FETCH_SEC)
            loaded = f to f + FETCH_SEC
            error = null
        } catch (e: UnauthenticatedException) {
            onUnauthenticated()
        } catch (e: Exception) {
            error = e.message
            loaded = f to f + FETCH_SEC // rows still render, as gaps
        }
    }

    // First focus: the channel watched last, on what is airing now.
    LaunchedEffect(lineup, loaded) {
        if (placed || lineup.isEmpty() || loaded.second == 0L) return@LaunchedEffect
        placed = true
        val idx = lineup.indexOfFirst { it.number == prefs.lastChannel }.coerceAtLeast(0)
        listState.scrollToItem(idx)
        focusTarget = lineup[idx].number to now
    }

    // Turn the page and put focus on the same channel at the moment asked
    // for (clamped into the new window).
    fun page(forward: Boolean, channel: String, around: Long) {
        val newFrom = if (forward) from + STEP_SEC else maxOf(minFrom, from - STEP_SEC)
        if (newFrom == from) return
        focusTarget = channel to around.coerceIn(newFrom, newFrom + WINDOW_SEC - 1)
        from = newFrom
    }

    Column(Modifier.fillMaxSize().padding(horizontal = 40.dp, vertical = 24.dp)) {
        TopBar(Tab.Guide, user, onTab)
        Spacer(Modifier.height(12.dp))
        DetailPanel(focused)
        error?.let { Text(it, color = Err, fontSize = 13.sp, modifier = Modifier.padding(top = 4.dp)) }
        notice?.let { Text(it, color = Accent, fontSize = 13.sp, modifier = Modifier.padding(top = 4.dp)) }
        Spacer(Modifier.height(10.dp))

        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
            val timelineW = maxWidth - LABEL_W
            val dpPerMin: Dp = timelineW / (WINDOW_SEC / 60f)
            fun xOf(t: Long): Dp = LABEL_W + dpPerMin * ((t - from) / 60f)

            Column(Modifier.fillMaxSize()) {
                // Time ruler.
                Box(Modifier.fillMaxWidth().height(24.dp)) {
                    Text(fmtDay(from), color = Muted, fontSize = 12.sp, modifier = Modifier.align(Alignment.CenterStart))
                    var t = from
                    while (t < to) {
                        Text(fmtTime(t), color = Muted, fontSize = 12.sp, modifier = Modifier.offset(x = xOf(t)).align(Alignment.CenterStart))
                        t += 1800
                    }
                }
                Box(Modifier.weight(1f).fillMaxWidth()) {
                    LazyColumn(state = listState) {
                        items(lineup, key = { it.number }) { c ->
                            GuideRow(
                                c, listings[c.number] ?: emptyList(), from, to, now, dpPerMin, c.number in live,
                                target = focusTarget?.takeIf { it.first == c.number }?.second,
                                onTargetTaken = { focusTarget = null },
                                onFocus = { p -> focused = c to p },
                                onPlay = { menu = c to focused?.takeIf { f -> f.first.number == c.number }?.second },
                                onPage = { fwd, around -> page(fwd, c.number, around) },
                            )
                        }
                    }
                    if (now in from until to) {
                        Box(Modifier.offset(x = xOf(now)).width(2.dp).fillMaxHeight().background(Accent.copy(alpha = 0.6f)))
                    }
                }
            }
        }
        Hints("◀ ▶ along the day", "▲ ▼ channels", "OK: watch or record", "Back: channels")
    }

    menu?.let { (c, p) ->
        val now = nowSec()
        val onAir = p != null && p.start <= now && now < p.end
        fun done(msg: String) { menu = null; notice = msg }
        fun run(block: suspend () -> String) {
            scope.launch { done(try { block() } catch (e: Exception) { e.message ?: "Failed" }) }
        }
        val items = mutableListOf<Pair<String, () -> Unit>>()
        if (p == null || onAir) items.add("Watch ${c.number} ${c.name}" to { menu = null; onPlay(c.number) })
        if (p != null && canRecord) {
            items.add((if (onAir) "Record the rest of this" else "Record this airing") to { run { val r = api.recordAiring(c.number, p.start); "Will record ${r.title}" } })
            items.add("Record every episode" to { run { "Pass created: " + api.createPass(p.title, p.seriesId, null, false) } })
            items.add("Record new episodes only" to { run { "Pass created: " + api.createPass(p.title, p.seriesId, null, true) } })
        }
        if (!onAir && p != null) items.add("Watch ${c.number} now" to { menu = null; onPlay(c.number) })
        ActionMenu(
            title = p?.title ?: "${c.number} ${c.name}",
            subtitle = p?.let { "${c.number} ${c.name} · ${fmtTime(it.start)} – ${fmtTime(it.end)}" + (it.episodeTitle?.let { e -> " · $e" } ?: "") },
            items = items, onDismiss = { menu = null },
        )
    }
}

@Composable
private fun DetailPanel(focused: Pair<Channel, Program?>?) {
    Column(Modifier.fillMaxWidth().height(84.dp)) {
        val c = focused?.first
        val p = focused?.second
        when {
            c == null -> Text("Loading the guide…", color = Muted)
            p == null -> {
                Text("${c.number}  ${c.name}", color = TextColor, fontSize = 18.sp, fontWeight = FontWeight.SemiBold)
                Text("No listing for this time. OK plays the channel.", color = Muted, fontSize = 13.sp)
            }
            else -> {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(p.title, color = TextColor, fontSize = 18.sp, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (p.isNew) {
                        Spacer(Modifier.width(8.dp))
                        Tag("NEW", Accent)
                    }
                }
                Text(
                    listOfNotNull("${c.number} ${c.name}", "${fmtTime(p.start)} – ${fmtTime(p.end)}", p.episodeTitle, p.episodeNumber).joinToString(" · "),
                    color = Accent, fontSize = 13.sp, maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
                Text(p.synopsis ?: "", color = Muted, fontSize = 13.sp, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

@Composable
private fun GuideRow(
    c: Channel, programs: List<Program>, from: Long, to: Long, now: Long, dpPerMin: Dp, live: Boolean,
    target: Long?, onTargetTaken: () -> Unit, onFocus: (Program?) -> Unit, onPlay: () -> Unit, onPage: (Boolean, Long) -> Unit,
) {
    val cells = remember(programs, from, to) { cells(programs, from, to) }
    // The cell holding the target moment, else the last one before it.
    val targetIndex = target?.let { t -> cells.indexOfFirst { t < it.end }.let { if (it < 0) cells.lastIndex else it } }

    Row(Modifier.fillMaxWidth().height(ROW_H).padding(bottom = 3.dp)) {
        ChannelLabel(c, live, onFocus = { onFocus(null) }, onPlay = onPlay, onLeft = { onPage(false, from - 1) }, modifier = Modifier.width(LABEL_W - 3.dp).fillMaxHeight())
        Spacer(Modifier.width(3.dp))
        Box(Modifier.weight(1f).fillMaxHeight()) {
            cells.forEachIndexed { i, cell ->
                ProgramCell(
                    cell,
                    x = dpPerMin * ((cell.start - from) / 60f),
                    w = dpPerMin * ((cell.end - cell.start) / 60f) - 3.dp,
                    airing = now in cell.start until cell.end,
                    last = i == cells.lastIndex,
                    wantsFocus = i == targetIndex,
                    onTargetTaken = onTargetTaken,
                    onFocus = { onFocus(cell.program) },
                    onPlay = onPlay,
                    onPage = onPage,
                )
            }
        }
    }
}

@Composable
private fun ChannelLabel(c: Channel, live: Boolean, onFocus: () -> Unit, onPlay: () -> Unit, onLeft: () -> Unit, modifier: Modifier) {
    var focused by remember { mutableStateOf(false) }
    Column(
        modifier
            .onFocusChanged { focused = it.isFocused; if (it.isFocused) onFocus() }
            .onPreviewKeyEvent { ev ->
                if (ev.type == KeyEventType.KeyDown && ev.key == Key.DirectionLeft) { onLeft(); true } else false
            }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(8.dp))
            .background(if (focused) FocusFill else Panel2, RoundedCornerShape(8.dp))
            .clickable(onClick = onPlay)
            .padding(horizontal = 10.dp, vertical = 6.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(c.number, color = Accent, fontSize = 16.sp, fontWeight = FontWeight.Bold)
            if (live) {
                Spacer(Modifier.width(6.dp))
                Box(Modifier.size(7.dp).background(Accent, CircleShape))
            }
        }
        Text(c.name, color = TextColor, fontSize = 13.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
private fun ProgramCell(
    cell: Cell, x: Dp, w: Dp, airing: Boolean, last: Boolean, wantsFocus: Boolean,
    onTargetTaken: () -> Unit, onFocus: () -> Unit, onPlay: () -> Unit, onPage: (Boolean, Long) -> Unit,
) {
    var focused by remember { mutableStateOf(false) }
    val requester = remember { FocusRequester() }
    if (wantsFocus) {
        LaunchedEffect(Unit) {
            withFrameNanos { }
            runCatching { requester.requestFocus() }
            onTargetTaken()
        }
    }
    val p = cell.program
    Column(
        Modifier
            .offset(x = x)
            .width(w.coerceAtLeast(8.dp))
            .fillMaxHeight()
            .focusRequester(requester)
            .onFocusChanged { focused = it.isFocused; if (it.isFocused) onFocus() }
            .onPreviewKeyEvent { ev ->
                if (ev.type != KeyEventType.KeyDown) return@onPreviewKeyEvent false
                when (ev.key) {
                    Key.DirectionRight -> if (last) { onPage(true, cell.end); true } else false
                    Key.MediaFastForward -> { onPage(true, cell.start + STEP_SEC); true }
                    Key.MediaRewind -> { onPage(false, cell.start - STEP_SEC); true }
                    else -> false
                }
            }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(6.dp))
            .background(
                when {
                    focused -> FocusFill
                    p == null -> Color.Transparent
                    airing -> Panel2
                    else -> Panel
                },
                RoundedCornerShape(6.dp),
            )
            .clickable(onClick = onPlay)
            .padding(horizontal = 8.dp, vertical = 5.dp),
    ) {
        Text(
            p?.title ?: "No listing", color = if (p == null) Muted else TextColor, fontSize = 14.sp,
            fontWeight = if (airing && p != null) FontWeight.SemiBold else FontWeight.Normal, maxLines = 1, overflow = TextOverflow.Ellipsis,
        )
        p?.let {
            Text(
                it.episodeTitle ?: "${fmtTime(it.start)} – ${fmtTime(it.end)}",
                color = Muted, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
    }
}
