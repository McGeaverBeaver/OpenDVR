package com.opendvr.tv.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.opendvr.tv.Api
import com.opendvr.tv.Prefs
import kotlinx.coroutines.launch

/**
 * Server address and device name. Shown on first run and from Settings.
 *
 * Moving focus onto a field does not open the keyboard; pressing OK on it
 * does, and Next/Done on the keyboard moves on. On first run focus starts
 * in the address field; later it starts on Connect, so a visit to Settings
 * never pops a keyboard up by itself.
 */
@Composable
fun SetupScreen(prefs: Prefs, api: Api, autoName: String, initialError: String?, onDone: () -> Unit, onRestart: () -> Unit, onCancel: (() -> Unit)?) {
    var url by remember { mutableStateOf(prefs.serverUrl.ifEmpty { "http://" }) }
    var name by remember { mutableStateOf(prefs.deviceName) }
    var error by remember { mutableStateOf(initialError) }
    var busy by remember { mutableStateOf(false) }
    var scanning by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf<String?>(null) }
    var updating by remember { mutableStateOf<Int?>(null) }
    val context = androidx.compose.ui.platform.LocalContext.current
    LaunchedEffect(Unit) { if (prefs.hasSession || !firstRunCheck(prefs)) com.opendvr.tv.Updater.check(api) }
    val scope = rememberCoroutineScope()
    val keyboard = LocalSoftwareKeyboardController.current
    val focusManager = LocalFocusManager.current
    val urlField = remember { FocusRequester() }
    val connect = remember { FocusRequester() }
    val firstRun = prefs.serverUrl.isEmpty()
    LaunchedEffect(Unit) { runCatching { (if (firstRun) urlField else connect).requestFocus() } }

    // OK on a focused field opens the keyboard; focusing alone does not.
    val okOpensKeyboard = Modifier.onPreviewKeyEvent { ev ->
        if (ev.type == KeyEventType.KeyDown && (ev.key == Key.DirectionCenter || ev.key == Key.Enter || ev.key == Key.NumPadEnter)) {
            keyboard?.show()
            true
        } else {
            false
        }
    }
    val fieldColors = OutlinedTextFieldDefaults.colors(
        focusedBorderColor = Accent, unfocusedBorderColor = Muted, cursorColor = Accent,
        focusedLabelColor = Accent, unfocusedLabelColor = Muted, focusedTextColor = TextColor, unfocusedTextColor = TextColor,
    )

    fun save() {
        busy = true
        error = null
        prefs.serverUrl = url.filterNot { it.isWhitespace() }
        prefs.deviceName = name
        scope.launch {
            try {
                api.health()
                onDone()
            } catch (e: Exception) {
                error = "Cannot reach ${prefs.serverUrl}: ${e.message}"
            } finally {
                busy = false
            }
        }
    }

    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 24.dp, vertical = 32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text("OpenDVR", color = Accent, fontSize = 20.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(8.dp))
        Text(if (firstRun) "Where is the server?" else "Settings", color = TextColor, fontSize = 28.sp, fontWeight = FontWeight.SemiBold)
        Text("The address you open in a browser, for example http://192.168.1.10:8089", color = Muted, textAlign = TextAlign.Center)
        Spacer(Modifier.height(24.dp))
        OutlinedTextField(
            // An address never holds a space. Keyboards that add one after each
            // "." (autocorrect's space after punctuation) are told not to, and
            // the ones that ignore that have the space dropped as it is typed.
            value = url, onValueChange = { v -> url = v.filterNot { it.isWhitespace() } }, singleLine = true, colors = fieldColors,
            label = { Text("Server address") },
            supportingText = { Text("Press OK to type", color = Muted) },
            keyboardOptions = KeyboardOptions(
                capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false,
                keyboardType = KeyboardType.Uri, imeAction = ImeAction.Next, showKeyboardOnFocus = false,
            ),
            keyboardActions = KeyboardActions(onNext = { focusManager.moveFocus(FocusDirection.Down) }),
            modifier = Modifier.fillMaxWidth(0.9f).widthIn(max = 520.dp).focusRequester(urlField).then(okOpensKeyboard),
        )
        Spacer(Modifier.height(8.dp))
        OutlinedTextField(
            value = name, onValueChange = { name = it }, singleLine = true, colors = fieldColors,
            label = { Text("Device name (optional)") },
            placeholder = { Text(autoName, color = Muted) },
            supportingText = { Text("Empty uses \u201c$autoName\u201d. Press OK to type.", color = Muted) },
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done, showKeyboardOnFocus = false),
            keyboardActions = KeyboardActions(onDone = { keyboard?.hide(); focusManager.moveFocus(FocusDirection.Down) }),
            modifier = Modifier.fillMaxWidth(0.9f).widthIn(max = 520.dp).then(okOpensKeyboard),
        )
        Spacer(Modifier.height(20.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            TvButton(if (busy) "Connecting…" else "Connect", enabled = !busy, modifier = Modifier.focusRequester(connect)) { save() }
            if (onCancel != null) TvButton("Back") { onCancel() }
            if (onCancel != null) {
                TvButton(if (scanning) "Scanning…" else "Rescan tuners", enabled = !scanning) {
                    scanning = true
                    error = null
                    note = null
                    scope.launch {
                        try {
                            note = "Lineup refreshed: ${api.rescan()} channels."
                        } catch (e: Exception) {
                            error = "Rescan failed: ${e.message}"
                        } finally {
                            scanning = false
                        }
                    }
                }
            }
            if (prefs.hasSession) TvButton("Sign out") { prefs.signOut(); onRestart() }
        }
        com.opendvr.tv.Updater.available?.let { release ->
            Spacer(Modifier.height(16.dp))
            Text(
                "Version ${release.versionName} is on the server (this is ${com.opendvr.tv.BuildConfig.VERSION_NAME}).",
                color = TextColor, textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(8.dp))
            TvButton(updating?.let { "Downloading\u2026 $it%" } ?: "Update to ${release.versionName}", enabled = updating == null) {
                keyboard?.hide()
                updating = 0
                error = null
                note = null
                scope.launch {
                    try {
                        val msg = com.opendvr.tv.Updater.install(context, api, release) { updating = it }
                        if (msg != null) note = msg
                    } catch (e: Exception) {
                        error = "Update failed: ${e.message}"
                    } finally {
                        updating = null
                    }
                }
            }
        }
        error?.let {
            Spacer(Modifier.height(16.dp))
            Text(it, color = Err, textAlign = TextAlign.Center)
        }
        note?.let {
            Spacer(Modifier.height(16.dp))
            Text(it, color = Accent, textAlign = TextAlign.Center)
        }
    }
}

/** Settings is also the first-run screen; only check for updates once a server is set. */
private fun firstRunCheck(prefs: Prefs): Boolean = prefs.serverUrl.isEmpty()
