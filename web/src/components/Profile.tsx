import { useEffect, useState } from 'react'
import { viewer, type Me } from '../api'
import { autoDeviceName } from '../device'

/**
 * Profile: who is signed in, and what this device is called next to their
 * name on the Status page. The device name is automatic unless one is set
 * here; it is kept on this device only.
 */
export default function Profile({ me, onClose }: { me: Me; onClose: () => void }) {
  const [auto, setAuto] = useState('')
  const [name, setName] = useState(viewer.name())
  const [custom, setCustom] = useState(viewer.name() !== '')
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    autoDeviceName().then(setAuto)
  }, [])

  const save = (value: string) => {
    viewer.setName(value)
    setName(viewer.name())
    setCustom(viewer.name() !== '')
    setSaved(true)
  }
  const user = me.user

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Profile</h2>
        {user ? (
          <p className="muted">
            Signed in as <strong style={{ color: 'var(--text)' }}>{user.name || user.email || user.sub}</strong>
            {user.name && user.email ? ` · ${user.email}` : ''}
          </p>
        ) : (
          <p className="muted">Sign-in is off on this server.</p>
        )}
        <div className="form-grid">
          <label htmlFor="device-name">This device</label>
          <input
            id="device-name"
            type="text"
            value={name}
            maxLength={40}
            placeholder={auto || 'Automatic'}
            onChange={(e) => {
              setName(e.target.value)
              setSaved(false)
            }}
          />
        </div>
        <p className="muted" style={{ fontSize: 13 }}>
          Shown next to your name while you watch. Leave it empty and it is named automatically: by the name your home
          network knows it by, otherwise as {auto ? <strong>{auto}</strong> : 'its browser and system'}.
        </p>
        {saved && (
          <p style={{ color: 'var(--accent)', fontSize: 13 }}>
            Saved{custom ? '' : ': automatic'}. It shows from the next channel you open.
          </p>
        )}
        <div className="modal-actions">
          {custom && <button onClick={() => save('')}>Use automatic name</button>}
          <button onClick={() => save(name)}>Save</button>
          <button onClick={onClose}>Close</button>
        </div>
        {user && (
          <div className="modal-actions profile-signout">
            <a href="/auth/logout" className="btn">
              Sign out
            </a>
          </div>
        )}
      </div>
    </div>
  )
}
