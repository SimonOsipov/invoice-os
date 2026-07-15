// All landing content, re-authored from the prototype's support.js state as
// typed, static TS constants (the support.js Mustache runtime is NOT ported).
// Glyphs are pre-built <Icon> nodes so section components stay pure layout.

import type { ReactNode } from 'react'
import { Icon } from './icons'

/* ------------------------------------------------------------------ */
/* Hero — animated validation mock                                     */
/* ------------------------------------------------------------------ */

export type HeroCheck = {
  label: string
  tag: string
  icon: ReactNode
  bg: string
  fg: string
}

const tick = <Icon paths={['M20 6 9 17l-5-5']} size={11} strokeWidth={3} />
const cross = <Icon paths={['M18 6 6 18M6 6l12 12']} size={11} strokeWidth={3} />
const warn = <Icon paths={['M12 9v4M12 17h.01']} size={11} strokeWidth={3} />

const OK_BG = 'var(--status-green-bg)'
const OK_FG = 'var(--status-green-text)'

export const HERO_CHECKS: HeroCheck[] = [
  { label: 'Buyer TIN format · 12345678-0001', tag: 'PASS', icon: tick, bg: OK_BG, fg: OK_FG },
  { label: 'VAT computed at 7.5%', tag: 'PASS', icon: tick, bg: OK_BG, fg: OK_FG },
  { label: 'Mandatory seller fields present', tag: 'PASS', icon: tick, bg: OK_BG, fg: OK_FG },
  { label: 'WHT applied on services line', tag: 'WARN', icon: warn, bg: 'var(--status-amber-bg)', fg: 'var(--status-amber-text)' },
  { label: 'Invoice number not duplicated', tag: 'PASS', icon: tick, bg: OK_BG, fg: OK_FG },
  { label: 'Line totals reconcile to header', tag: 'FAIL', icon: cross, bg: 'var(--status-red-bg)', fg: 'var(--status-red-text)' },
]

/* ------------------------------------------------------------------ */
/* How it works — 3 steps                                              */
/* ------------------------------------------------------------------ */

export type Step = {
  num: string
  title: string
  glyph: ReactNode
  body: string
  points: string[]
}

export const STEPS: Step[] = [
  {
    num: '01',
    title: 'Connect or import',
    glyph: <Icon paths={['M21 12a9 9 0 1 1-6.2-8.6', 'M21 3v6h-6']} size={20} />,
    body: 'Pull invoices from your ERP via API, or upload CSV / XLSX from any accounting system. No migration.',
    points: ['REST API & webhooks', 'CSV / XLSX bulk import', 'ERP connectors'],
  },
  {
    num: '02',
    title: 'Validate against MBS rules',
    glyph: <Icon paths={['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']} size={20} />,
    body: 'The engine checks tax IDs, VAT/WHT, totals, duplicates, and mandatory fields — flagging errors before they cost you.',
    points: ['Nigeria rule pack', 'Field & tax logic checks', 'Inline fix suggestions'],
  },
  {
    num: '03',
    title: 'Approve, archive & transmit',
    glyph: <Icon paths={['M22 11.08V12a10 10 0 1 1-5.93-9.14', 'm22 4-10 10.01-3-3']} size={20} />,
    body: 'Route for approval, generate branded PDF + UBL data, store an immutable audit trail, and transmit to FIRS.',
    points: ['Approval workflow', 'PDF + JSON/XML/UBL export', 'Immutable audit log'],
  },
]

/* ------------------------------------------------------------------ */
/* Platform — 12 modules                                               */
/* ------------------------------------------------------------------ */

export type Module = { title: string; body: string; glyph: ReactNode }

const mg = (paths: string[]) => <Icon paths={paths} size={22} />

export const MODULES: Module[] = [
  { title: 'Business profile', body: 'Multi-tenant setup, tax details, numbering, currency, branches.', glyph: mg(['M3 21h18', 'M5 21V7l8-4v18', 'M19 21V11l-6-4']) },
  { title: 'User access', body: 'Role-based access, team invites, accountant-client links.', glyph: mg(['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']) },
  { title: 'Customer / vendor', body: 'Buyer & seller database, tax IDs, duplicate detection.', glyph: mg(['M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2', 'M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z']) },
  { title: 'Invoice management', body: 'Drafts, line items, credit & debit notes, cancellations.', glyph: mg(['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6', 'M16 13H8M16 17H8']) },
  { title: 'Validation engine', body: 'Rule-based checks for fields, tax logic, totals, numbering.', glyph: mg(['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']) },
  { title: 'Approval workflow', body: 'Creator, reviewer, approver, rejection notes, status trail.', glyph: mg(['M20 6 9 17l-5-5']) },
  { title: 'Document generation', body: 'PDF, JSON, XML/UBL export, QR placeholder, versioning.', glyph: mg(['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6', 'm9 15 2 2 4-4']) },
  { title: 'Integration / API', body: 'REST, webhooks, ERP connectors, API keys, OAuth2.', glyph: mg(['M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.8 1.7', 'M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7']) },
  { title: 'Archive & audit', body: 'Immutable logs, document storage, search, retention rules.', glyph: mg(['M21 8v13H3V8', 'M1 3h22v5H1z', 'M10 12h4']) },
  { title: 'Reporting & analytics', body: 'Volume, tax summaries, error patterns, readiness score.', glyph: mg(['M3 3v18h18', 'm19 9-5 5-4-4-3 3']) },
  { title: 'Partner portal', body: 'Accountants manage multiple client companies & exports.', glyph: mg(['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87', 'M16 3.13a4 4 0 0 1 0 7.75']) },
  { title: 'Platform admin', body: 'Tenants, subscriptions, country modules, support, config.', glyph: mg(['M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2Z', 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z']) },
]

/* ------------------------------------------------------------------ */
/* Compliance — MBS readiness rules                                    */
/* ------------------------------------------------------------------ */

export type Rule = { title: string; body: string; glyph: ReactNode }

const rg = (paths: string[]) => <Icon paths={paths} size={13} />

export const RULES: Rule[] = [
  { title: 'TIN & VAT identifier checks', body: 'Format, presence, and buyer/seller match validated automatically.', glyph: rg(['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']) },
  { title: 'WHT & tax computation logic', body: 'Withholding and VAT recalculated and reconciled per line.', glyph: rg(['M12 2v20M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6']) },
  { title: 'Duplicate & sequence detection', body: 'Repeated invoice numbers and out-of-order dates flagged.', glyph: rg(['M8 16H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2', 'M14 8h6a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-8a2 2 0 0 1-2-2v-6']) },
  { title: 'Readiness score, live', body: 'A single number that tells you exactly how audit-ready you are.', glyph: rg(['M3 3v18h18', 'm19 9-5 5-4-4-3 3']) },
]

/* ------------------------------------------------------------------ */
/* Dual audience — firm vs in-house                                    */
/* ------------------------------------------------------------------ */

type StatusTriplet = { bg: string; border: string; text: string }

const STATUS: Record<'green' | 'amber' | 'muted', StatusTriplet> = {
  green: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  amber: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  muted: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
}

export type Client = {
  name: string
  initials: string
  tin: string
  score: string
  status: string
  statusBg: string
  statusBorder: string
  statusText: string
}

const cl = (name: string, initials: string, tin: string, score: string, st: 'green' | 'amber' | 'muted'): Client => ({
  name,
  initials,
  tin,
  score,
  status: st === 'green' ? 'READY' : st === 'amber' ? 'REVIEW' : 'DRAFT',
  statusBg: STATUS[st].bg,
  statusBorder: STATUS[st].border,
  statusText: STATUS[st].text,
})

export const CLIENTS: Client[] = [
  cl('Lagos Freight & Logistics Ltd', 'LF', 'TIN 20184412-0001', '94%', 'green'),
  cl('Sahara Foods Distribution', 'SF', 'TIN 19847720-0001', '88%', 'green'),
  cl('Nigerian Delta Supplies Co.', 'ND', 'TIN 22310984-0001', '71%', 'amber'),
  cl('Adeyemi & Sons Trading', 'AS', 'TIN 20991043-0001', '63%', 'amber'),
  cl('Kano Textile Mills Plc', 'KT', 'TIN 18772300-0001', '—', 'muted'),
]

export type PipelineStat = { stage: string; count: string; color: string }

export const PIPELINE: PipelineStat[] = [
  { stage: 'Drafts', count: '18', color: 'var(--fg-1)' },
  { stage: 'In review', count: '7', color: 'var(--status-amber-text)' },
  { stage: 'Approved', count: '12', color: 'var(--status-green-text)' },
  { stage: 'Transmitted', count: '204', color: 'var(--fg-1)' },
]

export type Approval = {
  id: string
  party: string
  amount: string
  who: string
  assignee: string
  stage: string
  statusBg: string
  statusBorder: string
  statusText: string
}

const ap = (id: string, party: string, amount: string, who: string, assignee: string, st: 'green' | 'amber' | 'muted', label: string): Approval => ({
  id,
  party,
  amount,
  who,
  assignee,
  stage: label,
  statusBg: STATUS[st].bg,
  statusBorder: STATUS[st].border,
  statusText: STATUS[st].text,
})

export const APPROVALS: Approval[] = [
  ap('INV-2026-00518', 'Sahara Foods Distribution', '₦2.41M', 'TA', 'Tunde A. · reviewer', 'amber', 'IN REVIEW'),
  ap('INV-2026-00517', 'MTN Nigeria Plc', '₦880k', 'OB', 'Ola B. · approver', 'green', 'APPROVED'),
  ap('INV-2026-00516', 'Dangote Cement Plc', '₦5.07M', 'TA', 'Tunde A. · reviewer', 'amber', 'IN REVIEW'),
  ap('INV-2026-00515', 'Lagos Freight Ltd', '₦1.12M', 'KE', 'Kemi E. · creator', 'muted', 'DRAFT'),
]

export type AudienceFeature = { title: string; body: string; glyph: ReactNode }
export type AudienceStat = { value: string; label: string; color: string }

const fg = (paths: string[]) => <Icon paths={paths} size={14} />
const tg = (paths: string[]) => <Icon paths={paths} size={15} />

export type Audience = {
  tabIcon: ReactNode
  headline: string
  body: string
  features: AudienceFeature[]
  stats: AudienceStat[]
  cta: string
}

export const FIRM: Audience = {
  tabIcon: tg(['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']),
  headline: "Run every client's compliance from one portal.",
  body: "Manage filings, validation queues and readiness scores across your whole book of business. Switch between clients in one login, and become their compliance partner — and a distribution channel for FiscalBridge.",
  features: [
    { title: 'Multi-client portal', body: 'Every client company in one switchable workspace.', glyph: fg(['M3 3h7v7H3z', 'M14 3h7v7h-7z', 'M14 14h7v7h-7z', 'M3 14h7v7H3z']) },
    { title: 'Bulk validation queues', body: 'Run and clear validation across clients in one pass.', glyph: fg(['m3 17 2 2 4-4', 'm3 7 2 2 4-4', 'M13 6h8', 'M13 12h8', 'M13 18h8']) },
    { title: 'Per-client readiness scores', body: "See who's audit-ready and who needs attention.", glyph: fg(['m12 14 4-4', 'M3.34 19a10 10 0 1 1 17.32 0']) },
    { title: 'Partner revenue share', body: 'Earn 25% recurring on every client you bring on.', glyph: fg(['M19 5 5 19', 'M6.5 9a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z', 'M17.5 20a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z']) },
    { title: 'White-glove client onboarding', body: 'Templates and imports to set up new books fast.', glyph: fg(['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M19 8v6', 'M22 11h-6']) },
  ],
  stats: [
    { value: '1 login', label: 'All client companies, switch instantly.', color: 'var(--fg-1)' },
    { value: '25%', label: 'Recurring partner revenue share.', color: 'var(--accent)' },
  ],
  cta: 'Join the partner program',
}

export const INHOUSE: Audience = {
  tabIcon: tg(['M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z', 'M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2', 'M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2', 'M10 6h4', 'M10 10h4', 'M10 14h4']),
  headline: 'Give your finance team a compliance cockpit.',
  body: 'Route invoices through your own approval chain, assign reviewer and approver roles, and keep one company audit-ready. Built for finance departments that own the whole process in-house.',
  features: [
    { title: 'Role-based approval chain', body: 'Creator → reviewer → approver, with rejection notes.', glyph: fg(['M6 3v12', 'M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z', 'M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z', 'M15 6a9 9 0 0 0-9 9']) },
    { title: 'Departmental readiness dashboard', body: 'One company, every metric your controller needs.', glyph: fg(['M3 3h7v9H3z', 'M14 3h7v5h-7z', 'M14 12h7v9h-7z', 'M3 16h7v5H3z']) },
    { title: 'ERP & accounting sync', body: 'Two-way sync with SAP, NetSuite, Sage & QuickBooks.', glyph: fg(['M21 12a9 9 0 1 1-6.2-8.6', 'M21 3v6h-6']) },
    { title: 'SSO & team management', body: 'Provision finance staff with SSO, SCIM & roles.', glyph: fg(['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z']) },
    { title: 'Month-end close reports', body: 'VAT/WHT summaries and exception reports on demand.', glyph: fg(['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6', 'M16 13H8', 'M16 17H8']) },
  ],
  stats: [
    { value: '4 roles', label: 'Creator, reviewer, approver & admin built in.', color: 'var(--fg-1)' },
    { value: '1 company', label: 'Full control of your own compliance.', color: 'var(--accent)' },
  ],
  cta: 'Book a team demo',
}

/* ------------------------------------------------------------------ */
/* Developers — API points                                             */
/* ------------------------------------------------------------------ */

export type ApiPoint = { glyph: ReactNode; text: string }

const ag = (paths: string[]) => <Icon paths={paths} size={18} />

export const API_POINTS: ApiPoint[] = [
  { glyph: ag(['M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.8 1.7', 'M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7']), text: 'REST API — create, validate, fetch status, fetch documents' },
  { glyph: ag(['M22 12h-4l-3 9L9 3l-3 9H2']), text: 'Signed webhooks on status change & transmission events' },
  { glyph: ag(['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']), text: 'OAuth2 + scoped API keys, per-tenant isolation' },
  { glyph: ag(['M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z', 'M3.6 9h16.8M3.6 15h16.8']), text: 'Sandbox MBS/FIRS adapter — production on accreditation' },
]

/* ------------------------------------------------------------------ */
/* Pricing — plans (monthly / annual)                                  */
/* ------------------------------------------------------------------ */

export type PlanVariant = 'light' | 'dark'

export type Plan = {
  name: string
  featured: boolean
  tagline: string
  priceMonthly: string
  priceAnnual: string
  unit: string
  metaMonthly: string
  metaAnnual: string
  cta: string
  variant: PlanVariant
  features: string[]
}

export const PLANS: Plan[] = [
  {
    name: 'Starter',
    featured: false,
    tagline: 'Validation, export & archiving for a single business.',
    priceMonthly: '₦95k',
    priceAnnual: '₦79k',
    unit: '/mo',
    metaMonthly: 'BILLED MONTHLY · 1 TENANT',
    metaAnnual: 'BILLED ANNUALLY · 1 TENANT',
    cta: 'Start free',
    variant: 'light',
    features: ['Up to 1,000 invoices / mo', 'Validation engine + readiness score', 'PDF + JSON/XML/UBL export', 'Immutable audit log', 'Email support'],
  },
  {
    name: 'Growth',
    featured: true,
    tagline: 'For medium taxpayers & high-volume suppliers going live.',
    priceMonthly: '₦340k',
    priceAnnual: '₦283k',
    unit: '/mo',
    metaMonthly: 'BILLED MONTHLY · UP TO 5 TENANTS',
    metaAnnual: 'BILLED ANNUALLY · UP TO 5 TENANTS',
    cta: 'Book a demo',
    variant: 'dark',
    features: ['Up to 25,000 invoices / mo', 'API v1 + signed webhooks', 'Approval workflows & roles', 'Live MBS/FIRS transmission', 'ERP connectors', 'Priority support'],
  },
  {
    name: 'Firm / Enterprise',
    featured: false,
    tagline: 'For accounting firms & enterprises managing many clients.',
    priceMonthly: 'Custom',
    priceAnnual: 'Custom',
    unit: '',
    metaMonthly: 'PARTNER PROGRAM · UNLIMITED TENANTS',
    metaAnnual: 'PARTNER PROGRAM · UNLIMITED TENANTS',
    cta: 'Talk to sales',
    variant: 'light',
    features: ['Unlimited invoices & tenants', 'Multi-client partner portal', '25% recurring revenue share', 'SSO, SCIM & audit exports', 'Dedicated compliance manager', 'Country-module roadmap access'],
  },
]

/* Resolved color set per pricing card variant. */
export const PLAN_COLORS: Record<PlanVariant, {
  cardBg: string
  cardBorder: string
  titleColor: string
  subColor: string
  featColor: string
  btnBg: string
  btnFg: string
  btnBorder: string
  checkColor: string
}> = {
  light: {
    cardBg: 'var(--bg-2)',
    cardBorder: 'var(--line-2)',
    titleColor: 'var(--fg-1)',
    subColor: 'var(--fg-3)',
    featColor: 'var(--fg-2)',
    btnBg: 'transparent',
    btnFg: 'var(--fg-1)',
    btnBorder: 'var(--line-2)',
    checkColor: 'var(--accent)',
  },
  dark: {
    cardBg: 'var(--slate-900)',
    cardBorder: 'var(--slate-900)',
    titleColor: '#fff',
    subColor: 'var(--slate-400)',
    featColor: 'var(--slate-200)',
    btnBg: 'var(--accent)',
    btnFg: '#fff',
    btnBorder: 'var(--accent)',
    checkColor: 'var(--teal-300)',
  },
}
