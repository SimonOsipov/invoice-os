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
export const STEP_LABELS = { upload: 'Upload', map: 'Match columns', email: 'Email', sent: 'Sent' } as const
