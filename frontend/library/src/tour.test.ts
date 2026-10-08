import { describe, expect, it } from 'vitest'
import { STAGES, TOUR } from './content'
import {
  CALLOUT_H, CALLOUT_W, calloutPos, cardScrollDelta, spotRect, TOUR_START, tourBack, tourButtonLabel,
  tourCallout, tourNext, tourStage, tourStepNumber, type CalloutPos, type Rect, type TourState,
} from './tour'

const walk = (): TourState[] => {
  const out: TourState[] = []
  let s: TourState | null = TOUR_START
  while (s) { out.push(s); s = tourNext(s) }
  return out
}

describe('tour', () => {
  it('tourSteps_walkFourteenStepsInOrder', () => {
    expect(TOUR.length).toBe(7)
    const states = walk()
    expect(states.length).toBe(14)
    states.forEach((s, k) => expect(s).toEqual({ i: Math.floor(k / 2), phase: k % 2 === 0 ? 'menu' : 'card' }))
    expect(states.map(tourStepNumber)).toEqual(Array.from({ length: 14 }, (_, k) => k + 1))
    expect(tourNext(states[13])).toBeNull()
  })

  it('tourBack_invertsTourNextAndStopsAtTheFirstStep', () => {
    const states = walk()
    for (const s of states.slice(0, 13)) expect(tourBack(tourNext(s)!)).toEqual(s)
    expect(tourBack(TOUR_START)).toEqual(TOUR_START)
    expect(tourBack({ i: 1, phase: 'menu' })).toEqual({ i: 0, phase: 'card' })
    expect(tourBack({ i: 3, phase: 'card' })).toEqual({ i: 3, phase: 'menu' })
  })

  it('tourButtonLabel_countsStepsOfFourteen', () => {
    expect(tourButtonLabel(null)).toBe('Take the tour')
    expect(tourButtonLabel(TOUR_START)).toBe('Tour 1 of 14 · exit')
    expect(tourButtonLabel({ i: 0, phase: 'card' })).toBe('Tour 2 of 14 · exit')
    expect(tourButtonLabel({ i: 3, phase: 'menu' })).toBe('Tour 7 of 14 · exit')
    expect(tourButtonLabel({ i: 6, phase: 'card' })).toBe('Tour 14 of 14 · exit')
    expect(new Set(walk().map(tourButtonLabel)).size).toBe(14)
  })

  it('tourStage_isTheStopStageAndTheSeventhHasNone', () => {
    const stages = walk().map(tourStage)
    expect(stages).toEqual([0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, -1, -1])
    expect(TOUR[6].g).toBe('reports')
    expect(STAGES.some(([, gid]) => gid === 'reports')).toBe(false)
  })

  it('tourCallout_menuStepNamesTheGroup', () => {
    expect(tourCallout({ i: 0, phase: 'menu' })).toEqual({
      step: 'STEP 01 OF 14', title: 'Open Invoices', text: 'Import, create and track every invoice.',
      showWatch: false, backOff: true, nextLabel: 'Next',
    })
    const last = tourCallout({ i: 6, phase: 'menu' })
    expect(last.step).toBe('STEP 13 OF 14')
    expect(last.title).toBe('Open Reports & analytics')
    expect(last.text).toBe('Readiness and results at a glance.')
  })

  it('tourCallout_cardStepShowsTheStopTitleAndText', () => {
    const c = tourCallout({ i: 0, phase: 'card' })
    expect(c.step).toBe('STEP 02 OF 14')
    expect(c.title).toBe('Start with the data you already have')
    expect(c.text).toBe('Import a file from your accounting system. Each row becomes an invoice, and unreadable rows are listed with the reason.')
    expect(c.showWatch).toBe(true)
    expect(c.backOff).toBe(false)
    const k = tourCallout({ i: 5, phase: 'card' })
    expect(k.step).toBe('STEP 12 OF 14')
    expect(k.title).toBe('Keep the evidence')
  })

  it('tourCallout_controlsFollowTheStep', () => {
    const cs = walk().map(tourCallout)
    expect(cs.filter((c) => c.showWatch).length).toBe(7)
    expect(cs.filter((c) => c.backOff).length).toBe(1)
    expect(cs[0].backOff).toBe(true)
    expect(cs.filter((c) => c.nextLabel === 'Finish').length).toBe(1)
    expect(cs[13].nextLabel).toBe('Finish')
    expect(cs.filter((c) => c.nextLabel === 'Next').length).toBe(13)
  })

  it('spotRect_enclosesTheTargetByThePhasePadding', () => {
    expect(spotRect('menu', { left: 10, top: 100, width: 267, height: 36 })).toEqual({ x: 6, y: 97, w: 275, h: 42 })
    expect(spotRect('card', { left: 330, top: 300, width: 340, height: 300 })).toEqual({ x: 322, y: 292, w: 356, h: 316 })
    const targets = [
      { left: 0, top: 0, width: 0, height: 0 }, { left: 10, top: 20, width: 0, height: 0 },
      { left: 50, top: 60, width: 100, height: 40 }, { left: 300, top: 5, width: 1, height: 900 },
      { left: 700, top: 700, width: 267, height: 36 },
    ]
    for (const phase of ['menu', 'card'] as const) {
      for (const t of targets) {
        const s = spotRect(phase, t)
        expect(t.left - s.x).toBe(s.x + s.w - (t.left + t.width))
        expect(t.top - s.y).toBe(s.y + s.h - (t.top + t.height))
        expect(t.left - s.x).toBeGreaterThan(0)
        expect(t.top - s.y).toBeGreaterThan(0)
      }
    }
  })

  it('cardScrollDelta_placesTheCardBelowTheStepper', () => {
    expect(cardScrollDelta(400, 0, 64, 768)).toBe(280)
    expect(cardScrollDelta(400, 0, 64, 700)).toBe(320)
    expect(cardScrollDelta(123, 0, 64, 768)).toBe(0)
    expect(cardScrollDelta(125, 0, 64, 768)).toBe(5)
    for (const inner of [600, 759, 760, 900]) {
      for (const cardTop of [0, 50, 119, 120, 124, 125, 300, 1200]) {
        const delta = cardScrollDelta(cardTop, 10, 64, inner)
        const want = 64 + (inner < 760 ? 16 : 56)
        const left = cardTop - delta - 10
        expect(left === want || (delta === 0 && Math.abs(left - want) <= 4)).toBe(true)
      }
    }
  })

  it('calloutPos_followsTheSixBranches', () => {
    const W = 1440
    const win = { w: W, h: 900 }
    expect(calloutPos({ x: 6, y: 97, w: 208, h: 42 }, 'menu', win)).toEqual({ left: 232, top: 85 })
    expect(calloutPos({ x: 6, y: 850, w: 208, h: 42 }, 'menu', win)).toEqual({ left: 232, top: 590 })
    expect(calloutPos(null, 'card', win)).toEqual({ left: 540, top: 350 })
    expect(calloutPos({ x: 320, y: 104, w: 356, h: 316 }, 'card', win)).toEqual({ left: 320, top: 436 })
    expect(calloutPos({ x: 1200, y: 104, w: 356, h: 316 }, 'card', win)).toEqual({ left: 1064, top: 436 })
    expect(calloutPos({ x: 2, y: 104, w: 356, h: 316 }, 'card', win)).toEqual({ left: 16, top: 436 })
    expect(calloutPos({ x: 320, y: 340, w: 356, h: 316 }, 'card', { w: W, h: 700 })).toEqual({ left: 320, bottom: 376 })
    const short = { w: W, h: 600 }
    expect(calloutPos({ x: 320, y: 104, w: 356, h: 316 }, 'card', short)).toEqual({ left: 692, top: 104 })
    expect(calloutPos({ x: 320, y: 310, w: 356, h: 316 }, 'card', short)).toEqual({ left: 692, top: 310 })
    expect(calloutPos({ x: 320, y: 311, w: 356, h: 316 }, 'card', short)).toEqual({ left: 320, bottom: 305 })
    expect(calloutPos({ x: 700, y: 104, w: 356, h: 316 }, 'card', { w: 1280, h: 600 })).toEqual({ left: 324, top: 104 })
  })

  it('calloutPos_staysInsideTheWindowAndOffTheSpotlight', () => {
    const seen = new Set<string>()
    let rows = 0
    const check = (r: Rect, phase: 'menu' | 'card', W: number, H: number) => {
      const p: CalloutPos = calloutPos(r, phase, { w: W, h: H })
      const top = 'top' in p ? p.top : H - p.bottom - CALLOUT_H
      const left = p.left
      expect(left).toBeGreaterThanOrEqual(0)
      expect(left + CALLOUT_W).toBeLessThanOrEqual(W)
      expect(top).toBeGreaterThanOrEqual(0)
      expect(top + CALLOUT_H).toBeLessThanOrEqual(H)
      const overlaps = left < r.x + r.w && left + CALLOUT_W > r.x && top < r.y + r.h && top + CALLOUT_H > r.y
      expect(overlaps).toBe(false)
      if (phase === 'menu') seen.add('menu')
      else if ('bottom' in p) seen.add('above')
      else if (top > r.y + r.h) seen.add('below')
      else if (left >= r.x + r.w) seen.add('right')
      else seen.add('left')
      rows++
    }
    for (const W of [2560, 1920, 1440, 1280]) {
      for (const H of [700, 768, 900, 1080]) {
        for (const y of [80, 320, 560]) check({ x: 6, y, w: 276, h: 42 }, 'menu', W, H)
        for (const [x, y] of [[320, 104], [700, 104], [700, 300], [700, 400]]) check({ x, y, w: 356, h: 316 }, 'card', W, H)
      }
    }
    expect(rows).toBe(112)
    for (const W of [2560, 1920, 1440, 1280]) {
      for (const H of [600, 640]) {
        for (const x of [328, 700]) check({ x, y: 72, w: 356, h: 316 }, 'card', W, H)
      }
    }
    expect([...seen].sort()).toEqual(['above', 'below', 'left', 'menu', 'right'])
  })
})
