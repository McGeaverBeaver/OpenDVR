import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { api, type Me } from './api'
import Login from './pages/Login'
import Profile from './components/Profile'
import { openInstalledApp, useInstall } from './install'

function initials(name: string): string {
  const parts = name.replace(/@.*/, '').split(/[\s._-]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '?') + (parts.length > 1 ? parts[parts.length - 1][0] : '')).toUpperCase()
}

/** Safari on iPhone and iPad cannot be prompted; this says where the button is. */
function IOSInstallHelp({ onClose }: { onClose: () => void }) {
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Install OpenDVR</h2>
        <ol className="install-steps">
          <li>
            Tap the <strong>Share</strong> button in Safari&apos;s toolbar.
          </li>
          <li>
            Choose <strong>Add to Home Screen</strong>, then <strong>Add</strong>.
          </li>
        </ol>
        <p className="muted" style={{ fontSize: 13 }}>
          OpenDVR then opens full screen from its icon, like any other app.
        </p>
        <div className="modal-actions">
          <button onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  )
}


export default function App() {
  const { pathname, search } = useLocation()
  const watching = pathname.startsWith('/watch/') || pathname.startsWith('/recordings/')
  const install = useInstall()
  const [iosHelp, setIOSHelp] = useState(false)
  const [me, setMe] = useState<Me | null>(null)
  const [meError, setMeError] = useState<string | null>(null)
  const [profile, setProfile] = useState(false)

  // A link into the site opens the Android app when it is installed.
  useEffect(() => {
    openInstalledApp(pathname)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    api
      .me()
      .then(setMe)
      .catch((e: Error) => setMeError(e.message))
  }, [])

  if (meError) {
    return (
      <div className="app">
        <main>
          <p className="error">Cannot reach the server: {meError}</p>
        </main>
      </div>
    )
  }
  if (!me) {
    return (
      <div className="app">
        <main>
          <p className="muted">Loading…</p>
        </main>
      </div>
    )
  }
  if (me.enabled && !me.user) {
    const next = pathname === '/login' ? new URLSearchParams(search).get('next') || '/' : pathname + search
    return (
      <div className="app">
        <main>
          <Login next={next} />
        </main>
      </div>
    )
  }

  return (
    <div className={watching ? 'app app-watch' : 'app'}>
      {!watching && (
        <header className="topbar">
          <NavLink to="/" className="brand">
            <img src="/favicon.svg" alt="" className="brand-mark" /> OpenDVR
          </NavLink>
          <nav>
            <NavLink to="/" end>
              Channels
            </NavLink>
            <NavLink to="/guide">Guide</NavLink>
            <NavLink to="/dvr">DVR</NavLink>
            <NavLink to="/status">Status</NavLink>
            {me.admin && <NavLink to="/admin">Admin</NavLink>}
          </nav>
          <div className="topbar-right">
            {install.kind && (
              <button
                className="install"
                onClick={async () => setIOSHelp(await install.run())}
                title={install.title}
              >
                {install.label}
              </button>
            )}
            {me.enabled && me.user && (
              <span className="user">
                <button className="link-btn" onClick={() => setProfile(true)} title="Profile and this device's name">
                  <span className="user-name">{me.user.name || me.user.email || me.user.sub}</span>
                  <span className="avatar" aria-label="Profile">
                    {initials(me.user.name || me.user.email || me.user.sub)}
                  </span>
                </button>
                <a href="/auth/logout" className="btn sign-out">
                  Sign out
                </a>
              </span>
            )}
            {!me.enabled && (
              <button className="link-btn" onClick={() => setProfile(true)} title="This device's name">
                This device
              </button>
            )}
          </div>
        </header>
      )}
      <main>
        <Outlet />
      </main>
      {profile && <Profile me={me} onClose={() => setProfile(false)} />}
      {iosHelp && <IOSInstallHelp onClose={() => setIOSHelp(false)} />}
    </div>
  )
}
