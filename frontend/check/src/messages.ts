import { SIZE_SENTENCE, TYPE_SENTENCE } from './copy'

// Gateway sentences (internal/gateway/readiness_check.go) and developer-worded 400s.
export const IP_SENTENCE = 'This network has run 10 checks today. Try again tomorrow.' // checkIPSentence
export const UNREADABLE_SENTENCE = 'We could not read this file.' // checkUnreadableSentence
export const UNAVAILABLE_SENTENCE = 'The check is unavailable. Try again in a few minutes.' // checkUnavailableSentence
export const SLOW_UPLOAD_SENTENCE = 'The upload took too long. Try again on a faster connection.' // checkSlowUploadSentence
export const ARMOR_SENTENCE = 'Too many checks from this network. Try again in an hour.'
export const EMAIL_SENTENCE = 'Enter a valid email address.'
export const CONSENT_SENTENCE = 'Tick the box to get your results by email.'
export const MAPPING_SENTENCE = 'We could not use this column mapping. Check that the invoice number column is right and try again.'
export const COLUMNS_SENTENCE = 'We could not read the columns of this file.'

export type Route = 'mapping' | 'run' | 'document'
export interface Refusal {
  text: string
}

const FORMAT_ERRORS = ['file must be CSV, XLSX, PDF or DOCX', 'file must be a CSV or XLSX spreadsheet']

// A gateway error is shown only as a string of 1 to 300 characters.
const sentence = (error: string | null, fallback: string): string =>
  error !== null && error.length >= 1 && error.length <= 300 ? error : fallback

function text(route: Route, status: number | null, error: string | null): string {
  switch (status) {
    case 429:
      return sentence(error, ARMOR_SENTENCE)
    case 422:
      return sentence(error, UNREADABLE_SENTENCE)
    case 408:
      return sentence(error, SLOW_UPLOAD_SENTENCE)
    case 413:
      return SIZE_SENTENCE
    case 400:
      if (error === 'email is invalid') return EMAIL_SENTENCE
      if (error === 'consent_text must be 1 to 500 characters') return CONSENT_SENTENCE
      if (error !== null && FORMAT_ERRORS.includes(error)) return TYPE_SENTENCE
      if (route === 'document') return UNREADABLE_SENTENCE
      return route === 'run' ? MAPPING_SENTENCE : COLUMNS_SENTENCE
    default:
      return UNAVAILABLE_SENTENCE
  }
}

export const refusal = (route: Route, status: number | null, error: string | null): Refusal => ({
  text: text(route, status, error),
})
