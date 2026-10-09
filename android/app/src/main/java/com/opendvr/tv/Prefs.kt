package com.opendvr.tv

import android.content.Context
import android.content.SharedPreferences

/** Persistent settings: server address, the session cookie, this device's name and viewer id. */
class Prefs(context: Context) {
    private val sp: SharedPreferences = context.getSharedPreferences("opendvr", Context.MODE_PRIVATE)

    /** The server's address. Changing it drops the sign-in, which belongs to the old server. */
    var serverUrl: String
        get() = sp.getString("serverUrl", "") ?: ""
        set(v) {
            val url = v.trim().trimEnd('/')
            val e = sp.edit().putString("serverUrl", url)
            if (url != serverUrl) e.remove("cookies")
            e.apply()
        }

    /**
     * Cookies the server has set, name to value: the sign-in session. The
     * app never assumes the cookie's name, so it keeps working with any
     * server version.
     */
    var cookies: Map<String, String>
        get() = (sp.getString("cookies", "") ?: "").lineSequence().mapNotNull { line ->
            val i = line.indexOf('=')
            if (i <= 0) null else line.substring(0, i) to line.substring(i + 1)
        }.toMap()
        set(v) = sp.edit().putString("cookies", v.entries.joinToString("\n") { "${it.key}=${it.value}" }).apply()

    /** Whether the server has given this device a session. */
    val hasSession: Boolean get() = cookies.isNotEmpty()

    fun signOut() = sp.edit().remove("cookies").remove("session").apply()

    var deviceName: String
        get() = sp.getString("deviceName", "") ?: ""
        set(v) = sp.edit().putString("deviceName", v.trim().take(40)).apply()

    var viewerId: String
        get() = sp.getString("viewerId", "") ?: ""
        set(v) = sp.edit().putString("viewerId", v).apply()

    var lastChannel: String
        get() = sp.getString("lastChannel", "") ?: ""
        set(v) = sp.edit().putString("lastChannel", v).apply()
}
