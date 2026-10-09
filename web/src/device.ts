// The name a device gets when nobody has named it. Web pages cannot read a
// computer's or phone's own name, so this is the browser and the system,
// with the phone model where the browser offers it (Chrome on Android
// does). On the home network the server prefers the name the router knows
// the device by; a name set in the profile beats both.

interface UAData {
  platform?: string
  mobile?: boolean
  brands?: { brand: string; version: string }[]
  getHighEntropyValues?: (hints: string[]) => Promise<{ model?: string }>
}

function browserName(ua: string, data?: UAData): string {
  if (/Edg(A|iOS)?\//.test(ua)) return 'Edge'
  if (/OPR\//.test(ua)) return 'Opera'
  if (/SamsungBrowser\//.test(ua)) return 'Samsung Internet'
  if (/Firefox\/|FxiOS\//.test(ua)) return 'Firefox'
  if (/CriOS\/|Chrome\//.test(ua)) return (data?.brands ?? []).some((b) => b.brand === 'Brave') ? 'Brave' : 'Chrome'
  if (/Safari\//.test(ua)) return 'Safari'
  return 'Browser'
}

function systemName(ua: string, data?: UAData): string {
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) return 'iPad'
  if (/Android/.test(ua)) return 'Android'
  if (/CrOS/.test(ua)) return 'ChromeOS'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Macintosh|Mac OS X/.test(ua)) return 'Mac'
  if (data?.platform) return data.platform
  if (/Linux/.test(ua)) return 'Linux'
  return 'this device'
}

async function detect(): Promise<string> {
  const ua = navigator.userAgent
  const data = (navigator as Navigator & { userAgentData?: UAData }).userAgentData
  const browser = browserName(ua, data)
  let model = ''
  try {
    if (data?.mobile && data.getHighEntropyValues) model = (await data.getHighEntropyValues(['model'])).model ?? ''
  } catch {
    /* the browser declined */
  }
  return model ? `${model} · ${browser}` : `${browser} on ${systemName(ua, data)}`
}

let cached: Promise<string> | null = null

/** "Chrome on Windows", "Safari on iPhone", "Pixel 8 · Chrome". */
export function autoDeviceName(): Promise<string> {
  if (!cached) cached = detect().catch(() => 'Browser')
  return cached
}
