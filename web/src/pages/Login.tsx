import { useEffect, useRef, useState } from 'react'
import { api, type DeviceStart } from '../api'

interface Props {
  /** Where to go after signing in. */
  next: string
}

/**
 * Sign-in page shown whenever auth is on and there is no session.
 *
 * Two ways in: the normal redirect to the identity provider, and the
 * device flow for TVs: the page shows a QR code and a short code, the
 * viewer approves on their phone, and the page polls until the server
 * has set its session cookie.
 */
export default function Login({ next }: Props) {
  const [device, setDevice] = useState<DeviceStart | null>(null)
  const [deviceError, setDeviceError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)
  const pollTimer = useRef<number | null>(null)

  const signIn = () => {
    window.location.assign(`/auth/login?next=${encodeURIComponent(next)}`)
  }

  const startDevice = async () => {
    setStarting(true)
    setDeviceError(null)
    try {
      setDevice(await api.deviceStart())
    } catch (e) {
      setDeviceError((e as Error).message)
    } finally {
      setStarting(false)
    }
  }

  useEffect(() => {
    if (!device) return
    let cancelled = false
    const tick = async () => {
      if (cancelled) return
      try {
        const r = await api.devicePoll(device.handle)
        if (cancelled) return
        if (r.status === 'ok') {
          window.location.assign(next)
          return
        }
        if (r.status === 'error') {
          setDeviceError(r.error ?? 'Sign-in failed')
          setDevice(null)
          return
        }
      } catch (e) {
        if (cancelled) return
        setDeviceError((e as Error).message)
        setDevice(null)
        return
      }
      pollTimer.current = window.setTimeout(tick, Math.max(2, device.interval) * 1000)
    }
    pollTimer.current = window.setTimeout(tick, Math.max(2, device.interval) * 1000)
    return () => {
      cancelled = true
      if (pollTimer.current) window.clearTimeout(pollTimer.current)
    }
  }, [device, next])

  return (
    <section className="login">
      <div className="login-card">
        <div className="brand">
          <img src="/favicon.svg" alt="" className="brand-mark" /> OpenDVR
        </div>
        <h1>Sign in</h1>
        <p className="muted">This server is private. Sign in with your account to watch.</p>
        <button className="primary" onClick={signIn}>
          Sign in
        </button>

        <h2>On a TV? Use your phone</h2>
        {!device && (
          <button onClick={startDevice} disabled={starting}>
            {starting ? 'Getting a code…' : 'Show a QR code'}
          </button>
        )}
        {device && (
          <div className="device-login">
            <img src={device.qr} alt="QR code to sign in" className="qr" />
            <p>
              Scan the code, or open <strong>{device.verificationUri.replace(/^https?:\/\//, '')}</strong> on your phone and enter
            </p>
            <div className="user-code">{device.userCode}</div>
            <p className="muted">Waiting for approval… this page signs in on its own once you approve.</p>
          </div>
        )}
        {deviceError && <p className="error">{deviceError}</p>}
      </div>
    </section>
  )
}
