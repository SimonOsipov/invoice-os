import { parseBlogPath } from './route'

const KEY = 'ascomply.blogConfirmToken'
const TOKEN_RE = /^[A-Za-z0-9_-]{43}$/

// Exactly one `token` key with a 43-character value; anything else is no token.
function queryToken(search: string): string | null {
  const entries = [...new URLSearchParams(search)]
  return entries.length === 1 && entries[0][0] === 'token' && TOKEN_RE.test(entries[0][1]) ? entries[0][1] : null
}

// Reads the token from the query, keeps it for this tab and strips the query from the address bar.
export function captureConfirmToken(
  loc: Pick<Location, 'pathname' | 'search' | 'hash'>,
  hist: Pick<History, 'replaceState'>,
  storage: Pick<Storage, 'getItem' | 'setItem'>,
): string | null {
  if (parseBlogPath(loc.pathname).view !== 'confirm') return null
  const fresh = queryToken(loc.search)
  let stored: string | null = null
  try {
    if (fresh) storage.setItem(KEY, fresh)
    else stored = storage.getItem(KEY)
  } catch {
    console.warn('confirm token storage is unavailable')
  }
  if (loc.search !== '' || loc.hash !== '') {
    try {
      hist.replaceState(null, '', loc.pathname)
    } catch {
      console.warn('confirm link query could not be removed')
    }
  }
  if (fresh) return fresh
  return stored !== null && TOKEN_RE.test(stored) ? stored : null
}

let captured: string | null = null
try {
  // Storage is read lazily so a throwing getter (Safari, storage blocked) cannot skip the strip.
  captured = captureConfirmToken(location, history, {
    getItem: (k) => sessionStorage.getItem(k),
    setItem: (k, v) => sessionStorage.setItem(k, v),
  })
} catch {
  console.warn('confirm link capture failed')
}

export const confirmToken = (): string | null => captured

export function clearConfirmToken(): void {
  captured = null
  try {
    sessionStorage.removeItem(KEY)
  } catch {
    console.warn('confirm token storage is unavailable')
  }
}
