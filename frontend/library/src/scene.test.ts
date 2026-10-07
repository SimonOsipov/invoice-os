import { describe, expect, it } from 'vitest'
import { FEATURES } from './content.ts'
import { CHIP, sceneState, THUMB_STEP, thumbStep } from './scene.ts'
import type { SceneState } from './scene.ts'
import type { Feature, Scene } from './types.ts'

const feat = (id: string) => {
  const f = FEATURES.find((x) => x.id === id)
  if (!f) throw new Error(`no feature ${id}`)
  return f
}
const at = <K extends SceneState['kind']>(id: string, idx: number, kind: K) => {
  const s = sceneState(feat(id).sc, idx)
  if (s.kind !== kind) throw new Error(`${id} is ${s.kind}`)
  return s as Extract<SceneState, { kind: K }>
}

describe('scene', () => {
  it('sceneState_listAppliesSetShowFocusCumulativelyAndBannerPerStep', () => {
    const s0 = at('import-files', 0, 'list')
    expect(s0.rows).toHaveLength(0)
    expect(s0.banner).toBe('sahara-foods-june.csv · 42 rows')
    expect(s0.stepNum).toBe('01 / 04')
    const s1 = at('import-files', 1, 'list')
    expect(s1.rows).toHaveLength(2)
    expect(s1.rows.map((r) => r.focused)).toEqual([true, false])
    expect(s1.banner).toBe('Mapped 9 of 9 columns')
    const s2 = at('import-files', 2, 'list')
    expect(s2.rows.map((r) => r.status)).toEqual(['valid', 'valid', 'valid', 'new'])
    expect(s2.rows.map((r) => r.focused)).toEqual([false, false, false, true])
    expect(s2.banner).toBeNull()
    expect(at('import-files', 3, 'list').rows.map((r) => r.status)).toEqual(['valid', 'valid', 'valid', 'error'])
  })

  it('sceneState_listWithoutShow0ShowsEveryRow', () => {
    expect(at('invoice-status', 0, 'list').rows).toHaveLength(4)
    const r = at('invoice-status', 1, 'list').rows[1]
    expect(r.status).toBe('error')
    expect(r.focused).toBe(true)
  })

  it('sceneState_formRevealsMarksAndMessages', () => {
    const f = feat('create-invoice')
    const s0 = at('create-invoice', 0, 'form')
    expect(s0.fields.map((x) => x.shown)).toEqual([true, true, false, false, false])
    expect(s0.fields[0].val).toBe(f.sc.kind === 'form' ? f.sc.fields[0].v : '')
    expect(s0.fields[1].val).toBe(f.sc.kind === 'form' ? f.sc.fields[1].v : '')
    expect(s0.fields.slice(2).map((x) => x.val)).toEqual(['', '', ''])
    expect(s0.fields.map((x) => x.focused)).toEqual([true, false, false, false, false])
    expect(s0.msg).toBeNull()
    const s2 = at('create-invoice', 2, 'form')
    expect(s2.fields.some((x) => x.focused)).toBe(false)
    expect(s2.fields.map((x) => x.mark)).toEqual([null, 'ok', null, 'ok', 'ok'])
    expect(s2.msg).toEqual({ text: '12 fields checked · 0 errors', tone: 'ok' })
  })

  it('sceneState_formFixReplacesTheValueAndMarksAccumulate', () => {
    const s0 = at('validate', 0, 'form')
    expect(s0.fields[0].mark).toBe('err')
    expect(s0.fields[0].val).toBe('2018441-0001')
    expect(s0.msg?.tone).toBe('err')
    const s1 = at('validate', 1, 'form')
    expect(s1.fields[0].mark).toBe('fix')
    expect(s1.fields[0].val).toBe('20184412-0001')
    expect(s1.fields.slice(1).map((x) => x.mark)).toEqual(['ok', 'ok', 'ok', 'ok'])
    expect(s1.msg).toBeNull()
    const s2 = at('validate', 2, 'form')
    expect(s2.fields[0].mark).toBe('ok')
    expect(s2.fields[0].val).toBe('20184412-0001')
  })

  it('sceneState_formInfoToneAndDoc', () => {
    const s = at('learns', 1, 'form')
    expect(s.doc).toBe(true)
    expect(s.msg).toEqual({ text: 'Layout saved for Adeyemi & Sons Trading', tone: 'info' })
    expect(at('read-documents', 0, 'form').fields.every((x) => !x.shown)).toBe(true)
  })

  it('sceneState_flowMarksDoneActiveAndTodo', () => {
    const s0 = at('submit-clear', 0, 'flow')
    expect(s0.nodes.map((x) => x.state)).toEqual(['done', 'active', 'todo', 'todo', 'todo'])
    expect(s0.out).toBe('Invoice converted and signed')
    expect(at('submit-clear', 3, 'flow').out).toBe('Cleared · reference stored on the invoice')
    expect(at('submit-clear', 3, 'flow').nodes.map((x) => x.state)).toEqual(['done', 'done', 'done', 'done', 'active'])
  })

  it('sceneState_metricsGrowsTilesAndBars', () => {
    const s0 = at('overview', 0, 'metrics')
    expect(s0.tiles.map((x) => x.val)).toEqual(['193', '38%', '5'])
    expect(s0.bars.map((b) => b.h)).toEqual([21, 30, 27, 39, 43, 51, 57])
    expect(s0.bars.map((b) => b.last)).toEqual([false, false, false, false, false, false, true])
    const s2 = at('overview', 2, 'metrics')
    expect(s2.tiles.map((x) => x.val)).toEqual(['482', '94%', '12'])
    expect(s2.bars.map((b) => b.h)).toEqual([53, 75, 68, 98, 108, 128, 143])
  })

  it('sceneState_togglesFlipCumulatively', () => {
    const on = (i: number) => at('roles', i, 'toggles').rows.map((r) => r.on)
    const focus = (i: number) => at('roles', i, 'toggles').rows.findIndex((r) => r.focused)
    expect(on(0)).toEqual([true, false, true, true, false])
    expect(focus(0)).toBe(0)
    expect(on(1)).toEqual([true, false, true, false, false])
    expect(focus(1)).toBe(3)
    expect(on(2)).toEqual([true, false, true, false, false])
    expect(focus(2)).toBe(4)
  })

  it('sceneState_feedShowsTheFirstNItems', () => {
    const s0 = at('audit-trail', 0, 'feed')
    expect(s0.items).toHaveLength(2)
    expect(s0.items[0].time).toBe('14:02')
    expect(s0.items[0].tag).toBe('g')
    const s2 = at('audit-trail', 2, 'feed')
    expect(s2.items).toHaveLength(5)
    expect(s2.items[4].l).toBe('Cleared by FIRS')
  })

  it('sceneState_everyFeatureEveryStep', () => {
    const kinds = new Set<string>()
    for (const f of FEATURES) {
      const n = f.sc.steps.length
      for (let i = 0; i < n; i++) {
        const s = sceneState(f.sc, i)
        expect(s.kind).toBe(f.sc.kind)
        expect(s.cap).toBe(f.sc.steps[i].cap)
        expect(s.stepNum).toBe(`${String(i + 1).padStart(2, '0')} / ${String(n).padStart(2, '0')}`)
        kinds.add(s.kind)
      }
    }
    expect(FEATURES).toHaveLength(26)
    expect(kinds.size).toBe(6)
  })

  it('thumbStep_isThePrototypePickOrTheLastStep', () => {
    expect(THUMB_STEP).toEqual({
      'import-files': 3, 'invoice-status': 1, 'read-documents': 1, 'review-fields': 1, 'rule-library': 1, validate: 0,
      'approval-queue': 1, 'workflow-builder': 1, 'fiscal-outcomes': 2, overview: 1, portfolio: 1, contacts: 1,
      roles: 1, channels: 1, 'erp-connectors': 1,
    })
    expect(Object.keys(THUMB_STEP)).toHaveLength(15)
    expect(thumbStep(feat('validate'))).toBe(0)
    expect(thumbStep(feat('import-files'))).toBe(3)
    expect(thumbStep(feat('fiscal-outcomes'))).toBe(2)
    expect(thumbStep(feat('create-invoice'))).toBe(2)
    expect(thumbStep(feat('submit-clear'))).toBe(3)
    for (const f of FEATURES) {
      expect(thumbStep(f)).toBeGreaterThanOrEqual(0)
      expect(thumbStep(f)).toBeLessThanOrEqual(f.sc.steps.length - 1)
    }
  })

  it('chip_mapsEachStatusToItsLabelAndTone', () => {
    expect(CHIP).toEqual({
      new: ['Imported', 'muted'], valid: ['Valid', 'green'], error: ['Needs fixing', 'red'], draft: ['Draft', 'muted'],
      pending: ['Awaiting approval', 'amber'], approved: ['Approved', 'green'], sent: ['Submitted', 'amber'],
      cleared: ['Cleared', 'green'], rejected: ['Rejected', 'red'], failed: ['Failed', 'red'], ready: ['Ready', 'green'],
      progress: ['In progress', 'amber'], none: ['Not started', 'muted'],
    })
    expect(Object.keys(CHIP)).toHaveLength(13)
  })

  it('sceneState_omittedFocusCarriesOverAndOmittedToneIsInfo', () => {
    const list: Scene = { kind: 'list', win: 'w', grid: 'g', cols: ['a', 'b', 'c', 'd'], rows: [['1', '1', '1', 'new'], ['2', '2', '2', 'new']],
      steps: [{ cap: 'x', focus: 1 }, { cap: 'y' }] }
    expect(sceneState(list, 1).kind === 'list' && sceneState(list, 1).rows.map((r) => r.focused)).toEqual([false, true])
    const form: Scene = { kind: 'form', win: 'w', fields: [{ l: 'a', v: 'A' }, { l: 'b', v: 'B' }],
      steps: [{ cap: 'x', reveal: 2, focus: 1, msg: 'hello' }, { cap: 'y' }] }
    const f0 = sceneState(form, 0)
    const f1 = sceneState(form, 1)
    if (f0.kind !== 'form' || f1.kind !== 'form') throw new Error('kind')
    expect(f0.msg).toEqual({ text: 'hello', tone: 'info' })
    expect(f1.msg).toBeNull()
    expect(f1.fields.map((x) => x.focused)).toEqual([false, true])
    expect(f1.fields.map((x) => x.shown)).toEqual([true, true])
    const tg: Scene = { kind: 'toggles', win: 'w', rows: [['a', 'a', true], ['b', 'b', true]], steps: [{ cap: 'x', focus: 1, flip: { 0: false } }, { cap: 'y', focus: 1 }] }
    const t1 = sceneState(tg, 1)
    if (t1.kind !== 'toggles') throw new Error('kind')
    expect(t1.rows.map((r) => r.on)).toEqual([false, true])
  })

  it('sceneState_boundsAtFirstAndLastStepAndZeroGrow', () => {
    const m: Scene = { kind: 'metrics', win: 'w', tiles: [['t', 100, '%']], bars: [0.01, 1], steps: [{ cap: 'x', grow: 0 }, { cap: 'y', grow: 1 }] }
    const m0 = sceneState(m, 0)
    if (m0.kind !== 'metrics') throw new Error('kind')
    expect(m0.bars.map((b) => b.h)).toEqual([4, 4])
    expect(m0.tiles[0].val).toBe('0%')
    const feed: Scene = { kind: 'feed', win: 'w', items: [['1', 'a', 'a', 'g']], steps: [{ cap: 'x', show: 0 }, { cap: 'y', show: 1 }] }
    const e = sceneState(feed, 0)
    if (e.kind !== 'feed') throw new Error('kind')
    expect(e.items).toHaveLength(0)
    expect(e.stepNum).toBe('01 / 02')
    expect(sceneState(feed, 1).stepNum).toBe('02 / 02')
    const one: Scene = { kind: 'flow', win: 'w', nodes: [['a', 'a'], ['b', 'b']], steps: [{ cap: 'only', at: 0, out: 'o' }] }
    expect(sceneState(one, 0).stepNum).toBe('01 / 01')
  })

  it('thumbStep_fallsBackToTheLastStepForAnIdOutsideTheTable', () => {
    const f = { id: 'not-in-table', sc: { kind: 'feed', win: 'w', items: [], steps: [{ cap: 'a', show: 0 }, { cap: 'b', show: 0 }, { cap: 'c', show: 0 }, { cap: 'd', show: 0 }, { cap: 'e', show: 0 }] } } as unknown as Feature
    expect(thumbStep(f)).toBe(4)
    expect(Object.keys(THUMB_STEP)).not.toContain('not-in-table')
  })
})
