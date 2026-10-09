import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Channel, type NowNext, type StreamStatus } from '../api'

function fmtTime(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

export default function Channels() {
  const [channels, setChannels] = useState<Channel[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [favoritesOnly, setFavoritesOnly] = useState(() => {
    try {
      return localStorage.getItem('opendvr.favoritesOnly') === '1'
    } catch {
      return false
    }
  })
  const toggleFavorites = () => {
    setFavoritesOnly((v) => {
      try {
        localStorage.setItem('opendvr.favoritesOnly', v ? '0' : '1')
      } catch {
        /* private mode */
      }
      return !v
    })
  }
  const [refreshing, setRefreshing] = useState(false)
  const [live, setLive] = useState<Record<string, StreamStatus>>({})
  const [onNow, setOnNow] = useState<Record<string, NowNext>>({})
  const [stars, setStars] = useState<Set<string>>(new Set())
  const [signedIn, setSignedIn] = useState(false)

  // Favourites: per account on the server when signed in, else the
  // HDHomeRun app's stars only.
  useEffect(() => {
    api
      .me()
      .then((m) => {
        setSignedIn(!!(m.enabled && m.user))
        setStars(new Set(m.favorites ?? []))
      })
      .catch(() => {})
  }, [])
  const toggleStar = (e: React.MouseEvent, number: string) => {
    e.preventDefault()
    e.stopPropagation()
    if (!signedIn) return
    const next = new Set(stars)
    if (next.has(number)) next.delete(number)
    else next.add(number)
    setStars(next)
    api.setFavorites([...next]).catch(() => {})
  }
  const isFav = (c: Channel) => c.favorite || stars.has(c.guideNumber)

  // What is on each channel, refreshed every minute.
  useEffect(() => {
    const poll = () => api.guideNow().then(setOnNow).catch(() => {})
    poll()
    const t = window.setInterval(poll, 60000)
    return () => window.clearInterval(t)
  }, [])

  const load = () =>
    api
      .channels()
      .then((c) => {
        setChannels(c)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))

  useEffect(() => {
    load()
  }, [])

  // Channels already streaming can be joined without a tuner; show them.
  useEffect(() => {
    const poll = () =>
      api
        .streams()
        .then((s) => setLive(Object.fromEntries(s.filter((x) => x.running).map((x) => [x.channel, x]))))
        .catch(() => {})
    poll()
    const t = window.setInterval(poll, 5000)
    return () => window.clearInterval(t)
  }, [])

  const refresh = async () => {
    setRefreshing(true)
    try {
      await api.refreshDevices()
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRefreshing(false)
    }
  }

  const hasFavorites = useMemo(() => (channels ?? []).some((c) => isFav(c)) || signedIn, [channels, stars, signedIn])
  const visible = useMemo(() => {
    if (!channels) return []
    const q = query.trim().toLowerCase()
    let out = channels
    if (favoritesOnly) out = out.filter((c) => isFav(c))
    if (q) out = out.filter((c) => c.guideNumber.includes(q) || c.guideName.toLowerCase().includes(q))
    // Favourites float to the top.
    return [...out].sort((a, b) => Number(isFav(b)) - Number(isFav(a)))
  }, [channels, query, favoritesOnly, stars])

  return (
    <section>
      <div className="toolbar">
        <h1>Channels</h1>
        <input
          type="search"
          placeholder="Search number or name"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {hasFavorites && (
          <button className={favoritesOnly ? 'on' : ''} onClick={toggleFavorites} title={signedIn ? 'Your starred channels' : 'Channels starred in the HDHomeRun app'}>
            ★ Favourites
          </button>
        )}
        <button onClick={refresh} disabled={refreshing}>
          {refreshing ? 'Scanning…' : 'Rescan tuners'}
        </button>
      </div>

      {error && <p className="error">{error}</p>}
      {channels === null && !error && <p className="muted">Loading lineup…</p>}
      {channels !== null && channels.length === 0 && (
        <p className="muted">
          No channels yet. Check that HDHR_HOSTS points at your HDHomeRun, then rescan.
        </p>
      )}

      <div className="grid">
        {visible.map((c) => (
          <Link
            key={c.guideNumber}
            to={`/watch/${encodeURIComponent(c.guideNumber)}`}
            className={c.drm ? 'card disabled' : 'card'}
            aria-disabled={c.drm}
          >
            <div className="card-number">
              {c.guideNumber}
              <span
                className={isFav(c) ? 'star on' : 'star'}
                onClick={(e) => toggleStar(e, c.guideNumber)}
                title={signedIn ? (isFav(c) ? 'Remove from favourites' : 'Add to favourites') : ''}
              >
                ★
              </span>
            </div>
            <div className="card-name">{c.guideName}</div>
            {onNow[c.guideNumber]?.now && (
              <div className="card-now" title={onNow[c.guideNumber].now!.title}>
                <strong>{onNow[c.guideNumber].now!.title}</strong> · until {fmtTime(onNow[c.guideNumber].now!.end)}
              </div>
            )}
            <div className="card-tags">
              {live[c.guideNumber] && (
                <span className="tag live" title="Already streaming; joining needs no tuner">
                  LIVE{live[c.guideNumber].viewers.length > 0 && ` · ${live[c.guideNumber].viewers.length}`}
                </span>
              )}
              {c.hd && <span className="tag">HD</span>}
              {c.drm && <span className="tag warn">DRM</span>}
              {c.sources.length > 1 && <span className="tag">{c.sources.length} tuners</span>}
            </div>
          </Link>
        ))}
      </div>
    </section>
  )
}
