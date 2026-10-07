import { describe, expect, it } from 'vitest'
import { nextClock, START, type Clock } from './player'
import { stepAt } from './timing'

const tick = (c: Clock, steps = 4) => nextClock(c, { type: 'tick' }, steps)
const toggle = (c: Clock) => nextClock(c, { type: 'toggle' }, 4)
const seek = (c: Clock, frac: number) => nextClock(c, { type: 'seek', frac }, 4)

describe('player clock', () => {
  it('clock_startsPlayingAtZero', () => {
    expect(START).toEqual({ tenths: 0, playing: true, ended: false })
  })

  it('clock_tickAdvancesOnlyWhilePlaying', () => {
    expect(tick({ tenths: 5, playing: true, ended: false })).toEqual({ tenths: 6, playing: true, ended: false })
    const paused = { tenths: 5, playing: false, ended: false }
    expect(tick(paused)).toEqual(paused)
  })

  it('clock_stopsOnTheLastStepAtTheEnd', () => {
    const end = { tenths: 136, playing: false, ended: true }
    const last = tick({ tenths: 135, playing: true, ended: false })
    expect(last).toEqual(end)
    expect(stepAt(last.tenths / 10, 4)).toBe(3)
    expect(tick(last)).toEqual(end)
    let c = START
    for (let i = 0; i < 136; i++) c = tick(c)
    expect(c).toEqual(end)
  })

  it('clock_togglePausesResumesAndReplays', () => {
    const paused = toggle(START)
    expect(paused.playing).toBe(false)
    expect(toggle(paused).playing).toBe(true)
    expect(toggle({ tenths: 136, playing: false, ended: true })).toEqual(START)
  })

  it('clock_seekMovesWithoutEnding', () => {
    expect(seek(START, 0.5).tenths).toBe(68)
    expect(seek(START, 1).tenths).toBe(135)
    expect(seek(START, -0.2).tenths).toBe(0)
    expect(seek({ tenths: 136, playing: false, ended: true }, 0.5)).toEqual({ tenths: 68, playing: false, ended: false })
    expect(seek(START, 0.5).playing).toBe(true)
  })

  it('clock_jumpMovesToTheStepAndPlays', () => {
    const c = nextClock({ tenths: 3, playing: false, ended: false }, { type: 'jump', step: 2 }, 4)
    expect(c).toEqual({ tenths: 68, playing: true, ended: false })
    expect(stepAt(c.tenths / 10, 4)).toBe(2)
  })
})
