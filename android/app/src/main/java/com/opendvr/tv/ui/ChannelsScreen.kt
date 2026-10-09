package com.opendvr.tv.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
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
import com.opendvr.tv.Channel
import com.opendvr.tv.NowNext
import com.opendvr.tv.Prefs
import com.opendvr.tv.UnauthenticatedException
import kotlinx.coroutines.delay
import java.text.DateFormat
import java.util.Date

private fun fmtTime(unix: Long): String = DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(unix * 1000))

/**
 * The channel grid, with what is on now and which channels are already
 * streaming. Focus starts on the channel watched last; up from the top
 * row reaches the tabs.
 */
@Composable
fun ChannelsScreen(prefs: Prefs, api: Api, onPlay: (String) -> Unit, onTab: (Tab) -> Unit, onUnauthenticated: () -> Unit) {
    var channels by remember { mutableStateOf<List<Channel>>(emptyList()) }
    var onNow by remember { mutableStateOf<Map<String, NowNext>>(emptyMap()) }
    var live by remember { mutableStateOf<Map<String, Int>>(emptyMap()) }
    var error by remember { mutableStateOf<String?>(null) }
    var user by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(Unit) {
        try {
            channels = api.channels().filter { !it.drm }
            user = api.me().userName
        } catch (e: UnauthenticatedException) {
            onUnauthenticated()
            return@LaunchedEffect
        } catch (e: Exception) {
            error = e.message
        }
        while (true) {
            runCatching { onNow = api.guideNow() }
            repeat(6) {
                runCatching { live = api.streams().associate { it.channel to it.viewers } }
                delay(10_000)
            }
        }
    }

    Column(Modifier.fillMaxSize().padding(horizontal = 40.dp, vertical = 24.dp)) {
        TopBar(Tab.Channels, user, onTab)
        error?.let { Text(it, color = Err, modifier = Modifier.padding(vertical = 8.dp)) }
        Spacer(Modifier.height(16.dp))

        val first = remember { FocusRequester() }
        val initial = channels.indexOfFirst { it.number == prefs.lastChannel }.let { if (it < 0) 0 else it }
        LaunchedEffect(channels) { if (channels.isNotEmpty()) runCatching { first.requestFocus() } }

        LazyVerticalGrid(
            columns = GridCells.Fixed(5),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
            contentPadding = PaddingValues(bottom = 24.dp),
        ) {
            items(channels, key = { it.number }) { c ->
                val idx = channels.indexOf(c)
                ChannelCard(
                    c, onNow[c.number]?.now?.let { "${it.title} · until ${fmtTime(it.end)}" }, live[c.number],
                    modifier = if (idx == initial) Modifier.focusRequester(first) else Modifier,
                ) { onPlay(c.number) }
            }
        }
    }
}

@Composable
private fun ChannelCard(c: Channel, nowLine: String?, viewers: Int?, modifier: Modifier = Modifier, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Column(
        modifier
            .onFocusChanged { focused = it.isFocused }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(10.dp))
            .background(if (focused) Panel2 else Panel, RoundedCornerShape(10.dp))
            .clickable(onClick = onClick)
            .padding(14.dp)
            .height(92.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(c.number, color = Accent, fontSize = 20.sp, fontWeight = FontWeight.Bold)
            Spacer(Modifier.weight(1f))
            if (viewers != null) Tag("LIVE" + if (viewers > 0) " · $viewers" else "", Accent)
            if (c.hd) {
                Spacer(Modifier.width(6.dp))
                Tag("HD", Muted)
            }
        }
        Text(c.name, color = TextColor, fontSize = 15.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Spacer(Modifier.weight(1f))
        Text(nowLine ?: "", color = Muted, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}
