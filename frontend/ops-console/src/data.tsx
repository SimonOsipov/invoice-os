// All Developer Console seed content, re-authored from the prototype's
// support.js state/seed methods (seedJobs, apiKeys, webhooks, …) as typed,
// static TS constants. Glyphs are pre-built <Icon> nodes so section components
// stay pure layout.

import type { ReactNode } from 'react'
import { buildEvidenceBundles, computeBillLine, naira, nairaC, SCALE_PLAN, spendTotals, type EvidenceBundle } from './charts'
import { Icon } from './icons'
import type { ApiKey, ApiRequest, BillItem, Delivery, Env, InvoiceKind, InvoiceStatus, Job, JobState, PastInvoice, Quota, RateLimit, Screen, Webhook } from './types'

/* ------------------------------------------------------------------ */
/* Common icon glyphs (this.g(paths, size) in the prototype)           */
/* ------------------------------------------------------------------ */

export const SEARCH_ICON = <Icon paths={['M21 21l-4.35-4.35', 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z']} size={16} />
export const CHEVRON_RIGHT_ICON = <Icon paths={['m9 18 6-6-6-6']} size={14} />
export const CLOSE_ICON = <Icon paths={['M18 6 6 18M6 6l12 12']} size={15} />
export const ALERT_ICON = <Icon paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']} size={18} />
export const LOCK_ICON = <Icon paths={['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']} size={15} />
// Prototype's rotateGlyph — the same path array is used for the re-drive
// action and the API-key rotate action (proto:866).
export const REDRIVE_ICON = <Icon paths={['M21 2v6h-6', 'M3 12a9 9 0 0 1 15-6.7L21 8', 'M3 22v-6h6', 'M21 12a9 9 0 0 1-15 6.7L3 16']} size={15} />
export const COPY_ICON = <Icon paths={['M20 9H11a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-9a2 2 0 0 0-2-2Z', 'M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1']} size={15} />
// Prototype defines downloadGlyph and exportGlyph as the same path array
// (proto:864) — one const covers both.
export const EXPORT_ICON = <Icon paths={['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3']} size={15} />
export const CHECK_ICON = <Icon paths={['M20 6 9 17l-5-5']} size={16} />
export const SHIELD_ICON = <Icon paths={['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z', 'm9 12 2 2 4-4']} size={13} />
export const EYE_ICON = <Icon paths={['M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z', 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z']} size={15} />
export const EYE_OFF_ICON = (
  <Icon
    paths={['m2 2 20 20', 'M6.7 6.7C3.9 8.3 2 12 2 12s3.5 7 10 7c1.9 0 3.6-.5 5-1.3', 'M9.9 5.1A9.8 9.8 0 0 1 12 5c6.5 0 10 7 10 7a17 17 0 0 1-2.2 3.1']}
    size={15}
  />
)
export const LINK_ICON = <Icon paths={['M9 17H7A5 5 0 0 1 7 7h2', 'M15 7h2a5 5 0 1 1 0 10h-2', 'M8 12h8']} size={16} />
export const PLUS_ICON = <Icon paths={['M12 5v14', 'M5 12h14']} size={15} />
export const ARROW_UP_ICON = <Icon paths={['M12 19V5', 'm5 12 7-7 7 7']} size={13} />
export const ARROW_DOWN_ICON = <Icon paths={['M12 5v14', 'm19 12-7 7-7-7']} size={13} />
export const GEAR_ICON = (
  <Icon
    paths={[
      'M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2Z',
      'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z',
    ]}
    size={16}
  />
)

// proto:1204-1213 — the evidence bundle's QR mark. Three 16x16 finder squares plus
// five 6x6 dots, all hand-placed: it is decorative, not a generated block matrix, so
// there is no .map here. Byte-identical for all 8 bundles, hence one shared node
// rather than a per-row `qr` field. The #fff values stay literal on purpose — CSS
// var() does not resolve inside SVG presentation attributes.
export const EVIDENCE_QR = (
  <svg width={64} height={64} viewBox="0 0 64 64" fill="none" aria-hidden="true">
    <rect x={6} y={6} width={16} height={16} rx={2} stroke="#fff" strokeWidth={3} />
    <rect x={42} y={6} width={16} height={16} rx={2} stroke="#fff" strokeWidth={3} />
    <rect x={6} y={42} width={16} height={16} rx={2} stroke="#fff" strokeWidth={3} />
    <rect x={30} y={30} width={6} height={6} fill="#fff" />
    <rect x={42} y={30} width={6} height={6} fill="#fff" />
    <rect x={30} y={42} width={6} height={6} fill="#fff" />
    <rect x={42} y={42} width={6} height={6} fill="#fff" />
    <rect x={52} y={52} width={6} height={6} fill="#fff" />
  </svg>
)

/* ------------------------------------------------------------------ */
/* Sidebar nav (prototype lines 838–843)                               */
/* ------------------------------------------------------------------ */

export type NavItem = { key: Screen; label: string; glyph: ReactNode }

export const NAV_ITEMS: NavItem[] = [
  { key: 'overview', label: 'Overview', glyph: <Icon paths={['M3 3h8v8H3z', 'M13 3h8v5h-8z', 'M13 12h8v9h-8z', 'M3 15h8v6H3z']} size={17} /> },
  { key: 'submissions', label: 'Submissions', glyph: <Icon paths={['M3 12h4l2 5 4-12 2 7h6']} size={17} /> },
  {
    key: 'evidence',
    label: 'Evidence',
    glyph: <Icon paths={['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z', 'm9 12 2 2 4-4']} size={17} />,
  },
  { key: 'api', label: 'API & webhooks', glyph: <Icon paths={['m18 16 4-4-4-4', 'm6 8-4 4 4 4', 'm14.5 4-5 16']} size={17} /> },
  { key: 'billing', label: 'Usage & billing', glyph: <Icon paths={['M2 5h20v14H2z', 'M2 10h20']} size={17} /> },
  { key: 'status', label: 'Status', glyph: <Icon paths={['M3 12h4l2-7 4 14 2-7h6']} size={17} /> },
]

// The crumb intentionally differs from the nav label (and from the screen <h1>)
// on `evidence` and `status` — ported verbatim from prototype line 849.
export const CRUMB_BY_SCREEN: Record<Screen, string> = {
  overview: 'Overview',
  submissions: 'Submissions',
  evidence: 'Compliance evidence',
  api: 'API & webhooks',
  billing: 'Usage & billing',
  status: 'API status',
}

/* ------------------------------------------------------------------ */
/* Submissions — jobs                                                  */
/* ------------------------------------------------------------------ */

// proto:785-794. Client-facing submissions: `buyer`/`btin`/`raw`/`desc`/`latency`
// replace the operator-era `tenant`/`tin`/`app`. `btin` carries no `TIN ` prefix.
export const SEED_SUBMISSIONS: Job[] = [
  { id: 'sub_9f2a91', buyer: 'Konga Online Ltd', btin: '20184412-0001', invoice: 'ZP-INV-0088412', raw: 4120000, desc: 'Marketplace settlement', state: 'accepted', attempts: 1, lastError: '—', age: '2m', latency: '1.6s' },
  { id: 'sub_9f2a72', buyer: 'Bolt Nigeria', btin: '19847720-0001', invoice: 'ZP-INV-0088410', raw: 918500, desc: 'Ride commission', state: 'submitting', attempts: 1, lastError: '—', age: '3m', latency: '—' },
  { id: 'sub_9f2a55', buyer: 'ShopRite NG', btin: '22310984-0001', invoice: 'ZP-INV-0088402', raw: 2740000, desc: 'POS settlement', state: 'pending', attempts: 2, lastError: 'Awaiting FIRS clearance', age: '11m', latency: '—' },
  { id: 'sub_9f29d1', buyer: 'Jumia Foods', btin: '20991043-0001', invoice: 'ZP-INV-0088388', raw: 663200, desc: 'Vendor payout', state: 'rejected', attempts: 3, lastError: 'MBS-422 buyer TIN not registered', age: '24m', latency: '2.1s' },
  { id: 'sub_9f29a8', buyer: 'MTN Nigeria', btin: '18772300-0001', invoice: 'ZP-INV-0088371', raw: 15400000, desc: 'Airtime bulk settlement', state: 'dead-letter', attempts: 5, lastError: 'FIRS 503 — gateway timeout (x5)', age: '1h 12m', latency: '—' },
  { id: 'sub_9f2987', buyer: 'GTBank Merchant Svcs', btin: '21004552-0001', invoice: 'ZP-INV-0088355', raw: 8730000, desc: 'Card settlement', state: 'failed', attempts: 4, lastError: 'Schema: lines[2].description missing', age: '2h 03m', latency: '—' },
  { id: 'sub_9f2961', buyer: 'Chowdeck Ltd', btin: '20554418-0001', invoice: 'ZP-INV-0088340', raw: 412700, desc: 'Delivery commission', state: 'accepted', attempts: 1, lastError: '—', age: '2h', latency: '1.5s' },
  { id: 'sub_9f2944', buyer: 'Konga Online Ltd', btin: '20184412-0001', invoice: 'ZP-INV-0088331', raw: 1240000, desc: 'Marketplace settlement', state: 'queued', attempts: 0, lastError: '—', age: '6s', latency: '—' },
  { id: 'sub_9f2930', buyer: 'Piggyvest', btin: '22887301-0001', invoice: 'ZP-INV-0088320', raw: 305000, desc: 'Savings payout fee', state: 'accepted', attempts: 1, lastError: '—', age: '8m', latency: '1.7s' },
  { id: 'sub_9f2911', buyer: 'Bolt Nigeria', btin: '19847720-0001', invoice: 'ZP-INV-0088314', raw: 756000, desc: 'Ride commission', state: 'queued', attempts: 0, lastError: '—', age: '14s', latency: '—' },
]

export const JOB_FILTER_KEYS: JobState[] = ['queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed', 'dead-letter']

/* ------------------------------------------------------------------ */
/* Evidence — signed bundles                                           */
/* ------------------------------------------------------------------ */

// proto:1203-1236, via the tested pure builder in charts.ts. Derived once at import:
// every field on the bundle is env-independent.
//
// The drawer's "Submitted invoice" JSON is deliberately NOT part of this const. It is
// built by reqJSON(row, env), which interpolates the live sandbox/live toggle, so
// freezing it here would silently pin every bundle to whichever env was active at
// module load. EvidenceDrawer computes it per render instead — the same split
// JobDrawer already uses.
export const EVIDENCE_DATA: EvidenceBundle[] = buildEvidenceBundles()

/* ------------------------------------------------------------------ */
/* API & webhooks                                                      */
/* ------------------------------------------------------------------ */

// proto:992-995. Both card colours and the LIVE card's green border are seed fields, not
// derived from `tag` — note the asymmetry: LIVE gets --status-green-border, SANDBOX the
// neutral --line-1. The mask is 20 x U+00B7 MIDDLE DOT (NOT '*' and NOT the '•' bullet);
// it is built exactly as the prototype builds it rather than redacted from `full`.
export const API_KEYS: ApiKey[] = [
  {
    id: 'live',
    tag: 'LIVE',
    name: 'Production secret',
    full: 'fb_live_sk_9f2a71c4d8e0b6a3f19c72e4',
    mask: 'fb_live_sk_' + '·'.repeat(20) + '72e4',
    tagBg: 'var(--status-green-bg)',
    tagBorder: 'var(--status-green-border)',
    tagText: 'var(--status-green-text)',
    created: 'Jan 12, 2026',
    lastUsed: '12s ago',
    borderColor: 'var(--status-green-border)',
  },
  {
    id: 'sandbox',
    tag: 'SANDBOX',
    name: 'Sandbox secret',
    full: 'fb_test_sk_4c71a90f2b8e6d13c05a9f4c',
    mask: 'fb_test_sk_' + '·'.repeat(20) + '9f4c',
    tagBg: 'var(--status-amber-bg)',
    tagBorder: 'var(--status-amber-border)',
    tagText: 'var(--status-amber-text)',
    created: 'Jan 12, 2026',
    lastUsed: '2m ago',
    borderColor: 'var(--line-1)',
  },
]

// proto:1002-1005. The ACTIVE pill both cards carry is hardcoded in the markup, not a
// seed field — every endpoint in the prototype is active.
export const WEBHOOKS: Webhook[] = [
  {
    url: 'https://api.zephyrpay.io/hooks/fiscalbridge',
    env: 'LIVE',
    envBg: 'var(--status-green-bg)',
    envBorder: 'var(--status-green-border)',
    envText: 'var(--status-green-text)',
    events: ['invoice.cleared', 'invoice.rejected', 'submission.failed'],
  },
  {
    url: 'https://sandbox.zephyrpay.io/hooks/fiscalbridge',
    env: 'SANDBOX',
    envBg: 'var(--status-amber-bg)',
    envBorder: 'var(--status-amber-border)',
    envText: 'var(--status-amber-text)',
    events: ['invoice.cleared', 'invoice.rejected'],
  },
]

// proto:1007-1012. The `id` field is ours, not the prototype's: `invoice.cleared` occurs
// three times, so keying on `event` would trip React's duplicate-key console.error and
// (via e2e/smoke/smoke.spec.ts) red the smoke gate. Row colours are applied at render —
// httpCodeColor(code) for the status, an inline ternary for the retry counter — so this
// table stays free of a `charts` import.
export const DELIVERIES: Delivery[] = [
  { id: 'dlv_1', event: 'invoice.cleared', code: 200, latency: '142ms', retry: '—' },
  { id: 'dlv_2', event: 'invoice.rejected', code: 200, latency: '118ms', retry: '—' },
  { id: 'dlv_3', event: 'submission.failed', code: 500, latency: '—', retry: '2/3' },
  { id: 'dlv_4', event: 'invoice.cleared', code: 200, latency: '96ms', retry: '—' },
  { id: 'dlv_5', event: 'invoice.cleared', code: 200, latency: '134ms', retry: '—' },
]

// proto:1016-1022. Same story as DELIVERIES: `POST /v2/invoices` occurs four times, so
// the `id` is what makes the key unique without falling back to the array index.
export const REQ_LOG: ApiRequest[] = [
  { id: 'req_1', m: 'POST', ep: '/v2/invoices', code: 202, lat: '88ms' },
  { id: 'req_2', m: 'GET', ep: '/v2/invoices/sub_9f2a91', code: 200, lat: '42ms' },
  { id: 'req_3', m: 'POST', ep: '/v2/invoices', code: 202, lat: '91ms' },
  { id: 'req_4', m: 'POST', ep: '/v2/invoices', code: 422, lat: '76ms' },
  { id: 'req_5', m: 'GET', ep: '/v2/evidence/ZP-INV-0088412', code: 200, lat: '38ms' },
  { id: 'req_6', m: 'POST', ep: '/v2/invoices', code: 202, lat: '84ms' },
]

// proto:1024-1026. Env-aware, and every field is a literal — including `width`. The live
// bar is pinned at '68%' even though 341/500 is 68.2%, so deriving the width from
// current/limit would drift from the design. Do not compute it.
export const RATE_LIMIT: Record<Env, RateLimit> = {
  sandbox: { current: '58', limit: '100', width: '58%', color: 'var(--accent)', detail: 'Sandbox throughput · resets each second' },
  live: { current: '341', limit: '500', width: '68%', color: 'var(--accent)', detail: 'Production throughput · burst to 750 req·s' },
}

// proto:1014-1015. Two-entry lookup maps kept beside the seed they colour.
export const METHOD_BG: Record<string, string> = { POST: 'var(--accent-tint)', GET: 'var(--status-muted-bg)' }
export const METHOD_FG: Record<string, string> = { POST: 'var(--accent)', GET: 'var(--fg-2)' }

/* ------------------------------------------------------------------ */
/* Usage & billing (proto:1029-1041)                                   */
/* ------------------------------------------------------------------ */

// proto:468-487. Only the *amount* fields below are computed; the meter's headline,
// legend and sub-stat figures are seed literals formatted with `fmt` at render.
export const QUOTA: Quota = {
  used: 48214,
  includedWidth: '83%',
  overWidth: '17%',
  clearedInvoices: 46820,
  evidenceExports: 1020,
}

// proto:1029-1034. `label`, `detail`, `qty` and `color` are display literals ported
// verbatim (the `detail` strings use U+00D7 MULTIPLICATION SIGN and the platform-fee
// qty is U+2014 EM DASH). Every ₦ amount routes through `computeBillLine`/`SCALE_PLAN`
// so the unit-tested arithmetic and the rendered figure cannot drift apart. The
// Evidence exports row has no amount at all — it renders the word `included`.
//
// These four rows deliberately do NOT sum to the total row: Σ = ₦3,417,788 while the
// total renders `spendTotals().proj` = ₦5.08M. The billing line items and the seeded
// spend series are unlinked streams in the prototype (task-138 GAP-4). Do not
// reconcile them — porting the discrepancy is the correct behaviour.
export const BILL_ITEMS: BillItem[] = [
  { label: 'Scale platform fee', detail: 'Monthly base', qty: '—', amount: naira(SCALE_PLAN.baseFee), color: 'var(--fg-1)' },
  { label: 'Cleared invoices', detail: '46,820 × ₦40', qty: '46,820', amount: naira(computeBillLine(46820, SCALE_PLAN.clearedRate)), color: 'var(--fg-1)' },
  { label: 'Overage requests', detail: '8,214 over included × ₦42', qty: '8,214', amount: naira(computeBillLine(8214, SCALE_PLAN.overageRate)), color: 'var(--status-amber-text)' },
  { label: 'Evidence exports', detail: '1,020 signed bundles', qty: '1,020', amount: 'included', color: 'var(--fg-3)' },
]

// proto:1036-1040. Keyed on `id` at render — never on `amount`, which mixes a computed
// compact figure with three literals and would collide on a seed change.
export const PAST_INVOICES: PastInvoice[] = [
  { id: 'FB-2026-07', period: 'Jul 2026 · due Aug 5', amount: nairaC(spendTotals().proj), kind: 'open' },
  { id: 'FB-2026-06', period: 'Jun 2026', amount: '₦3,184,200', kind: 'paid' },
  { id: 'FB-2026-05', period: 'May 2026', amount: '₦2,940,500', kind: 'paid' },
  { id: 'FB-2026-04', period: 'Apr 2026', amount: '₦2,712,300', kind: 'paid' },
]

// proto:1035's `invSt(kind)` — a two-entry lookup map, same shape as METHOD_BG/METHOD_FG
// above, kept beside the seed it colours.
export const INVOICE_STATUS: Record<InvoiceKind, InvoiceStatus> = {
  paid: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'PAID' },
  open: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'OPEN' },
}
