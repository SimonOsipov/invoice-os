// Specs for the pure presentation helpers. These carry the design-system contract the
// screens depend on: token NAMES only (never literals), and a sparkline that survives the
// degenerate inputs the live data actually produces.
import { describe, expect, it } from 'vitest'

import { SPARK_HEIGHT, SPARK_WIDTH, healthTone, sentenceCase, severityStyle, sparkline, statusStyle, responseJSON, requestJSON } from './helpers'
import type { JobState, Severity } from './types'

const ALL_STATES: JobState[] = ['queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed', 'dead-letter']
const ALL_SEVERITIES: Severity[] = ['error', 'warn', 'info']

describe('statusStyle', () => {
  it('HLP-1: covers every job state', () => {
    for (const s of ALL_STATES) {
      expect(statusStyle(s), `state ${s}`).toBeDefined()
    }
  })

  // Raw colour literals are a documented design-drift defect — every value must be a
  // token reference so a token change reaches this console for free.
  it('HLP-2: emits only token references, never colour literals', () => {
    for (const s of ALL_STATES) {
      const st = statusStyle(s)
      for (const v of [st.bg, st.border, st.text]) {
        expect(v, `state ${s}`).toMatch(/^var\(--[a-z0-9-]+\)$/)
      }
    }
  })

  it('HLP-3: labels are uppercase and match the state', () => {
    expect(statusStyle('dead-letter').label).toBe('DEAD-LETTER')
    expect(statusStyle('accepted').label).toBe('ACCEPTED')
  })

  // The three failure states share one visual treatment on purpose: an operator triaging
  // a queue should see "broken" before they see "which kind of broken".
  it('HLP-4: rejected, failed and dead-letter all read red', () => {
    for (const s of ['rejected', 'failed', 'dead-letter'] as JobState[]) {
      expect(statusStyle(s).text, `state ${s}`).toBe('var(--status-red-text)')
    }
  })
})

describe('severityStyle', () => {
  it('HLP-5: covers every severity and emits only token references', () => {
    for (const s of ALL_SEVERITIES) {
      const st = severityStyle(s)
      expect(st.label).toBe(s.toUpperCase())
      for (const v of [st.bg, st.border, st.text]) {
        expect(v, `severity ${s}`).toMatch(/^var\(--[a-z0-9-]+\)$/)
      }
    }
  })
})

describe('sentenceCase', () => {
  it('HLP-6: turns a badge label into a timeline heading', () => {
    expect(sentenceCase('DEAD-LETTER')).toBe('Dead-letter')
    expect(sentenceCase('ACCEPTED')).toBe('Accepted')
  })
})

describe('sparkline', () => {
  it('HLP-7: starts with a moveto and continues with linetos, one per point', () => {
    const { line } = sparkline([1, 2, 3, 4])
    expect(line.startsWith('M')).toBe(true)
    expect(line.match(/L/g)).toHaveLength(3)
  })

  it('HLP-8: spans the full viewBox width', () => {
    const { line } = sparkline([5, 1, 9])
    expect(line.startsWith('M0.0 ')).toBe(true)
    expect(line).toContain(`L${SPARK_WIDTH.toFixed(1)} `)
  })

  // The Dead-letter card genuinely goes flat once every dead-letter job is re-driven; a
  // naive (max-min) divisor would emit NaN and blank the card.
  it('HLP-9: a flat series produces finite coordinates, not NaN', () => {
    const { line, area } = sparkline([0, 0, 0, 0])
    expect(line).not.toContain('NaN')
    expect(area).not.toContain('NaN')
  })

  it('HLP-10: the area closes back along the baseline', () => {
    const { area } = sparkline([1, 2])
    expect(area.endsWith(`L${SPARK_WIDTH} ${SPARK_HEIGHT} L0 ${SPARK_HEIGHT} Z`)).toBe(true)
  })

  // A single reading has no line to draw; returning empty strings renders nothing rather
  // than an `M NaN` path the browser silently discards.
  it('HLP-11: fewer than two points yields empty paths', () => {
    expect(sparkline([])).toEqual({ line: '', area: '' })
    expect(sparkline([7])).toEqual({ line: '', area: '' })
  })
})

describe('healthTone', () => {
  it('HLP-12: every tone emits token references only', () => {
    for (const t of ['green', 'amber', 'red'] as const) {
      const tone = healthTone(t)
      for (const v of [tone.dot, tone.stroke, tone.fill]) {
        expect(v, `tone ${t}`).toMatch(/^var\(--[a-z0-9-]+\)$/)
      }
    }
  })

  // A card must never pair one tone's dot with another's fill.
  it('HLP-13: amber and red are self-consistent', () => {
    expect(healthTone('amber')).toEqual({ dot: 'var(--status-amber-text)', stroke: 'var(--status-amber-text)', fill: 'var(--status-amber-bg)' })
    expect(healthTone('red')).toEqual({ dot: 'var(--status-red-text)', stroke: 'var(--status-red-text)', fill: 'var(--status-red-bg)' })
  })
})

describe('payload builders', () => {
  const job = { id: 'job_8f2a91', tin: 'TIN 20184412-0001', invoice: 'INV-2026-04417', app: 'AP-Sterling' }

  it('HLP-14: the request derives its idempotency key from the job id and strips the TIN prefix', () => {
    const out = requestJSON(job, 'sandbox')
    expect(JSON.parse(out)).toMatchObject({
      idempotency_key: 'idem_8f2a91',
      environment: 'sandbox',
      tenant_tin: '20184412-0001',
      app_target: 'AP-Sterling',
    })
  })

  it('HLP-15: every response variant is valid JSON and states its status', () => {
    const cases: [JobState, string][] = [
      ['accepted', 'ACCEPTED'],
      ['rejected', 'REJECTED'],
      ['dead-letter', 'ERROR'],
      ['failed', 'SCHEMA_ERROR'],
      ['pending', 'PENDING'],
      ['queued', 'PENDING'],
    ]
    for (const [state, status] of cases) {
      const parsed = JSON.parse(responseJSON({ state, invoice: 'INV-2026-04417' }))
      expect(parsed.status, `state ${state}`).toBe(status)
    }
  })
})
