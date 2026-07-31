import { describe, expect, it } from 'vitest'

import { nodeSub, toOptions } from './WorkflowParts'
import { roleOf, seedPolicies, slaText, type ApprovalNode, type RoleKey, type Sla, type WfNode } from '../lib/workflows'

// MEMB-01-08 widened `nodeSub` by an optional 2nd argument and added `toOptions`. Both are
// rendered by `WorkflowCanvas` in BOTH modes, and neither had a test — the whole firm-identity
// guarantee of that subtask rests on `nodeSub(node)` staying byte-identical, so it is pinned
// here rather than left to inspection.

const A = (role: RoleKey, sla: Sla): ApprovalNode => ({ id: 'n', type: 'approval', role, sla, delegate: false })

describe('nodeSub — one argument (FIRM mode, and every mode before MEMB-01-08)', () => {
  it('joins the abstract role line with the SLA, unchanged by the new parameter', () => {
    expect(nodeSub(A('fin_dir', '48'))).toBe('Finance · within 48h')
    expect(nodeSub(A('line_mgr', '48'))).toBe('Requesting dept. · within 48h')
    expect(nodeSub(A('cfo', '72'))).toBe('Executive · within 72h')
  })

  it('passing the 2nd argument explicitly as undefined is the same as omitting it', () => {
    const n = A('fin_dir', '24')
    expect(nodeSub(n, undefined)).toBe(nodeSub(n))
  })

  it('renders the SLA alone rather than a leading separator when the role line is empty', () => {
    // roleOf's unknown-key fallback carries an empty line; joining unconditionally would
    // render " · within 48h".
    expect(roleOf('nope').line).toBe('')
    expect(nodeSub(A('nope' as RoleKey, '48'))).toBe('within 48h')
  })

  it("renders the role alone when the SLA is the 'no deadline' sentinel", () => {
    expect(nodeSub(A('fin_dir', '0'))).toBe('Finance · no deadline')
  })

  it('every seeded FIRM approval card still reads "<role line> · <sla>"', () => {
    const seen: string[] = []
    const walk = (ns: readonly WfNode[]) => {
      for (const n of ns) {
        if (n.type === 'approval') {
          expect(nodeSub(n)).toBe([roleOf(n.role).line, slaText(n.sla)].filter(Boolean).join(' · '))
          seen.push(nodeSub(n))
        }
        if (n.type === 'condition') {
          walk(n.then)
          walk(n.else)
        }
      }
    }
    seedPolicies().firm.forEach((p) => walk(p.nodes))
    expect(seen).toContain('Finance · within 48h')
    expect(seen.length).toBeGreaterThan(0)
  })

  it('leaves notify and auto-approve sub-lines alone', () => {
    expect(nodeSub({ id: 'n', type: 'notify', target: 'Tax Team', channel: 'In-app' })).toBe('Watcher · In-app')
    expect(nodeSub({ id: 'n', type: 'autoapprove' })).toBe('Clears without manual sign-off')
  })
})

describe('nodeSub — two arguments (IN-HOUSE mode)', () => {
  it('REPLACES the role line with the resolved person and keeps the SLA', () => {
    expect(nodeSub(A('fin_dir', '48'), 'Ngozi Balogun +1')).toBe('Ngozi Balogun +1 · within 48h')
    expect(nodeSub(A('cfo', '72'), 'Adebayo Ogunlesi — suspended')).toBe('Adebayo Ogunlesi — suspended · within 72h')
    expect(nodeSub(A('ceo', '72'), 'Nobody assigned')).toBe('Nobody assigned · within 72h')
  })

  it('does not fall back to the role line once a resolved line is supplied', () => {
    expect(nodeSub(A('fin_dir', '48'), 'Ngozi Balogun +1')).not.toContain('Finance')
  })

  it('is ignored for notify and auto-approve, which have no position to resolve', () => {
    expect(nodeSub({ id: 'n', type: 'notify', target: 'Tax Team', channel: 'Email' }, 'Ngozi Balogun')).toBe('Watcher · Email')
    expect(nodeSub({ id: 'n', type: 'autoapprove' }, 'Ngozi Balogun')).toBe('Clears without manual sign-off')
  })

  it('an empty resolved line falls back rather than rendering a bare separator', () => {
    // `??` only guards null/undefined, so '' would reach the join — filter(Boolean) catches it.
    expect(nodeSub(A('fin_dir', '48'), '')).toBe('within 48h')
  })
})

describe('toOptions', () => {
  it('is self-labelling — value and label are the same string', () => {
    expect(toOptions(['Finance', 'Board'])).toEqual([
      { value: 'Finance', label: 'Finance' },
      { value: 'Board', label: 'Board' },
    ])
  })

  it('preserves order and does not de-duplicate (the callers own both)', () => {
    expect(toOptions(['b', 'a', 'b']).map((o) => o.value)).toEqual(['b', 'a', 'b'])
  })

  it('returns a fresh array on an empty list', () => {
    expect(toOptions([])).toEqual([])
  })

  it('never mutates its input', () => {
    const src = ['Finance', 'Executive']
    toOptions(src)
    expect(src).toEqual(['Finance', 'Executive'])
  })
})
