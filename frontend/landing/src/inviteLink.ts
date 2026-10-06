import { isInvitePath } from './route'

const KEY = 'ascomply.inviteToken'
const TOKEN_RE = /^[A-Za-z0-9_-]{43}$/

// Exactly one `token` key with a 43-character value; anything else is no token.
function fragmentToken(hash: string): string | null {
  const entries = [...new URLSearchParams(hash.replace(/^#/, ''))]
  return entries.length === 1 && entries[0][0] === 'token' && TOKEN_RE.test(entries[0][1]) ? entries[0][1] : null
}

// Reads the token from the fragment, keeps it for this tab and strips the fragment from the address bar.
export function captureInviteToken(
  loc: Pick<Location, 'pathname' | 'search' | 'hash'>,
  hist: Pick<History, 'replaceState'>,
  storage: Pick<Storage, 'getItem' | 'setItem'>,
): string | null {
  if (!isInvitePath(loc.pathname)) return null
  const fresh = fragmentToken(loc.hash)
  let stored: string | null = null
  try {
    if (fresh) storage.setItem(KEY, fresh)
    else stored = storage.getItem(KEY)
  } catch {
    console.warn('invite token storage is unavailable')
  }
  if (loc.hash !== '') {
    try {
      hist.replaceState(null, '', loc.pathname + loc.search)
    } catch {
      console.warn('invite link fragment could not be removed')
    }
  }
  if (fresh) return fresh
  return stored !== null && TOKEN_RE.test(stored) ? stored : null
}

let captured: string | null = null
try {
  captured = captureInviteToken(location, history, sessionStorage)
} catch {
  console.warn('invite link capture failed')
}

export const inviteToken = (): string | null => captured
