import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Channel, type DVRSummary, type Recording, type Rule, type SearchHit } from '../api'
import RecordDialog from '../components/RecordDialog'

type Tab = 'library' | 'upcoming' | 'passes' | 'search'

function fmtWhen(unix: number): string {
  return new Date(unix * 1000).toLocaleString([], {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  })
}

function fmtTime(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString([], {
    hour: 'numeric',
    minute: '2-digit',
  })
}

function fmtBytes(n?: number): string {
  if (!n) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i >= 3 ? 1 : 0)} ${units[i]}`
}

function fmtDuration(sec?: number): string {
  if (!sec) return ''
  const m = Math.round(sec / 60)
  return m >= 60 ? `${Math.floor(m / 60)}h ${m % 60}m` : `${m}m`
}

const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

const STATUS_LABEL: Record<Recording['status'], string> = {
  scheduled: 'Scheduled',
  conflict: 'No tuner free',
  recording: '● Recording',
  processing: 'Processing',
  ready: 'Ready',
  failed: 'Failed',
}

/**
 * The DVR: what has been recorded, what is coming up, the passes that
 * decide it, and a guide search to set more up.
 */
export default function DVR() {
  const [tab, setTab] = useState<Tab>('library')
  const [summary, setSummary] = useState<DVRSummary | null>(null)
  const [recordings, setRecordings] = useState<Recording[]>([])
  const [rules, setRules] = useState<Rule[]>([])
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const load = useCallback(() => {
    api
      .dvr()
      .then(setSummary)
      .catch((e: Error) => setError(e.message))
    api
      .recordings()
      .then(setRecordings)
      .catch(() => {})
    api
      .rules()
      .then(setRules)
      .catch(() => {})
  }, [])

  useEffect(() => {
    load()
    const t = window.setInterval(load, 10000)
    return () => window.clearInterval(t)
  }, [load])

  useEffect(() => {
    if (!notice) return
    const t = window.setTimeout(() => setNotice(null), 6000)
    return () => window.clearTimeout(t)
  }, [notice])

  const counts = summary?.counts ?? {}
  const upcoming = useMemo(
    () =>
      recordings
        .filter((r) => ['scheduled', 'conflict', 'recording'].includes(r.status))
        .sort((a, b) => a.start - b.start),
    [recordings],
  )
  const library = useMemo(
    () => recordings.filter((r) => ['ready', 'processing', 'failed', 'recording'].includes(r.status)),
    [recordings],
  )

  return (
    <section>
      <div className="toolbar">
        <h1>DVR</h1>
        {summary && (
          <span className="muted">
            {fmtBytes(summary.diskFreeBytes)} free of {fmtBytes(summary.diskTotalBytes)}
            {summary.usedBytes ? ` · recordings ${fmtBytes(summary.usedBytes)}` : ''} ·{' '}
            {summary.comskip ? 'commercial detection on' : 'comskip not installed'} · {summary.tunerCount} tuners
          </span>
        )}
      </div>
      {error && <p className="error">{error}</p>}
      {summary?.planError && <p className="error">{summary.planError}</p>}
      {notice && <p style={{ color: 'var(--accent)' }}>{notice}</p>}

      <div className="tabs">
        <button className={tab === 'library' ? 'on' : ''} onClick={() => setTab('library')}>
          Recordings{counts.ready ? ` (${counts.ready})` : ''}
        </button>
        <button className={tab === 'upcoming' ? 'on' : ''} onClick={() => setTab('upcoming')}>
          Upcoming{upcoming.length ? ` (${upcoming.length})` : ''}
          {counts.conflict ? (
            <span className="tag rec" style={{ marginLeft: 6 }}>
              {counts.conflict} conflict
            </span>
          ) : null}
        </button>
        <button className={tab === 'passes' ? 'on' : ''} onClick={() => setTab('passes')}>
          Passes{rules.length ? ` (${rules.length})` : ''}
        </button>
        <button className={tab === 'search' ? 'on' : ''} onClick={() => setTab('search')}>
          Find something to record
        </button>
      </div>

      {tab === 'library' && <Library items={library} onChanged={load} />}
      {tab === 'upcoming' && <Upcoming items={upcoming} rules={rules} onChanged={load} />}
      {tab === 'passes' && <Passes rules={rules} recordings={recordings} onChanged={load} />}
      {tab === 'search' && (
        <Search
          comskip={summary?.comskip ?? false}
          onDone={(m) => {
            setNotice(m)
            load()
          }}
        />
      )}
    </section>
  )
}

function Library({ items, onChanged }: { items: Recording[]; onChanged: () => void }) {
  const [confirm, setConfirm] = useState<Recording | null>(null)
  const groups = useMemo(() => {
    const byTitle = new Map<string, Recording[]>()
    for (const r of [...items].sort((a, b) => b.airing - a.airing)) {
      const list = byTitle.get(r.title) ?? []
      list.push(r)
      byTitle.set(r.title, list)
    }
    return [...byTitle.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [items])

  const remove = async () => {
    if (!confirm) return
    await api.deleteRecording(confirm.id).catch(() => {})
    setConfirm(null)
    onChanged()
  }
  const [redo, setRedo] = useState<Recording | null>(null)
  const redetect = async (commercials: Recording['commercials']) => {
    if (!redo) return
    await api.redetect(redo.id, commercials).catch((e: Error) => window.alert(e.message))
    setRedo(null)
    onChanged()
  }
  const toggleWatched = async (r: Recording) => {
    await api.setWatched(r.id, !r.watched).catch(() => {})
    onChanged()
  }

  // Continue watching: started but not finished. Up next: the oldest
  // unwatched episode of each show, so a season plays in order.
  const ready = items.filter((r) => r.status === 'ready')
  const continuing = ready
    .filter((r) => !r.watched && (r.position ?? 0) > 60 && r.durationSec && (r.position ?? 0) < r.durationSec - 60)
    .sort((a, b) => new Date(b.finished ?? b.created).getTime() - new Date(a.finished ?? a.created).getTime())
  const upNext = (() => {
    const seen = new Set<string>()
    const out: Recording[] = []
    for (const r of [...ready].sort((a, b) => a.airing - b.airing)) {
      if (r.watched || continuing.includes(r) || seen.has(r.title)) continue
      seen.add(r.title)
      out.push(r)
    }
    return out.slice(0, 12)
  })()

  if (items.length === 0) {
    return (
      <p className="muted">
        Nothing recorded yet. Open the <Link to="/guide">guide</Link>, click a programme and choose Record, or use Find
        something to record.
      </p>
    )
  }
  return (
    <>
      {continuing.length > 0 && (
        <>
          <div className="rec-group">Continue watching</div>
          <div className="shelf">
            {continuing.map((r) => (
              <Link key={r.id} to={`/recordings/${encodeURIComponent(r.id)}`} className="card shelf-card">
                <div className="card-name">{r.title}</div>
                <div className="card-now">{r.episodeTitle || r.episodeNumber || fmtWhen(r.airing)}</div>
                <div className="watch-progress" style={{ marginTop: 8 }}>
                  <div
                    style={{
                      width: `${Math.min(100, ((r.position ?? 0) / (r.durationSec || 1)) * 100)}%`,
                    }}
                  />
                </div>
                <div className="rec-sub">{fmtDuration((r.durationSec ?? 0) - (r.position ?? 0))} left</div>
              </Link>
            ))}
          </div>
        </>
      )}
      {upNext.length > 0 && (
        <>
          <div className="rec-group">Up next</div>
          <div className="shelf">
            {upNext.map((r) => (
              <Link key={r.id} to={`/recordings/${encodeURIComponent(r.id)}`} className="card shelf-card">
                <div className="card-name">{r.title}</div>
                <div className="card-now">
                  {[r.episodeNumber, r.episodeTitle].filter(Boolean).join(' · ') || fmtWhen(r.airing)}
                </div>
                <div className="rec-sub">{fmtDuration(r.durationSec)}</div>
              </Link>
            ))}
          </div>
        </>
      )}
      <div className="rec-group">All recordings</div>
      {groups.map(([title, recs]) => (
        <div key={title}>
          <div className="rec-group">
            {title}{' '}
            <span className="muted" style={{ fontWeight: 400 }}>
              · {recs.length}
            </span>
          </div>
          {recs.map((r) => (
            <div key={r.id} className="rec-row">
              <div className="rec-main">
                <div>
                  {r.episodeNumber && <strong>{r.episodeNumber} </strong>}
                  {r.episodeTitle || (r.episodeNumber ? '' : fmtWhen(r.airing))}
                  {r.status !== 'ready' && (
                    <span className={`tag ${r.status === 'failed' ? 'rec' : ''}`} style={{ marginLeft: 8 }}>
                      {STATUS_LABEL[r.status]}
                    </span>
                  )}
                </div>
                <div className="rec-sub">
                  {fmtWhen(r.airing)} · {r.channel} {r.channelName}
                  {r.durationSec ? ` · ${fmtDuration(r.durationSec)}` : ''}
                  {r.sizeBytes ? ` · ${fmtBytes(r.sizeBytes)}` : ''}
                  {r.cut
                    ? ' · commercials removed'
                    : r.skips?.length
                      ? ` · ${r.skips.length} commercial breaks marked`
                      : ''}
                  {r.captions ? ' · CC' : ''}
                  {r.watched ? ' · watched' : ''}
                  {r.position && r.durationSec && r.position < r.durationSec - 30
                    ? ` · resume at ${fmtDuration(r.position)}`
                    : ''}
                  {r.error ? ` · ${r.error}` : ''}
                </div>
              </div>
              <div className="rec-actions">
                {r.status === 'ready' && (
                  <Link to={`/recordings/${encodeURIComponent(r.id)}`} className="btn">
                    ▶ Watch
                  </Link>
                )}
                {r.original && (r.status === 'ready' || r.status === 'failed') && (
                  <button
                    onClick={() => setRedo(r)}
                    title="Find commercials again from the original broadcast, with the current settings"
                  >
                    Commercials…
                  </button>
                )}
                {r.status === 'ready' && (
                  <button
                    onClick={() => toggleWatched(r)}
                    title={r.watched ? 'Mark as not watched' : 'Mark as watched'}
                  >
                    {r.watched ? 'Unwatched' : 'Watched'}
                  </button>
                )}
                <button onClick={() => setConfirm(r)}>{r.status === 'recording' ? 'Stop' : 'Delete'}</button>
              </div>
            </div>
          ))}
        </div>
      ))}
      {redo && (
        <div className="modal-backdrop" onClick={() => setRedo(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>Commercials in {redo.title}</h2>
            <p className="muted">
              The original broadcast is kept
              {redo.originalUntil ? ` until ${new Date(redo.originalUntil).toLocaleDateString()}` : ''}, so commercials
              can be found again with the current settings and the video rebuilt. It takes a few minutes.
            </p>
            <div className="modal-actions" style={{ flexWrap: 'wrap' }}>
              <button onClick={() => setRedo(null)}>Cancel</button>
              <button onClick={() => redetect('none')}>Put them all back</button>
              <button onClick={() => redetect('skip')}>Find and skip</button>
              <button className="btn-rec" onClick={() => redetect('remove')}>
                Find and cut out
              </button>
            </div>
          </div>
        </div>
      )}
      {confirm && (
        <div className="modal-backdrop" onClick={() => setConfirm(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>{confirm.status === 'recording' ? 'Stop recording?' : 'Delete recording?'}</h2>
            <p className="muted">
              {confirm.title}
              {confirm.episodeTitle ? ` · ${confirm.episodeTitle}` : ''} · {fmtWhen(confirm.airing)}
              {confirm.status === 'recording'
                ? '. What has been captured so far is discarded.'
                : '. The file is removed from disk.'}
            </p>
            <div className="modal-actions">
              <button onClick={() => setConfirm(null)}>Cancel</button>
              <button className="btn-rec" onClick={remove}>
                {confirm.status === 'recording' ? 'Stop and discard' : 'Delete'}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

function Upcoming({ items, rules, onChanged }: { items: Recording[]; rules: Rule[]; onChanged: () => void }) {
  const ruleName = (id?: string) => rules.find((r) => r.id === id)?.title
  const [editing, setEditing] = useState<Recording | null>(null)
  const cancel = async (r: Recording) => {
    if (r.status === 'recording' && !window.confirm(`Stop recording ${r.title} and discard it?`)) return
    await api.deleteRecording(r.id).catch(() => {})
    onChanged()
  }
  if (items.length === 0) return <p className="muted">Nothing scheduled. Passes fill this in as the guide loads.</p>
  return (
    <>
      {editing && (
        <EditRecording
          rec={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            onChanged()
          }}
        />
      )}
      <table>
        <thead>
          <tr>
            <th>When</th>
            <th>Channel</th>
            <th>Programme</th>
            <th>From</th>
            <th>Status</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {items.map((r) => (
            <tr key={r.id}>
              <td>
                {fmtWhen(r.start)} – {fmtTime(r.end)}
              </td>
              <td>
                {r.channel} <span className="muted">{r.channelName}</span>
              </td>
              <td>
                <strong>{r.title}</strong>
                {r.episodeNumber || r.episodeTitle ? (
                  <div className="muted" style={{ fontSize: 12 }}>
                    {[r.episodeNumber, r.episodeTitle].filter(Boolean).join(' · ')}
                  </div>
                ) : null}
              </td>
              <td className="muted">{r.manual ? 'Manual' : (ruleName(r.ruleId) ?? 'Pass')}</td>
              <td className={`status-${r.status}`} title={r.error}>
                {STATUS_LABEL[r.status]}
              </td>
              <td>
                <div className="rec-actions">
                  <button onClick={() => setEditing(r)}>Edit</button>
                  <button onClick={() => cancel(r)}>{r.status === 'recording' ? 'Stop' : 'Cancel'}</button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  )
}

function Passes({ rules, recordings, onChanged }: { rules: Rule[]; recordings: Recording[]; onChanged: () => void }) {
  const [editing, setEditing] = useState<Rule | null>(null)
  const [slot, setSlot] = useState(false)
  const save = async (r: Rule) => {
    await api.saveRule(r).catch((e: Error) => window.alert(e.message))
    setEditing(null)
    onChanged()
  }
  const remove = async (r: Rule) => {
    if (!window.confirm(`Delete the pass for ${r.title}? Scheduled recordings are cancelled; finished ones stay.`))
      return
    await api.deleteRule(r.id).catch(() => {})
    onChanged()
  }
  const move = async (i: number, dir: -1 | 1) => {
    const j = i + dir
    if (j < 0 || j >= rules.length) return
    const a = { ...rules[i], priority: rules[j].priority }
    const b = { ...rules[j], priority: rules[i].priority }
    if (a.priority === b.priority) b.priority += dir
    await Promise.all([api.saveRule(a), api.saveRule(b)]).catch(() => {})
    onChanged()
  }
  const count = (id: string) => recordings.filter((r) => r.ruleId === id && r.status === 'ready').length

  const slotButton = (
    <button onClick={() => setSlot(true)} style={{ marginBottom: 12 }}>
      + Record a time slot
    </button>
  )
  if (rules.length === 0) {
    return (
      <>
        <p className="muted">
          No passes yet. A pass records every airing of a show automatically: pick one in the{' '}
          <Link to="/guide">guide</Link> or with Find something to record. A time slot records a channel at a fixed time
          whatever is on.
        </p>
        {slotButton}
        {slot && (
          <TimeSlot
            onClose={() => setSlot(false)}
            onSaved={() => {
              setSlot(false)
              onChanged()
            }}
          />
        )}
      </>
    )
  }
  return (
    <>
      <p className="muted">Passes are tried in this order when tuners run short; use the arrows to change it.</p>
      {slotButton}
      {slot && (
        <TimeSlot
          onClose={() => setSlot(false)}
          onSaved={() => {
            setSlot(false)
            onChanged()
          }}
        />
      )}
      <table>
        <thead>
          <tr>
            <th>Pass</th>
            <th>Channel</th>
            <th>Options</th>
            <th>Recorded</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rules.map((r, i) => (
            <tr key={r.id} style={r.paused ? { opacity: 0.5 } : undefined}>
              <td>
                <strong>{r.title}</strong>
                {r.kind === 'manual' && (
                  <span className="tag" style={{ marginLeft: 8 }}>
                    time slot
                  </span>
                )}
                {r.paused && (
                  <span className="tag" style={{ marginLeft: 8 }}>
                    Paused
                  </span>
                )}
              </td>
              <td>{r.channel || <span className="muted">Any</span>}</td>
              <td className="muted" style={{ fontSize: 13 }}>
                {[
                  r.kind === 'manual'
                    ? `${r.timeOfDay} for ${r.durationMin} min ${r.date ? `on ${r.date}` : (r.days ?? []).map((d) => DAY_NAMES[d]).join(', ')}`
                    : r.newOnly
                      ? 'new episodes only'
                      : 'all episodes',
                  r.kind !== 'manual' && r.season ? `season ${r.season}` : '',
                  r.keep ? `keep ${r.keep}` : '',
                  r.deleteWatched ? 'delete after watching' : '',
                  `${r.padBefore}/${r.padAfter} min padding`,
                  r.commercials === 'skip'
                    ? 'skip commercials'
                    : r.commercials === 'remove'
                      ? 'remove commercials'
                      : 'commercials kept',
                ]
                  .filter(Boolean)
                  .join(' · ')}
              </td>
              <td>{count(r.id)}</td>
              <td>
                <div className="rec-actions">
                  <button onClick={() => move(i, -1)} disabled={i === 0} title="Higher priority">
                    ↑
                  </button>
                  <button onClick={() => move(i, 1)} disabled={i === rules.length - 1} title="Lower priority">
                    ↓
                  </button>
                  <button onClick={() => setEditing(r)}>Edit</button>
                  <button onClick={() => save({ ...r, paused: !r.paused })}>{r.paused ? 'Resume' : 'Pause'}</button>
                  <button onClick={() => remove(r)}>Delete</button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {editing && <EditRule rule={editing} onClose={() => setEditing(null)} onSave={save} />}
    </>
  )
}

function EditRule({ rule, onClose, onSave }: { rule: Rule; onClose: () => void; onSave: (r: Rule) => void }) {
  const [r, setR] = useState<Rule>(rule)
  const set = <K extends keyof Rule>(k: K, v: Rule[K]) => setR({ ...r, [k]: v })
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Edit pass</h2>
        <div className="form-grid">
          <label>Title</label>
          <input type="text" value={r.title} onChange={(e) => set('title', e.target.value)} />
          <label>Channel</label>
          <input
            type="text"
            value={r.channel ?? ''}
            placeholder="Any"
            onChange={(e) => set('channel', e.target.value)}
          />
          <label>Episodes</label>
          <div className="choice">
            <label>
              <input type="checkbox" checked={r.newOnly} onChange={(e) => set('newOnly', e.target.checked)} /> New only
            </label>
            <label>
              Season{' '}
              <input
                type="number"
                min={0}
                value={r.season ?? 0}
                onChange={(e) => set('season', parseInt(e.target.value, 10) || 0)}
                style={{ width: 70 }}
              />
              <span className="muted">(0 = all)</span>
            </label>
          </div>
          <label>Keep</label>
          <div className="choice">
            <input
              type="number"
              min={0}
              value={r.keep}
              onChange={(e) => set('keep', parseInt(e.target.value, 10) || 0)}
              style={{ width: 80 }}
            />
            <label>
              <input
                type="checkbox"
                checked={!!r.deleteWatched}
                onChange={(e) => set('deleteWatched', e.target.checked)}
              />{' '}
              Delete a day after watching
            </label>
          </div>
          <label>Start early (min)</label>
          <input
            type="number"
            min={0}
            max={120}
            value={r.padBefore}
            onChange={(e) => set('padBefore', parseInt(e.target.value, 10) || 0)}
          />
          <label>Run late (min)</label>
          <input
            type="number"
            min={0}
            max={180}
            value={r.padAfter}
            onChange={(e) => set('padAfter', parseInt(e.target.value, 10) || 0)}
          />
          <label>Commercials</label>
          <select value={r.commercials} onChange={(e) => set('commercials', e.target.value as Rule['commercials'])}>
            <option value="skip">Detect and skip during playback</option>
            <option value="remove">Detect and cut out of the file</option>
            <option value="none">Leave as broadcast</option>
          </select>
        </div>
        <div className="modal-actions">
          <button onClick={onClose}>Cancel</button>
          <button onClick={() => onSave(r)}>Save</button>
        </div>
      </div>
    </div>
  )
}

function Search({ comskip, onDone }: { comskip: boolean; onDone: (m: string) => void }) {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [pick, setPick] = useState<SearchHit | null>(null)

  useEffect(() => {
    const words = q.trim()
    if (words.length < 2) {
      setHits(null)
      return
    }
    setBusy(true)
    const t = window.setTimeout(() => {
      api
        .guideSearch(words)
        .then(setHits)
        .catch(() => setHits([]))
        .finally(() => setBusy(false))
    }, 350)
    return () => window.clearTimeout(t)
  }, [q])

  return (
    <>
      <div className="toolbar">
        <input
          type="search"
          placeholder="Show, episode or words in the description"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          autoFocus
        />
        {busy && <span className="muted">Searching…</span>}
      </div>
      {hits && hits.length === 0 && <p className="muted">Nothing in the next day of listings matches.</p>}
      {hits?.map((h) => (
        <div key={`${h.channel}-${h.program.start}`} className="rec-row">
          <div className="rec-main">
            <div>
              <strong>{h.program.title}</strong>
              {h.program.new && (
                <span className="tag" style={{ marginLeft: 6 }}>
                  NEW
                </span>
              )}
              {h.program.episodeTitle ? <span className="muted"> · {h.program.episodeTitle}</span> : null}
            </div>
            <div className="rec-sub">
              {fmtWhen(h.program.start)} – {fmtTime(h.program.end)} · {h.channel} {h.channelName}
              {h.program.episodeNumber ? ` · ${h.program.episodeNumber}` : ''}
            </div>
            {h.program.synopsis && <div className="rec-sub">{h.program.synopsis}</div>}
          </div>
          <div className="rec-actions">
            <button className="btn-rec" onClick={() => setPick(h)}>
              ● Record
            </button>
          </div>
        </div>
      ))}
      {pick && (
        <RecordDialog
          channel={pick.channel}
          channelName={pick.channelName}
          program={pick.program}
          comskip={comskip}
          onClose={() => setPick(null)}
          onDone={(m) => {
            setPick(null)
            onDone(m)
          }}
        />
      )}
    </>
  )
}

/** Record a channel at a fixed time, whatever the guide says. */
function TimeSlot({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [channels, setChannels] = useState<Channel[]>([])
  const [channel, setChannel] = useState('')
  const [time, setTime] = useState('20:00')
  const [duration, setDuration] = useState(60)
  const [repeat, setRepeat] = useState(true)
  const [days, setDays] = useState<number[]>([1, 2, 3, 4, 5])
  const [date, setDate] = useState(() => new Date().toISOString().slice(0, 10))
  const [title, setTitle] = useState('')
  const [keep, setKeep] = useState(0)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    api.channels().then((c) => {
      const ok = c.filter((x) => !x.drm)
      setChannels(ok)
      if (ok[0]) setChannel(ok[0].guideNumber)
    })
  }, [])
  const save = async () => {
    try {
      await api.saveRule({
        kind: 'manual',
        channel,
        timeOfDay: time,
        durationMin: duration,
        days: repeat ? days : [],
        date: repeat ? '' : date,
        title,
        keep,
        newOnly: false,
        padBefore: 0,
        padAfter: 0,
        commercials: 'skip',
      })
      onSaved()
    } catch (e) {
      setError((e as Error).message)
    }
  }
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Record a time slot</h2>
        <div className="form-grid">
          <label>Channel</label>
          <select value={channel} onChange={(e) => setChannel(e.target.value)}>
            {channels.map((c) => (
              <option key={c.guideNumber} value={c.guideNumber}>
                {c.guideNumber} {c.guideName}
              </option>
            ))}
          </select>
          <label>Starts</label>
          <input type="time" value={time} onChange={(e) => setTime(e.target.value)} />
          <label>Length</label>
          <select value={duration} onChange={(e) => setDuration(parseInt(e.target.value, 10))}>
            {[15, 30, 45, 60, 90, 120, 180, 240].map((n) => (
              <option key={n} value={n}>
                {n} min
              </option>
            ))}
          </select>
          <label>When</label>
          <div className="choice">
            <label>
              <input type="radio" checked={repeat} onChange={() => setRepeat(true)} /> Every
            </label>
            {repeat &&
              DAY_NAMES.map((d, i) => (
                <label key={d}>
                  <input
                    type="checkbox"
                    checked={days.includes(i)}
                    onChange={(e) => setDays(e.target.checked ? [...days, i].sort() : days.filter((x) => x !== i))}
                  />{' '}
                  {d}
                </label>
              ))}
            <label>
              <input type="radio" checked={!repeat} onChange={() => setRepeat(false)} /> Once on
            </label>
            {!repeat && <input type="date" value={date} onChange={(e) => setDate(e.target.value)} />}
          </div>
          <label>Name</label>
          <input
            type="text"
            value={title}
            placeholder="Channel and time if empty"
            onChange={(e) => setTitle(e.target.value)}
          />
          <label>Keep</label>
          <select value={keep} onChange={(e) => setKeep(parseInt(e.target.value, 10))}>
            <option value={0}>All</option>
            {[1, 3, 5, 10].map((n) => (
              <option key={n} value={n}>
                Newest {n}
              </option>
            ))}
          </select>
        </div>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button onClick={onClose}>Cancel</button>
          <button className="btn-rec" onClick={save}>
            ● Save
          </button>
        </div>
      </div>
    </div>
  )
}

/** Change a scheduled or running recording's padding and commercial handling. */
function EditRecording({ rec, onClose, onSaved }: { rec: Recording; onClose: () => void; onSaved: () => void }) {
  const running = rec.status === 'recording'
  const [padBefore, setPadBefore] = useState(Math.max(0, Math.round((rec.airing - rec.start) / 60)))
  const [padAfter, setPadAfter] = useState(Math.max(0, Math.round((rec.end - rec.airingEnd) / 60)))
  const [commercials, setCommercials] = useState<Recording['commercials']>(rec.commercials)
  const [error, setError] = useState<string | null>(null)
  const save = async () => {
    try {
      await api.editRecording(rec.id, {
        padBefore: running ? undefined : padBefore,
        padAfter,
        commercials,
      })
      onSaved()
    } catch (e) {
      setError((e as Error).message)
    }
  }
  const mins = (list: number[], cur: number) => Array.from(new Set([...list, cur])).sort((a, b) => a - b)
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Edit {rec.title}</h2>
        <p className="muted">
          {rec.channel} {rec.channelName} · {fmtWhen(rec.airing)} – {fmtTime(rec.airingEnd)}
          {running ? ' · recording now' : ''}
        </p>
        <div className="form-grid">
          <label>Start early</label>
          <select value={padBefore} disabled={running} onChange={(e) => setPadBefore(parseInt(e.target.value, 10))}>
            {mins([0, 1, 2, 5, 10, 15, 30], padBefore).map((n) => (
              <option key={n} value={n}>
                {n} min
              </option>
            ))}
          </select>
          <label>Run late</label>
          <select value={padAfter} onChange={(e) => setPadAfter(parseInt(e.target.value, 10))}>
            {mins([0, 1, 3, 5, 10, 15, 30, 60, 90, 120], padAfter).map((n) => (
              <option key={n} value={n}>
                {n} min
              </option>
            ))}
          </select>
          <label>Commercials</label>
          <select value={commercials} onChange={(e) => setCommercials(e.target.value as Recording['commercials'])}>
            <option value="skip">Detect and skip during playback</option>
            <option value="remove">Detect and cut out of the file</option>
            <option value="none">Leave as broadcast</option>
          </select>
        </div>
        <p className="muted" style={{ fontSize: 13 }}>
          {running
            ? 'This recording has started, so only its end and commercial handling can change.'
            : 'Changes apply to this recording only; the pass it came from keeps its own settings.'}
        </p>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button onClick={onClose}>Cancel</button>
          <button onClick={save}>Save</button>
        </div>
      </div>
    </div>
  )
}
