// All Developer Console seed content, re-authored from the prototype's
// support.js state/seed methods (seedJobs, apiKeys, webhooks, …) as typed,
// static TS constants. Glyphs are pre-built <Icon> nodes so section components
// stay pure layout.

import type { ReactNode } from 'react'
import { Icon } from './icons'
import type { Job, JobState, Screen } from './types'

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
