package com.opendvr.tv.ui

import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme

val Bg = Color(0xFF0F1115)
val Panel = Color(0xFF171A21)
val Panel2 = Color(0xFF1F232C)
val TextColor = Color(0xFFE6E9F0)
val Muted = Color(0xFF8B93A7)
val Accent = Color(0xFF5EEAD4)
val Warn = Color(0xFFFBBF24)
val Err = Color(0xFFF87171)

/** The web app's dark palette. */
@Composable
fun Theme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = Accent,
            onPrimary = Bg,
            background = Bg,
            onBackground = TextColor,
            surface = Panel,
            onSurface = TextColor,
            surfaceVariant = Panel2,
            onSurfaceVariant = Muted,
            error = Err,
        ),
        content = content,
    )
}

/** Fill behind a focused control: the accent at low opacity. */
val FocusFill = Color(0x335EEAD4)
