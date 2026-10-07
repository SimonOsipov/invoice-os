import { describe, expect, it } from 'vitest'
import { formatSeconds, STEP_SECONDS } from './timing'

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
})
