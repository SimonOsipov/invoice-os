import { GROUPS, TOUR } from './content'

export type TourPhase = 'menu' | 'card'
export type TourState = { i: number; phase: TourPhase }
export type Rect = { x: number; y: number; w: number; h: number }
export type Win = { w: number; h: number }
export type CalloutPos = { left: number; top: number } | { left: number; bottom: number }
export type Callout = { step: string; title: string; text: string; showWatch: boolean; backOff: boolean; nextLabel: 'Next' | 'Finish' }

export const TOUR_START: TourState = { i: 0, phase: 'menu' }
export const CALLOUT_W = 360
export const CALLOUT_H = 280

const TOTAL = TOUR.length * 2
// Callout height plus its 30px of margins.
const K = CALLOUT_H + 30

export const tourStepNumber = (t: TourState): number => t.i * 2 + (t.phase === 'menu' ? 1 : 2)

export const tourButtonLabel = (t: TourState | null): string =>
  t ? `Tour ${tourStepNumber(t)} of ${TOTAL} · exit` : 'Take the tour'

export const tourStage = (t: TourState): number => TOUR[t.i].stage

export const tourNext = (t: TourState): TourState | null => {
  if (t.phase === 'menu') return { i: t.i, phase: 'card' }
  return t.i === TOUR.length - 1 ? null : { i: t.i + 1, phase: 'menu' }
}

export const tourBack = (t: TourState): TourState => {
  if (t.phase === 'card') return { i: t.i, phase: 'menu' }
  return t.i === 0 ? t : { i: t.i - 1, phase: 'card' }
}

export const tourCallout = (t: TourState): Callout => {
  const n = tourStepNumber(t)
  const stop = TOUR[t.i]
  const menu = t.phase === 'menu'
  const group = GROUPS.find((g) => g.id === stop.g)
  if (!group) throw new Error(`tour stop ${t.i} names unknown group ${stop.g}`)
  const pad = (v: number) => String(v).padStart(2, '0')
  return {
    step: `STEP ${pad(n)} OF ${pad(TOTAL)}`,
    title: menu ? `Open ${group.name}` : stop.t,
    text: menu ? group.one : stop.d,
    showWatch: !menu,
    backOff: n === 1,
    nextLabel: n === TOTAL ? 'Finish' : 'Next',
  }
}

export const spotRect = (phase: TourPhase, r: { left: number; top: number; width: number; height: number }): Rect =>
  phase === 'menu'
    ? { x: r.left - 4, y: r.top - 3, w: r.width + 8, h: r.height + 6 }
    : { x: r.left - 8, y: r.top - 8, w: r.width + 16, h: r.height + 16 }

export const cardScrollDelta = (cardTop: number, mainTop: number, barHeight: number, innerHeight: number): number => {
  const d = cardTop - (mainTop + barHeight + (innerHeight < 760 ? 16 : 56))
  return Math.abs(d) <= 4 ? 0 : d
}

export const calloutPos = (r: Rect | null, phase: TourPhase, win: Win): CalloutPos => {
  const { w: W, h: H } = win
  const x = r ? Math.round(Math.max(16, Math.min(r.x, W - 376))) : 0
  if (r && phase === 'menu') return { left: Math.round(r.x + r.w + 18), top: Math.round(Math.max(16, Math.min(r.y - 12, H - K))) }
  if (!r) return { left: Math.round(W / 2 - CALLOUT_W / 2), top: Math.round(H / 2 - 100) }
  if (r.y + r.h + 20 + CALLOUT_H < H) return { left: x, top: Math.round(r.y + r.h + 16) }
  if (r.y > K) return { left: x, bottom: Math.round(H - r.y + 16) }
  if (r.x + r.w + 16 <= W - 376) return { left: Math.round(r.x + r.w + 16), top: Math.round(Math.max(16, r.y)) }
  return { left: Math.round(Math.max(16, r.x - 16 - CALLOUT_W)), top: Math.round(Math.max(16, r.y)) }
}
