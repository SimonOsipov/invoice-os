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
    org: 'InvoiceOS Operations',
    email: 'a.okoye@invoiceos.africa',
    initials: 'AO',
    access: 'OPS CONSOLE',
    destLabel: 'Ops Console',
    target: 'ops',
    avBg: 'var(--slate-900)',
    avColor: '#fff',
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
    avBg: 'var(--accent-tint)',
    avColor: 'var(--accent)',
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
    avBg: 'var(--accent-tint)',
    avColor: 'var(--accent)',
  },
]

const trim = (s: string) => s.trim().replace(/\/+$/, '')
const appBase = () => trim(import.meta.env.VITE_APP_URL ?? 'https://app-development-3b4b.up.railway.app')
const opsBase = () => trim(import.meta.env.VITE_OPS_URL ?? 'https://ops-console-development.up.railway.app')

// destUrl is the SPA the persona's role may open. The Platform app gets ?persona=<id>
// so it auto-signs-in that persona (reusing M2-13's mint + /me path); the Ops Console
// is opened directly (its token consumption arrives at M7).
export function destUrl(p: LandingPersona): string {
  if (p.target === 'ops') return opsBase()
  return `${appBase()}?persona=${p.id}`
}

// maskedEmail hides the local part except its first character, e.g. c•••@okafor.ng.
export function maskedEmail(email: string): string {
  return email.replace(/^(.).*(@.*)$/, '$1•••$2')
}
