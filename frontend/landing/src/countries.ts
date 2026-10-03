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
