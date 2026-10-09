// The "Install app" button: the right app for the device in one tap.
//
//  - Android: the TV app's APK from this server. In Chrome-based browsers
//    the tap is an intent:// link, so Android opens the app when it is
//    installed and Chrome downloads the APK (the link's fallback) when not.
//  - iPhone and iPad: Safari has no install prompt, so the button explains
//    Share, Add to Home Screen.
//  - Everything else: the browser's own PWA install prompt, when it offers
//    one (Chrome, Edge). No prompt, no button.
//
// Inside the installed app (PWA or a home screen icon) there is nothing to
// offer, so the button hides.
import { useEffect, useState } from 'react'
import { api } from './api'

interface InstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

export type InstallKind = 'apk' | 'ios' | 'pwa'

// Set once Android handed an intent:// link to the app (the page went to
// the background straight after the tap), so this browser knows the app
// is on the phone.
const APP_KEY = 'opendvr.androidApp'
// Once per tab: the app was offered on page load already.
const AUTO_KEY = 'opendvr.appAutoOpened'

const ua = navigator.userAgent
export const isAndroid = /Android/i.test(ua)
const isIOS = /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)
// Browsers that follow intent:// links with a fallback URL. Firefox and
// Amazon's Silk get the plain download instead.
const intentCapable = isAndroid && /Chrome\//.test(ua) && !/Firefox\/|Silk\//.test(ua)

function standalone(): boolean {
  return (
    window.matchMedia('(display-mode: standalone)').matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true
  )
}

function store(key: string, value?: string, session = false): string | null {
  try {
    const s = session ? sessionStorage : localStorage
    if (value !== undefined) s.setItem(key, value)
    return s.getItem(key)
  } catch {
    return null
  }
}

// Pages the TV app has a screen for. Admin and Status stay on the web.
const appPaths = /^\/($|watch\/|guide$|dvr$|recordings\/)/

/**
 * opendvr://open/<path> as an intent:// link for the TV app. The app reads
 * the path (/watch/57.1 plays that channel). With a fallback Chrome goes
 * there when the app is missing. Without one the package is left out too:
 * a package and no fallback sends Chrome to the Play Store, while the bare
 * scheme does nothing when no app claims it.
 */
function intentURL(path: string, fallback?: string): string {
  let url = `intent://open${path}#Intent;scheme=opendvr;`
  if (fallback) url += `package=com.opendvr.tv;S.browser_fallback_url=${encodeURIComponent(fallback)};`
  return url + 'end'
}

// The app opened if the page lost focus right after the tap.
function noteIfAppOpens() {
  const onHide = () => {
    if (document.visibilityState === 'hidden') store(APP_KEY, 'installed')
  }
  document.addEventListener('visibilitychange', onHide)
  setTimeout(() => document.removeEventListener('visibilitychange', onHide), 2500)
}

/**
 * On Android, a link into this site opens the app instead when this browser
 * has seen the app open before. Once per tab, and only for pages the app
 * has, so coming back to the browser leaves you on the website.
 */
export function openInstalledApp(pathname: string) {
  if (!intentCapable || standalone() || store(APP_KEY) !== 'installed') return
  if (!appPaths.test(pathname) || store(AUTO_KEY, undefined, true)) return
  store(AUTO_KEY, '1', true)
  // No fallback: if the app was uninstalled since, nothing happens.
  window.location.href = intentURL(pathname)
}

export function useInstall() {
  const [prompt, setPrompt] = useState<InstallPromptEvent | null>(null)
  const [apk, setApk] = useState<string | null>(null)
  const [installed, setInstalled] = useState(standalone())

  useEffect(() => {
    const onPrompt = (e: Event) => {
      e.preventDefault()
      setPrompt(e as InstallPromptEvent)
    }
    const onInstalled = () => {
      setPrompt(null)
      setInstalled(true)
    }
    window.addEventListener('beforeinstallprompt', onPrompt)
    window.addEventListener('appinstalled', onInstalled)
    return () => {
      window.removeEventListener('beforeinstallprompt', onPrompt)
      window.removeEventListener('appinstalled', onInstalled)
    }
  }, [])

  useEffect(() => {
    if (!isAndroid || installed) return
    api
      .downloads()
      .then((ds) => {
        const apks = ds.filter((d) => d.name.endsWith('.apk'))
        const pick = apks.find((d) => d.builtIn) ?? apks[0]
        if (pick) setApk(pick.url)
      })
      .catch(() => {})
  }, [installed])

  let kind: InstallKind | null = null
  if (installed) kind = null
  else if (apk) kind = 'apk'
  else if (prompt) kind = 'pwa'
  else if (isIOS) kind = 'ios'

  const appKnown = kind === 'apk' && intentCapable && store(APP_KEY) === 'installed'

  /** Runs the install. Returns true when the caller should show the iOS steps. */
  const run = async (): Promise<boolean> => {
    if (kind === 'apk' && apk) {
      const download = new URL(apk, window.location.origin).href
      if (intentCapable) {
        noteIfAppOpens()
        const path = appPaths.test(window.location.pathname) ? window.location.pathname : '/'
        window.location.href = intentURL(path, download)
      } else {
        window.location.href = download
      }
      return false
    }
    if (kind === 'pwa' && prompt) {
      await prompt.prompt()
      const { outcome } = await prompt.userChoice
      if (outcome === 'accepted') setPrompt(null)
      return false
    }
    return kind === 'ios'
  }

  return {
    kind,
    label: appKnown ? 'Open app' : 'Install app',
    title:
      kind === 'apk'
        ? appKnown
          ? 'Open the OpenDVR app'
          : 'Open the OpenDVR app, or download it if it is not installed'
        : 'Add OpenDVR to your home screen or desktop',
    run,
  }
}
