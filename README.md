<p align="center"><img src="docs/logo.png" alt="OpenDVR" width="420"></p>

# OpenDVR

Self-hosted live TV and DVR for HDHomeRun tuners, built to run as an Unraid
container with Intel Quick Sync.

The core idea: **one tuner session per channel, shared by any number of
viewers**. A two-tuner HDHomeRun can serve a whole household as long as they
are watching at most two distinct channels. Each viewer gets their own pause
and rewind inside a server-side ring buffer, so one person pausing never
affects anyone else.

Status: **phase 4** (DVR with series passes and commercial skipping, Android TV app, audit log).
See [docs/ROADMAP.md](docs/ROADMAP.md) for what comes next and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how it is put together.

## Quick start (Unraid)

1. Add the template from `unraid/opendvr.xml` (or install from the
   GHCR image `ghcr.io/mcgeaverbeaver/opendvr:latest`).
2. Set **HDHomeRun hosts** to the tuner's IP. Over a VPN this is required:
   UDP discovery does not cross subnets.
3. Keep the `--device=/dev/dri:/dev/dri` extra parameter for Quick Sync.
4. Point **Live buffer** at cache/NVMe storage.
5. Open the web UI on port 8089.

> Without `AUTH_ISSUER` the server has no login, and anyone who can reach
> port 8089 can watch, record and delete. Keep it on the LAN or behind a VPN,
> and turn on [authentication](#authentication-authentik) before exposing it
> through a reverse proxy.

## Quick start (docker compose)

```sh
cp .env.example .env     # set HDHR_HOSTS
docker compose up -d --build
```

## Development

Requirements: Go 1.24, Node 22, ffmpeg on PATH.

```sh
make test                         # go vet, go test (incl. end-to-end with a fake tuner), tsc
make run HDHR_HOSTS=192.168.1.50  # backend on :8089 serving web/dist
cd web && npm run dev             # Vite dev server on :5173, proxies /api
```

The end-to-end test in `internal/api/e2e_test.go` spins up a fake HDHomeRun
that emits real MPEG-2/AC-3 transport streams, so the whole tune, transcode
and serve path runs in CI without hardware.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8089` | HTTP listen address |
| `HDHR_HOSTS` | | Comma separated tuner IPs or hostnames |
| `HDHR_DISCOVER` | `true` if no hosts | UDP broadcast discovery on the local subnet |
| `DATA_DIR` | `./data` (`/config` in Docker) | Persistent state |
| `BUFFER_DIR` | `DATA_DIR/buffer` (`/buffer`) | Live ring buffers; use fast disk |
| `ENCODER` | `auto` | `auto`, `qsv`, `vaapi`, `software` |
| `RENDER_DEVICE` | `/dev/dri/renderD128` | DRM render node for hardware encoding |
| `DEINTERLACE` | `true` | Deinterlace 1080i broadcast |
| `SEGMENT_SECONDS` | `2` | HLS segment length |
| `BUFFER_MINUTES` | `30` | Live history kept per channel |
| `IDLE_TIMEOUT` | `45s` | Session lifetime after the last viewer request |
| `VIEWER_TIMEOUT` | `20s` | How long a viewer handle survives without a heartbeat |
| `GUIDE_REFRESH` | `30m` | How often programme listings are re-fetched from SiliconDust |
| `GUIDE_URL` | SiliconDust guide API | Override the guide endpoint (tests, proxies) |
| `RECORDINGS_DIR` | `DATA_DIR/recordings` (`/recordings`) | Where the DVR writes `Show/Show - S01E02 - Episode.mp4` |
| `COMSKIP_PATH` | `comskip` on PATH | Commercial detector; `off` disables detection |
| `DVR_PAD_BEFORE` / `DVR_PAD_AFTER` | `1` / `3` | Default minutes recorded before and after the listed times |
| `DVR_LOOKAHEAD` | `24h` | How far ahead passes are matched against the guide |
| `APK_DIR` | `/app/apk` in Docker | Extra folder of app packages offered at `/downloads` |
| `DVR_KEEP_ORIGINAL_DAYS` | `7` | Keep each recording's original broadcast file this many days, so commercials can be found again with other settings; `0` deletes it after processing |
| `DVR_MAX_GB` | `0` (no cap) | Keep the recordings folder under this size; watched recordings go first, then the oldest |
| `CAPTIONS` | `true` | Extract closed captions from recordings to WebVTT sidecars |
| `HDHR_EMULATE` | `false` | Serve `discover.json`, `lineup.json` and `/auto/v{ch}` so Plex, Jellyfin or Emby can use this server as a tuner |
| `NOTIFY_WEBHOOK` | | URL to POST JSON to for failed recordings and conflicts (ntfy, Gotify, Discord, Slack, Home Assistant) |
| `NOTIFY_ON_READY` | `false` | Also notify when a recording is ready |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASS` / `SMTP_FROM` | | Email transport for the same notifications |
| `NOTIFY_EMAIL` | | Comma separated recipients |
| `AUTH_ISSUER` | | OpenID Connect issuer (Authentik: `https://auth.example.com/application/o/<slug>/`). Setting it puts everything behind login |
| `AUTH_CLIENT_ID` | | OIDC client id |
| `AUTH_CLIENT_SECRET` | | OIDC client secret |
| `AUTH_PUBLIC_URL` | derived from each request | How browsers reach this server, e.g. `https://tv.example.com`; the redirect URI is `<url>/auth/callback` |
| `AUTH_SESSION_SECRET` | generated into `DATA_DIR/session.key` | HMAC key for session cookies and stream tokens |
| `AUTH_SESSION_TTL` | `720h` | How long a sign-in lasts |
| `AUTH_STREAM_TTL` | `12h` | Lifetime of a signed stream URL |
| `MCP_TOKEN` | | Bearer token `/mcp` accepts when auth is on |
| `AUTH_GROUP` | | Authentik group a user must belong to in order to sign in at all |
| `MCP_GROUP` | | Authentik group required to use `/mcp` through the connector, e.g. `admin` |
| `ADMIN_GROUP` | `MCP_GROUP` | Authentik group that holds the administrator role (audit log, who is watching, connectors, stream control). With both empty every signed-in user is an administrator |
| `TRUSTED_PROXIES` | loopback and private ranges | Networks whose `X-Forwarded-For` is believed for client addresses |
| `PUID` / `PGID` / `UMASK` | `99` / `100` / `022` | Account the container drops to after start-up (Docker image only) |
| `READY_TIMEOUT` | `20s` | How long a viewer waits for a channel to start before it is retried or failed |
| `VIDEO_BITRATE_K` | `6000` | Video bitrate, kbit/s |
| `AUDIO_BITRATE_K` | `192` | Audio bitrate, kbit/s |
| `FFMPEG_LOGLEVEL` | `info` | ffmpeg's own log level for sessions (`verbose` when diagnosing) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

If a hardware encoder fails to start, the session is retried with the
software encoder and the Status page shows the server as degraded.

## Troubleshooting

```sh
# Can the container reach the tuner?
docker exec opendvr /app/opendvr -check 192.168.1.50

# Run the exact transcode pipeline for one channel with full ffmpeg output
docker exec opendvr /app/opendvr -encode-test 57.1
```

The Status page shows the encoder in use, the hardware self-test result,
and the last ten session failures with ffmpeg's output and command line.

## Claude Desktop connector (MCP)

The server speaks the Model Context Protocol at `/mcp`, so Claude Desktop
can read the lineup, tuner status, active streams, failures and the server
log, start or stop channels, and run the DVR: search the guide, record a
programme or create a pass, list what is scheduled and recorded, and
delete. This is the fastest way to troubleshoot ("why isn't 57.1
playing") and the easiest way to set up recordings ("record every new
Jeopardy and skip the ads").

Claude Desktop runs local MCP servers over stdio, so a small bridge
(`mcp-remote`, needs Node.js) forwards to the container on your LAN. Add
this to `claude_desktop_config.json` (macOS: `~/Library/Application
Support/Claude/`, Windows: `%APPDATA%\Claude\`) and restart Claude Desktop:

```json
{
  "mcpServers": {
    "opendvr": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://<server-ip>:8089/mcp", "--allow-http"]
    }
  }
}
```

Once the server sits behind your reverse proxy with HTTPS, it can instead be
added directly as a custom connector with the URL `https://your-host/mcp`,
no bridge needed.

Tools: `get_status`, `list_channels`, `get_tuners`, `list_streams`,
`list_viewers`, `get_guide`, `get_failures`, `start_stream`, `stop_stream`,
`rescan_tuners`, `test_connectivity`, `get_logs`.

With auth on, `/mcp` is an OAuth 2.1 protected resource and the server is
its own authorization server for MCP clients, so nothing about the
connector is configured in Authentik. In Claude, add a custom connector
with `https://your-host/mcp` and leave client id and secret empty: Claude
discovers the metadata, registers itself (RFC 7591), and opens the
authorization page, which sends you through the normal Authentik sign-in
and then shows a consent screen. Tokens are the server's own: access
tokens last an hour and refresh silently for 30 days with rotation. The
Status page lists every connector and session with last activity and
lets you revoke a session or remove a connector. `MCP_GROUP` (default
`admin` in the Unraid template) restricts who may connect an assistant;
members of that group also manage connectors. A static `MCP_TOKEN` bearer
is still accepted for bridges such as `mcp-remote` on the LAN. Without
auth the endpoint is open, so keep the server on the LAN. A static
`MCP_TOKEN` bearer is still accepted for bridges such as `mcp-remote`
(`"--header", "Authorization:Bearer <token>"`). Without auth the endpoint
is open, so keep the server on the LAN.

## API

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | Liveness |
| GET | `/api/system` | Version, ffmpeg capabilities, encoder in use |
| GET | `/api/devices` | Known tuners |
| POST | `/api/devices/refresh` | Rescan tuners and lineups |
| GET | `/api/devices/{id}/status` | Live tuner status (signal, lock, client) |
| GET | `/api/channels` | Merged channel lineup |
| GET | `/api/channels/{n}/stream/index.m3u8` | HLS playlist; starts the session if needed |
| GET | `/api/channels/{n}/stream/seg_000123.ts` | HLS segment |
| POST | `/api/channels/{n}/stream/release` | Free the tuner within seconds instead of waiting for `IDLE_TIMEOUT` (anonymous players) |
| POST | `/api/watch` | Join a channel as a viewer: `{channel, viewerId?, name?, deviceName?, client?}` returns the viewer handle and playlist; `409` lists the locked tuners when none is free. The device is shown as `name` (chosen in the profile), else `deviceName` (the device's own), else its name on the home network (reverse DNS), else `client` or the User-Agent |
| POST | `/api/watch/{id}/heartbeat` | Keep a viewer alive: `{paused}`; `404` means join again |
| POST | `/api/watch/{id}/leave` | Drop a viewer; releases the session if it was the last one |
| GET | `/api/viewers` | Everyone watching |
| GET | `/api/guide?from=&to=` | Programme listings for every channel in a window (unix seconds); extends the cache on demand |
| GET | `/api/guide/now` | What is on now and next, per channel |
| GET | `/api/guide/{n}?to=` | One channel's listings |
| GET | `/api/guide/search?q=` | Upcoming airings matching words in the title, episode or synopsis |
| GET | `/api/dvr` | DVR summary: folder, disk space, comskip, tuner count, counts by state |
| GET/POST | `/api/dvr/rules` | List or create series passes |
| PUT/DELETE | `/api/dvr/rules/{id}` | Edit or delete a pass |
| GET | `/api/dvr/recordings?status=` | Recordings; `upcoming`, `library`, or one state |
| POST | `/api/dvr/recordings` | Record one airing: `{channel, airing, padBefore?, padAfter?, commercials?}` |
| POST | `/api/dvr/record-now` | Record a channel from now: `{channel, minutes?}` |
| GET/DELETE | `/api/dvr/recordings/{id}` | One recording; delete cancels or removes it and its file |
| GET | `/api/dvr/recordings/{id}/video` | The MP4, with range requests; accepts a stream token |
| GET | `/api/dvr/recordings/{id}/captions.vtt` | Closed captions as WebVTT, when extracted |
| PUT | `/api/dvr/recordings/{id}/position` | Remember where playback stopped: `{position, watched?}` |
| GET/PUT | `/api/me/prefs` | The caller's favourites |
| GET | `/api/users` | Accounts seen, with profiles (administrators) |
| PUT | `/api/users/{sub}` | Kids mode: `{restricted, allowed}` (administrators) |
| GET | `/api/admin/backup` | Zip of the state files (administrators) |
| POST | `/api/admin/restore` | Upload a backup zip (administrators) |
| GET | `/discover.json`, `/lineup.json`, `/lineup_status.json`, `/auto/v{ch}` | HDHomeRun emulation for Plex and Jellyfin, when `HDHR_EMULATE` is on |
| GET | `/api/streams` | Active sessions |
| DELETE | `/api/streams/{n}` | Stop a session |
| GET | `/api/streams/failures` | Recent session failures with ffmpeg output |
| GET | `/api/diagnostics?host=…` | Connectivity probe from inside the container |
| GET | `/api/logs?lines=200` | Recent server log lines |
| POST | `/mcp` | Model Context Protocol endpoint |
| GET | `/api/me` | Whether auth is on, who is signed in and whether they are an administrator |
| GET | `/api/audit?kind=&q=&limit=` | The audit log, newest first (administrators) |
| GET | `/downloads/{file}.apk` | Serves an app package: the APK baked into the image, or one placed in `DATA_DIR` (`/config`) |
| GET | `/api/downloads` | Lists the app packages on offer |
| GET | `/api/mcp/info` | MCP URL, whether OAuth is on, whether the caller may manage connectors |
| GET | `/api/mcp/clients` | Registered connectors with their sessions and last activity (MCP group) |
| DELETE | `/api/mcp/clients/{id}` | Remove a connector and revoke its tokens |
| DELETE | `/api/mcp/sessions/{id}` | Revoke one connector sign-in |
| GET | `/.well-known/oauth-authorization-server` | OAuth metadata for MCP clients |
| POST | `/mcp/oauth/register` | Dynamic client registration |
| GET | `/mcp/oauth/authorize` | Authorization endpoint (PKCE, consent) |
| POST | `/mcp/oauth/token` | Code and refresh grants |
| POST | `/mcp/oauth/revoke` | Token revocation |
| GET | `/auth/login?next=` | Start the OpenID sign-in |
| GET | `/auth/callback` | OpenID redirect target |
| GET/POST | `/auth/logout` | End the session |
| POST | `/auth/device/start` | Begin a device (QR) sign-in: user code, verification URL, QR image |
| POST | `/auth/device/poll` | Poll a device sign-in; sets the session cookie when approved |

## DVR

Recordings are set up from the guide (click a programme, **Record…**),
from the DVR page's search, from the player's **● Rec** button (records
the rest of what is on), from the TV app, or by an assistant over MCP.
A one-off records one airing; a **pass** records every airing of a show,
with options for new episodes only, one season, only one channel, how
many to keep, padding, and what to do about commercials.

How a recording is made:

1. At the start time (minus padding) the server takes a tuner, like a
   live viewer would, and copies the broadcast transport stream to
   `RECORDINGS_DIR/Show/… .ts` unchanged. A dropped stream is retried
   until the end time; a busy tuner is retried every 30 s.
2. When it ends, `comskip` scans the file for commercial breaks (unless
   the recording asked for none).
3. ffmpeg transcodes to H.264/AAC MP4 with the same encoder as live TV
   (Quick Sync on Unraid). With **remove**, the breaks are cut out during
   that transcode; with **skip**, they are kept in the file and marked,
   and the browser and TV players jump over them automatically (there is
   a toggle). The `.ts` is deleted.

Conflicts: passes are tried in priority order (drag them on the Passes
tab) and anything that would need more tuners than you have shows as
**No tuner free** on the Upcoming tab; it still records if a tuner is
free when the time comes. Live viewers are not reserved for, so a
recording can take the last tuner from a viewer who changes channel.

Commercial detection settings live in `DATA_DIR/comskip.ini`, with
optional per-channel files in `DATA_DIR/comskip/<channel>.ini` that
replace it for that channel; a channel file containing
`opendvr_disable=1` turns detection off there, for channels without
ads. The defaults use the station logo, silence and scene changes as
well as black frames, since many stations run ads without black frames
between them. While the original broadcast is kept, **Commercials…** on
a recording finds them again with the current settings and rebuilds the
video, and the assistant tools `test_comskip` and `save_comskip_settings`
try settings on a kept recording without changing it.

Files are named `Show/Show - S01E02 - Episode.mp4` so Plex or Jellyfin
can index the folder too. `DATA_DIR/comskip.ini` is written on first use
and can be tuned.

More DVR behaviour:

- **Time slots** (Passes tab, *Record a time slot*): a channel at a time
  of day on chosen weekdays or on one date, for programmes the guide does
  not list or to record a whole block.
- **Watched**: a recording is marked watched when playback reaches the
  end (or by hand). *Continue watching* and *Up next* on the DVR page and
  the TV app come from that. A pass can *delete a day after watching*,
  and `DVR_MAX_GB` keeps the folder under a size, removing watched
  recordings first and then the oldest.
- **Closed captions**: the broadcast's CEA-608 captions ride through live
  streams (the **CC** button in the player, *Captions* in the TV app's
  menu) and are extracted from recordings to a WebVTT file, with cue
  times shifted when commercials were cut.
- **Notifications**: failed recordings and conflicts (and, with
  `NOTIFY_ON_READY`, finished recordings) go to a webhook and/or email.
- **Profiles**: favourites are stored per account (the star on a channel
  card). An administrator can put an account in **kids mode** on the
  Admin page: it sees only the ticked channels in the grid, the guide and
  the TV app, and cannot change recordings.
- **Tuner for Plex, Jellyfin, Emby**: with `HDHR_EMULATE=true`, add this
  server's address as an HDHomeRun in those apps. They get the lineup and
  raw MPEG-TS streams through OpenDVR's tuner selection, so a stream they
  open counts against the same tuners as everyone else. With auth on, the
  stream URLs in the lineup carry a long-lived token; the two JSON files
  are open and reveal only channel numbers and names.
- **Backup and restore** on the Admin page: passes, recording metadata,
  profiles, connector tokens, comskip settings, the session key, the TV
  app signing key and the audit log in one zip (not the video files).

## Administrators, audit log and GeoIP

Members of `ADMIN_GROUP` (by default the same group as `MCP_GROUP`) get
an **Admin** page: who is watching right now with their account, device
name, address and country; the audit log of sign-ins and sign-in
failures, TV approvals, channels watched, recordings and passes created
or deleted, assistants connected and administrative actions; connector
management; stream failures and the connectivity test. The same role is
required for `/api/viewers`, `/api/logs`, `/api/diagnostics`, stopping a
stream and rescanning tuners. Everyone else sees the Status page without
addresses or accounts.

The log is `DATA_DIR/audit.jsonl` (rotated at 8 MB, one older copy kept)
and the newest few thousand entries are kept in memory.

Countries and cities come from a MaxMind database you supply: create a
free MaxMind account, download `GeoLite2-City.mmdb` (or the Country
edition) and drop it into the config folder next to `guide.json`. The
file is picked up within a minute, re-read when it changes, and never
downloaded by the server itself. LAN addresses show as `LAN`.

Behind a reverse proxy the client address is taken from
`X-Forwarded-For` only when the connection comes from a private or
loopback address; set `TRUSTED_PROXIES` to your proxy's address to
tighten that further.

## Running as non-root

The image starts as root only to prepare the mounted folders, then drops
to `PUID:PGID` (default `99:100`, Unraid's `nobody:users`) with
`setpriv`, adding the groups that own `/dev/dri` so Quick Sync keeps
working. On first start after upgrading it changes ownership of
`/config` and `/buffer`, and of the top two levels of `/recordings`, so
files the previous root-running version created become usable. Set
`PUID=0` to stay root, or run with `--user` to skip the setup.

Other hardening in the image and server: every response carries a
content security policy confined to the server's own origin, no framing
by other sites, no MIME sniffing and a same-origin referrer policy;
session and sign-in cookies are HttpOnly, SameSite=Lax and Secure behind
HTTPS; stream URLs are signed per channel and expire; the session key,
audit log and connector tokens are written with mode 0600.

## Authentication (Authentik)

Leave `AUTH_ISSUER` unset and the server is open: fine on a LAN, not for
the internet. To put it behind Authentik:

1. In Authentik create an **OAuth2/OpenID Provider**: client type
   *Confidential*, redirect URI `https://your-host/auth/callback`,
   signing key selected, scopes `openid profile email`. Create an
   **Application** bound to it and give the users or groups who may watch
   access to that application.
2. Set the container variables: `AUTH_ISSUER` to the provider's issuer
   (`https://auth.example.com/application/o/<application-slug>/`),
   `AUTH_CLIENT_ID`, `AUTH_CLIENT_SECRET`, `AUTH_PUBLIC_URL`, and an
   `MCP_TOKEN` for the Claude connector.

What changes with auth on: the web UI shows a sign-in page (redirect to
Authentik, or for TVs a code with the server's `/tv` address and a QR
code: the phone opens it, signs in the normal way and approves the TV on
a page served by this server); every `/api` call needs
the session cookie; playlists and segments carry a signed, channel-bound
stream token (`?t=`) so native players, AirPlay and Chromecast work
without cookies; `/mcp` needs the bearer token. The container health
check and the app shell stay open.

For local development without Authentik, `go run ./internal/auth/authtest/cmd
http://<your-ip>:9400` runs a fake provider that approves every sign-in;
point `AUTH_ISSUER` at it with client id `cc` and secret `secret`.

## Android TV / Fire TV app

`android/` holds a Kotlin app (Jetpack Compose, Media3 ExoPlayer) for
Android TV and Fire TV: channel grid, guide, recordings, playback with
pause and rewind, QR sign-in when auth is on, and the "all tuners busy,
join one of these" screen. The image build compiles the app and bakes it
in, so there is nothing to copy onto the server: every container serves
it at `http://<server>:8089/downloads/opendvr-tv.apk`, and the Status
page has the download link and Fire TV Downloader instructions.

The server signs that APK with its own key, created on first start as
`DATA_DIR/apk-signing.pem` and included in the Admin page backup. Every
version a server hands out therefore carries the same signature, and a
newer app installs over the old one keeping its settings and sign-in.
CI's own builds use a throwaway key per run, so an APK taken from a CI
artifact, or built on a PC, will not update an app installed from the
server: uninstall first. An .apk dropped into `/config` is listed as
*added by hand* and served unchanged. See `android/README.md`.

### Install app button

The web app's **Install app** button picks the right app for the device.
On Android it offers the APK above; in Chrome-based browsers the same tap
opens the app instead when it is already installed (an `intent://` link
with the APK as its fallback). Everywhere else it installs the web app
(PWA): the browser's own prompt where there is one, Share, Add to Home
Screen steps on iPhone and iPad. An installed web app opens links to the
server itself where the browser supports it (Chrome on Android always;
desktop Chrome and Edge through the manifest's `launch_handler`).

## License

MIT; see [LICENSE](LICENSE).
