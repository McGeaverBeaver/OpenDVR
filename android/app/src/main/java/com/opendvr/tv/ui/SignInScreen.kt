package com.opendvr.tv.ui

import android.content.Intent
import android.graphics.BitmapFactory
import android.net.Uri
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
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
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.opendvr.tv.Api
import com.opendvr.tv.DeviceStart
import kotlinx.coroutines.delay

/**
 * Device sign-in. On a TV the viewer scans the QR code or types the short
 * address and code on a phone; on a phone or tablet the "Sign in here"
 * button opens the same page in the browser, and the app signs itself in
 * once the server reports approval.
 *
 * Wide screens get the QR code beside the text; narrow ones stack and
 * scroll, so the code and buttons are always reachable. Focus starts on
 * the first button so the remote has somewhere to be.
 */
@Composable
fun SignInScreen(api: Api, onSignedIn: () -> Unit, onChangeServer: () -> Unit) {
    val context = LocalContext.current
    var start by remember { mutableStateOf<DeviceStart?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var attempt by remember { mutableStateOf(0) }
    val first = remember { FocusRequester() }

    LaunchedEffect(attempt) {
        error = null
        start = null
        val s = try {
            api.deviceStart()
        } catch (e: Exception) {
            error = e.message ?: "Could not start sign-in"
            return@LaunchedEffect
        }
        start = s
        while (true) {
            delay(maxOf(2, s.interval) * 1000L)
            val r = try {
                api.devicePoll(s.handle)
            } catch (e: Exception) {
                "error: ${e.message}"
            }
            when {
                r == "ok" -> {
                    // The approval hands this device its session. Check the
                    // session really works before leaving: if it does not,
                    // say so here rather than bouncing back with a new code.
                    val me = try { api.me() } catch (e: Exception) { null }
                    if (me != null && (!me.enabled || me.userName != null)) {
                        onSignedIn()
                    } else {
                        error = "The sign-in was approved, but this device did not get a working session from the server. " +
                            "Make sure the server is up to date, then try again."
                        start = null
                    }
                    return@LaunchedEffect
                }
                r.startsWith("error") -> {
                    error = r.removePrefix("error: ")
                    start = null
                    return@LaunchedEffect
                }
            }
        }
    }
    LaunchedEffect(start, error) { runCatching { first.requestFocus() } }

    val openInBrowser: () -> Unit = {
        start?.let { s ->
            val target = s.verificationUriComplete.ifEmpty { s.verificationUri }
            runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(target)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
        }
    }

    BoxWithConstraints(Modifier.fillMaxSize()) {
        val wide = maxWidth > 640.dp
        val qrSize = if (wide) minOf(maxHeight * 0.55f, 260.dp) else 180.dp

        @Composable
        fun Qr() {
            start?.let { s ->
                val bmp = remember(s.handle) { BitmapFactory.decodeByteArray(s.qrPng, 0, s.qrPng.size) }
                Image(bmp.asImageBitmap(), contentDescription = "QR code", modifier = Modifier.size(qrSize))
            }
        }

        @Composable
        fun Details() {
            Text("Sign in", color = TextColor, fontSize = 26.sp, fontWeight = FontWeight.SemiBold)
            Spacer(Modifier.height(8.dp))
            when {
                start != null -> {
                    val s = start!!
                    Text("Scan the code with your phone, or open", color = Muted, textAlign = TextAlign.Center)
                    Text(s.verificationUri.removePrefix("https://").removePrefix("http://"), color = TextColor, fontWeight = FontWeight.SemiBold)
                    Text("and enter this code:", color = Muted)
                    Text(s.userCode, color = Accent, fontSize = 36.sp, fontWeight = FontWeight.Bold, letterSpacing = 5.sp)
                    Text("Waiting for approval…", color = Muted)
                    Spacer(Modifier.height(12.dp))
                    TvButton("Sign in on this device", modifier = Modifier.focusRequester(first), onClick = openInBrowser)
                    Text("Opens the sign-in in this device's browser; come back here once it says approved.", color = Muted, fontSize = 12.sp, textAlign = TextAlign.Center)
                }
                error != null -> Text(error!!, color = Err, textAlign = TextAlign.Center)
                else -> Text("Getting a code…", color = Muted)
            }
            Spacer(Modifier.height(16.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                if (error != null) TvButton("Try again", modifier = Modifier.focusRequester(first)) { attempt++ }
                TvButton("Change server", onClick = onChangeServer)
            }
        }

        if (wide) {
            Row(
                Modifier.fillMaxSize().padding(horizontal = 40.dp, vertical = 24.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.Center,
            ) {
                Qr()
                Spacer(Modifier.width(40.dp))
                Column(
                    Modifier.verticalScroll(rememberScrollState()),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) { Details() }
            }
        } else {
            Column(
                Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Spacer(Modifier.height(8.dp))
                Qr()
                Spacer(Modifier.height(16.dp))
                Details()
                Spacer(Modifier.height(24.dp))
            }
        }
    }
}
