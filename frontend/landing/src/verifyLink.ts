import { appBase, handoffTarget } from './auth'

const TOKEN_RE = /^[A-Za-z0-9_-]{1,256}$/
const CODE_RE = /^[A-Za-z0-9_-]{43}$/

type Loc = Pick<Location, 'pathname' | 'search' | 'hash'>

// `strip` is the address-bar URL to leave behind; `target` is where to forward, or null to render.
export function verifyForward(loc: Loc, app: string | null): { strip: string; target: string | null; failed: boolean } {
  const params = new URLSearchParams(loc.search)
  const same = { strip: loc.pathname + loc.search + loc.hash, target: null, failed: false }

  if (params.has('confirm')) {
    const entries = [...new URLSearchParams(loc.hash.replace(/^#/, ''))]
    const token = entries.length === 1 && entries[0][0] === 'token' ? entries[0][1] : ''
    const kinds = params.getAll('confirm')
    // `invite` is the Location literal pinned by the gateway's verify_page_test.go.
    const kind = kinds.length === 1 && (kinds[0] === '1' || kinds[0] === 'invite') ? kinds[0] : ''
    const valid = kind !== '' && TOKEN_RE.test(token)
    params.delete('confirm')
    if (valid && app) {
      return { strip: loc.pathname + (params.size ? `?${params}` : ''), target: `${app}?auth=${kind === 'invite' ? 'verify-invite' : 'verify'}#token=${encodeURIComponent(token)}`, failed: false }
    }
    params.set('verify', 'failed')
    return { strip: `${loc.pathname}?${params}`, target: null, failed: true }
  }

  if (params.has('handoff')) {
    const codes = params.getAll('handoff')
    const ok = codes.length === 1 && CODE_RE.test(codes[0]) && params.getAll('verified').join() === '1'
    params.delete('handoff')
    const strip = loc.pathname + (params.size ? `?${params}` : '') + loc.hash
    return { strip, target: ok && app ? handoffTarget(app, codes[0]) : null, failed: false }
  }

  return same
}

let forwardingNow = false
try {
  const plan = verifyForward(location, appBase())
  if (plan.strip !== location.pathname + location.search + location.hash) history.replaceState(null, '', plan.strip)
  if (plan.target) {
    forwardingNow = true
    location.replace(plan.target)
  }
} catch {
  console.warn('verify link forward failed')
}

export const forwarding = forwardingNow
