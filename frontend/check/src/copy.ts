import { ACCEPTED_PICKED_TYPES, MAX_UPLOAD_BYTES } from './fields'

// Pinned to the Go constants by whatYouGet.limits.test.ts.
export const MAX_INVOICES = 2 // publiccheck.MaxInvoices
export const MAX_RUNS_PER_EMAIL = 3 // readinesscheck.MaxRunsPerEmail
const MAX_MIB = MAX_UPLOAD_BYTES / 1024 / 1024
export const FILE_TYPE_NAMES = ACCEPTED_PICKED_TYPES.map((r) => r.ext.slice(1).toUpperCase())

export const CHECK_TITLE = 'Free e-invoice readiness check — ASComply Africa'
export const CHECK_DESCRIPTION =
  'Upload one invoice file and get the MBS rule results for its first 2 invoices by email. Free, no account needed.'
export const CHECK_H1 = 'Check your invoices against the MBS rules'
export const CHECK_INTRO =
  'Upload one CSV, XLSX, PDF or DOCX file of up to 15 MiB. We check the first 2 invoices in it and email you the results. No account needed.'
export const SENT_TEXT = (email: string): string => `Your results are on their way to ${email}.`
export const SENT_NOTE = 'They come from no-reply@ascomply.com. Check your spam folder if you cannot find them.'
export const SENDING_TEXT = 'Checking your file. This can take a minute or two.'
export const READING_TEXT = 'Reading the columns…'
export const TYPE_SENTENCE = 'Choose a CSV, XLSX, PDF or DOCX file.'
export const SIZE_SENTENCE = 'This file is over 15 MiB. Choose a smaller file.'
export const UPLOAD_STEP_NO = '01 · Upload'
export const UPLOAD_TITLE = 'Add one invoice file'
export const FILE_LABEL = 'Invoice file'
export const DROP_TITLE = 'Drag your invoice file here'
export const DROP_TITLE_OVER = 'Drop it here to start'
export const DROP_OR = 'or'
export const DROP_BUTTON = 'Choose a file'
export const DROP_HELP = `One file, up to ${MAX_MIB} MiB. We check its first ${MAX_INVOICES} invoices.`
export const FILE_PRIVACY = 'Your file is used only for this check and deleted when it finishes.'
export const STEP_LABELS = { upload: 'Upload', map: 'Match columns', email: 'Email', sent: 'Sent' } as const
export const CHECK_EYEBROW = 'FREE E-INVOICE READINESS CHECK'
export const COMPANY_LINE = '© 2026 ASComply Africa Limited · Lagos, Nigeria'
export const TRUST_CUES = [
  { glyph: 'circle-check', title: 'Free', text: `${MAX_RUNS_PER_EMAIL} checks per email address` },
  { glyph: 'lock', title: 'No account', text: 'Just an email for the results' },
  { glyph: 'trash-2', title: 'File not kept', text: 'Deleted when the check finishes' },
  { glyph: 'mail', title: 'Results by email', text: 'Never shown on this page' },
] as const

export const WHAT_EYEBROW = 'WHAT YOU GET'
export const WHAT_H2 = ['Two invoices, checked rule by rule.', 'The results land in your inbox.'] as const
export const WHAT_RULES = {
  title: 'What the MBS rules are',
  text: "MBS is the e-invoicing system Nigeria's tax authority uses to receive and validate invoices. To be accepted, an invoice has to carry a set of required fields in a set format: the supplier's and buyer's details and TINs, the invoice number and dates, the amounts and the VAT.",
} as const
export const WHAT_EMAIL = {
  title: 'What the email tells you',
  text: 'For each of the first 2 invoices in your file: every rule it fails, with the field the rule checked and the rule’s message. An invoice that fails no rule says “Passes every rule”. Rules that only warn are listed under Warnings, and rules that need your company profile are listed as Not checked. You see what to fix in your invoicing before it matters.',
} as const
export const WHAT_LIMITS = [
  { n: String(MAX_INVOICES), caption: 'invoices checked per file' },
  { n: String(MAX_RUNS_PER_EMAIL), caption: 'free checks per email address' },
  { n: `${MAX_MIB} MiB`, caption: 'largest file, one at a time' },
  { n: String(FILE_TYPE_NAMES.length), caption: `file types: ${FILE_TYPE_NAMES.join(', ')}` },
] as const
export const WHAT_CTA = 'Check a file'
// Subject, sender and pass text are the real mail's (accountmail.ReadinessCheckSubject, sender.From, readiness_check.html);
// labels and messages are fields.go and the last rule-set migration. whatYouGet.mail.test.ts pins each.
export const EXAMPLE_MAIL = {
  from: 'no-reply@ascomply.com',
  subject: 'Your ASComply readiness check results',
  pass: 'Passes every rule',
  invoices: [
    {
      number: 'INV-1041',
      rows: [
        { label: 'Supplier TIN', value: 'Supplier TIN is required.' },
        { label: 'Buyer TIN', value: 'Buyer TIN, when present, must be NNNNNNNN-NNNN or an RN- incorporation number.' },
      ],
    },
    { number: 'INV-1042', rows: [] },
  ],
  line: 'Each failed rule shows its field and the rule’s message.',
  caption:
    'Illustrative. The real email also lists each invoice’s date, amounts and buyer from your file, and any warnings or rules not checked.',
} as const

export const FIELDS_TITLE = 'Invoice fields'
export const FIELDS_LEGEND = 'Required. Leave a field on “Not in this file” if your file has no column for it.'
export const GROUP_NONE = 'No columns matched yet'
export const MATCHED_LINE = (n: number): string => `${n} matched from your file`
export const REVIEW = 'Review'
export const HIDE = 'Hide'
export const SUGGESTED = 'Suggested'
export const NOT_IN_FILE = 'Not in this file'
export const SUPPLIER_HINT = 'A supplier name or TIN that is not in this file fails its rule in your results.'
export const MAP_STEP_NO = '02 · Match your columns'
export const MAP_TITLE = 'Tell us which column is which'
export const MAP_COLUMNS = (n: number): string => `· ${n} ${n === 1 ? 'column' : 'columns'}`
export const MAP_SUMMARY = (x: number, n: number): string => `We matched ${x} of ${n} fields to your columns. Review the gaps below.`
export const MAP_UNUSED = (unused: { letter: string; header: string }[], total: number): string =>
  `${unused.length} of your ${total} columns ${unused.length === 1 ? 'is' : 'are'} not used yet: ${unused.map((c) => `${c.letter} ${c.header}`).join(', ')}. Fields with no column stay on “${NOT_IN_FILE}”.`
export const MAP_ALL_USED = 'Every column in your file is matched to a field.'
export const PREVIEW_TITLE = 'Your columns'
export const PREVIEW_LABEL = 'Preview of your columns'
export const PREVIEW_MORE = (n: number): string => `Show all ${n} columns`
export const PREVIEW_LESS = 'Show fewer'
export const NOT_USED = 'Not used'
export const CHOOSE_ANOTHER = 'Choose another file'
export const CONTINUE = 'Continue'
export const NEED_INVOICE_NO = 'Choose the Invoice number column to continue.'
