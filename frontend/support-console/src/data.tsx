// Mock data for the Support Console, ported from the prototype's seed/static methods
// (Support Console.dc.html:741-767 and 1189-1245).
//
// ALL OF IT IS FICTION. This console reads across every tenant, and no such read path
// exists yet — the gateway refuses any token without a tenant (internal/gateway/
// gateway.go `authorize()`), and the three database roles are all RLS-bound. Wiring these
// screens to real data needs an operator identity and a cross-tenant read path, which is
// M7. Until then the shapes here are the contract that work will have to satisfy.

import { Icon } from './icons'
import type {
  AuditEntry,
  DiffRow,
  HealthCard,
  Job,
  LearnedRule,
  NavItem,
  ReconRow,
  Rule,
  RuleSetVersion,
  Screen,
  Tenant,
} from './types'

// ---------- shared glyphs ----------

export const SEARCH_ICON = <Icon paths={['M21 21l-4.35-4.35', 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z']} size={16} />
export const FILTER_ICON = <Icon paths={['M22 3H2l8 9.46V19l4 2v-8.54L22 3Z']} size={14} />
export const CHEVRON_RIGHT_ICON = <Icon paths={['m9 18 6-6-6-6']} size={14} />
export const CLOSE_ICON = <Icon paths={['M18 6 6 18M6 6l12 12']} size={15} />
export const CHECK_ICON = <Icon paths={['M20 6 9 17l-5-5']} size={16} />
export const ALERT_ICON = (
  <Icon paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']} size={18} />
)
export const LOCK_ICON = (
  <Icon paths={['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']} size={15} />
)
export const GLOBE_ICON = (
  <Icon
    paths={['M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z', 'M3.6 9h16.8M3.6 15h16.8', 'M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18Z']}
    size={16}
  />
)
export const REDRIVE_ICON = (
  <Icon paths={['M21 2v6h-6', 'M3 12a9 9 0 0 1 15-6.7L21 8', 'M3 22v-6h6', 'M21 12a9 9 0 0 1-15 6.7L3 16']} size={15} />
)
export const PUBLISH_ICON = <Icon paths={['M12 19V5', 'm5 12 7-7 7 7']} size={15} />
export const KILL_ICON = <Icon paths={['M18.36 6.64a9 9 0 1 1-12.73 0', 'M12 2v10']} size={15} />
export const SPARK_ICON = (
  <Icon paths={['M12 3 14.09 8.26 20 9.27l-4 3.64L17.18 19 12 16.1 6.82 19 8 12.91l-4-3.64 5.91-1.01z']} size={15} />
)
export const COPY_ICON = (
  <Icon
    paths={[
      'M20 9H11a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-9a2 2 0 0 0-2-2Z',
      'M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1',
    ]}
    size={15}
  />
)
export const EXPORT_ICON = (
  <Icon paths={['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3']} size={15} />
)
export const SANDBOX_ICON = (
  <Icon paths={['M9 3h6M10 3v6.5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 9.5V3', 'M7.5 14h9']} size={15} />
)
export const SIGN_OUT_ICON = <Icon paths={['M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9']} size={16} />

// ---------- navigation ----------

// proto:820. Five operations screens — this console has no "Overview": Submissions ops
// IS the landing screen, because a support engineer arrives here to work a queue.
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
        paths={[
          'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2',
          'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z',
          'M22 21v-2a4 4 0 0 0-3-3.87',
        ]}
        size={17}
      />
    ),
  },
  { key: 'health', label: 'System health', glyph: <Icon paths={['M22 12h-4l-3 9L9 3l-3 9H2']} size={17} /> },
]

// proto:832. The crumb differs from the nav label on three of the five screens.
export const CRUMB_BY_SCREEN: Record<Screen, string> = {
  submissions: 'Submissions ops',
  rules: 'Rules admin',
  audit: 'Audit & evidence',
  tenants: 'Tenants',
  health: 'System health',
}

// ---------- submissions ----------

// proto:742. Ten jobs spanning every state, across five tenants — the cross-tenant mix is
// the point: no tenant-scoped console could show this list.
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

// proto:866. Filter-chip order — 'all' first, then the pipeline in lifecycle order.
export const JOB_FILTERS = ['all', 'queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed', 'dead-letter'] as const

// proto:894. Jobs where our state and the Access Point's disagree. This is the UI for the
// M5-06 reconciliation sweep, which shipped headless.
export const RECON_ROWS: ReconRow[] = [
  { id: 'job_8f28f0', tenant: 'Sahara Foods Distribution', internal: 'pending', app: 'accepted', detail: 'APP cleared, local poll missed webhook' },
  { id: 'job_8f28c4', tenant: 'Adeyemi & Sons Trading', internal: 'accepted', app: 'rejected', detail: 'Late MBS reversal — duplicate IRN' },
  { id: 'job_8f2890', tenant: 'Kano Textile Mills Plc', internal: 'submitting', app: 'pending', detail: 'Stuck submitting > 30m, APP holds it' },
  { id: 'job_8f2851', tenant: 'Westgate Pharma Ltd', internal: 'failed', app: 'accepted', detail: 'Local schema fail after APP accepted' },
]

// ---------- rules ----------

// proto:757. Eight rules from the NG-MBS set, one per rule TYPE, so the drawer's
// parameter form has a case to render for each.
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

// proto:917.
export const RULE_SET_VERSIONS: RuleSetVersion[] = [
  { version: 'v9 · draft', meta: 'editing · 3 changes', tag: 'DRAFT', kind: 'draft' },
  { version: 'v8', meta: 'eff. 2026-06-01 · 42 rules', tag: 'ACTIVE', kind: 'active' },
  { version: 'v7', meta: 'eff. 2026-04-15 · 40 rules', tag: 'ARCHIVED', kind: 'arch' },
  { version: 'v6', meta: 'eff. 2026-02-01 · 38 rules', tag: 'ARCHIVED', kind: 'arch' },
]

// proto:928. Candidate rules inferred from recurring rejection codes, awaiting promotion.
export const LEARNED_RULES: LearnedRule[] = [
  { key: 'buyer.email.format', source: 'Derived from 47 MBS-419 rejections this week' },
  { key: 'lines[].hsn.required', source: 'Derived from 23 MBS-431 rejections' },
  { key: 'fx.rate.range', source: 'Derived from 11 USD invoice anomalies' },
]

// proto:1237. The publish-diff between draft v9 and active v8.
export const DIFF_ROWS: DiffRow[] = [
  { sign: '+', key: 'buyer.email.format', detail: 'format-regex · warn · from learned inbox', tag: 'ADDED', kind: 'added' },
  { sign: '+', key: 'lines[].hsn.required', detail: 'required · error · global', tag: 'ADDED', kind: 'added' },
  { sign: '+', key: 'fx.rate.range', detail: 'range · info · global', tag: 'ADDED', kind: 'added' },
  { sign: '~', key: 'vat.rate.taxmath', detail: 'tolerance ±0.01 → ±0.005 NGN', tag: 'CHANGED', kind: 'changed' },
  { sign: '−', key: 'line.qty.range', detail: 'disabled rule removed from set', tag: 'REMOVED', kind: 'removed' },
]

// proto:1170. Rule parameters, keyed by rule type — the drawer's read-only param form.
export const RULE_PARAMS: Record<string, { label: string; value: string }[]> = {
  tax_math: [
    { label: 'Operation', value: 'multiply' },
    { label: 'Operand (rate)', value: '0.075' },
    { label: 'Tolerance', value: '±0.01 NGN' },
  ],
  'format-regex': [
    { label: 'Pattern', value: '^\\d{8}-\\d{4}$' },
    { label: 'Flags', value: 'none' },
  ],
  required: [{ label: 'Applies when', value: 'always' }],
  enum: [{ label: 'Allowed values', value: 'NGN, USD, EUR' }],
  range: [
    { label: 'Min', value: '1' },
    { label: 'Max', value: '100000' },
  ],
  cross_field: [
    { label: 'When', value: 'line.type == "service"' },
    { label: 'Require', value: 'line.wht > 0' },
  ],
  date_rule: [{ label: 'Constraint', value: 'issue_date >= prev.issue_date' }],
  'expression-CEL': [{ label: 'CEL', value: 'unique(invoice_no, seller_tin)' }],
}

// ---------- audit ----------

const auditGlyph = (paths: string[]) => <Icon paths={paths} size={13} />

// proto:1202. Seven entries mixing human and system actors — the attribution split is the
// screen's whole argument, so both kinds must be present.
export const AUDIT_ENTRIES: AuditEntry[] = [
  { id: 'evt_b71f04', ts: '09:14:22.118', action: 'Submission accepted', object: 'INV-2026-04417 · IRN-NG-A91', objectType: 'submission', tenant: 'Lagos Freight & Logistics Ltd', actor: 'system', who: 'SY', tone: 'green', glyph: auditGlyph(['M20 6 9 17l-5-5']) },
  { id: 'evt_b71ef2', ts: '09:12:09.004', action: 'Kill-switch · rule disabled', object: 'line.qty.range', objectType: 'rule', tenant: 'All tenants', actor: 'Emeka Iroha', who: 'EI', tone: 'red', glyph: auditGlyph(['M18.36 6.64a9 9 0 1 1-12.73 0', 'M12 2v10']) },
  { id: 'evt_b71e88', ts: '09:08:41.553', action: 'Dead-letter re-driven', object: 'job_8f29a8', objectType: 'state', tenant: 'Kano Textile Mills Plc', actor: 'Emeka Iroha', who: 'EI', tone: 'amber', glyph: auditGlyph(['M21 2v6h-6', 'M3 12a9 9 0 0 1 15-6.7L21 8']) },
  { id: 'evt_b71e10', ts: '09:02:17.900', action: 'Submission rejected', object: 'INV-2026-04402 · MBS-422', objectType: 'submission', tenant: 'Adeyemi & Sons Trading', actor: 'system', who: 'SY', tone: 'red', glyph: auditGlyph(['M18 6 6 18M6 6l12 12']) },
  { id: 'evt_b71d9c', ts: '08:55:03.221', action: 'Rule promoted to draft', object: 'buyer.email.format', objectType: 'rule', tenant: 'All tenants', actor: 'Ada Nwosu', who: 'AN', tone: 'teal', glyph: auditGlyph(['M12 19V5', 'm5 12 7-7 7 7']) },
  { id: 'evt_b71d22', ts: '08:49:55.087', action: 'State change · pending→accepted', object: 'INV-2026-04369', objectType: 'state', tenant: 'Sahara Foods Distribution', actor: 'system', who: 'SY', tone: 'green', glyph: auditGlyph(['M5 12h14', 'm12 5 7 7-7 7']) },
  { id: 'evt_b71ca0', ts: '08:41:12.640', action: 'Submission queued', object: 'INV-2026-04371', objectType: 'submission', tenant: 'Lagos Freight & Logistics Ltd', actor: 'system', who: 'SY', tone: 'teal', glyph: auditGlyph(['M3 12h4l2 5 4-12 2 7h6']) },
]

// proto:947.
export const AUDIT_FILTERS: { key: 'all' | 'submission' | 'rule' | 'state'; label: string }[] = [
  { key: 'all', label: 'ALL' },
  { key: 'submission', label: 'SUBMISSION' },
  { key: 'rule', label: 'RULE' },
  { key: 'state', label: 'STATE CHANGE' },
]

// ---------- tenants ----------

// proto:1214. Five tenants at varying health, so the status dot has all three states.
export const TENANTS: Tenant[] = [
  {
    id: 't1',
    name: 'Lagos Freight & Logistics Ltd',
    initials: 'LF',
    tin: '20184412-0001',
    status: 'ok',
    entityCount: '3 entities · Growth plan',
    kpis: [
      { label: 'Readiness', value: '94%', tone: 'green' },
      { label: 'Submitted 30d', value: '2,841' },
      { label: 'Rejected', value: '12', tone: 'red' },
      { label: 'Members', value: '6' },
    ],
    members: [
      { name: 'Tunde Adeyemi', initials: 'TA', role: 'admin' },
      { name: 'Kemi Eze', initials: 'KE', role: 'preparer' },
      { name: 'Ola Bello', initials: 'OB', role: 'reviewer' },
    ],
    recent: [
      { invoice: 'INV-2026-04417', state: 'accepted', age: '2m' },
      { invoice: 'INV-2026-04371', state: 'queued', age: '4s' },
      { invoice: 'INV-2026-04355', state: 'accepted', age: '18m' },
      { invoice: 'INV-2026-04340', state: 'pending', age: '32m' },
    ],
  },
  {
    id: 't2',
    name: 'Sahara Foods Distribution',
    initials: 'SF',
    tin: '19847720-0001',
    status: 'ok',
    entityCount: '1 entity · Growth plan',
    kpis: [
      { label: 'Readiness', value: '88%', tone: 'green' },
      { label: 'Submitted 30d', value: '1,204' },
      { label: 'Rejected', value: '8', tone: 'red' },
      { label: 'Members', value: '4' },
    ],
    members: [
      { name: 'Chidi Okeke', initials: 'CO', role: 'admin' },
      { name: 'Ngozi Udeh', initials: 'NU', role: 'preparer' },
    ],
    recent: [
      { invoice: 'INV-2026-04416', state: 'submitting', age: '3m' },
      { invoice: 'INV-2026-04369', state: 'accepted', age: '5m' },
      { invoice: 'INV-2026-04350', state: 'accepted', age: '22m' },
    ],
  },
  {
    id: 't3',
    name: 'Nigerian Delta Supplies Co.',
    initials: 'ND',
    tin: '22310984-0001',
    status: 'warn',
    entityCount: '2 entities · Starter plan',
    kpis: [
      { label: 'Readiness', value: '71%', tone: 'amber' },
      { label: 'Submitted 30d', value: '642' },
      { label: 'Rejected', value: '24', tone: 'red' },
      { label: 'Members', value: '3' },
    ],
    members: [
      { name: 'Ibrahim Sani', initials: 'IS', role: 'admin' },
      { name: 'Funke Ade', initials: 'FA', role: 'reviewer' },
    ],
    recent: [
      { invoice: 'INV-2026-04410', state: 'pending', age: '11m' },
      { invoice: 'INV-2026-04388', state: 'rejected', age: '1h' },
    ],
  },
  {
    id: 't4',
    name: 'Kano Textile Mills Plc',
    initials: 'KT',
    tin: '18772300-0001',
    status: 'red',
    entityCount: '1 entity · Starter plan',
    kpis: [
      { label: 'Readiness', value: '58%', tone: 'red' },
      { label: 'Submitted 30d', value: '388' },
      { label: 'Rejected', value: '41', tone: 'red' },
      { label: 'Members', value: '2' },
    ],
    members: [{ name: 'Musa Bello', initials: 'MB', role: 'admin' }],
    recent: [{ invoice: 'INV-2026-04391', state: 'dead-letter', age: '1h 12m' }],
  },
  {
    id: 't5',
    name: 'Westgate Pharma Ltd',
    initials: 'WP',
    tin: '22887301-0001',
    status: 'ok',
    entityCount: '1 entity · Growth plan',
    kpis: [
      { label: 'Readiness', value: '90%', tone: 'green' },
      { label: 'Submitted 30d', value: '910' },
      { label: 'Rejected', value: '6', tone: 'red' },
      { label: 'Members', value: '5' },
    ],
    members: [
      { name: 'Grace Obi', initials: 'GO', role: 'admin' },
      { name: 'Peter Aluko', initials: 'PA', role: 'preparer' },
    ],
    recent: [{ invoice: 'INV-2026-04358', state: 'queued', age: '12s' }],
  },
]

// ---------- system health ----------

// proto:987. `dlCount` is live (it falls as dead-letter jobs are re-driven), so the
// Dead-letter card is built by the caller rather than frozen here.
export function healthCards(deadLetterCount: number): HealthCard[] {
  return [
    { label: 'Queue depth', value: '1,284', unit: 'jobs', status: 'NORMAL', tone: 'green', points: [820, 900, 1100, 980, 1200, 1180, 1284] },
    { label: 'Worker throughput', value: '342', unit: 'job/min', status: 'HEALTHY', tone: 'green', points: [300, 320, 290, 350, 330, 360, 342] },
    { label: 'APP latency p95', value: '1.8', unit: 's', status: 'ELEVATED', tone: 'amber', points: [0.9, 1.0, 1.2, 1.4, 1.6, 1.7, 1.8] },
    { label: 'APP error rate', value: '2.4', unit: '%', status: 'ELEVATED', tone: 'amber', points: [0.4, 0.6, 0.8, 1.5, 2.0, 2.2, 2.4] },
    { label: 'Dead-letter', value: String(deadLetterCount), unit: 'jobs', status: deadLetterCount ? 'ATTENTION' : 'CLEAR', tone: deadLetterCount ? 'red' : 'green', points: [0, 1, 1, 2, 2, 2, deadLetterCount] },
    { label: 'Recon backlog', value: String(RECON_ROWS.length), unit: 'open', status: 'NORMAL', tone: 'green', points: [9, 7, 6, 5, 4, 5, RECON_ROWS.length] },
  ]
}
