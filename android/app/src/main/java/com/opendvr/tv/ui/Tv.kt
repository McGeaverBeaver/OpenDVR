package com.opendvr.tv.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.window.Dialog
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// Controls built for a remote control. Everything here is reached with
// the D-pad and shows a bright ring when focused; Material's buttons are
// focusable too but their focus state is nearly invisible on a TV.

/** A push button with an unmistakable focus ring. */
@Composable
fun TvButton(text: String, modifier: Modifier = Modifier, enabled: Boolean = true, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Box(
        modifier
            .onFocusChanged { focused = it.isFocused }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(8.dp))
            .background(if (focused) FocusFill else Panel2, RoundedCornerShape(8.dp))
            .clickable(enabled = enabled, onClick = onClick)
            .padding(horizontal = 18.dp, vertical = 10.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, color = if (enabled) TextColor else Muted, fontSize = 15.sp, fontWeight = FontWeight.Medium)
    }
}

/** The screens reachable from the top of the Channels and Guide pages. */
enum class Tab(val label: String) { Channels("Channels"), Guide("Guide"), Recordings("Recordings"), Settings("Settings") }

/**
 * App name, the three tabs and who is signed in. Left/right moves between
 * tabs, down goes into the page. The current tab is marked and is skipped
 * when pressed.
 */
@Composable
fun TopBar(selected: Tab, user: String?, onSelect: (Tab) -> Unit, trailing: @Composable RowScope.() -> Unit = {}) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Text("OpenDVR", color = Accent, fontSize = 18.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.width(28.dp))
        Tab.entries.forEach { t ->
            TabItem(t.label, t == selected) { if (t != selected) onSelect(t) }
            Spacer(Modifier.width(6.dp))
        }
        Spacer(Modifier.weight(1f))
        com.opendvr.tv.Updater.available?.let {
            Text("Update ${it.versionName} available in Settings", color = Accent, fontSize = 13.sp)
            Spacer(Modifier.width(16.dp))
        }
        trailing()
        user?.let { Text(it, color = Muted, fontSize = 14.sp) }
    }
}

@Composable
private fun TabItem(label: String, selected: Boolean, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Column(
        Modifier
            .onFocusChanged { focused = it.isFocused }
            .border(2.dp, if (focused) Accent else Color.Transparent, RoundedCornerShape(8.dp))
            .background(if (focused) FocusFill else Color.Transparent, RoundedCornerShape(8.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 14.dp, vertical = 6.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(label, color = if (selected) TextColor else Muted, fontSize = 18.sp, fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal)
        Box(Modifier.padding(top = 3.dp).height(2.dp).width(28.dp).background(if (selected) Accent else Color.Transparent))
    }
}

/**
 * A list of choices in a dialog, driven by the remote: the first takes
 * focus, up/down move, OK picks, back closes.
 */
@Composable
fun ActionMenu(title: String, subtitle: String? = null, items: List<Pair<String, () -> Unit>>, onDismiss: () -> Unit) {
    val first = remember { FocusRequester() }
    LaunchedEffect(Unit) {
        withFrameNanos { }
        runCatching { first.requestFocus() }
    }
    Dialog(onDismissRequest = onDismiss) {
        Column(
            Modifier
                .widthIn(min = 320.dp, max = 520.dp)
                .background(Panel, RoundedCornerShape(12.dp))
                .border(1.dp, Panel2, RoundedCornerShape(12.dp))
                .padding(20.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(title, color = TextColor, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
            subtitle?.let { Text(it, color = Muted, fontSize = 13.sp) }
            Spacer(Modifier.height(4.dp))
            items.forEachIndexed { i, (label, action) ->
                TvButton(label, modifier = Modifier.fillMaxWidth().then(if (i == 0) Modifier.focusRequester(first) else Modifier)) { action() }
            }
        }
    }
}

/** A small outlined label such as HD or LIVE. */
@Composable
fun Tag(text: String, color: Color) {
    Box(
        Modifier
            .border(1.dp, color, RoundedCornerShape(999.dp))
            .padding(horizontal = 6.dp, vertical = 1.dp),
    ) {
        Text(text, color = color, fontSize = 11.sp)
    }
}

/** One line of remote-control hints at the foot of a page. */
@Composable
fun Hints(vararg hints: String) {
    Text(hints.joinToString("    "), color = Muted, fontSize = 12.sp, modifier = Modifier.padding(top = 8.dp))
}
