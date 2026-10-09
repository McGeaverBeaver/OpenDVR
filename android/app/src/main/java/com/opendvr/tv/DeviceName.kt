package com.opendvr.tv

import android.content.Context
import android.os.Build
import android.provider.Settings

/**
 * The name this TV or phone gives itself: the one set in its own settings
 * (Fire TV: "Living Room Fire TV"; Android TV: Device Preferences, About),
 * else maker and model. The server shows it next to the signed-in account
 * unless a name is set in this app's Settings.
 */
fun deviceLabel(context: Context): String {
    val named = runCatching { Settings.Global.getString(context.contentResolver, "device_name") }.getOrNull()
    if (!named.isNullOrBlank()) return named.trim().take(40)
    val maker = Build.MANUFACTURER.orEmpty().replaceFirstChar { it.uppercase() }
    val model = Build.MODEL.orEmpty()
    return (if (model.startsWith(maker, ignoreCase = true)) model else "$maker $model").trim().take(40)
}
