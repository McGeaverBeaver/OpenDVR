import { useState } from 'react'
import { api, type Commercials, type Program } from '../api'

interface Props {
  channel: string
  channelName?: string
  program: Program
  /** Whether comskip is available, which decides the default commercial handling. */
  comskip: boolean
  onClose: () => void
  /** Called with a short confirmation after the recording or pass is saved. */
  onDone: (message: string) => void
}

function seasonOf(episodeNumber?: string): number {
  const m = /^S(\d+)E\d+/i.exec(episodeNumber ?? '')
  return m ? parseInt(m[1], 10) : 0
}

function fmtWhen(unix: number): string {
  return new Date(unix * 1000).toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit' })
}

/**
 * "Record this" for a programme in the guide: once, or every airing as a
 * series pass with the usual options. Submits straight to the server.
 */
export default function RecordDialog({ channel, channelName, program, comskip, onClose, onDone }: Props) {
  const season = seasonOf(program.episodeNumber)
  const [mode, setMode] = useState<'once' | 'series'>('once')
  const [anyChannel, setAnyChannel] = useState(true)
  const [newOnly, setNewOnly] = useState(false)
  const [thisSeason, setThisSeason] = useState(false)
  const [keep, setKeep] = useState(0)
  const [deleteWatched, setDeleteWatched] = useState(false)
  const [padBefore, setPadBefore] = useState(1)
  const [padAfter, setPadAfter] = useState(3)
  const [commercials, setCommercials] = useState<Commercials>(comskip ? 'skip' : 'none')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      if (mode === 'once') {
        const r = await api.recordAiring(channel, program.start, { padBefore, padAfter, commercials })
        onDone(
          r.status === 'conflict'
            ? `Scheduled, but every tuner is busy at ${fmtWhen(r.start)}; it records if one frees up.`
            : r.status === 'recording'
              ? `Recording ${r.title} now.`
              : `${r.title} will record ${fmtWhen(r.start)}.`,
        )
      } else {
        const rule = await api.saveRule({
          title: program.title,
          seriesId: program.seriesId,
          channel: anyChannel ? '' : channel,
          newOnly,
          season: thisSeason ? season : 0,
          keep,
          deleteWatched,
          padBefore,
          padAfter,
          commercials,
        })
        onDone(`Pass created: ${rule.title} will record on ${anyChannel ? 'any channel' : channel}.`)
      }
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Record {program.title}</h2>
        <p className="muted">
          {channel} {channelName} · {fmtWhen(program.start)}
          {program.episodeNumber ? ` · ${program.episodeNumber}` : ''}
          {program.episodeTitle ? ` · ${program.episodeTitle}` : ''}
        </p>
        <div className="form-grid">
          <label>What</label>
          <div className="choice">
            <label>
              <input type="radio" checked={mode === 'once'} onChange={() => setMode('once')} /> Just this airing
            </label>
            <label>
              <input type="radio" checked={mode === 'series'} onChange={() => setMode('series')} /> Every episode (pass)
            </label>
          </div>
          {mode === 'series' && (
            <>
              <label>Channel</label>
              <div className="choice">
                <label>
                  <input type="radio" checked={anyChannel} onChange={() => setAnyChannel(true)} /> Any channel
                </label>
                <label>
                  <input type="radio" checked={!anyChannel} onChange={() => setAnyChannel(false)} /> Only {channel}
                </label>
              </div>
              <label>Episodes</label>
              <div className="choice">
                <label>
                  <input type="checkbox" checked={newOnly} onChange={(e) => setNewOnly(e.target.checked)} /> New episodes only
                </label>
                {season > 0 && (
                  <label>
                    <input type="checkbox" checked={thisSeason} onChange={(e) => setThisSeason(e.target.checked)} /> Only season{' '}
                    {season}
                  </label>
                )}
              </div>
              <label>Keep</label>
              <div className="choice">
                <select value={keep} onChange={(e) => setKeep(parseInt(e.target.value, 10))}>
                  <option value={0}>All recordings</option>
                  {[1, 3, 5, 10, 20].map((n) => (
                    <option key={n} value={n}>
                      Newest {n}
                    </option>
                  ))}
                </select>
                <label>
                  <input type="checkbox" checked={deleteWatched} onChange={(e) => setDeleteWatched(e.target.checked)} /> Delete a day after watching
                </label>
              </div>
            </>
          )}
          <label>Start early</label>
          <select value={padBefore} onChange={(e) => setPadBefore(parseInt(e.target.value, 10))}>
            {[0, 1, 2, 5, 10].map((n) => (
              <option key={n} value={n}>
                {n} min
              </option>
            ))}
          </select>
          <label>Run late</label>
          <select value={padAfter} onChange={(e) => setPadAfter(parseInt(e.target.value, 10))}>
            {[0, 1, 3, 5, 10, 30, 60].map((n) => (
              <option key={n} value={n}>
                {n} min
              </option>
            ))}
          </select>
          <label>Commercials</label>
          <select value={commercials} onChange={(e) => setCommercials(e.target.value as Commercials)} disabled={!comskip}>
            <option value="skip">Detect and skip during playback</option>
            <option value="remove">Detect and cut out of the file</option>
            <option value="none">Leave as broadcast</option>
          </select>
          {!comskip && (
            <>
              <span />
              <span className="muted" style={{ fontSize: 12 }}>
                comskip is not installed on the server, so commercials are kept.
              </span>
            </>
          )}
        </div>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button onClick={onClose}>Cancel</button>
          <button className="btn-rec" onClick={submit} disabled={busy}>
            {busy ? 'Saving…' : mode === 'once' ? '● Record' : '● Create pass'}
          </button>
        </div>
      </div>
    </div>
  )
}
