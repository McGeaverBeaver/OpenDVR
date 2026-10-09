import { autoDeviceName } from './device'

export interface Source {
  deviceId: string
  url: string
}

export interface Channel {
  guideNumber: string
  guideName: string
  hd: boolean
  drm: boolean
  videoCodec?: string
  audioCodec?: string
  favorite: boolean
  sources: Source[]
}

export interface Device {
  device: {
    FriendlyName: string
    ModelNumber: string
    FirmwareVersion: string
    DeviceID: string
    BaseURL: string
    TunerCount: number
  }
  host: string
  channelCount: number
  lastSeen: string
  lastError?: string
  discovered: boolean
}

export interface TunerStatus {
  Resource: string
  VctNumber?: string
  VctName?: string
  Frequency?: number
  SignalStrengthPercent?: number
  SignalQualityPercent?: number
  SymbolQualityPercent?: number
  TargetIP?: string
  NetworkRate?: number
}

export interface Viewer {
  id: string
  name?: string
  channel: string
  addr?: string
  user?: string
  userName?: string
  place?: string
  started: string
  lastSeen: string
  paused: boolean
}

export interface StreamStatus {
  channel: string
  deviceId: string
  encoder: string
  started: string
  lastAccess: string
  segments: number
  running: boolean
  error?: string
  logTail?: string[]
  viewers: Viewer[]
}

export interface Program {
  start: number
  end: number
  title: string
  episodeTitle?: string
  episodeNumber?: string
  synopsis?: string
  imageUrl?: string
  seriesId?: string
  originalAirdate?: number
  new?: boolean
  filter?: string[]
}

export interface GuideChannel {
  guideNumber: string
  guideName: string
  affiliate?: string
  imageUrl?: string
  programs: Program[]
}

export interface GuideWindow {
  from: number
  to: number
  channels: GuideChannel[]
}

export interface NowNext {
  now?: Program
  next?: Program
}

export interface GuideStatus {
  lastRefresh: string
  lastError?: string
  channels: number
  programs: number
  until: string
}

export interface AuthUser {
  sub: string
  name?: string
  email?: string
}

export interface Me {
  enabled: boolean
  user?: AuthUser
  /** Whether the caller holds the administrator role (always true with auth off). */
  admin?: boolean
  /** A kids profile: limited channels, cannot change the DVR. */
  restricted?: boolean
  favorites?: string[]
}

export interface Profile {
  sub: string
  name?: string
  email?: string
  favorites: string[]
  firstSeen: string
  lastSeen: string
  restricted: boolean
  allowed?: string[]
}

export interface AuditEvent {
  time: string
  kind: string
  user?: string
  name?: string
  ip?: string
  country?: string
  city?: string
  agent?: string
  detail?: string
  fields?: Record<string, string>
}

export interface AuditLog {
  geoip: boolean
  geoipFile: string
  adminGroup: string
  events: AuditEvent[]
}

export interface DeviceStart {
  handle: string
  userCode: string
  verificationUri: string
  verificationUriComplete?: string
  expiresIn: number
  interval: number
  qr: string
}

export interface DevicePoll {
  status: 'pending' | 'ok' | 'error'
  error?: string
  user?: AuthUser
}

export interface Download {
  name: string
  url: string
  size: number
  modified: string
  /** Came with this server's image (re-signed with the server's key). */
  builtIn: boolean
  signedBy?: string
}

export interface MCPSession {
  id: string
  clientId: string
  clientName: string
  user: AuthUser
  createdAt: string
  expiresAt: string
  lastUsedAt?: string
}

export interface MCPClient {
  clientId: string
  name: string
  redirectUris: string[]
  clientUri?: string
  createdAt: string
  lastUsedAt?: string
  sessions: MCPSession[]
}

export interface MCPInfo {
  mcpUrl: string
  oauth: boolean
  admin: boolean
  mcpGroup: string
  toolCount: number
}

export interface TunerUse {
  deviceId: string
  tuner: string
  channel: string
  name?: string
  client?: string
  shared: boolean
}

export interface WatchResult {
  viewerId: string
  channel: string
  deviceId: string
  encoder: string
  playlist: string
}

/** Thrown by api.watch when every tuner is locked. */
export class TunersBusyError extends Error {
  tuners: TunerUse[]
  constructor(tuners: TunerUse[]) {
    super('All tuners are busy')
    this.name = 'TunersBusyError'
    this.tuners = tuners
  }
}

// ---- DVR -------------------------------------------------------------------

export type Commercials = 'skip' | 'remove' | 'none'

export interface Rule {
  id: string
  title: string
  seriesId?: string
  channel?: string
  newOnly: boolean
  season?: number
  keep: number
  padBefore: number
  padAfter: number
  commercials: Commercials
  priority: number
  paused: boolean
  created: string
  deleteWatched?: boolean
  kind?: 'series' | 'manual'
  timeOfDay?: string
  durationMin?: number
  days?: number[]
  date?: string
}

export type RecordingStatus = 'scheduled' | 'conflict' | 'recording' | 'processing' | 'ready' | 'failed'

export interface Span {
  start: number
  end: number
}

export interface Recording {
  id: string
  ruleId?: string
  ruleTitle?: string
  channel: string
  channelName?: string
  title: string
  episodeTitle?: string
  episodeNumber?: string
  synopsis?: string
  seriesId?: string
  imageUrl?: string
  airing: number
  airingEnd: number
  start: number
  end: number
  manual?: boolean
  status: RecordingStatus
  error?: string
  file?: string
  sizeBytes?: number
  durationSec?: number
  commercials: Commercials
  skips?: Span[]
  cut?: boolean
  position?: number
  created: string
  finished?: string
  watched?: boolean
  watchedAt?: string
  /** The broadcast file kept for finding commercials again. */
  original?: string
  originalUntil?: string
  captions?: boolean
  playUrl?: string
  captionsUrl?: string
}

export interface DVRSummary {
  recordingsDir: string
  diskFreeBytes: number
  diskTotalBytes: number
  usedBytes: number
  comskip: boolean
  tunerCount: number
  rules: number
  counts: Partial<Record<RecordingStatus, number>>
  encoder: string
  lookaheadHours: number
  lastPlan: string
  planError?: string
  maxBytes?: number
  captions?: boolean
  notify?: string[]
  hdhrEmulate?: boolean
}

export interface SearchHit {
  channel: string
  channelName: string
  program: Program
}

export interface RecordOptions {
  padBefore?: number
  padAfter?: number
  commercials?: Commercials
}

function jsonInit(method: string, body: unknown): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

const VIEWER_KEY = 'opendvr.viewerId'
const NAME_KEY = 'opendvr.viewerName'

function stored(key: string): string {
  try {
    return localStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}

function store(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value)
    else localStorage.removeItem(key)
  } catch {
    /* private mode */
  }
}

/** The viewer handle this browser uses, once the server has issued one. */
export const viewer = {
  id: () => stored(VIEWER_KEY),
  setId: (id: string) => store(VIEWER_KEY, id),
  name: () => stored(NAME_KEY),
  setName: (name: string) => store(NAME_KEY, name.trim().slice(0, 40)),
}

export interface DiagCheck {
  name: string
  ok: boolean
  latency?: string
  detail: string
}

export interface DiagReport {
  host: string
  resolvedIps: string[] | null
  interfaces: { name: string; addrs: string[] }[] | null
  defaultGateway: string
  checks: DiagCheck[] | null
  hint: string
}

export interface SystemInfo {
  version: string
  uptime: string
  degraded: boolean
  hosts: string[] | null
  lastRefresh: string
  deviceCount: number
  channelCount: number
  guide?: GuideStatus | null
  ffmpeg: {
    ffmpegPath: string
    version: string
    hasQsv: boolean
    hasVaapi: boolean
    renderDevice: string
    renderPresent: boolean
    encoder: string
    requested: string
    hardwareError?: string
    driverPath?: string
  }
}

export interface StreamFailure {
  channel: string
  time: string
  encoder: string
  error: string
  logTail: string[] | null
  command?: string
}

// When the server says the session is gone, go to the sign-in page rather
// than showing an error on every panel. Guarded so overlapping requests
// only redirect once.
let redirecting = false
function toLogin() {
  if (redirecting || window.location.pathname === '/login') return
  redirecting = true
  const next = window.location.pathname + window.location.search
  window.location.assign(`/login?next=${encodeURIComponent(next)}`)
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  if (!res.ok) {
    let msg = res.statusText
    let code = ''
    try {
      const body = await res.json()
      if (body?.error) msg = body.error
      if (body?.code) code = body.code
    } catch {
      /* non-JSON error body */
    }
    if (res.status === 401 && code === 'unauthenticated') toLogin()
    throw new Error(msg)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export const api = {
  channels: () => request<Channel[]>('/api/channels'),
  devices: () => request<Device[]>('/api/devices'),
  refreshDevices: () => request<unknown>('/api/devices/refresh', { method: 'POST' }),
  deviceStatus: (id: string) => request<TunerStatus[]>(`/api/devices/${encodeURIComponent(id)}/status`),
  streams: () => request<StreamStatus[]>('/api/streams'),
  streamFailures: () => request<StreamFailure[]>('/api/streams/failures'),
  stopStream: (ch: string) => request<void>(`/api/streams/${encodeURIComponent(ch)}`, { method: 'DELETE' }),
  system: () => request<SystemInfo>('/api/system'),
  diagnostics: (host: string) => request<DiagReport>(`/api/diagnostics?host=${encodeURIComponent(host)}`),
  playlistUrl: (ch: string) => `/api/channels/${encodeURIComponent(ch)}/stream/index.m3u8`,
  viewers: () => request<Viewer[]>('/api/viewers'),
  audit: (kind = '', q = '', limit = 200) =>
    request<AuditLog>(`/api/audit?kind=${encodeURIComponent(kind)}&q=${encodeURIComponent(q)}&limit=${limit}`),
  /** Auth state. A 401 here is expected when signed out, so it is not redirected. */
  me: async (): Promise<Me> => {
    const res = await fetch('/api/me')
    if (res.status === 401) return { enabled: true }
    if (!res.ok) throw new Error(res.statusText)
    return res.json()
  },
  downloads: () => request<Download[]>('/api/downloads'),
  mcpInfo: () => request<MCPInfo>('/api/mcp/info'),
  mcpClients: () => request<MCPClient[]>('/api/mcp/clients'),
  deleteMcpClient: (id: string) => request<void>(`/api/mcp/clients/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  revokeMcpSession: (id: string) => request<void>(`/api/mcp/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  deviceStart: () => request<DeviceStart>('/auth/device/start', { method: 'POST' }),
  devicePoll: (handle: string) =>
    request<DevicePoll>('/auth/device/poll', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ handle }),
    }),
  guide: (from: number, to: number) => request<GuideWindow>(`/api/guide?from=${from}&to=${to}`),
  guideNow: () => request<Record<string, NowNext>>('/api/guide/now'),
  guideSearch: (q: string) => request<SearchHit[]>(`/api/guide/search?q=${encodeURIComponent(q)}`),
  dvr: () => request<DVRSummary>('/api/dvr'),
  rules: () => request<Rule[]>('/api/dvr/rules'),
  saveRule: (rule: Partial<Rule>) =>
    rule.id
      ? request<Rule>(`/api/dvr/rules/${encodeURIComponent(rule.id)}`, jsonInit('PUT', rule))
      : request<Rule>('/api/dvr/rules', jsonInit('POST', rule)),
  deleteRule: (id: string) => request<void>(`/api/dvr/rules/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  recordings: (status = '') => request<Recording[]>(`/api/dvr/recordings${status ? `?status=${status}` : ''}`),
  recording: (id: string) => request<Recording>(`/api/dvr/recordings/${encodeURIComponent(id)}`),
  recordAiring: (channel: string, airing: number, opts: RecordOptions = {}) =>
    request<Recording>('/api/dvr/recordings', jsonInit('POST', { channel, airing, ...opts })),
  recordNow: (channel: string, minutes = 0, opts: RecordOptions = {}) =>
    request<Recording>('/api/dvr/record-now', jsonInit('POST', { channel, minutes, ...opts })),
  deleteRecording: (id: string) => request<void>(`/api/dvr/recordings/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  redetect: (id: string, commercials?: string) =>
    request<Recording>(`/api/dvr/recordings/${encodeURIComponent(id)}/redetect`, jsonInit('POST', { commercials })),
  editRecording: (id: string, opts: RecordOptions) =>
    request<Recording>(`/api/dvr/recordings/${encodeURIComponent(id)}`, jsonInit('PATCH', opts)),
  setPosition: (id: string, position: number) =>
    fetch(`/api/dvr/recordings/${encodeURIComponent(id)}/position`, { ...jsonInit('PUT', { position }), keepalive: true }).catch(() => {}),
  setWatched: (id: string, watched: boolean) =>
    request<void>(`/api/dvr/recordings/${encodeURIComponent(id)}/position`, jsonInit('PUT', { position: 0, watched })),
  prefs: () => request<Profile>('/api/me/prefs'),
  setFavorites: (favorites: string[]) => request<Profile>('/api/me/prefs', jsonInit('PUT', { favorites })),
  users: () => request<Profile[]>('/api/users'),
  setUser: (sub: string, restricted: boolean, allowed: string[]) =>
    request<Profile>(`/api/users/${encodeURIComponent(sub)}`, jsonInit('PUT', { restricted, allowed })),
  backupUrl: '/api/admin/backup',
  restore: (file: File) =>
    fetch('/api/admin/restore', { method: 'POST', body: file }).then(async (res) => {
      const body = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(body.error ?? res.statusText)
      return body as { restored: string[]; note: string }
    }),
  guideChannel: (ch: string, to: number) => request<GuideChannel>(`/api/guide/${encodeURIComponent(ch)}?to=${to}`),
  /**
   * Join a channel. Blocks until the stream is ready. Rejects with
   * TunersBusyError when no tuner is free, carrying what they are tuned to.
   */
  watch: async (channel: string, signal?: AbortSignal): Promise<WatchResult> => {
    const res = await fetch('/api/watch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        channel,
        viewerId: viewer.id() || undefined,
        name: viewer.name() || undefined,
        client: await autoDeviceName(),
      }),
      signal,
    })
    if (res.status === 409) {
      const body = await res.json().catch(() => ({}))
      throw new TunersBusyError(body.tuners ?? [])
    }
    if (res.status === 401) toLogin()
    if (!res.ok) {
      let msg = res.statusText
      try {
        msg = (await res.json()).error ?? msg
      } catch {
        /* non-JSON */
      }
      throw new Error(msg)
    }
    const out = (await res.json()) as WatchResult
    viewer.setId(out.viewerId)
    return out
  },
  heartbeat: (id: string, paused: boolean) =>
    fetch(`/api/watch/${encodeURIComponent(id)}/heartbeat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paused }),
    }).then((res) => res.status !== 404),
  // sendBeacon survives page unloads and tab closes, which fetch does not.
  leave: (id: string) => {
    const url = `/api/watch/${encodeURIComponent(id)}/leave`
    if (!navigator.sendBeacon?.(url)) fetch(url, { method: 'POST', keepalive: true }).catch(() => {})
  },
}
