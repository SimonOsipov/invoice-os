import { describe, expect, it } from 'vitest'
import { formatSeconds, STEP_SECONDS, stepAt } from './timing'

describe('timing', () => {
  it('timing_formatsSecondsAsTheCardDuration', () => {
    expect(formatSeconds(13.6)).toBe('0:13')
    expect(formatSeconds(10.2)).toBe('0:10')
    expect(formatSeconds(0)).toBe('0:00')
    expect(formatSeconds(59.99)).toBe('0:59')
    expect(formatSeconds(60)).toBe('1:00')
    expect(formatSeconds(125)).toBe('2:05')
    expect(formatSeconds(-1)).toBe('0:00')
    expect(formatSeconds(4 * STEP_SECONDS)).toBe('0:13')
  })

  it('stepAt_mapsElapsedSecondsToTheStep', () => {
    const at = [0, 3.39, 3.4, 6.8, 10.2, 13.6, 99, -1].map((s) => stepAt(s, 4))
    expect(at).toEqual([0, 0, 1, 2, 3, 3, 3, 0])
    expect(stepAt(10.2, 3)).toBe(2)
    for (let i = 0; i <= 136; i++) expect(stepAt(i / 10, 4)).toBe(Math.min(3, Math.floor(i / 34)))
  })
})
