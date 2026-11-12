import { SIZE_SENTENCE, TYPE_SENTENCE } from './copy'
import { canSubmitMapping, classifyPickedFile, MAX_UPLOAD_BYTES, type Mapping } from './fields'

type Picked = { name: string; size: number }

// Extension only: the gateway picks the format by file name, so no declared type is passed.
export const kindOf = (f: Pick<Picked, 'name'>) => classifyPickedFile(f.name, '')

export function fileVerdict(f: Picked): string {
  if (kindOf(f) === null) return TYPE_SENTENCE
  return f.size > MAX_UPLOAD_BYTES ? SIZE_SENTENCE : ''
}

export interface SendState {
  file: Picked
  email: string
  ticked: boolean
  fields: Mapping | null
}

// The design's shape; the gateway's invalidEmail stays the judge.
export const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/

export function canSend(s: SendState): boolean {
  if (!EMAIL_RE.test(s.email.trim()) || !s.ticked) return false
  return kindOf(s.file) === 'spreadsheet' ? canSubmitMapping(s.fields) : true
}
