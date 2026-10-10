import type { Feature, FeedTag, Mark, RowStatus, Scene, Tone } from './types.ts'

type Base = { idx: number; n: number; cap: string; stepNum: string }
export type SceneState = Base & (
  | { kind: 'list'; grid: string; cols: [string, string, string, string]; banner: string | null
      rows: { c1: string; c2: string; c3: string; status: RowStatus; focused: boolean }[] }
  | { kind: 'form'; doc: boolean; msg: { text: string; tone: Tone } | null
      fields: { l: string; v: string; val: string; shown: boolean; mark: Mark | null; focused: boolean }[] }
  | { kind: 'flow'; out: string; nodes: { l: string; sub: string; state: 'done' | 'active' | 'todo' }[] }
  | { kind: 'metrics'; tiles: { l: string; val: string }[]; bars: { h: number; last: boolean }[] }
  | { kind: 'toggles'; rows: { l: string; d: string; on: boolean; focused: boolean }[] }
  | { kind: 'feed'; items: { time: string; l: string; d: string; tag: FeedTag }[] })

const pad = (x: number) => String(x).padStart(2, '0')

export function sceneState(sc: Scene, idx: number): SceneState {
  const n = sc.steps.length
  const base: Base = { idx, n, cap: sc.steps[idx].cap, stepNum: `${pad(idx + 1)} / ${pad(n)}` }
  switch (sc.kind) {
    case 'list': {
      const cur = sc.steps[idx]
      const status = sc.rows.map((r) => r[3])
      let show = sc.show0 ?? sc.rows.length
      let focus = -1
      for (const s of sc.steps.slice(0, idx + 1)) {
        for (const [i, v] of Object.entries(s.set ?? {})) status[Number(i)] = v
        if (s.show != null) show = s.show
        if (s.focus != null) focus = s.focus
      }
      return { ...base, kind: 'list', grid: sc.grid, cols: sc.cols, banner: cur.banner ?? null,
        rows: sc.rows.slice(0, show).map((r, i) => ({ c1: r[0], c2: r[1], c3: r[2], status: status[i], focused: i === focus })) }
    }
    case 'form': {
      const cur = sc.steps[idx]
      let reveal = 0
      let focus = -1
      const marks: Record<number, Mark> = {}
      for (const s of sc.steps.slice(0, idx + 1)) {
        if (s.reveal != null) reveal = s.reveal
        if (s.focus != null) focus = s.focus
        Object.assign(marks, s.mark)
      }
      return { ...base, kind: 'form', doc: !!sc.doc,
        msg: cur.msg ? { text: cur.msg, tone: cur.tone ?? 'info' } : null,
        fields: sc.fields.map((f, i) => {
          const shown = i < reveal
          const mark = shown ? marks[i] ?? null : null
          const val = shown ? ((mark === 'fix' || mark === 'ok') && f.fix ? f.fix : f.v) : ''
          return { l: f.l, v: f.v, val, shown, mark, focused: focus === i }
        }) }
    }
    case 'flow': {
      const cur = sc.steps[idx]
      const at = cur.at
      return { ...base, kind: 'flow', out: cur.out,
        nodes: sc.nodes.map((nd, i) => ({ l: nd[0], sub: nd[1], state: i < at ? 'done' : i === at ? 'active' : 'todo' })) }
    }
    case 'metrics': {
      const g = sc.steps[idx].grow
      return { ...base, kind: 'metrics',
        tiles: sc.tiles.map((x) => ({ l: x[0], val: Math.round(x[1] * g).toLocaleString('en-GB') + x[2] })),
        bars: sc.bars.map((h, i) => ({ h: Math.max(4, Math.round(h * 150 * g)), last: i === sc.bars.length - 1 })) }
    }
    case 'toggles': {
      const on = sc.rows.map((r) => r[2])
      let focus = -1
      for (const s of sc.steps.slice(0, idx + 1)) {
        for (const [i, v] of Object.entries(s.flip ?? {})) on[Number(i)] = v
        if (s.focus != null) focus = s.focus
      }
      return { ...base, kind: 'toggles', rows: sc.rows.map((r, i) => ({ l: r[0], d: r[1], on: on[i], focused: i === focus })) }
    }
    case 'feed':
      return { ...base, kind: 'feed',
        items: sc.items.slice(0, sc.steps[idx].show).map((x) => ({ time: x[0], l: x[1], d: x[2], tag: x[3] })) }
  }
}

export const THUMB_STEP: Readonly<Record<string, number>> = {
  'import-files': 3, 'invoice-status': 1, 'read-documents': 1, 'review-fields': 1, 'rule-library': 1, validate: 0,
  'approval-queue': 1, 'workflow-builder': 1, 'fiscal-outcomes': 2, overview: 1, portfolio: 1, contacts: 1,
  roles: 1, channels: 1, 'erp-connectors': 1,
}

export function thumbStep(f: Feature): number {
  return THUMB_STEP[f.id] ?? f.sc.steps.length - 1
}

export const CHIP: Record<RowStatus, readonly [label: string, tone: 'muted' | 'green' | 'amber' | 'red']> = {
  new: ['Imported', 'muted'], valid: ['Valid', 'green'], error: ['Needs fixing', 'red'], draft: ['Draft', 'muted'],
  pending: ['Awaiting approval', 'amber'], approved: ['Approved', 'green'], sent: ['Submitted', 'amber'],
  cleared: ['Cleared', 'green'], rejected: ['Rejected', 'red'], failed: ['Failed', 'red'], ready: ['Ready', 'green'],
  progress: ['In progress', 'amber'], none: ['Not started', 'muted'],
}
