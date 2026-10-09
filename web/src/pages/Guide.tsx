import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, type Channel, type GuideChannel, type Program, type Recording, type StreamStatus } from '../api'
import RecordDialog from '../components/RecordDialog'

// Pixels per minute in the grid. 3 px/min puts six hours in ~1100 px.
const PX_PER_MIN = 3
const WINDOW_HOURS = 6
const STEP_HOURS = 3
// Categories the guide tags programmes with (the Filter field).
const CATEGORIES = ['Movies', 'Sports', 'News', 'Kids']

function fmtTime(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

function halfHourFloor(unix: number): number {
  return unix - (unix % 1800)
}

/**
 * The programme grid: one row per channel in the lineup, time across the
 * top, the current moment marked. The server fetches further ahead on
 * demand when the window is pushed past what it has cached.
 */
export default function Guide() {
  const navigate = useNavigate()
  const [lineup, setLineup] = useState<Channel[]>([])
  const [guide, setGuide] = useState<GuideChannel[]>([])
  const [from, setFrom] = useState(() => halfHourFloor(Math.floor(Date.now() / 1000) - 1800))
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [live, setLive] = useState<Record<string, StreamStatus>>({})
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  const [detail, setDetail] = useState<{ channel: GuideChannel; program: Program } | null>(null)
  const [record, setRecord] = useState<{ channel: GuideChannel; program: Program } | null>(null)
  const [comskip, setComskip] = useState(false)
  const [canRecord, setCanRecord] = useState(true)
  const [planned, setPlanned] = useState<Record<string, Recording['status']>>({})
  const [notice, setNotice] = useState<string | null>(null)
  const [category, setCategory] = useState('')
  const inCategory = (p: Program) => !category || (p.filter ?? []).some((f) => f.toLowerCase() === category.toLowerCase())
  const scroller = useRef<HTMLDivElement>(null)
  const to = from + WINDOW_HOURS * 3600

  // Which airings the DVR has planned, to mark them in the grid.
  const loadPlanned = () =>
    api
      .recordings('upcoming')
      .then((rs) => setPlanned(Object.fromEntries(rs.map((r) => [`${r.channel}@${r.airing}`, r.status]))))
      .catch(() => {})
  useEffect(() => {
    api.dvr().then((d) => setComskip(d.comskip)).catch(() => {})
    api.me().then((m) => setCanRecord(!m.restricted)).catch(() => {})
    loadPlanned()
    const t = window.setInterval(loadPlanned, 30000)
    return () => window.clearInterval(t)
  }, [])
  useEffect(() => {
    if (!notice) return
    const t = window.setTimeout(() => setNotice(null), 6000)
    return () => window.clearTimeout(t)
  }, [notice])

  useEffect(() => {
    api.channels().then(setLineup).catch(() => {})
    const poll = () =>
      api
        .streams()
        .then((s) => setLive(Object.fromEntries(s.filter((x) => x.running).map((x) => [x.channel, x]))))
        .catch(() => {})
    poll()
    const t = window.setInterval(poll, 10000)
    const clock = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 30000)
    return () => {
      window.clearInterval(t)
      window.clearInterval(clock)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    api
      .guide(from, to)
      .then((g) => {
        if (cancelled) return
        setGuide(g.channels)
        setError(null)
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false))
    return () => {
      cancelled = true
    }
  }, [from, to])

  // Rows follow the tuner lineup so every tunable channel appears, with
  // listings joined by guide number. Channels only the guide knows about
  // (not in the lineup) are left out: nothing could play them.
  const rows = useMemo(() => {
    const byNumber = new Map(guide.map((g) => [g.guideNumber, g]))
    const all = lineup.filter((c) => !c.drm).map((c) => ({ channel: c, listings: byNumber.get(c.guideNumber) }))
    if (!category) return all
    // With a category chosen, only channels showing something in it.
    const cat = category.toLowerCase()
    return all.filter((r) => (r.listings?.programs ?? []).some((p) => (p.filter ?? []).some((f) => f.toLowerCase() === cat)))
  }, [lineup, guide, category])

  const ticks = useMemo(() => {
    const out: number[] = []
    for (let t = from; t < to; t += 1800) out.push(t)
    return out
  }, [from, to])

  const goNow = () => {
    const f = halfHourFloor(Math.floor(Date.now() / 1000) - 1800)
    setFrom(f)
    scroller.current?.scrollTo({ left: 0, behavior: 'smooth' })
  }

  const width = WINDOW_HOURS * 60 * PX_PER_MIN
  const nowX = (now - from) / 60 * PX_PER_MIN

  return (
    <section className="guide">
      <div className="toolbar">
        <h1>Guide</h1>
        <div className="guide-nav">
          <button onClick={() => setFrom(from - STEP_HOURS * 3600)}>← Earlier</button>
          <button onClick={goNow}>Now</button>
          <button onClick={() => setFrom(from + STEP_HOURS * 3600)}>Later →</button>
        </div>
        <div className="tabs" style={{ marginBottom: 0 }}>
          <button className={category === '' ? 'on' : ''} onClick={() => setCategory('')}>
            All
          </button>
          {CATEGORIES.map((c) => (
            <button key={c} className={category === c ? 'on' : ''} onClick={() => setCategory(c)}>
              {c}
            </button>
          ))}
        </div>
        <span className="muted">
          {new Date(from * 1000).toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' })}
          {loading && ' · loading…'}
        </span>
      </div>
      {error && <p className="error">{error}</p>}
      {notice && <p style={{ color: 'var(--accent)' }}>{notice}</p>}
      {rows.length === 0 && !error && (
        <p className="muted">{category ? `Nothing tagged ${category} in this window.` : 'Loading lineup…'}</p>
      )}

      <div className="guide-grid" ref={scroller}>
        <div className="guide-inner" style={{ width: width + 160 }}>
          <div className="guide-header">
            <div className="guide-corner" />
            <div className="guide-times" style={{ width }}>
              {ticks.map((t) => (
                <div key={t} className="guide-tick" style={{ left: (t - from) / 60 * PX_PER_MIN }}>
                  {fmtTime(t)}
                </div>
              ))}
            </div>
          </div>
          {rows.map(({ channel, listings }) => (
            <div key={channel.guideNumber} className="guide-row">
              <div className="guide-channel" onClick={() => navigate(`/watch/${encodeURIComponent(channel.guideNumber)}`)}>
                <div className="card-number">
                  {channel.guideNumber}
                  {live[channel.guideNumber] && <span className="tag live">LIVE</span>}
                </div>
                <div className="muted">{channel.guideName}</div>
              </div>
              <div className="guide-programs" style={{ width }}>
                {nowX >= 0 && nowX <= width && <div className="guide-now" style={{ left: nowX }} />}
                {(listings?.programs ?? []).map((p) => {
                  const start = Math.max(p.start, from)
                  const end = Math.min(p.end, to)
                  const left = (start - from) / 60 * PX_PER_MIN
                  const w = Math.max(((end - start) / 60) * PX_PER_MIN - 2, 8)
                  const onAir = p.start <= now && now < p.end
                  return (
                    <div
                      key={p.start}
                      className={onAir ? 'guide-program on-air' : 'guide-program'}
                      style={{ left, width: w, opacity: inCategory(p) ? 1 : 0.3 }}
                      title={`${p.title}${p.episodeTitle ? ' · ' + p.episodeTitle : ''}\n${fmtTime(p.start)} – ${fmtTime(p.end)}`}
                      onClick={() => setDetail(listings ? { channel: listings, program: p } : null)}
                    >
                      <div className="guide-title">
                        {planned[`${channel.guideNumber}@${p.start}`] && (
                          <span className="tag rec" title={planned[`${channel.guideNumber}@${p.start}`]}>
                            REC
                          </span>
                        )}{' '}
                        {p.new && <span className="tag">NEW</span>} {p.title}
                      </div>
                      {w > 90 && (
                        <div className="muted guide-sub">
                          {fmtTime(p.start)}
                          {p.episodeTitle ? ` · ${p.episodeTitle}` : ''}
                        </div>
                      )}
                    </div>
                  )
                })}
                {!listings && <div className="muted guide-empty">No listings</div>}
              </div>
            </div>
          ))}
        </div>
      </div>

      {detail && (
        <div className="modal-backdrop" onClick={() => setDetail(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            {detail.program.imageUrl && <img src={detail.program.imageUrl} alt="" className="modal-art" />}
            <h2>{detail.program.title}</h2>
            <p className="muted">
              {detail.channel.guideNumber} {detail.channel.guideName} · {fmtTime(detail.program.start)} –{' '}
              {fmtTime(detail.program.end)}
              {detail.program.episodeNumber ? ` · ${detail.program.episodeNumber}` : ''}
              {detail.program.new ? ' · New' : ''}
            </p>
            {detail.program.episodeTitle && <p><strong>{detail.program.episodeTitle}</strong></p>}
            {detail.program.synopsis && <p>{detail.program.synopsis}</p>}
            <div className="modal-actions">
              {canRecord && (
                <button className="btn-rec" onClick={() => { setRecord(detail); setDetail(null) }}>
                  ● Record…
                </button>
              )}
              {detail.program.start <= now && now < detail.program.end ? (
                <button onClick={() => navigate(`/watch/${encodeURIComponent(detail.channel.guideNumber)}`)}>
                  Watch now
                </button>
              ) : (
                <span className="muted">Starts {fmtTime(detail.program.start)}</span>
              )}
              <button onClick={() => setDetail(null)}>Close</button>
            </div>
          </div>
        </div>
      )}
      {record && (
        <RecordDialog
          channel={record.channel.guideNumber}
          channelName={record.channel.guideName}
          program={record.program}
          comskip={comskip}
          onClose={() => setRecord(null)}
          onDone={(m) => {
            setRecord(null)
            setNotice(m)
            loadPlanned()
          }}
        />
      )}
    </section>
  )
}
