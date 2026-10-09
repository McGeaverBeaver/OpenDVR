package com.opendvr.tv.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.ui.platform.LocalView
import androidx.media3.common.Player

/**
 * Keeps the TV from dimming or starting its screensaver while [player] is
 * playing or loading. Android does not count video as activity, so without
 * this the screensaver comes up mid-show after the TV's idle timeout. Paused,
 * stopped or failed playback lets the screen sleep as usual.
 *
 * Call it after the effect that releases the player, so this one is disposed
 * first and stops listening before the player goes away.
 */
@Composable
fun KeepScreenOnWhilePlaying(player: Player) {
    val view = LocalView.current
    DisposableEffect(player, view) {
        fun update() {
            val state = player.playbackState
            view.keepScreenOn = player.playWhenReady && (state == Player.STATE_READY || state == Player.STATE_BUFFERING)
        }
        val listener = object : Player.Listener {
            override fun onEvents(player: Player, events: Player.Events) = update()
        }
        player.addListener(listener)
        update()
        onDispose {
            player.removeListener(listener)
            view.keepScreenOn = false
        }
    }
}
