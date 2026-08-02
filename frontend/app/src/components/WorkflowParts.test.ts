import { describe, expect, it } from 'vitest'

import { nodeSub, nodeTitle, roleOptions, simSub, simTitle, toOptions } from './WorkflowParts'
import { SEED_FIRM_MEMBERS, SEED_INHOUSE_MEMBERS } from '../lib/members'
import { resolve, SEED_FIRM_ROLES, SEED_INHOUSE_ROLES } from '../lib/roles'
import type { ApprovalNode, AutoApproveNode, NotifyNode, RoleKey, Sla } from '../lib/workflows'

// nodeTitle/nodeSub/simTitle/simSub take the workspace's role list as an argument instead of
// reading a module constant, and roleOptions replaces the old ROLE_OPTIONS constant.

const A = (role: RoleKey, sla: Sla): ApprovalNode => ({ id: 'n', type: 'approval', role, sla, delegate: false })

describe('AC-2 — nodeTitle reads the supplied role list', () => {
  it('titles an approval step from the role list passed in, not a module constant', () => {
    expect(nodeTitle(A('fin_mgr', '48'), SEED_FIRM_ROLES)).toBe('Engagement Manager must approve')
  })
})

describe('AC-2 — nodeSub', () => {
  it('joins the resolved text with the SLA when a resolved line is supplied', () => {
    expect(nodeSub(A('fin_mgr', '48'), SEED_FIRM_ROLES, 'Musa Danjuma')).toBe('Musa Danjuma · within 48h')
  })

  it('never emits a leading separator when there is no resolved line and the role desc is empty', () => {
    // 'ghost' names no role — roleOf's deleted-role sentinel carries desc: ''.
    expect(nodeSub(A('ghost', '48'), SEED_FIRM_ROLES)).toBe('within 48h')
  })

  it('falls back to the role\'s own desc, not just the SLA, when the role still exists', () => {
    expect(nodeSub(A('fin_dir', '48'), SEED_FIRM_ROLES)).toBe('Second sign-off above ₦250m · within 48h')
  })
})

describe('AC-3 — roleOptions lists the workspace roles in list order', () => {
  it('values follow the role list order', () => {
    expect(roleOptions(SEED_FIRM_ROLES, 'fin_mgr').map((o) => o.value)).toEqual([
      'preparer',
      'fin_mgr',
      'fin_dir',
      'compliance',
      'cfo',
      'quality_reviewer',
    ])
  })

  it('labels are the title alone, with no department/desc suffix', () => {
    const labels = roleOptions(SEED_FIRM_ROLES, 'fin_mgr').map((o) => o.label)
    expect(labels).toEqual(['Invoice Preparer', 'Engagement Manager', 'Senior Manager', 'Tax Reviewer', 'Engagement Partner', 'Quality Reviewer'])
    for (const label of labels) expect(label).not.toContain(' · ')
  })
})

describe('AC-4 — roleOptions and a stored key naming no role', () => {
  it('prepends a Deleted role option for an unknown stored key', () => {
    const opts = roleOptions(SEED_FIRM_ROLES, 'gone')
    expect(opts).toHaveLength(7)
    expect(opts[0]).toEqual({ value: 'gone', label: 'Deleted role' })
  })

  it('does not duplicate a key it already lists', () => {
    const opts = roleOptions(SEED_FIRM_ROLES, 'cfo')
    expect(opts).toHaveLength(6)
    expect(opts.filter((o) => o.value === 'cfo')).toHaveLength(1)
  })
})

describe('AC-6 — the canvas line never contains the word suspended', () => {
  it('a blocked in-house role renders the holder alone', () => {
    const text = resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo').text
    const rendered = nodeSub(A('cfo', '72'), SEED_INHOUSE_ROLES, text)
    expect(rendered).toContain('Adebayo Ogunlesi')
    expect(rendered).not.toContain('suspended')
  })
})

describe('AC-7 — simSub renders the resolved holder, uppercased, never the department line', () => {
  it('an ordinary firm holder', () => {
    const out = simSub(A('fin_mgr', '48'), SEED_FIRM_ROLES, 'Musa Danjuma')
    expect(out).toBe('MUSA DANJUMA')
    expect(out).not.toContain('FINANCE')
    expect(out).not.toContain('SIGN-OFF') // fin_mgr's own desc, uppercased, must not leak either
  })

  it('a suspended-only in-house holder still names them, without the word SUSPENDED', () => {
    const res = resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo')
    expect(res.warn).toBe(true) // the row's amber trigger — simSub itself carries no tone
    const out = simSub(A('cfo', '72'), SEED_INHOUSE_ROLES, res.text)
    expect(out).toBe('ADEBAYO OGUNLESI')
    expect(out).not.toContain('SUSPENDED')
  })

  it('the unheld and deleted states', () => {
    const unheld = resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'ceo')
    expect(simSub(A('ceo', '48'), SEED_INHOUSE_ROLES, unheld.text)).toBe('NOBODY ASSIGNED')

    const deleted = resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'ghost')
    expect(simSub(A('ghost', '48'), SEED_FIRM_ROLES, deleted.text)).toBe('ROLE NO LONGER EXISTS')
  })

  it('leaves notify and auto-approve sub-lines unchanged from PR #118', () => {
    const notify: NotifyNode = { id: 'n', type: 'notify', target: 'Tax Team', channel: 'In-app' }
    const auto: AutoApproveNode = { id: 'n', type: 'autoapprove' }
    expect(simSub(notify, SEED_FIRM_ROLES)).toBe('IN-APP')
    expect(simSub(auto, SEED_FIRM_ROLES)).toBe('NO SIGN-OFF NEEDED')
  })

  it('falls back to the role\'s own desc, uppercased, when no resolved line is supplied', () => {
    expect(simSub(A('fin_dir', '48'), SEED_FIRM_ROLES)).toBe('SECOND SIGN-OFF ABOVE ₦250M')
  })

  it('a deleted role has no desc to fall back to — an empty string, not the resolved sentence', () => {
    expect(simSub(A('ghost', '48'), SEED_FIRM_ROLES)).toBe('')
  })
})

describe('AC-8 — a deleted role titles as Deleted role in both display functions', () => {
  it('nodeTitle and simTitle both name the deleted-role sentinel', () => {
    const withoutCompliance = SEED_FIRM_ROLES.filter((r) => r.key !== 'compliance')
    const n = A('compliance', '24')
    expect(nodeTitle(n, withoutCompliance)).toContain('Deleted role')
    expect(simTitle(n, withoutCompliance)).toContain('Deleted role')
  })

  it('a role still present titles normally — guards the check above against a vacuous pass', () => {
    expect(nodeTitle(A('fin_mgr', '48'), SEED_FIRM_ROLES)).toBe('Engagement Manager must approve')
    expect(simTitle(A('fin_mgr', '48'), SEED_FIRM_ROLES)).toBe('Engagement Manager')
  })
})

// Out-of-scope fence: notify/autoapprove titles and sublines must not change shape just
// because nodeTitle/nodeSub gained a roles argument for the approval branch.
describe('regression — notify and auto-approve are untouched by the roles threading', () => {
  it('nodeTitle and nodeSub ignore the roles argument for non-approval kinds', () => {
    const notify: NotifyNode = { id: 'n', type: 'notify', target: 'Tax Team', channel: 'Email' }
    const auto: AutoApproveNode = { id: 'n', type: 'autoapprove' }
    expect(nodeTitle(notify, SEED_FIRM_ROLES)).toBe('Notify Tax Team')
    expect(nodeTitle(auto, SEED_FIRM_ROLES)).toBe('Auto-approve')
    expect(nodeSub(notify, SEED_FIRM_ROLES)).toBe('Watcher · Email')
    expect(nodeSub(auto, SEED_FIRM_ROLES)).toBe('Clears without manual sign-off')
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
