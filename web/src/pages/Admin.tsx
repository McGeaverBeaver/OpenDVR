import { useEffect, useState } from 'react'
import { api, type AuditEvent, type AuditLog, type Channel, type Device, type Profile, type StreamFailure, type SystemInfo, type Viewer } from '../api'
import { Diagnostics, McpConnectors, useInterval } from './Status'

function fmtWhen(dt?: string | null) {
  return dt ? new Date(dt).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', second: '2-digit' }) : '—'
}

function ago(dt: string): string {
  const s = Math.max(0, Math.round((Date.now() - new Date(dt).getTime()) / 1000))
  if (s < 90) return `${s}s`
  const m = Math.round(s / 60)
  return m < 90 ? `${m}m` : `${Math.round(m / 60)}h`
}

const KINDS: Array<[string, string]> = [
  ['', 'Everything'],
  ['auth.', 'Sign-ins'],
  ['tv.', 'TV sign-ins'],
  ['stream.', 'Watching'],
  ['dvr.', 'DVR'],
  ['mcp.', 'Assistants'],
  ['admin.', 'Admin actions'],
]

/**
 * The administrator's page: who is watching right now and from where,
 * the audit trail of sign-ins and actions, assistants, stream failures
 * and the connectivity test. Everything here is served to the admin
 * group only.
 */
export default function Admin() {
  const [viewers, setViewers] = useState<Viewer[]>([])
  const [log, setLog] = useState<AuditLog | null>(null)
  const [kind, setKind] = useState('')
  const [q, setQ] = useState('')
  const [failures, setFailures] = useState<StreamFailure[]>([])
  const [system, setSystem] = useState<SystemInfo | null>(null)
  const [devices, setDevices] = useState<Device[]>([])
  const [error, setError] = useState<string | null>(null)

  useInterval(() => {
    api.viewers().then(setViewers).catch((e: Error) => setError(e.message))
    api.streamFailures().then(setFailures).catch(() => {})
    api.system().then(setSystem).catch(() => {})
    api.devices().then(setDevices).catch(() => {})
  }, 5000)

  useEffect(() => {
    const t = window.setTimeout(() => {
      api.audit(kind, q, 300).then(setLog).catch((e: Error) => setError(e.message))
    }, 250)
    return () => window.clearTimeout(t)
  }, [kind, q])
  useInterval(() => {
    api.audit(kind, q, 300).then(setLog).catch(() => {})
  }, 15000)

  return (
    <section>
      <h1>Admin</h1>
      {error && <p className="error">{error}</p>}

      <h2>Watching now</h2>
      {viewers.length === 0 && <p className="muted">Nobody is watching.</p>}
      {viewers.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Channel</th>
              <th>Account</th>
              <th>Device</th>
              <th>Address</th>
              <th>Where</th>
              <th>Since</th>
              <th>State</th>
            </tr>
          </thead>
          <tbody>
            {viewers.map((v) => (
              <tr key={v.id}>
                <td>
                  <strong>{v.channel}</strong>
                </td>
                <td>{v.userName || v.user || <span className="muted">—</span>}</td>
                <td>{v.name || <span className="muted">unnamed</span>}</td>
                <td>{v.addr}</td>
                <td>{v.place || <span className="muted">{log?.geoip ? 'unknown' : 'no GeoIP db'}</span>}</td>
                <td title={fmtWhen(v.started)}>{ago(v.started)}</td>
                <td>{v.paused ? <span className="tag">paused</span> : <span className="tag ok">playing</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h2>Audit log</h2>
      <div className="toolbar">
        <div className="tabs" style={{ marginBottom: 0 }}>
          {KINDS.map(([k, label]) => (
            <button key={k} className={kind === k ? 'on' : ''} onClick={() => setKind(k)}>
              {label}
            </button>
          ))}
        </div>
        <input type="search" placeholder="Filter by user, address, country or text" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      <p className="muted" style={{ fontSize: 13 }}>
        {log?.geoip ? (
          <>Countries come from {log.geoipFile.split(/[\\/]/).pop()} in the data folder. </>
        ) : (
          <>
            No GeoIP database: drop MaxMind&apos;s <code>GeoLite2-City.mmdb</code> (free with an account) into the
            config folder next to guide.json and countries appear within a minute.{' '}
          </>
        )}
        {log?.adminGroup ? (
          <>
            Administrators are members of <code>{log.adminGroup}</code>.
          </>
        ) : (
          <>Everyone signed in is an administrator; set ADMIN_GROUP to narrow it.</>
        )}
      </p>
      {log && log.events.length === 0 && <p className="muted">Nothing recorded yet.</p>}
      {log && log.events.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>When</th>
              <th>Event</th>
              <th>Account</th>
              <th>Address</th>
              <th>Where</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            {log.events.map((e, i) => (
              <AuditRow key={`${e.time}-${i}`} e={e} />
            ))}
          </tbody>
        </table>
      )}

      <h2>Accounts</h2>
      <Users />

      <h2>Backup and restore</h2>
      <Backup />

      <h2>Assistants (MCP)</h2>
      <McpConnectors />

      <h2>Recent stream failures</h2>
      {failures.length === 0 && <p className="muted">None.</p>}
      {failures.map((f) => (
        <div key={f.channel + f.time} className="panel">
          <div className="panel-head">
            <strong>{f.channel}</strong>
            <span className="muted">
              {new Date(f.time).toLocaleString()} · {f.encoder}
            </span>
            <span className="error">{f.error}</span>
          </div>
          {f.logTail && f.logTail.length > 0 && <pre className="logtail">{f.logTail.join('\n')}</pre>}
          {f.command && <pre className="logtail muted">{f.command}</pre>}
        </div>
      ))}

      <h2>Connectivity test</h2>
      <Diagnostics defaultHost={system?.hosts?.[0] ?? devices[0]?.host ?? ''} />
    </section>
  )
}

function AuditRow({ e }: { e: AuditEvent }) {
  const bad = e.kind.endsWith('refused') || e.kind.endsWith('failed') || e.kind.endsWith('denied')
  return (
    <tr>
      <td style={{ whiteSpace: 'nowrap' }}>{fmtWhen(e.time)}</td>
      <td>
        <span className={`tag ${bad ? 'rec' : ''}`}>{e.kind}</span>
      </td>
      <td>{e.name || e.user || <span className="muted">—</span>}</td>
      <td title={e.agent}>{e.ip}</td>
      <td>{[e.city, e.country].filter(Boolean).join(', ')}</td>
      <td className={bad ? 'error' : ''}>{e.detail}</td>
    </tr>
  )
}

/** Everyone who has signed in, with kids mode per account. */
function Users() {
  const [users, setUsers] = useState<Profile[] | null>(null)
  const [channels, setChannels] = useState<Channel[]>([])
  const [editing, setEditing] = useState<Profile | null>(null)
  const [allowed, setAllowed] = useState<string[]>([])
  const load = () => api.users().then(setUsers).catch(() => setUsers([]))
  useEffect(() => {
    load()
    api.channels().then((c) => setChannels(c.filter((x) => !x.drm))).catch(() => {})
  }, [])
  const save = async (restricted: boolean) => {
    if (!editing) return
    await api.setUser(editing.sub, restricted, allowed).catch(() => {})
    setEditing(null)
    load()
  }
  if (users === null) return <p className="muted">Loading…</p>
  if (users.length === 0) return <p className="muted">No accounts have signed in yet (with auth off there are none).</p>
  return (
    <>
      <table>
        <thead>
          <tr>
            <th>Account</th>
            <th>Last seen</th>
            <th>Favourites</th>
            <th>Profile</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.sub}>
              <td>
                {u.name || u.sub}
                <div className="muted" style={{ fontSize: 12 }}>
                  {u.email}
                </div>
              </td>
              <td>{fmtWhen(u.lastSeen)}</td>
              <td>{u.favorites.length}</td>
              <td>{u.restricted ? <span className="tag warn">kids · {(u.allowed ?? []).length} channels</span> : <span className="muted">full</span>}</td>
              <td>
                <button
                  onClick={() => {
                    setEditing(u)
                    setAllowed(u.allowed ?? [])
                  }}
                >
                  Edit
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {editing && (
        <div className="modal-backdrop" onClick={() => setEditing(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>{editing.name || editing.sub}</h2>
            <p className="muted">
              Kids mode limits this account to the channels ticked below, in the guide, the grid and the TV app, and stops it
              changing recordings.
            </p>
            <div className="choice" style={{ display: 'flex', flexWrap: 'wrap', gap: 10, maxHeight: 260, overflowY: 'auto' }}>
              {channels.map((c) => (
                <label key={c.guideNumber} style={{ display: 'flex', gap: 6, alignItems: 'center', minWidth: 160 }}>
                  <input
                    type="checkbox"
                    checked={allowed.includes(c.guideNumber)}
                    onChange={(e) =>
                      setAllowed(e.target.checked ? [...allowed, c.guideNumber] : allowed.filter((x) => x !== c.guideNumber))
                    }
                  />
                  {c.guideNumber} {c.guideName}
                </label>
              ))}
            </div>
            <div className="modal-actions">
              <button onClick={() => setEditing(null)}>Cancel</button>
              <button onClick={() => save(false)}>Full access</button>
              <button className="btn-rec" onClick={() => save(true)} disabled={allowed.length === 0}>
                Kids mode
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

/** Download the server's state, or upload one to restore. */
function Backup() {
  const [result, setResult] = useState<string | null>(null)
  const restore = async (f: File | undefined) => {
    if (!f) return
    if (!window.confirm(`Restore ${f.name}? Passes, recordings metadata and profiles are replaced.`)) return
    try {
      const out = await api.restore(f)
      setResult(`Restored ${out.restored.join(', ') || 'nothing'}. ${out.note}`)
    } catch (e) {
      setResult((e as Error).message)
    }
  }
  return (
    <div className="panel">
      <p className="muted">
        The backup holds passes, recording metadata and positions, profiles, connector tokens, the comskip settings, the
        session key, the TV app signing key and the audit log. It does not include the video files. Keep it private.
      </p>
      <div className="rec-actions">
        <a className="btn" href={api.backupUrl}>
          Download backup
        </a>
        <label className="btn" style={{ cursor: 'pointer' }}>
          Restore from file
          <input type="file" accept=".zip" style={{ display: 'none' }} onChange={(e) => restore(e.target.files?.[0])} />
        </label>
      </div>
      {result && <p className="muted">{result}</p>}
    </div>
  )
}
