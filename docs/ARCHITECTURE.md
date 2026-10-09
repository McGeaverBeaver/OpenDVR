# Architecture

## Goals

- Live TV from HDHomeRun tuners with **tuner sharing**: viewers of the same
  channel share one tuner and one transcode.
- **Per-viewer timeshift**: pause, rewind and catch up inside a server-side
  ring buffer without affecting other viewers or the tuner.
- Runs as a single container on Unraid with Intel Quick Sync.
- Web client that works as a PWA on phones and desktops; native apps for TV
  boxes and iOS later.
- Authentication through Authentik, including a QR code device flow for TVs.

## Topology

The HDHomeRun lives at a remote site reached over a routed VPN. The
application treats it as local: it only needs the tuner's IP. The one
practical consequence is that UDP broadcast discovery does not work across
the VPN, so tuner IPs are pinned with `HDHR_HOSTS`. The HTTP client rewrites
any URL the device reports about itself to the configured host, so a device
that believes it is `192.168.50.10` is still reached at whatever address was
configured.

## Components

```
                 ┌──────────────────────── container ─────────────────────────┐
HDHomeRun ──TS──▶│ ffmpeg (1 per tuned channel) ──▶ HLS ring buffer on disk   │
 (remote site)   │        ▲                              │                     │
                 │   stream.Manager  ◀── acquire/touch ──┘  api (Go, net/http) │──▶ viewers
                 │        ▲                                      ▲             │   (PWA, apps)
                 │   tuner.Registry ◀── discover.json/lineup.json│             │
                 │                                       web/dist (React)     │
                 └─────────────────────────────────────────────────────────────┘
```

### `internal/hdhomerun`

HTTP client for `discover.json`, `lineup.json` and `status.json`, plus the
UDP discovery protocol for same-subnet setups. Pure data access, no policy.

### `internal/tuner`

The registry polls every configured device, merges lineups across devices
(the same channel on two tuners becomes one channel with two sources) and
answers "which stream URL do I use for channel X". Before a session starts it
reads every candidate device's `status.json` and picks the device with the
most free tuners. A tuner streaming to this server for a channel it is
already serving is "shared" (viewers join it); one streaming to this server
for a channel it no longer serves is a just-released tuner the device has
not freed yet and counts as free. When nothing is free the registry returns
`ErrTunersBusy` listing what each tuner is locked to, which the API turns
into a `409` the player renders as "join one of these" buttons.

### `internal/stream`

The broker. `Manager.Acquire(channel)` returns a running session, starting
ffmpeg if there is none. A session is:

- one ffmpeg process reading the tuner's MPEG-TS over HTTP;
- hardware decode and encode via Quick Sync (VAAPI and libx264 as fallbacks);
- HLS output: 2-second MPEG-TS segments, an IDR keyframe forced on every
  boundary (Quick Sync needs `-forced_idr`, or the muxer waits for the
  driver's own ~4 s GOP and every channel start takes twice as long),
  a sliding window of `BUFFER_MINUTES` with old segments deleted by ffmpeg.

Every playlist or segment request touches the session's last-access time,
and a reaper stops sessions that nobody has touched within `IDLE_TIMEOUT`;
that alone keeps anonymous players such as VLC working. The web player goes
further: it joins through `POST /api/watch` with a viewer handle it keeps in
local storage and an optional display name, heartbeats every five seconds
(carrying its paused state, which also keeps a paused session alive for
rewind), and leaves on unload. A viewer whose heartbeats stop for
`VIEWER_TIMEOUT` is dropped; when a channel's last viewer goes the session
is released and the tuner freed within seconds.

Why HLS rather than piping the transport stream straight to each client:

- Browsers and iOS cannot play MPEG-2 video at all. One transcode per channel
  is unavoidable, and HLS lets that one transcode feed everyone.
- Pause and seek are free. A paused client simply stops fetching segments.
  The playlist still advertises the full window, so any client can jump back.
- Segments on disk are the recording. The DVR phase writes the same segments
  (or the untranscoded source) to permanent storage instead of a ring.

### `internal/api`

REST endpoints, HLS serving, and the SPA. The playlist handler blocks until
the first segments exist so clients get a real error message (DRM channel,
tuner unreachable, encoder failure) instead of a player timeout.

### `web/`

React + TypeScript + Vite. Safari plays HLS natively; everywhere else uses
hls.js. The client fetches the playlist once itself before handing it to the
player so that server errors are shown verbatim.

## Hardware transcoding

The image is Debian with `jellyfin-ffmpeg7`, which bundles Intel's oneVPL
runtime and the iHD media driver. The container needs `/dev/dri` passed
through. `ENCODER=auto` selects Quick Sync when the render node exists and
`h264_qsv` is available, otherwise VAAPI, otherwise software.

Decode is always in software. Consumer Intel GPUs do not decode MPEG-2,
which is what ATSC 1.0 carries, so a hardware decode path only produced a
fragile mismatch that died on the first glitchy frame. Decoding and
deinterlacing 1080i MPEG-2 costs a fraction of a core; the encode is the
expensive step and that runs on the GPU. Frames are uploaded with a
64-surface pool, because the Quick Sync encoder holds surfaces for its
async queue and a small pool deadlocks it silently.

At startup the server encodes five seconds of synthetic 720p through the
exact hardware path a session uses. If that fails, hardware is abandoned
for the run and the error is shown on the Status page. If a hardware
session later fails for any reason other than a tuner-side error, the
manager falls back to software for it and marks itself degraded.

## Guide

`internal/guide` fetches programme listings from SiliconDust's guide API
with each tuner's `DeviceAuth` (the free tier: the next several hours for
every channel in one call, about a day per channel on request; a DVR
subscription extends both). Listings are merged by channel and start
time, pruned two hours after they end, persisted to `DATA_DIR/guide.json`
so a restart is not blank, and refreshed every `GUIDE_REFRESH`. When a
client asks for a window beyond what is cached (`GET /api/guide?to=`), the
channels that fall short are extended in parallel, four at a time, with a
cooldown so scrolling back and forth does not hammer the API. The API
rejects Go's default User-Agent, so requests identify as
`opendvr/<version>`.

The web client shows what is on each channel card, a programme line with
progress in the watch bar, and a Guide page: a time grid with the current
moment marked, on-air programmes highlighted, and a detail card with
synopsis and a "Watch now" button.

## Installable PWA

`vite-plugin-pwa` generates the manifest and a Workbox service worker that
precaches the app shell only. Nothing under `/api`, `/mcp` or `/auth` is
ever cached: playlists and segments are live and auth responses are
private. The top bar offers "Install app" when the browser fires
`beforeinstallprompt`; iOS uses the Apple touch icon and "Add to Home
Screen".

## Authentication

`internal/auth` is inert without `AUTH_ISSUER`. With it, the backend is
the OpenID Connect client: `/auth/login` starts the authorization code
flow with PKCE and a nonce (state kept in a signed, short-lived cookie),
`/auth/callback` exchanges the code, verifies the ID token against the
provider's keys and sets an HMAC-signed session cookie (30 days,
HttpOnly, Secure behind HTTPS). Discovery is lazy with a retry cooldown so
the server starts even when the provider is down.

`Protect` wraps the whole mux: the SPA shell, `/auth/*`, `/api/health` and
`/api/me` are open; `/mcp` takes the `MCP_TOKEN` bearer or a session;
everything else under `/api` needs a session, except playlists and segments
carrying a stream token. Stream tokens are HMAC-signed, bound to one
channel and short-lived; the playlist handler rewrites segment lines to
carry the token because players resolve them relative to the playlist
without its query string.

TV sign-in is a device flow hosted by this server, not the provider's:
`/auth/device/start` issues a short code and returns it with the `/tv`
address and a QR of `/tv?code=…`; the phone opens `/tv`, which requires the
normal session (so the provider appears only as the login step), and
approves or denies the TV on a page served here; `/auth/device/poll` hands
the TV its session cookie once approved. Codes last ten minutes and the
approval form is bound to the approving user with a signed token. `internal/auth/authtest` is a small fake provider used
by the tests and available as a command for manual runs.

For MCP clients the server is both the protected resource (RFC 9728) and
the OAuth 2.1 authorization server (`internal/auth/mcpoauth.go`): `/mcp`
answers 401 with a `resource_metadata` challenge naming itself; clients
register dynamically (RFC 7591); `/mcp/oauth/authorize` requires PKCE S256
and an exactly registered redirect URI, reuses the OpenID sign-in for the
user, and shows a consent page bound to the user with a nonce; the token
endpoint issues opaque `cct_` access tokens (1 h) and `ccr_` refresh
tokens (30 d) with rotation, stored hashed in `DATA_DIR/mcp_oauth.json`.
Membership of `MCP_GROUP` is required to consent and to manage
connectors; the Status page lists connectors and sessions with last
activity and revokes them.

## Administrators and the audit log

`internal/audit` records events (`Event{Kind, User, IP, Country, Detail}`)
to `DATA_DIR/audit.jsonl` and keeps the newest in memory for
`/api/audit`. `internal/geoip` wraps a MaxMind database found in the
data directory and adds the country to every event and to the viewer
list for administrators. The administrator role is a group (`ADMIN_GROUP`,
defaulting to `MCP_GROUP`) checked by `auth.Service.IsAdmin`; the API
uses it to gate the audit log, viewer addresses, server logs, the
connectivity probe, stream control and connector management.

## Live rewind

The HLS playlist lists the whole `BUFFER_MINUTES` window, so the player's
native seek bar covers it and hls.js is configured not to snap back to the
live edge when the viewer pauses or scrubs back. The watch page shows how
far behind live the viewer is and offers a "Go live" button. Each viewer's
position is their own; the shared session only ever moves forward.

## Planned pieces

### Quality ladder (deferred from phase 2)

- Optional second rendition (720p) for phones on cellular. Each rendition is
  another encode per tuned channel, so it waits until remote access exists.

### DVR (phase 4, done)

`internal/dvr` is the recorder. State is a JSON file (`DATA_DIR/dvr.json`)
holding the passes and every recording's metadata; the files live under
`RECORDINGS_DIR`. Three loops run from `Run`:

- **plan** (every minute and on every rule change): extends the guide to
  the lookahead, matches passes against every airing, schedules one
  recording per episode (keyed by series id and episode number, or title
  and episode title, so a repeat on another channel is not recorded
  twice), drops scheduled items a pass no longer wants, and marks
  conflicts by a greedy sweep in priority order against the tuner count.
- **tick** (every five seconds): starts due recordings. A job resolves a
  tuner through the same `ResolveSource` as live viewers (recordings in
  progress and live sessions count as in use), then runs ffmpeg
  `-c copy` to append the transport stream to the file until the end
  time, restarting it if the stream drops. A busy tuner puts the job back
  with a 30 s retry.
- **post** (one at a time): comskip writes an EDL; ffmpeg transcodes to
  MP4 with the live encoder settings, either cutting the commercials
  (`trim`/`concat` filter graph) or leaving them for the players to skip
  from the stored spans. Keep counts are enforced per pass afterwards.

The video endpoint serves the MP4 with range requests and accepts a
stream token bound to `rec:<id>`, so the TV app's player needs no cookie.

### Native clients (phases 5 and 6)

- Android TV / Fire TV app (Kotlin, Media3 ExoPlayer) because TV browsers
  are unusable.
- iOS app (Swift, AVPlayer, which plays HLS natively) distributed outside
  the App Store via Ad Hoc provisioning: the server hosts the `.ipa` and an
  `itms-services` manifest so phones install it from a web page. Builds must
  be re-signed yearly; the free alternative is AltStore/SideStore with a
  7-day re-sign cycle.
- An HDHomeRun-compatible API on the server so Plex, Jellyfin and other
  existing clients can use it as a tuner and get sharing for free.
