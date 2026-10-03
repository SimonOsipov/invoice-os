import type { GlyphName } from './icons'

export type CountryId = 'NG' | 'KE' | 'ZA'

export const COUNTRY_ORDER = ['NG', 'KE', 'ZA'] as const

export type CountryCoverage = {
  name: string
  code: string
  pill: string
  pillTone: 'success' | 'progress'
  title: string
  body: string
  context: string
  flowLabel: string
  steps: readonly [string, string, string, string]
  disclaimer: string
  href: string
  linkLabel: string
}

export const COVERAGE: Record<CountryId, CountryCoverage> = {
  NG: {
    name: 'Nigeria',
    code: 'NG',
    pill: 'Launch market',
    pillTone: 'success',
    title: 'Our starting point. Your next step.',
    body: "Invoice validation, internal approvals and organised records, built around Nigeria's e-invoicing context.",
    context: 'NRS · Merchant Buyer Solution',
    flowLabel: 'INVOICE WORKFLOW',
    steps: ['Capture invoice data', 'Validate required fields', 'Approve internally', 'Submit to the NRS'],
    disclaimer: 'Custom integrations with your ERP, CRM or accounting system are available. Talk to our team about your setup.',
    href: 'https://einvoice.nrs.gov.ng/',
    linkLabel: 'NRS e-invoicing portal ↗',
  },
  KE: {
    name: 'Kenya',
    code: 'KE',
    pill: 'Planned expansion',
    pillTone: 'progress',
    title: 'A country workflow for Kenya.',
    body: "Our expansion plans follow Kenya's eTIMS environment, with country-specific invoice checks and review workflows.",
    context: 'KRA · eTIMS',
    flowLabel: 'PLANNED WORKFLOW',
    steps: ['Map invoice data', 'Check eTIMS requirements', 'Route exceptions for review', 'Prepare the country workflow'],
    disclaimer: 'Kenya is a planned market. Launch timing and production integration will be confirmed as the rollout develops.',
    href: 'https://www.kra.go.ke/',
    linkLabel: 'KRA eTIMS guidance ↗',
  },
  ZA: {
    name: 'South Africa',
    code: 'ZA',
    pill: 'Planned expansion',
    pillTone: 'progress',
    title: 'Prepare for what comes next.',
    body: "Our roadmap follows South Africa's evolving VAT landscape, including SARS proposals for a Digital VAT Model.",
    context: 'SARS · VAT modernisation',
    flowLabel: 'PLANNED WORKFLOW',
    steps: ['Follow SARS updates', 'Assess proposed changes', 'Review the impact on workflows', 'Prepare for confirmed rules'],
    disclaimer: 'South Africa is a planned market. The Digital VAT Model is a proposal under consultation, not a live ASComply connection.',
    href: 'https://www.sars.gov.za/',
    linkLabel: 'SARS Digital VAT consultation ↗',
  },
}

export const LEGEND = [
  { label: 'First launch', color: 'var(--accent)', border: 'transparent' },
  { label: 'Planned expansion', color: 'var(--sage)', border: 'transparent' },
  { label: 'Future vision', color: 'var(--on-dark-10)', border: 'var(--on-dark-20)' },
] as const

export const ROADMAP = [
  { n: '01', t: 'Nigeria', s: 'Our first launch market', chev: 'inline-flex' },
  { n: '02', t: 'Kenya and South Africa', s: 'Planned expansion markets', chev: 'inline-flex' },
  { n: '03', t: 'Pan-African ambition', s: 'Wider coverage, country by country', chev: 'none' },
] as const

export type CountryIntel = {
  name: string
  authority: string
  focus: string
  href: string
  tag: string
  cardTitle: string
  cardSub: string
}

export const INTEL: Record<CountryId, CountryIntel> = {
  NG: {
    name: 'Nigeria',
    authority: 'NRS · Merchant Buyer Solution',
    focus: 'MBS requirements & invoice validation',
    href: 'https://einvoice.nrs.gov.ng/',
    tag: 'First launch',
    cardTitle: "Nigeria's e-invoicing requirements",
    cardSub: 'Required invoice fields, validation rules and transmission readiness.',
  },
  KE: {
    name: 'Kenya',
    authority: 'KRA · eTIMS',
    focus: 'eTIMS system requirements',
    href: 'https://www.kra.go.ke/',
    tag: 'Planned',
    cardTitle: "Kenya's eTIMS guidance",
    cardSub: 'Invoice data mapping, system requirements and exception handling.',
  },
  ZA: {
    name: 'South Africa',
    authority: 'SARS · VAT modernisation',
    focus: 'VAT developments & proposed digital reporting',
    href: 'https://www.sars.gov.za/',
    tag: 'Planned',
    cardTitle: "South Africa's VAT modernisation proposals",
    cardSub: 'Proposed changes, their scope and implementation status.',
  },
}

export type IntelStep = {
  n: string
  label: string
  icon: GlyphName
  step: string
  title: string
  body: string
  card: string
  status: string
}

export const INTEL_STEPS: readonly IntelStep[] = [
  {
    n: '01',
    label: 'Monitor',
    icon: 'file-search',
    step: '01 / SOURCE REVIEW',
    title: 'Start at the official source.',
    body: 'Follow tax authority guidance and regulatory notices by country. Keep the source, publication date and jurisdiction attached to each update.',
    card: 'Source review',
    status: 'Official guidance enters the review queue.',
  },
  {
    n: '02',
    label: 'Understand',
    icon: 'sparkles',
    step: '02 / AI-ASSISTED ANALYSIS',
    title: 'Explain the change clearly.',
    body: 'Use AI assistance to summarise the update and identify potential effects on invoice data, validation rules and business workflows.',
    card: 'AI-assisted analysis',
    status: 'A draft summary and impact assessment are prepared.',
  },
  {
    n: '03',
    label: 'Review',
    icon: 'user-check',
    step: '03 / HUMAN REVIEW',
    title: 'People make the decision.',
    body: 'A compliance specialist checks the source, confirms the interpretation and decides what needs action. AI supports the review; a person approves it.',
    card: 'Human review',
    status: 'Interpretation and business impact are checked.',
  },
  {
    n: '04',
    label: 'Apply',
    icon: 'workflow',
    step: '04 / APPROVED WORKFLOW UPDATE',
    title: 'Turn approvals into workflow.',
    body: 'Prepare country-specific checks and tasks, assign an owner and keep a record of the approved update. Give finance and technology teams the same context.',
    card: 'Approved workflow update',
    status: 'Reviewed changes become clear, accountable tasks.',
  },
]
