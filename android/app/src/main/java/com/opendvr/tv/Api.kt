package com.opendvr.tv

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.Cookie
import okhttp3.CookieJar
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

data class Channel(val number: String, val name: String, val hd: Boolean, val drm: Boolean)
data class Program(
    val start: Long, val end: Long, val title: String, val episodeTitle: String?,
    val episodeNumber: String? = null, val synopsis: String? = null, val isNew: Boolean = false, val seriesId: String? = null,
)
data class NowNext(val now: Program?, val next: Program?)
data class TunerUse(val channel: String, val name: String?, val client: String?, val shared: Boolean)
data class WatchResult(val viewerId: String, val playlist: String)
data class DeviceStart(val handle: String, val userCode: String, val verificationUri: String, val verificationUriComplete: String, val qrPng: ByteArray, val interval: Int)
data class Me(val enabled: Boolean, val userName: String?, val restricted: Boolean = false)
data class Span(val start: Double, val end: Double)
data class Recording(
    val id: String, val channel: String, val channelName: String?, val title: String, val episodeTitle: String?,
    val episodeNumber: String?, val synopsis: String?, val airing: Long, val start: Long, val end: Long,
    val status: String, val error: String?, val durationSec: Double, val sizeBytes: Long, val skips: List<Span>,
    val cut: Boolean, val position: Double, val watched: Boolean, val captions: Boolean,
    val playUrl: String?, val captionsUrl: String?,
)
data class StreamInfo(val channel: String, val viewers: Int)

class ApiException(message: String, val status: Int = 0) : IOException(message)

/** Every tuner is locked; `tuners` says to what. */
class TunersBusyException(val tuners: List<TunerUse>) : IOException("All tuners are busy")

/** The session is missing or expired; sign in again. */
class UnauthenticatedException : IOException("Sign in required")

/**
 * Keeps the cookies the server sets (the sign-in session) in [Prefs] and
 * sends them back to that server only. Nothing here knows what the cookie
 * is called: an app that hard-coded the name stopped working the moment
 * the server and the app disagreed about it, and signing in then started
 * over with a new code forever.
 */
private class StoredCookies(private val prefs: Prefs) : CookieJar {
    private fun ours(url: HttpUrl) = prefs.serverUrl.toHttpUrlOrNull()?.host == url.host

    override fun saveFromResponse(url: HttpUrl, cookies: List<Cookie>) {
        if (cookies.isEmpty() || !ours(url)) return
        val jar = prefs.cookies.toMutableMap()
        val now = System.currentTimeMillis()
        for (c in cookies) {
            if (c.value.isEmpty() || c.expiresAt < now) jar.remove(c.name) else jar[c.name] = c.value
        }
        prefs.cookies = jar
    }

    override fun loadForRequest(url: HttpUrl): List<Cookie> {
        if (!ours(url)) return emptyList()
        return prefs.cookies.mapNotNull { (name, value) ->
            runCatching { Cookie.Builder().name(name).value(value).hostOnlyDomain(url.host).path("/").build() }.getOrNull()
        }
    }
}

/**
 * Thin client for the OpenDVR HTTP API. The session cookie, when auth is
 * on, is kept by [StoredCookies]; stream URLs carry their own token so
 * ExoPlayer needs no cookies.
 */
class Api(private val prefs: Prefs, private val deviceName: String = "") {
    private val http = OkHttpClient.Builder()
        .cookieJar(StoredCookies(prefs))
        .connectTimeout(10, TimeUnit.SECONDS)
        // The watch call blocks until the first segment exists; tuning plus
        // retries can take a while, and the server's own timeout is lower.
        .readTimeout(60, TimeUnit.SECONDS)
        .build()
    private val json = "application/json".toMediaType()

    val base: String get() = prefs.serverUrl

    fun absolute(path: String): String = if (path.startsWith("http")) path else base + path

    private fun req(path: String): Request.Builder =
        Request.Builder().url(absolute(path)).header("User-Agent", "opendvr-tv/${BuildConfig.VERSION_NAME}")

    private suspend fun call(b: Request.Builder): Pair<Int, String> = withContext(Dispatchers.IO) {
        http.newCall(b.build()).execute().use { res -> res.code to (res.body?.string() ?: "") }
    }

    private fun errorOf(code: Int, body: String): IOException {
        val obj = runCatching { JSONObject(body) }.getOrNull()
        if (code == 401 && obj?.optString("code") == "unauthenticated") return UnauthenticatedException()
        if (code == 409 && obj?.optString("code") == "tuners_busy") {
            val arr = obj.optJSONArray("tuners") ?: JSONArray()
            return TunersBusyException((0 until arr.length()).map { i ->
                val t = arr.getJSONObject(i)
                TunerUse(t.optString("channel"), t.optString("name").ifEmpty { null }, t.optString("client").ifEmpty { null }, t.optBoolean("shared"))
            })
        }
        return ApiException(obj?.optString("error")?.ifEmpty { null } ?: "HTTP $code", code)
    }

    private suspend fun getJson(path: String): String {
        val (code, body) = call(req(path))
        if (code !in 200..299) throw errorOf(code, body)
        return body
    }

    private suspend fun postJson(path: String, payload: JSONObject?): Pair<Int, String> {
        val b = req(path).post((payload?.toString() ?: "{}").toRequestBody(json))
        return call(b)
    }

    /** The health endpoint is open even when auth is on, so it is the right connectivity probe. */
    /** The newest app build the server offers, built into its image. */
    suspend fun latestApp(): AppRelease? {
        val arr = JSONArray(getJson("/api/downloads"))
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            if (o.optBoolean("builtIn") && o.optInt("versionCode") > 0) {
                return AppRelease(o.getInt("versionCode"), o.optString("versionName"), absolute(o.getString("url")))
            }
        }
        return null
    }

    /** Saves a file from the server, reporting percent done. */
    suspend fun download(url: String, to: java.io.File, progress: (Int) -> Unit) = withContext(Dispatchers.IO) {
        http.newCall(req(url).build()).execute().use { res ->
            if (!res.isSuccessful) throw ApiException("Download failed: HTTP ${res.code}", res.code)
            val body = res.body ?: throw ApiException("Empty download")
            val total = body.contentLength()
            var done = 0L
            var last = -1
            body.byteStream().use { input ->
                to.outputStream().use { out ->
                    val buf = ByteArray(64 * 1024)
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        done += n
                        if (total > 0) {
                            val pct = (done * 100 / total).toInt()
                            if (pct != last) { last = pct; progress(pct) }
                        }
                    }
                }
            }
        }
    }

    /** Re-reads the HDHomeRun lineup now; returns how many channels the server has. */
    suspend fun rescan(): Int {
        val (code, body) = postJson("/api/devices/refresh", null)
        if (code !in 200..299) throw errorOf(code, body)
        return JSONObject(body).optInt("channels")
    }

    suspend fun health(): Boolean {
        val (code, body) = call(req("/api/health"))
        if (code !in 200..299) throw errorOf(code, body)
        return JSONObject(body).optBoolean("ok")
    }

    suspend fun me(): Me {
        val (code, body) = call(req("/api/me"))
        val obj = runCatching { JSONObject(body) }.getOrNull() ?: throw ApiException("Unexpected reply from server", code)
        if (code == 401) return Me(enabled = true, userName = null)
        if (code !in 200..299) throw errorOf(code, body)
        val user = obj.optJSONObject("user")
        return Me(obj.optBoolean("enabled"), user?.let { it.optString("name").ifEmpty { it.optString("email").ifEmpty { it.optString("sub") } } }, obj.optBoolean("restricted"))
    }

    // ---- DVR -------------------------------------------------------------

    private fun recording(o: JSONObject): Recording {
        val sk = o.optJSONArray("skips") ?: JSONArray()
        return Recording(
            id = o.getString("id"), channel = o.optString("channel"), channelName = o.optString("channelName").ifEmpty { null },
            title = o.optString("title"), episodeTitle = o.optString("episodeTitle").ifEmpty { null },
            episodeNumber = o.optString("episodeNumber").ifEmpty { null }, synopsis = o.optString("synopsis").ifEmpty { null },
            airing = o.optLong("airing"), start = o.optLong("start"), end = o.optLong("end"),
            status = o.optString("status"), error = o.optString("error").ifEmpty { null },
            durationSec = o.optDouble("durationSec", 0.0), sizeBytes = o.optLong("sizeBytes"),
            skips = (0 until sk.length()).map { i -> sk.getJSONObject(i).let { Span(it.optDouble("start"), it.optDouble("end")) } },
            cut = o.optBoolean("cut"), position = o.optDouble("position", 0.0), watched = o.optBoolean("watched"),
            captions = o.optBoolean("captions"),
            playUrl = o.optString("playUrl").ifEmpty { null }?.let(::absolute),
            captionsUrl = o.optString("captionsUrl").ifEmpty { null }?.let(::absolute),
        )
    }

    suspend fun recordings(filter: String = ""): List<Recording> {
        val arr = JSONArray(getJson("/api/dvr/recordings" + if (filter.isNotEmpty()) "?status=$filter" else ""))
        return (0 until arr.length()).map { recording(arr.getJSONObject(it)) }
    }

    suspend fun recording(id: String): Recording = recording(JSONObject(getJson("/api/dvr/recordings/$id")))

    suspend fun recordAiring(channel: String, airing: Long): Recording {
        val (code, body) = postJson("/api/dvr/recordings", JSONObject().put("channel", channel).put("airing", airing))
        if (code !in 200..299) throw errorOf(code, body)
        return recording(JSONObject(body))
    }

    suspend fun recordNow(channel: String): Recording {
        val (code, body) = postJson("/api/dvr/record-now", JSONObject().put("channel", channel))
        if (code !in 200..299) throw errorOf(code, body)
        return recording(JSONObject(body))
    }

    /** Creates a series pass; returns its title. */
    suspend fun createPass(title: String, seriesId: String?, channel: String?, newOnly: Boolean): String {
        val payload = JSONObject().put("title", title).put("newOnly", newOnly)
        if (!seriesId.isNullOrEmpty()) payload.put("seriesId", seriesId)
        if (!channel.isNullOrEmpty()) payload.put("channel", channel)
        val (code, body) = postJson("/api/dvr/rules", payload)
        if (code !in 200..299) throw errorOf(code, body)
        return JSONObject(body).optString("title")
    }

    suspend fun deleteRecording(id: String) {
        val (code, body) = call(req("/api/dvr/recordings/$id").delete())
        if (code !in 200..299 && code != 404) throw errorOf(code, body)
    }

    suspend fun setPosition(id: String, position: Double) {
        runCatching { call(req("/api/dvr/recordings/$id/position").put(JSONObject().put("position", position).toString().toRequestBody(json))) }
    }

    suspend fun setWatched(id: String, watched: Boolean) {
        runCatching { call(req("/api/dvr/recordings/$id/position").put(JSONObject().put("position", 0).put("watched", watched).toString().toRequestBody(json))) }
    }

    suspend fun channels(): List<Channel> {
        val arr = JSONArray(getJson("/api/channels"))
        return (0 until arr.length()).map { i ->
            val c = arr.getJSONObject(i)
            Channel(c.getString("guideNumber"), c.optString("guideName"), c.optBoolean("hd"), c.optBoolean("drm"))
        }
    }

    suspend fun guideNow(): Map<String, NowNext> {
        val obj = JSONObject(getJson("/api/guide/now"))
        val out = HashMap<String, NowNext>()
        for (key in obj.keys()) {
            val nn = obj.getJSONObject(key)
            out[key] = NowNext(nn.optJSONObject("now")?.let(::program), nn.optJSONObject("next")?.let(::program))
        }
        return out
    }

    /** Listings for every channel between two unix times, keyed by guide number. */
    suspend fun guide(from: Long, to: Long): Map<String, List<Program>> {
        val body = getJson("/api/guide?from=$from&to=$to")
        val obj = runCatching { JSONObject(body) }.getOrNull() ?: throw ApiException("The server has no guide")
        val arr = obj.optJSONArray("channels") ?: JSONArray()
        val out = HashMap<String, List<Program>>()
        for (i in 0 until arr.length()) {
            val c = arr.getJSONObject(i)
            val ps = c.optJSONArray("programs") ?: JSONArray()
            out[c.getString("guideNumber")] = (0 until ps.length()).map { program(ps.getJSONObject(it)) }
        }
        return out
    }

    private fun program(p: JSONObject) = Program(
        p.optLong("start"), p.optLong("end"), p.optString("title"), p.optString("episodeTitle").ifEmpty { null },
        p.optString("episodeNumber").ifEmpty { null }, p.optString("synopsis").ifEmpty { null }, p.optBoolean("new"), p.optString("seriesId").ifEmpty { null },
    )

    suspend fun streams(): List<StreamInfo> {
        val arr = JSONArray(getJson("/api/streams"))
        return (0 until arr.length()).filter { arr.getJSONObject(it).optBoolean("running") }.map { i ->
            val s = arr.getJSONObject(i)
            StreamInfo(s.getString("channel"), s.optJSONArray("viewers")?.length() ?: 0)
        }
    }

    suspend fun watch(channel: String): WatchResult {
        val payload = JSONObject().put("channel", channel)
        if (prefs.viewerId.isNotEmpty()) payload.put("viewerId", prefs.viewerId)
        if (prefs.deviceName.isNotEmpty()) payload.put("name", prefs.deviceName)
        if (deviceName.isNotEmpty()) payload.put("deviceName", deviceName)
        val (code, body) = postJson("/api/watch", payload)
        if (code !in 200..299) throw errorOf(code, body)
        val obj = JSONObject(body)
        prefs.viewerId = obj.getString("viewerId")
        return WatchResult(obj.getString("viewerId"), absolute(obj.getString("playlist")))
    }

    /** Returns false when the server no longer knows the viewer (join again). */
    suspend fun heartbeat(viewerId: String, paused: Boolean): Boolean {
        val (code, _) = postJson("/api/watch/$viewerId/heartbeat", JSONObject().put("paused", paused))
        return code != 404
    }

    suspend fun leave(viewerId: String) {
        runCatching { postJson("/api/watch/$viewerId/leave", null) }
    }

    suspend fun deviceStart(): DeviceStart {
        val (code, body) = postJson("/auth/device/start", null)
        if (code !in 200..299) throw errorOf(code, body)
        val obj = JSONObject(body)
        val dataUrl = obj.getString("qr")
        val png = android.util.Base64.decode(dataUrl.substringAfter("base64,"), android.util.Base64.DEFAULT)
        return DeviceStart(obj.getString("handle"), obj.getString("userCode"), obj.getString("verificationUri"), obj.optString("verificationUriComplete"), png, obj.optInt("interval", 5))
    }

    /** "pending", "ok" or an error message. */
    suspend fun devicePoll(handle: String): String {
        val (code, body) = postJson("/auth/device/poll", JSONObject().put("handle", handle))
        val obj = runCatching { JSONObject(body) }.getOrNull() ?: return "error: HTTP $code"
        return when (obj.optString("status")) {
            "ok" -> "ok"
            "pending" -> "pending"
            else -> "error: " + obj.optString("error", "sign-in failed")
        }
    }
}
