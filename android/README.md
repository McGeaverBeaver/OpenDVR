# OpenDVR for Android TV / Fire TV

A Kotlin app (Jetpack Compose, Media3 ExoPlayer) that plays live TV from a
OpenDVR server. It is a thin client: the server does the tuning,
transcoding, guide and sign-in; the app shows the channel grid and plays
HLS.

## What it does

- First run asks for the server address (`http://192.168.1.10:8089`). The
  device is shown on the server's Status page by the name it gives itself
  (Fire TV and Android TV settings); Settings can override it.
- When the server has authentication on, the app signs in with a QR code
  and a short code approved from a phone (OAuth device flow). The session
  cookie is kept so the TV stays signed in.
- Built for the remote: every control is reached with the D-pad and shows
  a bright focus ring. Text fields open the keyboard only when you press
  OK on them.
- Tabs across the top: Channels, Guide, Settings.
- Channel grid with what is on now and a LIVE badge for channels already
  streaming (joinable without a tuner). Opens on the channel you watched
  last.
- Guide: a row per channel, three hours across. Up/down move between
  channels, left/right along the day; right past the last programme (or
  left past the channel name) turns the page, and rewind/fast-forward
  jump a page. The focused programme's details show above the grid; OK
  plays the channel.
- Player: up/down or channel keys change channel, left/right skip 30 s
  in the live buffer, OK shows the info bar, play/pause pauses (the
  server keeps the buffer), menu opens the guide, back returns to where
  the channel was picked.
- "All tuners are busy" is a menu: join a channel already streaming, try
  again, or go back, all with the remote.
- Recordings tab: Continue watching and Up next shelves, then every
  recording by show. OK on one: resume, play from the start, mark
  watched, delete. Playback resumes where it stopped, jumps over marked
  commercials, offers captions, and marks the recording watched at the
  end. Everything is stored on the server; the TV only plays.
- Record from the guide: OK on a programme offers Watch, Record this
  airing, Record every episode, Record new episodes only. In the player,
  OK twice opens a menu with Record the rest of this programme, captions
  on/off, Guide and Channels.
- Kids profiles (set by an administrator on the web Admin page) see only
  their channels and get no record choices.

## Build

Requirements: JDK 17 and the Android SDK (platform 34, build-tools 34).
Either open `android/` in Android Studio or, from the command line:

```
cd android
gradle assembleDebug        # or ./gradlew if you add the wrapper
```

The debug APK lands in `app/build/outputs/apk/debug/app-debug.apk`. CI
builds it on every push, attaches it as the `opendvr-tv-debug` artifact
and bakes it into the container image.

## Signing

Each build machine signs debug builds with its own throwaway key, and
Android only installs an update signed with the same key as the app
already installed. The server therefore re-signs the built-in APK with a
key of its own (`DATA_DIR/apk-signing.pem`, created on first start) when
it serves it. Install from the server and every later version installs
over the last. An APK from a CI artifact or a local build has a different
signature: uninstall the app before installing one of those.

## Install on a Fire TV

Enable *Apps from unknown sources* (Settings → My Fire TV → Developer
options), open the Downloader app and enter
`http://<server>:8089/downloads/opendvr-tv.apk` (the Status page shows the
exact address). From a PC: `adb connect <tv-ip>`, download the same URL,
then `adb install opendvr-tv.apk`.

## Updating

When the server has a newer build than the one installed, the top bar
says so and Settings offers **Update to x.y**. The app downloads it from
the server and opens Android's installer. The first time, Android asks
to allow OpenDVR to install apps (Fire TV: Settings, My Fire TV,
Developer options, Install unknown apps); after that each update is one
press of Install. Android does not let an app skip that confirmation.
The version comes from `opendvr-tv.json`, which CI writes next to the
APK in the image.

## Sign-in

The app keeps whatever session cookie the server sets after the phone
approves the code, without assuming its name, and checks the session
works before leaving the sign-in screen. If it does not, the screen says
so instead of starting over with a new code.
