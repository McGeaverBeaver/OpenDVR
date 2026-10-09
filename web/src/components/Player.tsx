import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Hls from 'hls.js'
import { api, TunersBusyError, type TunerUse } from '../api'

interface Props {
  channel: string
  /** Display name to register with; a change re-joins so it shows up. */
  name: string
}

type State = 'tuning' | 'playing' | 'error' | 'busy'

const HEARTBEAT_MS = 5000
// Within this many seconds of the live edge the player is "live".
const LIVE_SLACK = 4

// Browsers refuse autoplay with sound until the page has had a user
// gesture, which a typed URL, bookmark or reload never provides. Rather
// than leave the player paused on a grey frame, fall back to muted
// playback; the viewer can unmute from the controls.
function play(video: HTMLVideoElement) {
  video.play().catch(() => {
    video.muted = true
    video.play().catch(() => {})
  })
}

function liveEdge(video: HTMLVideoElement): number {
  const s = video.seekable
  return s.length ? s.end(s.length - 1) : NaN
}

function fmtBehind(sec: number): string {
  const s = Math.max(0, Math.round(sec))
  const m = Math.floor(s / 60)
  return m > 0 ? `${m}:${String(s % 60).padStart(2, '0')}` : `${s}s`
}

/**
 * Plays a channel's HLS stream as a registered viewer.
 *
 * Joining goes through POST /api/watch, which blocks until ffmpeg has
 * produced the first segment and answers 409 with the locked tuners when
 * none is free, so the viewer gets "join one of these" instead of a player
 * timeout. A heartbeat every few seconds keeps the viewer handle (and so
 * the session) alive, paused or not, which is what makes rewind work: the
 * ring buffer keeps growing while you are paused.
 *
 * hls.js does playback wherever Media Source Extensions exist. Chromium
 * answers "maybe" to canPlayType for HLS but cannot demux a playlist
 * natively, so native playback is reserved for browsers without MSE
 * (iOS Safari).
 */
export default function Player({ channel, name }: Props) {
  const videoRef = useRef<HTMLVideoElement>(null)
  const navigate = useNavigate()
  const [state, setState] = useState<State>('tuning')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState<TunerUse[]>([])
  const [behind, setBehind] = useState(0)
  const [attempt, setAttempt] = useState(0)
  const [captions, setCaptions] = useState(false)
  const [hasCaptions, setHasCaptions] = useState(false)

  // Captions arrive as text tracks once the stream carries CEA-608 data.
  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    const apply = () => {
      const tracks = Array.from(video.textTracks)
      setHasCaptions(tracks.length > 0)
      tracks.forEach((t, i) => {
        t.mode = captions && i === 0 ? 'showing' : 'hidden'
      })
    }
    apply()
    video.textTracks.addEventListener('addtrack', apply)
    return () => video.textTracks.removeEventListener('addtrack', apply)
  }, [captions, state])

  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    let hls: Hls | null = null
    let cancelled = false
    let viewerId = ''
    const ac = new AbortController()

    setState('tuning')
    setMessage('')
    setBusy([])

    const heartbeat = async () => {
      if (!viewerId) return
      const ok = await api.heartbeat(viewerId, video.paused).catch(() => true)
      if (!ok && !cancelled) {
        // The server forgot us (restart, expiry); re-register quietly.
        api.watch(channel).then((r) => (viewerId = r.viewerId)).catch(() => {})
      }
    }
    const timer = window.setInterval(heartbeat, HEARTBEAT_MS)

    ;(async () => {
      let src: string
      try {
        const r = await api.watch(channel, ac.signal)
        viewerId = r.viewerId
        src = r.playlist
      } catch (e) {
        if (cancelled) return
        if (e instanceof TunersBusyError) {
          setBusy(e.tuners)
          setState('busy')
        } else {
          setState('error')
          setMessage((e as Error).message || 'Unable to tune channel')
        }
        return
      }
      if (cancelled) return

      // iPhones and iPads get Safari's native HLS even though hls.js reports
      // itself supported there (Managed Media Source on iOS 17+): native
      // playback is what gives AirPlay, picture-in-picture and lock-screen
      // controls. Everywhere else hls.js is the reliable path.
      const apple = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
      if (!Hls.isSupported() || apple) {
        if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = src
          play(video)
          return
        }
        setState('error')
        setMessage('This browser cannot play HLS video.')
        return
      }
      hls = new Hls({
        // Start on the newest segment as soon as it exists rather than
        // holding back several segments from the live edge.
        liveSyncDurationCount: 1,
        // Never snap back to live on our own: pausing or scrubbing back
        // into the ring buffer is the point. "Go live" is a button.
        liveMaxLatencyDurationCount: Infinity,
        maxBufferLength: 30,
        backBufferLength: 120,
        manifestLoadingTimeOut: 40000,
        manifestLoadingMaxRetry: 4,
        levelLoadingMaxRetry: 8,
        fragLoadingMaxRetry: 8,
        fragLoadingRetryDelay: 500,
      })
      hls.loadSource(src)
      hls.attachMedia(video)
      hls.on(Hls.Events.MANIFEST_PARSED, () => play(video))
      hls.on(Hls.Events.ERROR, (_evt, data) => {
        if (!data.fatal || !hls) return
        switch (data.type) {
          case Hls.ErrorTypes.NETWORK_ERROR:
            hls.startLoad()
            break
          case Hls.ErrorTypes.MEDIA_ERROR:
            hls.recoverMediaError()
            break
          default:
            setState('error')
            setMessage(`Playback failed: ${data.details}`)
            hls.destroy()
        }
      })
    })()

    const onPlaying = () => setState('playing')
    const onTime = () => {
      const edge = liveEdge(video)
      setBehind(Number.isFinite(edge) ? edge - video.currentTime : 0)
    }
    video.addEventListener('playing', onPlaying)
    video.addEventListener('timeupdate', onTime)
    video.addEventListener('pause', onTime)
    const tick = window.setInterval(onTime, 1000)
    const onUnload = () => viewerId && api.leave(viewerId)
    window.addEventListener('pagehide', onUnload)

    return () => {
      cancelled = true
      ac.abort()
      window.clearInterval(timer)
      window.clearInterval(tick)
      video.removeEventListener('playing', onPlaying)
      video.removeEventListener('timeupdate', onTime)
      video.removeEventListener('pause', onTime)
      window.removeEventListener('pagehide', onUnload)
      hls?.destroy()
      video.removeAttribute('src')
      video.load()
      if (viewerId) api.leave(viewerId)
    }
  }, [channel, name, attempt])

  const goLive = () => {
    const video = videoRef.current
    if (!video) return
    const edge = liveEdge(video)
    if (Number.isFinite(edge)) video.currentTime = Math.max(0, edge - 1)
    play(video)
  }

  const shared = busy.filter((t) => t.shared)
  const external = busy.filter((t) => !t.shared)
  const isLive = state === 'playing' && behind <= LIVE_SLACK && !videoRef.current?.paused

  return (
    <div className="player">
      <video ref={videoRef} controls playsInline autoPlay x-webkit-airplay="allow" />
      {state === 'playing' && hasCaptions && (
        <button className={captions ? 'cc-toggle on' : 'cc-toggle'} onClick={() => setCaptions((c) => !c)} title="Closed captions">
          CC
        </button>
      )}
      {state === 'playing' && (
        <div className={isLive ? 'live-chip live' : 'live-chip'}>
          {isLive ? (
            <span>LIVE</span>
          ) : (
            <>
              <span>{fmtBehind(behind)} behind</span>
              <button onClick={goLive}>Go live</button>
            </>
          )}
        </div>
      )}
      {state === 'tuning' && (
        <div className="overlay">
          <div className="spinner" />
          <p>Tuning {channel}…</p>
        </div>
      )}
      {state === 'busy' && (
        <div className="overlay busy">
          <p>
            <strong>All tuners are busy.</strong>
          </p>
          {shared.length > 0 && (
            <>
              <p className="muted">These channels are already streaming; join one without a tuner:</p>
              <div className="busy-choices">
                {shared.map((t) => (
                  <button key={t.deviceId + t.tuner} onClick={() => navigate(`/watch/${encodeURIComponent(t.channel)}`)}>
                    Join {t.channel} {t.name ?? ''}
                  </button>
                ))}
              </div>
            </>
          )}
          {external.length > 0 && (
            <p className="muted">
              In use elsewhere:{' '}
              {external.map((t) => `${t.channel}${t.name ? ' ' + t.name : ''}${t.client ? ' (' + t.client + ')' : ''}`).join(', ')}
            </p>
          )}
          <button onClick={() => setAttempt((n) => n + 1)}>Try {channel} again</button>
        </div>
      )}
      {state === 'error' && (
        <div className="overlay error">
          <p>{message}</p>
          <p className="muted">ffmpeg output for this failure is on the Status page.</p>
          <button onClick={() => setAttempt((n) => n + 1)}>Try again</button>
        </div>
      )}
    </div>
  )
}
