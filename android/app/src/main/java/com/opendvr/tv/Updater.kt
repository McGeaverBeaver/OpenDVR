package com.opendvr.tv

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.content.FileProvider
import java.io.File

/** A newer build of this app offered by the server. */
data class AppRelease(val versionCode: Int, val versionName: String, val url: String)

/**
 * Updating the app from the server it came from. The server serves the
 * built-in APK signed with its own key, so a newer one installs over this
 * one. Android asks once for permission to install apps, and shows its own
 * install screen for every update; an app cannot skip either.
 */
object Updater {
    /** Set when the server has a newer build than this one. */
    var available by mutableStateOf<AppRelease?>(null)
        private set

    suspend fun check(api: Api) {
        available = runCatching { api.latestApp() }.getOrNull()?.takeIf { it.versionCode > BuildConfig.VERSION_CODE }
    }

    /**
     * Downloads the release and hands it to Android's installer. Returns a
     * message for the screen when the person has to do something first.
     */
    suspend fun install(context: Context, api: Api, release: AppRelease, progress: (Int) -> Unit): String? {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !context.packageManager.canRequestPackageInstalls()) {
            val intent = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${context.packageName}"))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            return try {
                context.startActivity(intent)
                "Allow OpenDVR to install apps on the screen that opened, then come back and choose Update again."
            } catch (e: ActivityNotFoundException) {
                "Allow OpenDVR to install apps first. On a Fire TV: Settings, My Fire TV, Developer options, " +
                    "Install unknown apps, then turn on OpenDVR. Then choose Update again."
            }
        }
        val dir = File(context.cacheDir, "updates").apply { mkdirs() }
        dir.listFiles()?.forEach { it.delete() }
        val file = File(dir, "opendvr-tv-${release.versionName}.apk")
        api.download(release.url, file, progress)
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.updates", file)
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        return try {
            context.startActivity(intent)
            null
        } catch (e: ActivityNotFoundException) {
            "This device has no installer for downloaded apps. Install it with the Downloader app instead."
        }
    }
}
