// All Ops Console seed content, re-authored from the prototype's support.js
// state/seed methods (seedJobs, seedRules, auditData, tenantData, diffData,
// versionRows, learnedRows, reconRows) as typed, static TS constants. Glyphs
// are pre-built <Icon> nodes so section components stay pure layout.

import type { ReactNode } from 'react'
import { Icon } from './icons'
import { jobStateStyle } from './helpers'
import type { AuditTone } from './helpers'
import type { Job, JobState, Rule, Screen } from './types'

/* ------------------------------------------------------------------ */
/* Common icon glyphs (this.g(paths, size) in the prototype)           */
/* ------------------------------------------------------------------ */

export const SEARCH_ICON = <Icon paths={['M21 21l-4.35-4.35', 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z']} size={16} />
export const FILTER_ICON = <Icon paths={['M22 3H2l8 9.46V19l4 2v-8.54L22 3Z']} size={14} />
export const CHEVRON_RIGHT_ICON = <Icon paths={['m9 18 6-6-6-6']} size={14} />
export const CLOSE_ICON = <Icon paths={['M18 6 6 18M6 6l12 12']} size={15} />
export const ALERT_ICON = <Icon paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']} size={18} />
export const LOCK_ICON = <Icon paths={['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']} size={15} />
export const REDRIVE_ICON = <Icon paths={['M21 2v6h-6', 'M3 12a9 9 0 0 1 15-6.7L21 8', 'M3 22v-6h6', 'M21 12a9 9 0 0 1-15 6.7L3 16']} size={15} />
export const PUBLISH_ICON = <Icon paths={['M12 19V5', 'm5 12 7-7 7 7']} size={15} />
export const KILL_ICON = <Icon paths={['M18.36 6.64a9 9 0 1 1-12.73 0', 'M12 2v10']} size={15} />
export const SPARK_ICON = <Icon paths={['M12 3 14.09 8.26 20 9.27l-4 3.64L17.18 19 12 16.1 6.82 19 8 12.91l-4-3.64 5.91-1.01z']} size={15} />
export const COPY_ICON = <Icon paths={['M20 9H11a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-9a2 2 0 0 0-2-2Z', 'M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1']} size={15} />
export const EXPORT_ICON = <Icon paths={['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3']} size={15} />
export const CHECK_ICON = <Icon paths={['M20 6 9 17l-5-5']} size={16} />
export const GLOBE_ICON = <Icon paths={['M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z', 'M3.6 9h16.8M3.6 15h16.8', 'M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18Z']} size={16} />
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
/* Sidebar nav                                                         */
/* ------------------------------------------------------------------ */

export type NavItem = { key: Screen; label: string; glyph: ReactNode }

export const NAV_ITEMS: NavItem[] = [
  { key: 'submissions', label: 'Submissions', glyph: <Icon paths={['M3 12h4l2 5 4-12 2 7h6']} size={17} /> },
  {
    key: 'rules',
    label: 'Rules',
    glyph: <Icon paths={['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']} size={17} />,
  },
  { key: 'audit', label: 'Audit', glyph: <Icon paths={['M21 8v13H3V8', 'M1 3h22v5H1z', 'M10 12h4']} size={17} /> },
  {
    key: 'tenants',
    label: 'Tenants',
    glyph: (
      <Icon
        paths={['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']}
        size={17}
      />
    ),
  },
  { key: 'health', label: 'System health', glyph: <Icon paths={['M22 12h-4l-3 9L9 3l-3 9H2']} size={17} /> },
]

export const CRUMB_BY_SCREEN: Record<Screen, string> = {
  submissions: 'Submissions ops',
  rules: 'Rules admin',
  audit: 'Audit & evidence',
  tenants: 'Tenants',
  health: 'System health',
}

/* ------------------------------------------------------------------ */
/* Submissions — jobs                                                  */
/* ------------------------------------------------------------------ */

export const SEED_JOBS: Job[] = [
  { id: 'job_8f2a91', tenant: 'Lagos Freight & Logistics Ltd', tin: 'TIN 20184412-0001', invoice: 'INV-2026-04417', state: 'accepted', attempts: 1, lastError: '—', age: '2m', app: 'AP-Sterling' },
  { id: 'job_8f2a72', tenant: 'Sahara Foods Distribution', tin: 'TIN 19847720-0001', invoice: 'INV-2026-04416', state: 'submitting', attempts: 1, lastError: '—', age: '3m', app: 'AP-Sterling' },
  { id: 'job_8f2a55', tenant: 'Nigerian Delta Supplies Co.', tin: 'TIN 22310984-0001', invoice: 'INV-2026-04410', state: 'pending', attempts: 2, lastError: 'APP poll: clearance in progress', age: '11m', app: 'AP-Interswitch' },
  { id: 'job_8f29d1', tenant: 'Adeyemi & Sons Trading', tin: 'TIN 20991043-0001', invoice: 'INV-2026-04402', state: 'rejected', attempts: 3, lastError: 'MBS-422 buyer TIN not registered', age: '24m', app: 'AP-Sterling' },
  { id: 'job_8f29a8', tenant: 'Kano Textile Mills Plc', tin: 'TIN 18772300-0001', invoice: 'INV-2026-04391', state: 'dead-letter', attempts: 5, lastError: 'APP 503 — gateway timeout (x5)', age: '1h 12m', app: 'AP-Interswitch' },
  { id: 'job_8f2987', tenant: 'Port Harcourt Steel Co.', tin: 'TIN 21004552-0001', invoice: 'INV-2026-04388', state: 'dead-letter', attempts: 5, lastError: 'Signature mismatch — CSID rejected', age: '1h 40m', app: 'AP-Sterling' },
  { id: 'job_8f2961', tenant: 'Abuja Medical Supplies', tin: 'TIN 20554418-0001', invoice: 'INV-2026-04377', state: 'failed', attempts: 4, lastError: 'Schema: lines[2].vat_rate missing', age: '2h 03m', app: 'AP-Interswitch' },
  { id: 'job_8f2944', tenant: 'Lagos Freight & Logistics Ltd', tin: 'TIN 20184412-0001', invoice: 'INV-2026-04371', state: 'queued', attempts: 0, lastError: '—', age: '4s', app: 'AP-Sterling' },
  { id: 'job_8f2930', tenant: 'Sahara Foods Distribution', tin: 'TIN 19847720-0001', invoice: 'INV-2026-04369', state: 'accepted', attempts: 1, lastError: '—', age: '5m', app: 'AP-Sterling' },
  { id: 'job_8f2911', tenant: 'Westgate Pharma Ltd', tin: 'TIN 22887301-0001', invoice: 'INV-2026-04358', state: 'queued', attempts: 0, lastError: '—', age: '12s', app: 'AP-Interswitch' },
]

export const JOB_FILTER_KEYS: JobState[] = ['queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed', 'dead-letter']

/* ------------------------------------------------------------------ */
/* Submissions — reconciliation                                        */
/* ------------------------------------------------------------------ */

export type ReconRowBase = { id: string; tenant: string; int: JobState; app: JobState; detail: string }

export const RECON_BASE: ReconRowBase[] = [
  { id: 'job_8f28f0', tenant: 'Sahara Foods Distribution', int: 'pending', app: 'accepted', detail: 'APP cleared, local poll missed webhook' },
  { id: 'job_8f28c4', tenant: 'Adeyemi & Sons Trading', int: 'accepted', app: 'rejected', detail: 'Late MBS reversal — duplicate IRN' },
  { id: 'job_8f2890', tenant: 'Kano Textile Mills Plc', int: 'submitting', app: 'pending', detail: 'Stuck submitting > 30m, APP holds it' },
  { id: 'job_8f2851', tenant: 'Westgate Pharma Ltd', int: 'failed', app: 'accepted', detail: 'Local schema fail after APP accepted' },
]

/* ------------------------------------------------------------------ */
/* Rules                                                                */
/* ------------------------------------------------------------------ */

export const SEED_RULES: Rule[] = [
  { key: 'buyer.tin.required', type: 'required', field: 'buyer.tin', severity: 'error', scope: 'global', enabled: true, message: 'Buyer TIN is mandatory' },
  { key: 'buyer.tin.format', type: 'format-regex', field: 'buyer.tin', severity: 'error', scope: 'global', enabled: true, message: 'TIN must match NNNNNNNN-NNNN' },
  { key: 'vat.rate.taxmath', type: 'tax_math', field: 'lines[].vat', severity: 'error', scope: 'global', enabled: true, message: 'VAT must equal 7.5% of line net' },
  { key: 'wht.services.crossfield', type: 'cross_field', field: 'lines[].wht', severity: 'warn', scope: 'global', enabled: true, message: 'WHT expected on service lines' },
  { key: 'currency.enum', type: 'enum', field: 'header.currency', severity: 'error', scope: 'global', enabled: true, message: 'Currency must be NGN, USD or EUR' },
  { key: 'invoice.no.unique', type: 'expression-CEL', field: 'header.invoice_no', severity: 'error', scope: 'global', enabled: true, message: 'Invoice number must be unique per seller' },
  { key: 'issue.date.sequence', type: 'date_rule', field: 'header.issue_date', severity: 'warn', scope: 'tenant-override', enabled: true, message: 'Issue date must not precede prior invoice' },
  { key: 'line.qty.range', type: 'range', field: 'lines[].qty', severity: 'info', scope: 'global', enabled: false, message: 'Quantity outside expected range' },
]

export type VersionRow = { version: string; meta: string; tag: string; bg: string; tagBg: string; tagBorder: string; tagText: string }

const VERSION_SEED: { version: string; meta: string; tag: string; kind: 'draft' | 'active' | 'arch' }[] = [
  { version: 'v9 · draft', meta: 'editing · 3 changes', tag: 'DRAFT', kind: 'draft' },
  { version: 'v8', meta: 'eff. 2026-06-01 · 42 rules', tag: 'ACTIVE', kind: 'active' },
  { version: 'v7', meta: 'eff. 2026-04-15 · 40 rules', tag: 'ARCHIVED', kind: 'arch' },
  { version: 'v6', meta: 'eff. 2026-02-01 · 38 rules', tag: 'ARCHIVED', kind: 'arch' },
]

export const VERSION_ROWS: VersionRow[] = VERSION_SEED.map((v) => {
  const tg =
    v.kind === 'active'
      ? ['var(--status-green-bg)', 'var(--status-green-border)', 'var(--status-green-text)']
      : v.kind === 'draft'
        ? ['var(--status-amber-bg)', 'var(--status-amber-border)', 'var(--status-amber-text)']
        : ['var(--status-muted-bg)', 'var(--status-muted-border)', 'var(--status-muted-text)']
  return {
    version: v.version,
    meta: v.meta,
    tag: v.tag,
    bg: v.kind === 'draft' ? 'var(--accent-tint)' : 'var(--bg-2)',
    tagBg: tg[0],
    tagBorder: tg[1],
    tagText: tg[2],
  }
})

export type LearnedRuleBase = { key: string; source: string }

export const LEARNED_ROWS: LearnedRuleBase[] = [
  { key: 'buyer.email.format', source: 'Derived from 47 MBS-419 rejections this week' },
  { key: 'lines[].hsn.required', source: 'Derived from 23 MBS-431 rejections' },
  { key: 'fx.rate.range', source: 'Derived from 11 USD invoice anomalies' },
]

export type DiffRow = { sign: string; key: string; detail: string; tag: string; bg: string; color: string }

export const DIFF_ROWS: DiffRow[] = [
  { sign: '+', key: 'buyer.email.format', detail: 'format-regex · warn · from learned inbox', tag: 'ADDED', bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' },
  { sign: '+', key: 'lines[].hsn.required', detail: 'required · error · global', tag: 'ADDED', bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' },
  { sign: '+', key: 'fx.rate.range', detail: 'range · info · global', tag: 'ADDED', bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' },
  { sign: '~', key: 'vat.rate.taxmath', detail: 'tolerance ±0.01 → ±0.005 NGN', tag: 'CHANGED', bg: 'var(--status-amber-bg)', color: 'var(--status-amber-text)' },
  { sign: '−', key: 'line.qty.range', detail: 'disabled rule removed from set', tag: 'REMOVED', bg: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
]

/* ------------------------------------------------------------------ */
/* Audit                                                                */
/* ------------------------------------------------------------------ */

export type AuditObjectType = 'submission' | 'rule' | 'state'

export type AuditEntry = {
  id: string
  ts: string
  action: string
  object: string
  objectType: AuditObjectType
  tenant: string
  actor: string
  who: string
  tone: AuditTone
  glyph: ReactNode
  hash: string
  prevHash: string
  response: string
  // fields needed to reconstruct the captured request against the current env
  reqTin: string
  reqInvoice: string
}

const auditGlyph = (paths: string[]) => <Icon paths={paths} size={13} />

function mkAuditEntry(
  id: string,
  ts: string,
  action: string,
  object: string,
  objectType: AuditObjectType,
  tenant: string,
  actor: string,
  who: string,
  tone: AuditTone,
  glyph: ReactNode,
): AuditEntry {
  return {
    id,
    ts,
    action,
    object,
    objectType,
    tenant,
    actor,
    who,
    tone,
    glyph,
    hash: 'sha256:9f' + id.slice(-4) + 'a3e1b7c4d09f' + id.slice(-2) + '8e2c5a1f0b6d3e7c9a4',
    prevHash: 'sha256:8e' + id.slice(-3) + 'c2',
    response: '{\n  "result": "ok",\n  "actor": "' + actor + '",\n  "object": "' + object + '",\n  "action": "' + action + '"\n}',
    reqTin: 'TIN ' + tenant.replace(/\D/g, '').slice(0, 8) + '-0001',
    reqInvoice: object,
  }
}

export const AUDIT_ENTRIES: AuditEntry[] = [
  mkAuditEntry('evt_b71f04', '09:14:22.118', 'Submission accepted', 'INV-2026-04417 · IRN-NG-A91', 'submission', 'Lagos Freight & Logistics Ltd', 'system', 'SY', 'green', auditGlyph(['M20 6 9 17l-5-5'])),
  mkAuditEntry('evt_b71ef2', '09:12:09.004', 'Kill-switch · rule disabled', 'line.qty.range', 'rule', 'All tenants', 'Emeka Iroha', 'EI', 'red', auditGlyph(['M18.36 6.64a9 9 0 1 1-12.73 0', 'M12 2v10'])),
  mkAuditEntry('evt_b71e88', '09:08:41.553', 'Dead-letter re-driven', 'job_8f29a8', 'state', 'Kano Textile Mills Plc', 'Emeka Iroha', 'EI', 'amber', auditGlyph(['M21 2v6h-6', 'M3 12a9 9 0 0 1 15-6.7L21 8'])),
  mkAuditEntry('evt_b71e10', '09:02:17.900', 'Submission rejected', 'INV-2026-04402 · MBS-422', 'submission', 'Adeyemi & Sons Trading', 'system', 'SY', 'red', auditGlyph(['M18 6 6 18M6 6l12 12'])),
  mkAuditEntry('evt_b71d9c', '08:55:03.221', 'Rule promoted to draft', 'buyer.email.format', 'rule', 'All tenants', 'Ada Nwosu', 'AN', 'teal', auditGlyph(['M12 19V5', 'm5 12 7-7 7 7'])),
  mkAuditEntry('evt_b71d22', '08:49:55.087', 'State change · pending→accepted', 'INV-2026-04369', 'state', 'Sahara Foods Distribution', 'system', 'SY', 'green', auditGlyph(['M5 12h14', 'm12 5 7 7-7 7'])),
  mkAuditEntry('evt_b71ca0', '08:41:12.640', 'Submission queued', 'INV-2026-04371', 'submission', 'Lagos Freight & Logistics Ltd', 'system', 'SY', 'teal', auditGlyph(['M3 12h4l2 5 4-12 2 7h6'])),
]

/* ------------------------------------------------------------------ */
/* Tenants                                                              */
/* ------------------------------------------------------------------ */

type RoleTriplet = { bg: string; border: string; text: string }
const ROLE_STYLE: Record<'admin' | 'reviewer' | 'preparer', RoleTriplet> = {
  admin: { bg: 'var(--accent-tint)', border: 'var(--teal-200)', text: 'var(--accent)' },
  reviewer: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  preparer: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
}

export type TenantMember = { name: string; initials: string; role: string; roleBg: string; roleBorder: string; roleColor: string }
export type TenantRecent = { invoice: string; age: string; stBg: string; stBorder: string; stText: string; stLabel: string }
export type TenantKpi = { label: string; value: string; color: string }

export type Tenant = {
  id: string
  name: string
  initials: string
  tin: string
  status: 'ok' | 'warn' | 'red'
  entityCount: string
  kpis: TenantKpi[]
  members: TenantMember[]
  recent: TenantRecent[]
}

const kpi = (label: string, value: string, color?: string): TenantKpi => ({ label, value, color: color || 'var(--fg-1)' })
const member = (name: string, initials: string, role: 'admin' | 'reviewer' | 'preparer'): TenantMember => {
  const rc = ROLE_STYLE[role]
  return { name, initials, role: role.toUpperCase(), roleBg: rc.bg, roleBorder: rc.border, roleColor: rc.text }
}
const recent = (invoice: string, state: JobState, age: string): TenantRecent => {
  const b = jobStateStyle(state)
  return { invoice, age, stBg: b.bg, stBorder: b.border, stText: b.text, stLabel: b.label }
}

export const TENANTS: Tenant[] = [
  {
    id: 't1',
    name: 'Lagos Freight & Logistics Ltd',
    initials: 'LF',
    tin: '20184412-0001',
    status: 'ok',
    entityCount: '3 entities · Growth plan',
    kpis: [kpi('Readiness', '94%', 'var(--status-green-text)'), kpi('Submitted 30d', '2,841'), kpi('Rejected', '12', 'var(--status-red-text)'), kpi('Members', '6')],
    members: [member('Tunde Adeyemi', 'TA', 'admin'), member('Kemi Eze', 'KE', 'preparer'), member('Ola Bello', 'OB', 'reviewer')],
    recent: [recent('INV-2026-04417', 'accepted', '2m'), recent('INV-2026-04371', 'queued', '4s'), recent('INV-2026-04355', 'accepted', '18m'), recent('INV-2026-04340', 'pending', '32m')],
  },
  {
    id: 't2',
    name: 'Sahara Foods Distribution',
    initials: 'SF',
    tin: '19847720-0001',
    status: 'ok',
    entityCount: '1 entity · Growth plan',
    kpis: [kpi('Readiness', '88%', 'var(--status-green-text)'), kpi('Submitted 30d', '1,204'), kpi('Rejected', '8', 'var(--status-red-text)'), kpi('Members', '4')],
    members: [member('Chidi Okeke', 'CO', 'admin'), member('Ngozi Udeh', 'NU', 'preparer')],
    recent: [recent('INV-2026-04416', 'submitting', '3m'), recent('INV-2026-04369', 'accepted', '5m'), recent('INV-2026-04350', 'accepted', '22m')],
  },
  {
    id: 't3',
    name: 'Nigerian Delta Supplies Co.',
    initials: 'ND',
    tin: '22310984-0001',
    status: 'warn',
    entityCount: '2 entities · Starter plan',
    kpis: [kpi('Readiness', '71%', 'var(--status-amber-text)'), kpi('Submitted 30d', '642'), kpi('Rejected', '24', 'var(--status-red-text)'), kpi('Members', '3')],
    members: [member('Ibrahim Sani', 'IS', 'admin'), member('Funke Ade', 'FA', 'reviewer')],
    recent: [recent('INV-2026-04410', 'pending', '11m'), recent('INV-2026-04388', 'rejected', '1h')],
  },
  {
    id: 't4',
    name: 'Kano Textile Mills Plc',
    initials: 'KT',
    tin: '18772300-0001',
    status: 'red',
    entityCount: '1 entity · Starter plan',
    kpis: [kpi('Readiness', '58%', 'var(--status-red-text)'), kpi('Submitted 30d', '388'), kpi('Rejected', '41', 'var(--status-red-text)'), kpi('Members', '2')],
    members: [member('Musa Bello', 'MB', 'admin')],
    recent: [recent('INV-2026-04391', 'dead-letter', '1h 12m')],
  },
  {
    id: 't5',
    name: 'Westgate Pharma Ltd',
    initials: 'WP',
    tin: '22887301-0001',
    status: 'ok',
    entityCount: '1 entity · Growth plan',
    kpis: [kpi('Readiness', '90%', 'var(--status-green-text)'), kpi('Submitted 30d', '910'), kpi('Rejected', '6', 'var(--status-red-text)'), kpi('Members', '5')],
    members: [member('Grace Obi', 'GO', 'admin'), member('Peter Aluko', 'PA', 'preparer')],
    recent: [recent('INV-2026-04358', 'queued', '12s')],
  },
]
