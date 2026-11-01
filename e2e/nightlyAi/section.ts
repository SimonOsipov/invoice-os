// Verdicts and the ai section of summary.json. Pure; main.ts does the I/O.
export type AiRow = { client: string; purpose: string; outcome: string; cost: string }
export type CallSet = { required: string; judged: string[]; noted: string[] }
export type Verdict = { ok: boolean; detail: string }
export type Step = { name: string; status: 'passed' | 'failed'; duration_ms: number; detail?: string }

export const STEP_NAMES = ['extraction', 'suggest-mapping', 'explain', 'check-mapping', 'accuracy'] as const

// Rows are "client/purpose". A noted row never reddens a step, except a fake one.
export const CALL_SETS: Record<'extraction' | 'suggest-mapping' | 'explain' | 'check-mapping', CallSet> = {
  extraction: { required: 'ai/document', judged: ['ai/document', 'ai/line_items'], noted: ['jev/value_check', 'jev/document_type'] },
  'suggest-mapping': { required: 'ai/spreadsheet', judged: ['ai/spreadsheet'], noted: [] },
  explain: { required: 'ai/explain', judged: ['ai/explain'], noted: [] },
  'check-mapping': { required: 'jev/mapping_check', judged: ['jev/mapping_check'], noted: [] },
}

const keyOf = (r: AiRow): string => `${r.client}/${r.purpose}`
const positive = (cost: string): boolean => /^\d*\.?\d*$/.test(cost) && /[1-9]/.test(cost)

export function judgeCalls(rows: AiRow[], set: CallSet): Verdict {
  const problems: string[] = []
  const notes: string[] = []
  if (!rows.some((r) => keyOf(r) === set.required)) problems.push(`no ${set.required} row`)
  for (const r of rows) {
    const key = keyOf(r)
    if (set.judged.includes(key)) {
      if (r.outcome === 'fake') problems.push(`${key} fake`)
      else if (r.outcome !== 'ok') problems.push(`${key} ${r.outcome}`)
      else if (!positive(r.cost)) problems.push(`${key} ok but cost ${r.cost}`)
    } else if (set.noted.includes(key)) {
      if (r.outcome === 'fake') problems.push(`${key} fake`)
      else notes.push(`noted ${key} ${r.outcome} cost ${r.cost}`)
    }
  }
  return { ok: problems.length === 0, detail: [...problems, ...notes].join('; ') }
}

export function judgeAccuracy(accuracy: number, threshold: number, provisional: boolean): Verdict {
  const ok = accuracy >= threshold
  const mark = provisional ? ' (provisional threshold)' : ''
  const detail = ok
    ? `accuracy ${accuracy.toFixed(4)} meets threshold ${threshold.toFixed(2)}${mark}`
    : `accuracy ${accuracy.toFixed(4)} is below threshold ${threshold.toFixed(2)}${mark}`
  return { ok, detail }
}

// Exact decimal sum: a float sum of per-call costs would drift.
export function addDecimals(values: string[]): string {
  if (values.length === 0) return '0'
  for (const v of values) if (!/^\d+(\.\d+)?$/.test(v)) throw new Error(`not a decimal: ${v.slice(0, 20)}`)
  const scale = Math.max(...values.map((v) => (v.split('.')[1] ?? '').length))
  const sum = values.reduce((acc, v) => {
    const [whole, frac = ''] = v.split('.')
    return acc + BigInt(whole + frac.padEnd(scale, '0'))
  }, 0n)
  if (scale === 0) return sum.toString()
  const digits = sum.toString().padStart(scale + 1, '0')
  return `${digits.slice(0, -scale)}.${digits.slice(-scale)}`
}

export type Section = {
  ok: boolean
  accuracy: number
  threshold: number
  threshold_provisional: boolean
  cost_usd: string | null
  steps: Step[]
}

export function buildSection(steps: Step[], accuracy: number, threshold: number, provisional: boolean, costUsd: string | null): Section {
  const ordered = STEP_NAMES.map((name): Step => steps.find((s) => s.name === name) ?? { name, status: 'failed', duration_ms: 0, detail: 'step did not run' })
  return {
    ok: ordered.every((s) => s.status === 'passed'),
    accuracy,
    threshold,
    threshold_provisional: provisional,
    cost_usd: costUsd,
    steps: ordered,
  }
}
