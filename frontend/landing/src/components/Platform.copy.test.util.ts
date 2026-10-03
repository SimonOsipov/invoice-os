// Platform copy retyped here, never imported from data.tsx, so a drifting data file fails the tests.
// The Submit body and tags are the approved wording, not the prototype's.
import type { GlyphName } from '../icons'

export type PlatformCopy = {
  id: 'validate' | 'approve' | 'submit'
  tabText: string
  tabIcon: GlyphName
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
  rows: [string, string][]
}

export const PLATFORM_COPY: PlatformCopy[] = [
  {
    id: 'validate',
    tabText: '01Validate',
    tabIcon: 'shield-check',
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
    rows: [
      ['Buyer & seller details', 'Passed'],
      ['Tax calculation & totals', 'Passed'],
      ['Invoice number & duplicates', 'Passed'],
      ['Required invoice fields', 'Passed'],
    ],
  },
  {
    id: 'approve',
    tabText: '02Approve',
    tabIcon: 'user-check',
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
    rows: [
      ['Invoice created', 'Complete'],
      ['Reviewed by finance', 'Complete'],
      ['Approved by authorised user', 'Complete'],
      ['Approval trail recorded', 'Complete'],
    ],
  },
  {
    id: 'submit',
    tabText: '03Submit',
    tabIcon: 'send',
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
    rows: [
      ['Approved invoice submitted', 'Sent'],
      ['Authority response received', 'Received'],
      ['Submission status tracked', 'Tracked'],
      ['QR code and audit trail generated', 'Generated'],
    ],
  },
]

export const PLATFORM_CAPABILITIES: [GlyphName, string, string][] = [
  ['layout-dashboard', 'One invoice workspace', 'Manage drafts, line items, credit notes and invoice records.'],
  ['key-round', 'The right access', 'Give your team clear roles across creation, review and approval.'],
  ['chart-column', 'Useful visibility', 'Review invoice volumes, tax summaries and validation results.'],
]

export const PLATFORM_SIDE_COPY = 'Prove and organise your invoices. Less time moving between tools. More confidence in every step.'
