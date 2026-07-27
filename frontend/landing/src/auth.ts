// Landing sign-in personas + cross-SPA routing (task-21). The full mock flow —
// persona picker → 6-digit OTP → route to the workspace the role is allowed to open —
// is a faithful port of the sign-in prototype. Routing is plain navigation to the
// already-deployed sibling SPA; no backend call happens here. The destination Platform
// app performs the real JWT mint + /v1/me round trip on arrival (M2-13), so the two
// stories share one mechanism.

// The demo one-time code, matching the prototype. Client-side theater only.
export const DEMO_CODE = '481920'

export interface LandingPersona {
  id: 'support' | 'firm' | 'inhouse'
  name: string
  title: string
  org: string
  email: string
  initials: string
  access: string
  destLabel: string
  target: 'app' | 'ops'
  avBg: string
  avColor: string
}

export const LANDING_PERSONAS: LandingPersona[] = [
  {
    id: 'support',
    name: 'Amara Okoye',
    title: 'Support officer',
    org: 'ASComply Operations',
    email: 'a.okoye@ascomply.com',
    initials: 'AO',
    access: 'OPS CONSOLE',
    destLabel: 'Ops Console',
    target: 'ops',
    avBg: 'var(--slate-900)',
    avColor: 'var(--text-on-dark)',
  },
  {
    id: 'firm',
    name: 'Chinedu Okafor',
    title: 'Firm accountant',
    org: 'Okafor & Partners',
    email: 'c.okafor@okafor.ng',
    initials: 'CO',
    access: 'PLATFORM · FIRM',
    destLabel: 'firm workspace',
    target: 'app',
    avBg: 'var(--action-tint)',
    avColor: 'var(--action)',
  },
  {
    id: 'inhouse',
    name: 'Ngozi Balogun',
    title: 'In-house accountant',
    org: 'Honeywell Group · Finance',
    email: 'n.balogun@honeywell.ng',
    initials: 'NB',
    access: 'PLATFORM · IN-HOUSE',
    destLabel: 'in-house workspace',
    target: 'app',
    avBg: 'var(--action-tint)',
    avColor: 'var(--action)',
  },
]

// Mirrors gatewayBase()'s null-when-unset contract (@invoice-os/api-client/client,
// C8b/C8c): each PR now deploys to its own ephemeral Railway environment with an
// unpredictable domain suffix (M4-23), so a hardcoded dev-deploy fallback would silently
// route a sign-in to the wrong environment. Return null rather than defaulting.
const resolveBase = (v: string | undefined): string | null => {
  const trimmed = (v ?? '').trim().replace(/\/+$/, '')
  return trimmed || null
}
const appBase = () => resolveBase(import.meta.env.VITE_APP_URL)
const opsBase = () => resolveBase(import.meta.env.VITE_OPS_URL)

// destUrl is the SPA the persona's role may open. BOTH targets now carry ?persona=<id>:
// the Platform app auto-signs-in that persona (reusing M2-13's mint + /me path), and the
// Ops Console records it as its sign-in. The console used to be opened with a bare
// navigation, which is why it had no way to tell a signed-in visitor from a stranger with
// the URL — it now refuses to render without one and sends you back here.
// (Its VERIFIED token consumption still arrives at M7; this is routing, not enforcement.)
// Returns null — the documented unconfigured path — when the target SPA's URL isn't set;
// callers must not navigate on null.
export function destUrl(p: LandingPersona): string | null {
  const base = p.target === 'ops' ? opsBase() : appBase()
  return base ? `${base}?persona=${p.id}` : null
}

// maskedEmail hides the local part except its first character, e.g. c•••@okafor.ng.
export function maskedEmail(email: string): string {
  return email.replace(/^(.).*(@.*)$/, '$1•••$2')
}
