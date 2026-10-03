// All landing content, re-authored from the prototype's support.js state as
// typed, static TS constants (the support.js Mustache runtime is NOT ported).

import type { ReactNode } from 'react'
import { Icon, type GlyphName } from './icons'

/* ------------------------------------------------------------------ */
/* Hero — animated validation mock                                     */
/* ------------------------------------------------------------------ */

export type HeroCheck = {
  label: string
  tag: string
  icon: GlyphName
  bg: string
  fg: string
}

const PASS = { bg: 'var(--status-success-bg)', fg: 'var(--status-success-fg)', icon: 'check' } as const
const WARN = { bg: 'var(--status-progress-bg)', fg: 'var(--status-progress-fg)', icon: 'triangle-alert' } as const
const FAIL = { bg: 'color-mix(in srgb, var(--destructive) 14%, var(--card))', fg: 'var(--destructive)', icon: 'x' } as const

export const HERO_CHECKS: HeroCheck[] = [
  { label: 'Buyer TIN format · 12345678-0001', tag: 'PASS', ...PASS },
  { label: 'VAT computed at 7.5%', tag: 'PASS', ...PASS },
  { label: 'Mandatory seller fields present', tag: 'PASS', ...PASS },
  { label: 'WHT applied on services line', tag: 'WARN', ...WARN },
  { label: 'Invoice number not duplicated', tag: 'PASS', ...PASS },
  { label: 'Line totals reconcile to header', tag: 'FAIL', ...FAIL },
]

/* ------------------------------------------------------------------ */
/* The problem — what breaks today                                     */
/* ------------------------------------------------------------------ */

// Row order and outcomes are pinned by Problem.render.test.tsx PR-05; Problem.tsx resolves them from CHECKING one by one.
const PROBLEM_ROWS: [string, 'FAIL' | 'WARN'][] = [
  ['Missing or incomplete tax fields', 'FAIL'],
  ['Incorrect customer or supplier information', 'FAIL'],
  ['Duplicate invoice numbers', 'FAIL'],
  ['Weak approval workflows', 'WARN'],
  ['Poor audit trails', 'WARN'],
  ['Manual invoice corrections', 'WARN'],
  ['Disconnected accounting and ERP systems', 'WARN'],
  ['Lack of readiness for structured e-invoicing requirements', 'FAIL'],
]

export const PROBLEMS: HeroCheck[] = PROBLEM_ROWS.map(([label, tag]) =>
  tag === 'FAIL' ? { label, tag, ...FAIL } : { label, tag, ...WARN },
)

export const CHECKING = { tag: 'CHECKING', icon: 'loader-circle', bg: 'var(--muted)', fg: 'var(--muted-foreground)' } as const

/* ------------------------------------------------------------------ */
/* Platform — 12 modules                                               */
/* ------------------------------------------------------------------ */

export type Module = { title: string; body: string; icon: GlyphName }

export const MODULES: Module[] = [
  { title: 'Business profile', body: 'Multi-tenant setup, tax details, numbering, currency, branches.', icon: 'building-2' },
  { title: 'User access', body: 'Role-based access, team invites, accountant-client links.', icon: 'users' },
  { title: 'Customer / vendor', body: 'Buyer & seller database, TIN & company verification, duplicate detection.', icon: 'contact' },
  { title: 'Invoice management', body: 'Drafts, line items, credit & debit notes, cancellations.', icon: 'file-text' },
  { title: 'Validation engine', body: 'Rule-based checks for fields, tax logic, totals, numbering.', icon: 'circle-check' },
  { title: 'Approval workflow', body: 'Creator, reviewer, approver, rejection notes, status trail.', icon: 'check' },
  { title: 'Document generation', body: 'PDF, JSON, XML/UBL export, QR placeholder, versioning.', icon: 'file-check' },
  { title: 'Integration / API', body: 'REST, webhooks, ERP connectors, API keys, OAuth2.', icon: 'link' },
  { title: 'Archive & audit', body: 'Immutable logs, document storage, search, retention rules.', icon: 'archive' },
  { title: 'Reporting & analytics', body: 'Volume, tax summaries, error patterns, readiness score.', icon: 'chart-line' },
  { title: 'Partner portal', body: 'Accountants manage multiple client companies & exports.', icon: 'users-round' },
  { title: 'Platform admin', body: 'Tenants, subscriptions, country modules, support, config.', icon: 'settings' },
]

/* ------------------------------------------------------------------ */
/* Platform tabs — Validate / Approve / Submit                         */
/* ------------------------------------------------------------------ */

export type PlatformTabId = 'validate' | 'approve' | 'submit'

export type PlatformTab = {
  id: PlatformTabId
  label: string
  icon: GlyphName
  stepLabel: string
  h1: string
  h2: string
  body: string
  tags: string[]
  link: string
  cardIcon: GlyphName
  cardTitle: string
  kind: string
  result: string
  sub: string
  rows: { label: string; status: string }[]
}

const rows = (labels: string[], status: string[]) => labels.map((label, i) => ({ label, status: status[i] }))

export const PLATFORM_TABS: PlatformTab[] = [
  {
    id: 'validate',
    label: 'Validate',
    icon: 'shield-check',
    stepLabel: '01 / VALIDATE',
    h1: 'Get it right.',
    h2: 'Before it goes out.',
    body: 'Bring in invoice data from your CRM or ERP, such as Odoo or Sage, or upload PDFs, photos, Excel and CSV files. Check required fields, totals and duplicates before an invoice moves forward. Give your team clear results they can act on.',
    tags: ['Required fields', 'Tax logic', 'Duplicate checks'],
    link: 'See validation in action →',
    cardIcon: 'file-check',
    cardTitle: 'Invoice validation',
    kind: 'VALIDATION RESULT',
    result: 'All checks passed.',
    sub: 'Ready for internal review.',
    rows: rows(
      ['Buyer & seller details', 'Tax calculation & totals', 'Invoice number & duplicates', 'Required invoice fields'],
      ['Passed', 'Passed', 'Passed', 'Passed'],
    ),
  },
  {
    id: 'approve',
    label: 'Approve',
    icon: 'user-check',
    stepLabel: '02 / APPROVE',
    h1: 'Every decision.',
    h2: 'A clear owner.',
    body: 'Move invoices from creator to reviewer to approver, or build custom approval workflows that match how your team works. Keep rejection notes and status history together so your team can resolve issues with context.',
    tags: ['Custom workflows', 'Review roles', 'Rejection notes', 'Status trail'],
    link: 'See approvals in action →',
    cardIcon: 'user-check',
    cardTitle: 'Invoice approval',
    kind: 'APPROVAL RESULT',
    result: 'Approval complete.',
    sub: 'Ready for submit.',
    rows: rows(
      ['Invoice created', 'Reviewed by finance', 'Approved by authorised user', 'Approval trail recorded'],
      ['Complete', 'Complete', 'Complete', 'Complete'],
    ),
  },
  {
    id: 'submit',
    label: 'Submit',
    icon: 'send',
    stepLabel: '03 / SUBMIT',
    h1: 'Send it on.',
    h2: 'Track every response.',
    body: 'Submit approved invoices to the tax authority — in Nigeria, the NRS, via its Merchant Buyer Solution (MBS). Generate QR codes, follow each submission, and keep an audit trail of every action with the invoice record.',
    tags: ['QR code', 'Status tracking', 'Full audit trail'],
    link: 'See submission in action →',
    cardIcon: 'send',
    cardTitle: 'Tax authority submission',
    kind: 'SUBMISSION RESULT',
    result: 'Submission accepted.',
    sub: 'Response recorded with the invoice.',
    rows: rows(
      ['Approved invoice submitted', 'Authority response received', 'Submission status tracked', 'QR code and audit trail generated'],
      ['Sent', 'Received', 'Tracked', 'Generated'],
    ),
  },
]

export const CAPABILITIES: { icon: GlyphName; title: string; body: string }[] = [
  { icon: 'layout-dashboard', title: 'One invoice workspace', body: 'Manage drafts, line items, credit notes and invoice records.' },
  { icon: 'key-round', title: 'The right access', body: 'Give your team clear roles across creation, review and approval.' },
  { icon: 'chart-column', title: 'Useful visibility', body: 'Review invoice volumes, tax summaries and validation results.' },
]

/* ------------------------------------------------------------------ */
/* Compliance — MBS readiness rules                                    */
/* ------------------------------------------------------------------ */

export type Rule = { title: string; body: string; glyph: ReactNode }

// Rule-list glyphs sit in 36px icon tiles, so they render at the DS's 18px rung.
const rg = (paths: string[]) => <Icon paths={paths} size={18} />

export const RULES: Rule[] = [
  { title: 'TIN & VAT identifier checks', body: 'Format, presence, and buyer/seller match validated automatically.', glyph: rg(['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']) },
  { title: 'WHT & tax computation logic', body: 'Withholding and VAT recalculated and reconciled per line.', glyph: rg(['M12 2v20M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6']) },
  { title: 'Duplicate & sequence detection', body: 'Repeated invoice numbers and out-of-order dates flagged.', glyph: rg(['M8 16H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2', 'M14 8h6a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-8a2 2 0 0 1-2-2v-6']) },
  { title: 'Readiness score, live', body: 'A single number that tells you exactly how audit-ready you are.', glyph: rg(['M3 3v18h18', 'm19 9-5 5-4-4-3 3']) },
]

/* ------------------------------------------------------------------ */
/* Solutions — three workspaces behind one switch                      */
/* ------------------------------------------------------------------ */

export type SolutionId = 'fin' | 'firm' | 'dev'

export type Solution = {
  id: SolutionId
  tab: string
  cardIcon: GlyphName
  cardTitle: string
  overview: string
  cardHead: string
  rows: [name: string, status: 'Validated' | 'In review' | 'Connected' | 'Mapped'][]
  label: string
  h3: string
  body: string
  points: string[]
  cta: string
}

export const SOLUTIONS: readonly Solution[] = [
  {
    id: 'fin',
    tab: 'Finance teams',
    cardIcon: 'layout-dashboard',
    cardTitle: 'Invoice workspace',
    overview: 'WORKSPACE OVERVIEW',
    cardHead: 'A clearer working day.',
    rows: [['Invoice INV-2026-0481', 'Validated'], ['Invoice INV-2026-0482', 'In review'], ['Invoice INV-2026-0483', 'Validated']],
    label: 'MORE CONTROL. LESS CHASING.',
    h3: 'Keep your team focused on the business.',
    body: 'Bring invoices, approvals and compliance checks into a single workflow. Spot what needs attention and keep everyone working from the same record.',
    points: ['Review exceptions before submission', 'Give every approval a clear owner', 'Keep invoice records ready for review'],
    cta: 'Find your workflow',
  },
  {
    id: 'firm',
    tab: 'Accounting & tax firms',
    cardIcon: 'building-2',
    cardTitle: 'Client portfolio',
    overview: 'WORKSPACE OVERVIEW',
    cardHead: 'Your clients, connected.',
    rows: [['Client company A', 'Validated'], ['Client company B', 'In review'], ['Client company C', 'Validated']],
    label: 'ONE WORKSPACE. EVERY CLIENT.',
    h3: 'Bring every client company into clearer view.',
    body: 'Work across client companies from one portal. Organise invoice reviews and identify the businesses that need attention without juggling separate workspaces.',
    points: ['Switch between client companies', 'Track approvals by client', "Keep each client's records organised"],
    cta: 'Find your workflow',
  },
  {
    id: 'dev',
    tab: 'Developers & partners',
    cardIcon: 'plug',
    cardTitle: 'Integration workspace',
    overview: 'WORKSPACE OVERVIEW',
    cardHead: 'Your data, connected.',
    rows: [['ERP invoice import', 'Connected'], ['Field mapping', 'Mapped'], ['Validation API request', 'Validated']],
    label: 'YOUR STACK. OUR COMPLIANCE SOLUTION.',
    h3: 'Build compliance into the way you work.',
    body: 'Plan an invoice workflow around your existing ERP, accounting or fintech platform. Talk to our team about API access, data mapping and your integration requirements.',
    points: ['Explore invoice validation via API', 'Map your invoice data and workflow', 'Discuss connector and partner opportunities'],
    cta: 'Discuss a partnership',
  },
]

/* ------------------------------------------------------------------ */
/* Integrations — partners; API band — bullets                         */
/* ------------------------------------------------------------------ */

export type Partner = { mark: string; desc: string; size: number; weight: number; track?: string; glyph?: boolean }

export const PARTNERS: Partner[] = [
  { mark: 'SAP', desc: 'Enterprise resource planning', size: 30, weight: 800 },
  { mark: 'ORACLE', desc: 'Enterprise applications', size: 20, weight: 700, track: '0.16em' },
  { mark: 'Microsoft', desc: 'Dynamics 365', size: 22, weight: 700, glyph: true },
  { mark: 'QuickBooks', desc: 'Business accounting', size: 24, weight: 800 },
  { mark: 'sage', desc: 'Accounting and ERP', size: 26, weight: 800, track: '-0.04em' },
  { mark: 'odoo', desc: 'Business applications', size: 28, weight: 700, track: '-0.03em' },
]

export const API_BULLETS: { icon: GlyphName; text: string }[] = [
  { icon: 'link', text: 'REST API: create, validate, fetch status, fetch documents' },
  { icon: 'activity', text: 'Signed webhooks on status change and submission events' },
  { icon: 'key-round', text: 'OAuth2 and scoped API keys, with per-tenant isolation' },
  { icon: 'globe', text: 'Sandbox MBS/NRS adapter. Production on accreditation.' },
]

/* ------------------------------------------------------------------ */
/* Pricing — plans (monthly / annual)                                  */
/* ------------------------------------------------------------------ */

export type PlanVariant = 'light' | 'featured'

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
    variant: 'featured',
    features: ['Up to 25,000 invoices / mo', 'API v1 + signed webhooks', 'Approval workflows & roles', 'Live MBS/NRS transmission', 'ERP connectors', 'Priority support'],
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
    features: ['Unlimited invoices & tenants', 'Multi-client partner portal', 'Partner commercials on request', 'SSO, SCIM & audit exports', 'Dedicated compliance manager', 'Country-module roadmap access'],
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
    checkColor: 'var(--action)',
  },
  /* The featured tier: a flat dark --surface card with a
     transparent border and an accent CTA. On light cards the checks are teal
     (action); on this dark card they are accent, per the prototype. */
  featured: {
    cardBg: 'var(--surface)',
    cardBorder: 'transparent',
    titleColor: 'var(--surface-foreground)',
    subColor: 'oklch(100% 0 0 / .62)',
    featColor: 'oklch(100% 0 0 / .84)',
    btnBg: 'var(--accent)',
    btnFg: 'var(--ink)',
    btnBorder: 'var(--accent)',
    checkColor: 'var(--accent)',
  },
}
