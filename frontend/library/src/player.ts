import { STEP_SECONDS } from './timing.ts'

export type Clock = { tenths: number; playing: boolean; ended: boolean }
export type ClockAction =
  | { type: 'tick' }
  | { type: 'toggle' }
  | { type: 'seek'; frac: number }
  | { type: 'jump'; step: number }

export const START: Clock = { tenths: 0, playing: true, ended: false }

const STEP_TENTHS = STEP_SECONDS * 10

export function nextClock(c: Clock, a: ClockAction, steps: number): Clock {
  const total = steps * STEP_TENTHS
  switch (a.type) {
    case 'tick':
      if (!c.playing) return c
      return c.tenths + 1 >= total ? { tenths: total, playing: false, ended: true } : { ...c, tenths: c.tenths + 1 }
    case 'toggle':
      return c.ended ? START : { ...c, playing: !c.playing }
    case 'seek':
      return { ...c, tenths: Math.min(total - 1, Math.max(0, Math.floor(a.frac * total))), ended: false }
    case 'jump':
      return { tenths: a.step * STEP_TENTHS, playing: true, ended: false }
  }
}
