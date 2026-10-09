import { useEffect, useState } from 'react'
import {
  api,
  type Download,
  type MCPClient,
  type MCPInfo,
  type Device,
  type DiagReport,
  type StreamStatus,
  type SystemInfo,
  type TunerStatus,
} from '../api'

export function useInterval(fn: () => void, ms: number) {
  useEffect(() => {
    fn()
    const id = setInterval(fn, ms)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms])
}

export function Diagnostics({ defaultHost }: { defaultHost: string }) {
  const [host, setHost] = useState(defaultHost)
  const [report, setReport] = useState<DiagReport | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!host && defaultHost) setHost(defaultHost)
  }, [defaultHost, host])

  const run = async () => {
    setRunning(true)
    setError(null)
    try {
      setReport(await api.diagnostics(host))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="panel">
      <div className="toolbar" style={{ marginBottom: 8 }}>
        <input
          type="text"
          placeholder="HDHomeRun IP"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          style={{ maxWidth: 260 }}
        />
        <button onClick={run} disabled={running || !host}>
          {running ? 'Testing…' : 'Test connectivity'}
        </button>
        <span className="muted">Runs from inside the container, so it sees exactly what the server sees.</span>
      </div>
      {error && <p className="error">{error}</p>}
      {report && (
        <>
          <table>
            <tbody>
              <tr>
                <th>Container addresses</th>
                <td>
                  {(report.interfaces ?? []).map((i) => (
                    <div key={i.name}>
                      <code>{i.name}</code> {i.addrs.join(', ')}
                    </div>
                  ))}
                </td>
              </tr>
              <tr>
                <th>Default gateway</th>
                <td>{report.defaultGateway || <span className="error">none</span>}</td>
              </tr>
              <tr>
                <th>Target</th>
                <td>
                  {report.host} {report.resolvedIps && report.resolvedIps[0] !== report.host && `→ ${report.resolvedIps.join(', ')}`}
                </td>
              </tr>
            </tbody>
          </table>
          <table style={{ marginTop: 8 }}>
            <thead>
              <tr>
                <th>Check</th>
                <th>Result</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {(report.checks ?? []).map((c) => (
                <tr key={c.name} className={c.ok ? 'active' : ''}>
                  <td>{c.name}</td>
                  <td>
                    {c.ok ? 'ok' : <span className="error">fail</span>} {c.latency && <span className="muted">{c.latency}</span>}
                  </td>
                  <td className="muted">{c.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {report.hint && <p style={{ marginTop: 10 }}>{report.hint}</p>}
        </>
      )}
    </div>
  )
}

export default function Status() {
  const [system, setSystem] = useState<SystemInfo | null>(null)
  const [devices, setDevices] = useState<Device[]>([])
  const [tuners, setTuners] = useState<Record<string, TunerStatus[] | string>>({})
  const [streams, setStreams] = useState<StreamStatus[]>([])
  const [admin, setAdmin] = useState(false)

  useEffect(() => {
    api.me().then((m) => setAdmin(!!m.admin)).catch(() => {})
  }, [])

  useInterval(() => {
    api.system().then(setSystem).catch(() => {})
    api.streams().then(setStreams).catch(() => {})
    api
      .devices()
      .then(async (devs) => {
        setDevices(devs)
        const entries = await Promise.all(
          devs.map(async (d) => {
            try {
              return [d.device.DeviceID, await api.deviceStatus(d.device.DeviceID)] as const
            } catch (e) {
              return [d.device.DeviceID, (e as Error).message] as const
            }
          }),
        )
        setTuners(Object.fromEntries(entries))
      })
      .catch(() => {})
  }, 5000)

  const stop = async (ch: string) => {
    await api.stopStream(ch).catch(() => {})
    setStreams(await api.streams())
  }

  return (
    <section>
      <h1>Status</h1>

      <h2>Server</h2>
      {system ? (
        <table>
          <tbody>
            <tr>
              <th>Version</th>
              <td>{system.version}</td>
            </tr>
            <tr>
              <th>Uptime</th>
              <td>{system.uptime}</td>
            </tr>
            <tr>
              <th>ffmpeg</th>
              <td>
                {system.ffmpeg.version}
                <div className="muted">{system.ffmpeg.ffmpegPath}</div>
              </td>
            </tr>
            <tr>
              <th>Encoder</th>
              <td>
                {system.ffmpeg.encoder}
                {system.ffmpeg.requested !== system.ffmpeg.encoder && (
                  <span className="muted"> (requested {system.ffmpeg.requested})</span>
                )}
                {system.degraded && <span className="tag warn">hardware failed, using software</span>}
                <div className="muted">
                  QSV {system.ffmpeg.hasQsv ? 'yes' : 'no'} · VAAPI {system.ffmpeg.hasVaapi ? 'yes' : 'no'} ·{' '}
                  {system.ffmpeg.renderDevice} {system.ffmpeg.renderPresent ? 'present' : 'missing'}
                  {system.ffmpeg.driverPath && <> · driver {system.ffmpeg.driverPath}</>}
                </div>
                {system.ffmpeg.hardwareError && (
                  <pre className="logtail error">{system.ffmpeg.hardwareError}</pre>
                )}
              </td>
            </tr>
            <tr>
              <th>Lineup</th>
              <td>
                {system.channelCount} channels on {system.deviceCount} device(s)
              </td>
            </tr>
            <tr>
              <th>Guide</th>
              <td>
                {system.guide ? (
                  <>
                    {system.guide.channels} channels · {system.guide.programs} programmes
                    {system.guide.programs > 0 && <> · listings until {new Date(system.guide.until).toLocaleString()}</>}
                    <div className="muted">
                      {system.guide.lastRefresh && !system.guide.lastRefresh.startsWith('0001')
                        ? `refreshed ${new Date(system.guide.lastRefresh).toLocaleTimeString()}`
                        : 'not fetched yet'}
                    </div>
                    {system.guide.lastError && <pre className="logtail error">{system.guide.lastError}</pre>}
                  </>
                ) : (
                  <span className="muted">not available</span>
                )}
              </td>
            </tr>
          </tbody>
        </table>
      ) : (
        <p className="muted">Loading…</p>
      )}

      <h2>Tuners</h2>
      {devices.length === 0 && (
        <p className="muted">No HDHomeRun devices found. Use the connectivity test below to see why.</p>
      )}
      {devices.map((d) => {
        const st = tuners[d.device.DeviceID]
        return (
          <div key={d.device.DeviceID} className="panel">
            <div className="panel-head">
              <strong>{d.device.FriendlyName || d.device.ModelNumber}</strong>
              <span className="muted">
                {d.device.DeviceID} · {d.host} · fw {d.device.FirmwareVersion} · {d.device.TunerCount} tuners ·{' '}
                {d.channelCount} channels
              </span>
              {d.lastError && <span className="tag warn">{d.lastError}</span>}
            </div>
            {typeof st === 'string' && <p className="error">{st}</p>}
            {Array.isArray(st) && (
              <table>
                <thead>
                  <tr>
                    <th>Tuner</th>
                    <th>Channel</th>
                    <th>Signal</th>
                    <th>Quality</th>
                    <th>Symbol</th>
                    <th>Client</th>
                    <th>Rate</th>
                  </tr>
                </thead>
                <tbody>
                  {st.map((t) => {
                    const ours = t.VctNumber ? streams.find((s) => s.channel === t.VctNumber && s.running) : undefined
                    return (
                    <tr key={t.Resource} className={t.TargetIP ? 'active' : ''}>
                      <td>{t.Resource}</td>
                      <td>
                        {t.VctNumber ?? '—'} {t.VctName ?? ''}
                      </td>
                      <td>{t.SignalStrengthPercent ?? '—'}%</td>
                      <td>{t.SignalQualityPercent ?? '—'}%</td>
                      <td>{t.SymbolQualityPercent ?? '—'}%</td>
                      <td>
                        {t.TargetIP ?? 'idle'}
                        {ours && (
                          <div className="muted">
                            this server · {ours.viewers.length} {ours.viewers.length === 1 ? 'viewer' : 'viewers'}
                          </div>
                        )}
                      </td>
                      <td>{t.NetworkRate ? `${(t.NetworkRate / 1_000_000).toFixed(1)} Mb/s` : '—'}</td>
                    </tr>
                    )
                  })}
                </tbody>
              </table>
            )}
          </div>
        )
      })}

      <h2>Apps</h2>
      <Downloads />

      <h2>Active streams</h2>
      {streams.length === 0 && <p className="muted">Nothing is being watched.</p>}
      {streams.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Channel</th>
              <th>Tuner</th>
              <th>Encoder</th>
              <th>Buffer</th>
              <th>Started</th>
              <th>Viewers</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {streams.map((s) => (
              <tr key={s.channel}>
                <td>{s.channel}</td>
                <td>{s.deviceId}</td>
                <td>{s.encoder}</td>
                <td>{s.segments} segments</td>
                <td>{new Date(s.started).toLocaleTimeString()}</td>
                <td>
                  {s.viewers.length === 0 && (
                    <span className="muted">none · last request {new Date(s.lastAccess).toLocaleTimeString()}</span>
                  )}
                  {s.viewers.map((v) => (
                    <div key={v.id}>
                      <strong>{v.userName || v.name || 'Someone'}</strong>
                      {v.userName && v.name ? <span className="muted"> · {v.name}</span> : null}
                      {v.addr ? <span className="muted"> · {v.addr}{v.place ? ` (${v.place})` : ''}</span> : null}
                      {v.paused && <span className="tag">paused</span>}
                      <span className="muted"> · since {new Date(v.started).toLocaleTimeString()}</span>
                    </div>
                  ))}
                </td>
                <td>{admin && <button onClick={() => stop(s.channel)}>Stop</button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function fmtWhen(dt?: string | null) {
  return dt ? new Date(dt).toLocaleString() : '—'
}

/**
 * Connected assistants: the MCP URL to paste into Claude, and every
 * connector that has signed in, with last activity and a revoke button.
 */
export function McpConnectors() {
  const [info, setInfo] = useState<MCPInfo | null>(null)
  const [clients, setClients] = useState<MCPClient[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<{ kind: 'client' | 'session'; id: string; label: string } | null>(null)

  const load = () => {
    api.mcpInfo().then(setInfo).catch(() => {})
    api
      .mcpClients()
      .then((c) => {
        setClients(c)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))
  }
  useInterval(load, 15000)

  const revoke = async () => {
    if (!confirm) return
    try {
      if (confirm.kind === 'client') await api.deleteMcpClient(confirm.id)
      else await api.revokeMcpSession(confirm.id)
    } catch (e) {
      setError((e as Error).message)
    }
    setConfirm(null)
    load()
  }

  return (
    <div className="panel">
      <div className="panel-head">
        <strong>Connect Claude</strong>
        <span className="muted">
          {info ? `${info.toolCount} tools` : ''}
          {info?.oauth ? ' · sign-in through your account' : ' · open (no auth configured)'}
        </span>
      </div>
      <p className="muted">
        In Claude, add a custom connector with this URL. Leave client id and secret empty: the server registers the
        connector itself, sends you through your normal sign-in and asks for consent.
        {info?.mcpGroup && (
          <>
            {' '}
            Only members of the <code>{info.mcpGroup}</code> group can connect.
          </>
        )}
      </p>
      <pre className="logtail">{info?.mcpUrl ?? '…'}</pre>
      {error && <p className="error">{error}</p>}
      {clients && clients.length === 0 && <p className="muted">No assistant has connected yet.</p>}
      {clients && clients.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Connector</th>
              <th>Registered</th>
              <th>Sessions</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {clients.map((c) => (
              <tr key={c.clientId}>
                <td>
                  {c.name}
                  <div className="muted">{c.redirectUris.map((u) => new URL(u).host).join(', ')}</div>
                </td>
                <td>
                  {fmtWhen(c.createdAt)}
                  <div className="muted">last activity {fmtWhen(c.lastUsedAt)}</div>
                </td>
                <td>
                  {c.sessions.length === 0 && <span className="muted">none active</span>}
                  {c.sessions.map((s) => (
                    <div key={s.id}>
                      {s.user.name || s.user.email || s.user.sub}
                      <span className="muted">
                        {' '}
                        · since {fmtWhen(s.createdAt)} · last activity {fmtWhen(s.lastUsedAt)}
                      </span>{' '}
                      <button onClick={() => setConfirm({ kind: 'session', id: s.id, label: `${c.name} (${s.user.name || s.user.email || s.user.sub})` })}>
                        Revoke
                      </button>
                    </div>
                  ))}
                </td>
                <td>
                  <button onClick={() => setConfirm({ kind: 'client', id: c.clientId, label: c.name })}>Remove</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {confirm && (
        <div className="modal-backdrop" onClick={() => setConfirm(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>{confirm.kind === 'client' ? 'Remove connector?' : 'Revoke session?'}</h2>
            <p>
              {confirm.kind === 'client'
                ? `${confirm.label} will be forgotten and every token it holds revoked. It will have to be added again in Claude.`
                : `${confirm.label} will be signed out of the connector and must sign in again.`}
            </p>
            <div className="modal-actions">
              <button onClick={() => setConfirm(null)}>Cancel</button>
              <button onClick={revoke}>{confirm.kind === 'client' ? 'Remove' : 'Revoke'}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/** The Android TV / Fire TV app baked into the server, with install notes. */
function Downloads() {
  const [items, setItems] = useState<Download[] | null>(null)
  useEffect(() => {
    api.downloads().then(setItems).catch(() => setItems([]))
  }, [])
  const origin = window.location.origin
  return (
    <div className="panel">
      <div className="panel-head">
        <strong>Android TV / Fire TV app</strong>
        <span className="muted">install from this server, no store needed</span>
      </div>
      {items === null && <p className="muted">Loading…</p>}
      {items && items.length === 0 && (
        <p className="muted">
          No app package is on this server. The app is built into the container image; this image was built without it.
        </p>
      )}
      {items && items.length > 0 && (
        <>
          <table>
            <thead>
              <tr>
                <th>File</th>
                <th>Size</th>
                <th>Date</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map((d) => (
                <tr key={d.name}>
                  <td>
                    {d.name}{' '}
                    {d.builtIn ? (
                      <span className="tag ok">built in</span>
                    ) : (
                      <span className="tag" title="Added to the config folder by hand">added by hand</span>
                    )}
                  </td>
                  <td>{(d.size / 1048576).toFixed(1)} MB</td>
                  <td>{new Date(d.modified).toLocaleString()}</td>
                  <td>
                    <a className="btn" href={d.url} download>
                      Download
                    </a>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="muted">
            On a Fire TV: enable <em>Apps from unknown sources</em> (Settings → My Fire TV → Developer options), open the
            Downloader app and enter this address:
          </p>
          <pre className="logtail">{origin + items[0].url}</pre>
          <p className="muted">Or on a PC: <code>adb connect &lt;tv-ip&gt;</code> then <code>adb install {items[0].name}</code>.</p>
          {items[0].builtIn && items[0].signedBy && (
            <p className="muted" style={{ fontSize: 12 }}>
              The built-in app is signed with this server&apos;s own key, so a newer version installs over the old one
              and keeps its sign-in. Key SHA-256: <code style={{ wordBreak: 'break-all' }}>{items[0].signedBy}</code>
            </p>
          )}
          {items.some((d) => !d.builtIn) && (
            <p className="muted" style={{ fontSize: 12 }}>
              Packages added by hand to the config folder are served as they are. Delete them once the built-in app is
              the one you want.
            </p>
          )}
        </>
      )}
    </div>
  )
}
