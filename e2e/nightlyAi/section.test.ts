import { describe, expect, it } from 'vitest'

import { addDecimals, buildSection, CALL_SETS, judgeAccuracy, judgeCalls, STEP_NAMES, type AiRow, type Step } from './section'

const row = (key: string, outcome = 'ok', cost = '0.0001'): AiRow => {
  const [client, purpose] = key.split('/')
  return { client, purpose, outcome, cost }
}

describe('judgeCalls', () => {
  const set = CALL_SETS.explain

  it('passes on ok rows with cost', () => {
    expect(judgeCalls([row('ai/explain')], set)).toEqual({ ok: true, detail: '' })
  })

  it('fails on a fake row and names it', () => {
    const v = judgeCalls([row('ai/explain', 'fake', '0')], set)
    expect(v.ok).toBe(false)
    expect(v.detail).toContain('fake')
  })

  it.each(['unavailable', 'refused', 'off', 'skipped_unavailable', 'skipped_refused'])('fails on outcome %s', (outcome) => {
    const v = judgeCalls([row('ai/explain', outcome)], set)
    expect(v.ok).toBe(false)
    expect(v.detail).toContain(outcome)
  })

  it.each(['0', '0.000000'])('fails on outcome ok with cost %s', (cost) => {
    const v = judgeCalls([row('ai/explain', 'ok', cost)], set)
    expect(v.ok).toBe(false)
    expect(v.detail).toContain(`cost ${cost}`)
  })

  it('fails on a missing required row and names the pair', () => {
    const v = judgeCalls([row('ai/spreadsheet')], set)
    expect(v.ok).toBe(false)
    expect(v.detail).toContain('ai/explain')
  })

  it('fails when one of several rows failed', () => {
    expect(judgeCalls([row('ai/explain'), row('ai/explain', 'unavailable')], set).ok).toBe(false)
  })

  it('ignores a row outside its set', () => {
    expect(judgeCalls([row('ai/document'), row('ai/explain', 'unavailable')], CALL_SETS.extraction).ok).toBe(true)
  })

  it('notes the jev rows of the extraction step without judging them', () => {
    const v = judgeCalls([row('ai/document'), row('jev/document_type', 'ok', '0'), row('jev/value_check', 'skipped_unavailable', '0')], CALL_SETS.extraction)
    expect(v.ok).toBe(true)
    for (const part of ['jev/document_type', 'jev/value_check', 'skipped_unavailable', 'cost 0']) expect(v.detail).toContain(part)
  })

  it('fails extraction on a fake jev row', () => {
    const v = judgeCalls([row('ai/document'), row('jev/document_type', 'fake', '0')], CALL_SETS.extraction)
    expect(v.ok).toBe(false)
    expect(v.detail).toContain('fake')
  })

  it('does not pass extraction on jev rows alone', () => {
    const v = judgeCalls([row('jev/document_type')], CALL_SETS.extraction)
    expect(v.ok).toBe(false)
    expect(v.detail).toContain('ai/document')
  })

  it('fails extraction on a failed line-item call', () => {
    expect(judgeCalls([row('ai/document'), row('ai/line_items', 'unavailable')], CALL_SETS.extraction).ok).toBe(false)
  })

  it('fails check-mapping on a jev row of cost zero and names the cost', () => {
    const v = judgeCalls([row('jev/mapping_check', 'ok', '0')], CALL_SETS['check-mapping'])
    expect(v.ok).toBe(false)
    expect(v.detail).toContain('cost 0')
  })
})

describe('judgeAccuracy', () => {
  it('compares at the boundary', () => {
    expect(judgeAccuracy(0.9, 0.9, false).ok).toBe(true)
    expect(judgeAccuracy(0.8999, 0.9, false).ok).toBe(false)
  })

  it('marks a provisional threshold on a pass and on a fail', () => {
    const pass = judgeAccuracy(0.97, 0.7, true)
    const fail = judgeAccuracy(0.5, 0.7, true)
    expect([pass.ok, fail.ok]).toEqual([true, false])
    for (const [v, acc] of [[pass, '0.9700'], [fail, '0.5000']] as const) {
      expect(v.detail).toContain('provisional')
      expect(v.detail).toContain(acc)
      expect(v.detail).toContain('0.70')
    }
  })

  it('drops the mark on a measured threshold', () => {
    const v = judgeAccuracy(0.97, 0.9, false)
    expect(v.ok).toBe(true)
    expect(v.detail).not.toContain('provisional')
  })
})

describe('addDecimals', () => {
  it('sums exactly', () => {
    expect(addDecimals(['0.1', '0.2', '0.000001'])).toBe('0.300001')
  })

  it('keeps whole numbers and the empty sum', () => {
    expect([addDecimals(['2', '3']), addDecimals([]), addDecimals(['0.5', '0.5'])]).toEqual(['5', '0', '1.0'])
  })

  it('refuses a value that is not a decimal', () => {
    expect(() => addDecimals(['0.1', '1e-3'])).toThrow()
  })
})

describe('buildSection', () => {
  const passed = (name: string): Step => ({ name, status: 'passed', duration_ms: 1 })

  it('keeps the five steps in order', () => {
    const s = buildSection([...STEP_NAMES].reverse().map(passed), 0.97, 0.7, true, '0.1')
    expect(s.steps.map((x) => x.name)).toEqual([...STEP_NAMES])
    expect(s.ok).toBe(true)
  })

  it('writes the threshold and its mark; a failed step makes ok false', () => {
    const steps = STEP_NAMES.map(passed)
    steps[4] = { name: 'accuracy', status: 'failed', duration_ms: 1, detail: 'low' }
    const s = buildSection(steps, 0.5, 0.7, true, null)
    expect(s).toMatchObject({ ok: false, threshold: 0.7, threshold_provisional: true, cost_usd: null })
    expect(typeof s.threshold).toBe('number')
  })

  it('reports a missing step as failed', () => {
    const s = buildSection(STEP_NAMES.slice(0, 4).map(passed), 1, 0.7, true, null)
    expect(s.ok).toBe(false)
    expect(s.steps[4]).toMatchObject({ name: 'accuracy', status: 'failed' })
  })
})
