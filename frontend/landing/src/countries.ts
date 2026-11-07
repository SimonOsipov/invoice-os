import type { GlyphName } from './icons'

export type CountryIntel = {
  name: string
  authority: string
  focus: string
  href: string
  cardTitle: string
  cardSub: string
}

export const INTEL: CountryIntel = {
  name: 'Nigeria',
  authority: 'NRS · Merchant Buyer Solution',
  focus: 'MBS requirements & invoice validation',
  href: 'https://einvoice.nrs.gov.ng/',
  cardTitle: "Nigeria's e-invoicing requirements",
  cardSub: 'Required invoice fields, validation rules and transmission readiness.',
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
