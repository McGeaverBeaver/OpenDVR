import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, viewer, type Channel, type NowNext } from '../api'

function fmtTime(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}
import Player from '../components/Player'

export default function Watch() {
  const { number = '' } = useParams()
  const navigate = useNavigate()
  const [channels, setChannels] = useState<Channel[]>([])
  const name = viewer.name()
  const [onNow, setOnNow] = useState<NowNext | undefined>()
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  const [recMsg, setRecMsg] = useState<string | null>(null)

  // Record the rest of what is on now.
  const recordNow = async () => {
    try {
      const r = await api.recordNow(number)
      setRecMsg(`● Recording ${r.title} until ${fmtTime(r.end)}`)
    } catch (e) {
      setRecMsg((e as Error).message)
    }
    window.setTimeout(() => setRecMsg(null), 8000)
  }

  // What is playing on this channel, with a progress bar in the watch bar.
  useEffect(() => {
    const poll = () =>
      api
        .guideNow()
        .then((g) => setOnNow(g[number]))
        .catch(() => {})
    poll()
    const t = window.setInterval(poll, 60000)
    const clock = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 15000)
    return () => {
      window.clearInterval(t)
      window.clearInterval(clock)
    }
  }, [number])


  useEffect(() => {
    api.channels().then(setChannels).catch(() => {})
  }, [])

  const { current, prev, next } = useMemo(() => {
    const idx = channels.findIndex((c) => c.guideNumber === number)
    const playable = (i: number) => channels[i] && !channels[i].drm
    let p = idx - 1
    while (p >= 0 && !playable(p)) p--
    let n = idx + 1
    while (n < channels.length && !playable(n)) n++
    return {
      current: idx >= 0 ? channels[idx] : undefined,
      prev: p >= 0 ? channels[p] : undefined,
      next: n < channels.length ? channels[n] : undefined,
    }
  }, [channels, number])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement) return
      if (e.key === 'ArrowUp' && next) navigate(`/watch/${encodeURIComponent(next.guideNumber)}`)
      if (e.key === 'ArrowDown' && prev) navigate(`/watch/${encodeURIComponent(prev.guideNumber)}`)
      if (e.key === 'Escape') navigate('/guide')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [prev, next, navigate])

  return (
    <section className="watch">
      <Player channel={number} name={name} />
      <div className="watch-bar">
        <Link to="/guide" className="btn">
          ← Guide
        </Link>
        <div className="watch-title">
          <strong>{number}</strong> {current?.guideName ?? ''}
          {onNow?.now && (
            <>
              <div className="watch-program">
                {onNow.now.title}
                {onNow.now.episodeTitle ? ` · ${onNow.now.episodeTitle}` : ''} · {fmtTime(onNow.now.start)} –{' '}
                {fmtTime(onNow.now.end)}
                {onNow.next ? ` · Next: ${onNow.next.title}` : ''}
              </div>
              <div className="watch-progress">
                <div
                  style={{
                    width: `${Math.min(100, Math.max(0, ((now - onNow.now.start) / (onNow.now.end - onNow.now.start)) * 100))}%`,
                  }}
                />
              </div>
            </>
          )}
        </div>
        {recMsg && <span className="muted" style={{ whiteSpace: 'nowrap' }}>{recMsg}</span>}
        <button className="btn-rec" onClick={recordNow} title="Record the rest of this programme">
          ● Rec
        </button>
        <div className="watch-nav">
          <button disabled={!prev} onClick={() => prev && navigate(`/watch/${encodeURIComponent(prev.guideNumber)}`)}>
            ↓ {prev?.guideNumber ?? ''}
          </button>
          <button disabled={!next} onClick={() => next && navigate(`/watch/${encodeURIComponent(next.guideNumber)}`)}>
            ↑ {next?.guideNumber ?? ''}
          </button>
        </div>
      </div>
    </section>
  )
}
