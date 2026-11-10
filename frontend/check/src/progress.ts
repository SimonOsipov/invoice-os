import { STEP_LABELS } from './copy'
import { kindOf } from './flow'

export type Step = 'upload' | 'map' | 'details' | 'sent'

export type ItemState = 'current' | 'done' | 'skipped' | 'todo'
export interface ProgressItem {
  label: string
  state: ItemState
  mark: string
  skipNote?: string
}
export interface Progress {
  items: ProgressItem[]
  phone: { no: number; total: number; name: string; filled: number }
}

const LABELS = [STEP_LABELS.upload, STEP_LABELS.map, STEP_LABELS.email, STEP_LABELS.sent]
const CURRENT_INDEX: Record<Step, number> = { upload: 0, map: 1, details: 2, sent: 3 }

// No file counts as a spreadsheet. `details` is the Email step; at `sent` every item is done.
export function progress(step: Step, fileName: string | null): Progress {
  const isDocument = fileName !== null && (kindOf({ name: fileName }) ?? 'spreadsheet') !== 'spreadsheet'
  const cur = CURRENT_INDEX[step]
  const items = LABELS.map((label, i): ProgressItem => {
    if (i === 1 && isDocument) {
      const ext = fileName.slice(fileName.lastIndexOf('.') + 1).toUpperCase()
      return { label, state: 'skipped', mark: '–', skipNote: `Not needed for ${ext}` }
    }
    if (step === 'sent' || i < cur) return { label, state: 'done', mark: '✓' }
    return { label, state: i === cur ? 'current' : 'todo', mark: String(i + 1) }
  })
  const sequence = items.filter((it) => it.state !== 'skipped')
  const at = step === 'sent' ? sequence.length - 1 : sequence.findIndex((it) => it.state === 'current')
  return { items, phone: { no: at + 1, total: sequence.length, name: sequence[at].label, filled: at + 1 } }
}
