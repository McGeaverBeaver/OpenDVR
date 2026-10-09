# Roadmap

| Phase | Scope | Status |
|---|---|---|
| 1 | Tuner discovery, channel lineup, live playback in the browser via a shared transcoded HLS session per channel, Unraid container with Quick Sync | **done** |
| 2 | Per-viewer sessions, tuner selection from live status, "all tuners busy, join 5.1 or 7.2" UX, live rewind | **done** (quality ladder deferred) |
| 3 | Guide data, PWA (service worker, install), Authentik OIDC login, QR code device login for TVs, signed stream URLs for AirPlay/Chromecast | **done** |
| 4 | DVR: series passes, time slots, one-off and record-now, conflict detection, commercial detection and removal (comskip), recordings library with resume, watched tracking, storage rules, closed captions, notifications, playback in the browser and the TV app, MCP tools | **done** |
| 5 | Android TV / Fire TV app (guide, DVR, remote-first UI); HDHomeRun-compatible API for Plex/Jellyfin; admin role with audit log and GeoIP; per-account profiles and kids mode; backup and restore | **done** |
| 6 | iOS app (Swift, AVPlayer) with Ad Hoc over-the-air install hosted by the server | deferred: iPhones use the PWA (native HLS, AirPlay); needs a paid Apple Developer account and macOS builds |

## Phase 1 acceptance

- `GET /api/channels` lists the HDHomeRun lineup over the VPN.
- Opening a channel in the browser plays within a few seconds.
- Two browsers on the same channel show one tuner in use on the Status page.
- Pausing in one browser does not affect the other.
- Hardware encoder in use shows as `qsv` on the Status page.

## Phase 4 acceptance

- A pass created from the guide schedules every matching airing within
  the lookahead, once per episode, and the Upcoming tab shows conflicts
  when more overlap than there are tuners.
- A recording captures the broadcast unchanged, survives a dropped
  stream, and becomes an MP4 with commercials marked or removed within a
  few minutes of its end time.
- Playback in the browser and the TV app resumes where it stopped and
  jumps over marked commercials.
- An assistant can do all of it through the MCP tools.
