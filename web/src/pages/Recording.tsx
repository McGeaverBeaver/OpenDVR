import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, type Recording as Rec } from '../api'

function fmtWhen(unix: number): string {
  return new Date(unix * 1000).toLocaleString([], { weekday: 'short', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

/**
 * Plays a finished recording. The MP4 is served with range support, so
 * the browser's own player seeks. Detected commercial breaks are jumped
 * over automatically (toggle in the bar); playback resumes where it
 * stopped last time, and the position is saved as it goes.
 */
export default function Recording() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const videoRef = useRef<HTMLVideoElement>(null)
  const [rec, setRec] = useState<Rec | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [autoSkip, setAutoSkip] = useState(true)
  const [toast, setToast] = useState<string | null>(null)

  useEffect(() => {
    api
      .recording(id)
      .then(setRec)
      .catch((e: Error) => setError(e.message))
  }, [id])

  // Resume, skip commercials, save position.
  useEffect(() => {
    const video = videoRef.current
    if (!video || !rec) return
    const skips = rec.skips ?? []
    let resumed = false
    let lastSave = 0
    let lastSkip = -1

    const onMeta = () => {
      if (!resumed && rec.position && rec.position > 5 && rec.position < video.duration - 30) {
        video.currentTime = rec.position
      }
      resumed = true
    }
    const onTime = () => {
      const t = video.currentTime
      if (autoSkip) {
        for (let i = 0; i < skips.length; i++) {
          const s = skips[i]
          if (t >= s.start && t < s.end - 0.5 && i !== lastSkip) {
            lastSkip = i
            video.currentTime = s.end
            setToast(`Skipped a ${Math.round((s.end - s.start) / 60)} min break`)
            window.setTimeout(() => setToast(null), 2500)
            return
          }
          if (t >= s.end && i === lastSkip) lastSkip = -1
        }
      }
      if (Date.now() - lastSave > 10000) {
        lastSave = Date.now()
        api.setPosition(rec.id, t)
      }
    }
    const onPause = () => api.setPosition(rec.id, video.currentTime)
    const onEnded = () => {
      api.setWatched(rec.id, true).catch(() => {})
    }
    video.addEventListener('loadedmetadata', onMeta)
    video.addEventListener('timeupdate', onTime)
    video.addEventListener('pause', onPause)
    video.addEventListener('ended', onEnded)
    return () => {
      api.setPosition(rec.id, video.currentTime)
      video.removeEventListener('loadedmetadata', onMeta)
      video.removeEventListener('timeupdate', onTime)
      video.removeEventListener('pause', onPause)
      video.removeEventListener('ended', onEnded)
    }
  }, [rec, autoSkip])

  const remove = async () => {
    if (!rec || !window.confirm(`Delete ${rec.title}? The file is removed from disk.`)) return
    await api.deleteRecording(rec.id).catch(() => {})
    navigate('/dvr')
  }

  return (
    <section className="watch">
      <div className="player">
        {rec?.playUrl && (
          <video ref={videoRef} src={rec.playUrl} controls autoPlay playsInline crossOrigin="use-credentials">
            {rec.captionsUrl && <track kind="captions" label="Closed captions" srcLang="en" src={rec.captionsUrl} />}
          </video>
        )}
        {error && <div className="overlay error">{error}</div>}
        {rec && !rec.playUrl && <div className="overlay">This recording is not ready to play ({rec.status}).</div>}
        {toast && <div className="skip-toast">{toast}</div>}
      </div>
      <div className="watch-bar">
        <Link to="/dvr" className="btn">
          ← DVR
        </Link>
        <div className="watch-title">
          <strong>{rec?.title ?? '…'}</strong>
          {rec?.episodeTitle ? ` · ${rec.episodeTitle}` : ''}
          {rec?.episodeNumber ? ` · ${rec.episodeNumber}` : ''}
          {rec && (
            <div className="watch-program">
              {fmtWhen(rec.airing)} · {rec.channel} {rec.channelName}
              {rec.cut ? ' · commercials removed' : rec.skips?.length ? ` · ${rec.skips.length} breaks marked` : ''}
            </div>
          )}
        </div>
        {rec?.skips && rec.skips.length > 0 && (
          <label className="muted" style={{ display: 'flex', gap: 6, alignItems: 'center', whiteSpace: 'nowrap' }}>
            <input type="checkbox" checked={autoSkip} onChange={(e) => setAutoSkip(e.target.checked)} /> Skip commercials
          </label>
        )}
        <div className="watch-nav">
          <button onClick={remove}>Delete</button>
        </div>
      </div>
    </section>
  )
}
