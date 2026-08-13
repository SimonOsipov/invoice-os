import { describe, expect, it } from 'vitest'

import { nodeSub, nodeTitle, roleOptions, simSub, simTitle, toOptions, type WfOption } from './WorkflowParts'
import { toMember, type Member, type MembershipWire } from '../lib/members'
import { resolve, type Role } from '../lib/roles'
import type { ApprovalNode, AutoApproveNode, NotifyNode, RoleKey, Sla } from '../lib/workflows'

// nodeTitle/nodeSub/simTitle/simSub take the workspace's role list as an argument instead of
// reading a module constant, and roleOptions replaces the old ROLE_OPTIONS constant.

const A = (role: RoleKey, sla: Sla): ApprovalNode => ({ id: 'n', type: 'approval', role, sla, delegate: false })

// File-local mirror of lib/roles.ts's former SEED_FIRM_ROLES/SEED_INHOUSE_ROLES (subtask 04
// deleted the module-level seed; the DB seed's Go-side test is the source of truth now).
// Key/title/desc/members copied verbatim so every literal expectation below is unchanged.
const MOCK_FIRM_ROLES: readonly Role[] = [
  {
    key: 'preparer',
    title: 'Invoice Preparer',
    desc: 'Prepares and imports client invoices',
    members: ['c0000000-0000-0000-0000-000000000003', 'c0000000-0000-0000-0000-000000000006'],
  },
  { key: 'fin_mgr', title: 'Engagement Manager', desc: 'First sign-off on a client invoice', members: ['c0000000-0000-0000-0000-000000000004'] },
  { key: 'fin_dir', title: 'Senior Manager', desc: 'Second sign-off above ₦250m', members: ['c0000000-0000-0000-0000-000000000004'] },
  { key: 'compliance', title: 'Tax Reviewer', desc: 'Checks VAT, WHT and TIN detail before filing', members: ['c0000000-0000-0000-0000-000000000005'] },
  { key: 'cfo', title: 'Engagement Partner', desc: 'Signs off invoices above ₦1bn', members: ['c0000000-0000-0000-0000-000000000001'] },
  { key: 'quality_reviewer', title: 'Quality Reviewer', desc: 'Second-partner review on flagged engagements', members: [] },
]

const MOCK_INHOUSE_ROLES: readonly Role[] = [
  { key: 'preparer', title: 'Preparer', desc: 'Accounts Payable', members: ['c0000000-0000-0000-0000-000000000013'] },
  { key: 'line_mgr', title: 'Line Manager', desc: 'Requesting dept.', members: ['c0000000-0000-0000-0000-000000000009'] },
  { key: 'fin_mgr', title: 'Finance Manager', desc: 'Finance', members: [] },
  { key: 'controller', title: 'Financial Controller', desc: 'Finance', members: ['c0000000-0000-0000-0000-000000000010'] },
  {
    key: 'fin_dir',
    title: 'Finance Director',
    desc: 'Finance',
    members: ['c0000000-0000-0000-0000-000000000002', 'c0000000-0000-0000-0000-000000000008'],
  },
  { key: 'compliance', title: 'Compliance Officer', desc: 'Tax & Compliance', members: ['c0000000-0000-0000-0000-000000000011'] },
  { key: 'cfo', title: 'CFO', desc: 'Executive', members: ['c0000000-0000-0000-0000-000000000012'] },
  { key: 'ceo', title: 'CEO', desc: 'Executive', members: [] },
]

// The in-house directory as the server states it, keyed by the membership subjects
// MOCK_INHOUSE_ROLES points at — projected through `toMember`, so a change to either the
// projection or the fixture shows up here rather than in a hand-written row that agrees with
// nothing. `resolve` reads name and status only. The firm side needs no roster: its one spec
// resolves a key no role holds.
const wire = (userID: string, name: string, status: string): MembershipWire => ({
  user_id: userID,
  role: 'reviewer',
  status,
  display_name: name,
  email: null,
})

const INHOUSE_MEMBERS: readonly Member[] = [
  wire('c0000000-0000-0000-0000-000000000002', 'Ngozi Balogun', 'active'),
  wire('c0000000-0000-0000-0000-000000000008', 'Yetunde Fashola', 'active'),
  wire('c0000000-0000-0000-0000-000000000009', 'Emeka Uzowulu', 'active'),
  wire('c0000000-0000-0000-0000-000000000010', 'Tunde Adeyemi', 'active'),
  wire('c0000000-0000-0000-0000-000000000011', 'Ibrahim Bello', 'active'),
  wire('c0000000-0000-0000-0000-000000000012', 'Adebayo Ogunlesi', 'suspended'),
  wire('c0000000-0000-0000-0000-000000000013', 'Zainab Lawal', 'active'),
].map((w) => toMember(w, 'nobody'))

describe('AC-2 — nodeTitle reads the supplied role list', () => {
  it('titles an approval step from the role list passed in, not a module constant', () => {
    expect(nodeTitle(A('fin_mgr', '48'), MOCK_FIRM_ROLES)).toBe('Engagement Manager must approve')
  })
})

describe('AC-2 — nodeSub', () => {
  it('joins the resolved text with the SLA when a resolved line is supplied', () => {
    expect(nodeSub(A('fin_mgr', '48'), MOCK_FIRM_ROLES, 'Musa Danjuma')).toBe('Musa Danjuma · within 48h')
  })

  it('never emits a leading separator when there is no resolved line and the role desc is empty', () => {
    // 'ghost' names no role — roleOf's deleted-role sentinel carries desc: ''.
    expect(nodeSub(A('ghost', '48'), MOCK_FIRM_ROLES)).toBe('within 48h')
  })

  it('falls back to the role\'s own desc, not just the SLA, when the role still exists', () => {
    expect(nodeSub(A('fin_dir', '48'), MOCK_FIRM_ROLES)).toBe('Second sign-off above ₦250m · within 48h')
  })
})

describe('AC-3 — roleOptions lists the workspace roles in list order', () => {
  it('values follow the role list order', () => {
    expect(roleOptions(MOCK_FIRM_ROLES, 'fin_mgr').map((o) => o.value)).toEqual([
      'preparer',
      'fin_mgr',
      'fin_dir',
      'compliance',
      'cfo',
      'quality_reviewer',
    ])
  })

  it('labels are the title alone, with no department/desc suffix', () => {
    const labels = roleOptions(MOCK_FIRM_ROLES, 'fin_mgr').map((o) => o.label)
    expect(labels).toEqual(['Invoice Preparer', 'Engagement Manager', 'Senior Manager', 'Tax Reviewer', 'Engagement Partner', 'Quality Reviewer'])
    for (const label of labels) expect(label).not.toContain(' · ')
  })
})

describe('AC-4 — roleOptions and a stored key naming no role', () => {
  it('prepends a Deleted role option for an unknown stored key', () => {
    const opts = roleOptions(MOCK_FIRM_ROLES, 'gone')
    expect(opts).toHaveLength(7)
    expect(opts[0]).toEqual({ value: 'gone', label: 'Deleted role' })
  })

  it('does not duplicate a key it already lists', () => {
    const opts = roleOptions(MOCK_FIRM_ROLES, 'cfo')
    expect(opts).toHaveLength(6)
    expect(opts.filter((o) => o.value === 'cfo')).toHaveLength(1)
  })
})

describe('AC-6 — the canvas line never contains the word suspended', () => {
  it('a blocked in-house role renders the holder alone', () => {
    const text = resolve(MOCK_INHOUSE_ROLES, INHOUSE_MEMBERS, 'cfo').text
    const rendered = nodeSub(A('cfo', '72'), MOCK_INHOUSE_ROLES, text)
    expect(rendered).toContain('Adebayo Ogunlesi')
    expect(rendered).not.toContain('suspended')
  })
})

describe('AC-7 — simSub renders the resolved holder, uppercased, never the department line', () => {
  it('an ordinary firm holder', () => {
    const out = simSub(A('fin_mgr', '48'), MOCK_FIRM_ROLES, 'Musa Danjuma')
    expect(out).toBe('MUSA DANJUMA')
    expect(out).not.toContain('FINANCE')
    expect(out).not.toContain('SIGN-OFF') // fin_mgr's own desc, uppercased, must not leak either
  })

  it('a suspended-only in-house holder still names them, without the word SUSPENDED', () => {
    const res = resolve(MOCK_INHOUSE_ROLES, INHOUSE_MEMBERS, 'cfo')
    expect(res.warn).toBe(true) // the row's amber trigger — simSub itself carries no tone
    const out = simSub(A('cfo', '72'), MOCK_INHOUSE_ROLES, res.text)
    expect(out).toBe('ADEBAYO OGUNLESI')
    expect(out).not.toContain('SUSPENDED')
  })

  it('the unheld and deleted states', () => {
    const unheld = resolve(MOCK_INHOUSE_ROLES, INHOUSE_MEMBERS, 'ceo')
    expect(simSub(A('ceo', '48'), MOCK_INHOUSE_ROLES, unheld.text)).toBe('NOBODY ASSIGNED')

    const deleted = resolve(MOCK_FIRM_ROLES, [], 'ghost')
    expect(simSub(A('ghost', '48'), MOCK_FIRM_ROLES, deleted.text)).toBe('ROLE NO LONGER EXISTS')
  })

  it('leaves notify and auto-approve sub-lines unchanged from PR #118', () => {
    const notify: NotifyNode = { id: 'n', type: 'notify', target: 'Tax Team', channel: 'In-app' }
    const auto: AutoApproveNode = { id: 'n', type: 'autoapprove' }
    expect(simSub(notify, MOCK_FIRM_ROLES)).toBe('IN-APP')
    expect(simSub(auto, MOCK_FIRM_ROLES)).toBe('NO SIGN-OFF NEEDED')
  })

  it('falls back to the role\'s own desc, uppercased, when no resolved line is supplied', () => {
    expect(simSub(A('fin_dir', '48'), MOCK_FIRM_ROLES)).toBe('SECOND SIGN-OFF ABOVE ₦250M')
  })

  it('a deleted role has no desc to fall back to — an empty string, not the resolved sentence', () => {
    expect(simSub(A('ghost', '48'), MOCK_FIRM_ROLES)).toBe('')
  })
})

describe('AC-8 — a deleted role titles as Deleted role in both display functions', () => {
  it('nodeTitle and simTitle both name the deleted-role sentinel', () => {
    const withoutCompliance = MOCK_FIRM_ROLES.filter((r) => r.key !== 'compliance')
    const n = A('compliance', '24')
    expect(nodeTitle(n, withoutCompliance)).toContain('Deleted role')
    expect(simTitle(n, withoutCompliance)).toContain('Deleted role')
  })

  it('a role still present titles normally — guards the check above against a vacuous pass', () => {
    expect(nodeTitle(A('fin_mgr', '48'), MOCK_FIRM_ROLES)).toBe('Engagement Manager must approve')
    expect(simTitle(A('fin_mgr', '48'), MOCK_FIRM_ROLES)).toBe('Engagement Manager')
  })
})

// Out-of-scope fence: notify/autoapprove titles and sublines must not change shape just
// because nodeTitle/nodeSub gained a roles argument for the approval branch.
describe('regression — notify and auto-approve are untouched by the roles threading', () => {
  it('nodeTitle and nodeSub ignore the roles argument for non-approval kinds', () => {
    const notify: NotifyNode = { id: 'n', type: 'notify', target: 'Tax Team', channel: 'Email' }
    const auto: AutoApproveNode = { id: 'n', type: 'autoapprove' }
    expect(nodeTitle(notify, MOCK_FIRM_ROLES)).toBe('Notify Tax Team')
    expect(nodeTitle(auto, MOCK_FIRM_ROLES)).toBe('Auto-approve')
    expect(nodeSub(notify, MOCK_FIRM_ROLES)).toBe('Watcher · Email')
    expect(nodeSub(auto, MOCK_FIRM_ROLES)).toBe('Clears without manual sign-off')
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

// ============================================================================
// APPR-09-05 (task-509) AC-7 — slaOptions, the deadline passthrough
// ============================================================================
// `sla_hours` is a plain server int in 0..MaxInt32 (internal/approval/policy.go:278), so a
// stored policy can carry an hour count outside SLA_OPTIONS' fixed four and must still render.
//
// Reached through a cast, not a named import: `slaOptions` does not exist yet, and importing
// a missing export is a module error that would stop this file COLLECTING instead of failing
// an assertion. Drop the cast once the export lands.
type SlaOptions = (current: string) => WfOption[]

async function slaOptions(): Promise<SlaOptions> {
  const mod = (await import('./WorkflowParts')) as unknown as { slaOptions?: SlaOptions }
  expect(typeof mod.slaOptions, 'WorkflowParts must export slaOptions(current)').toBe('function')
  return mod.slaOptions as SlaOptions
}

describe('APPR-09-05 AC-7 — slaOptions and a stored deadline outside the four options', () => {
  it('prepends the stored value, labelled in the list\'s own register', async () => {
    const opts = (await slaOptions())('36')
    expect(opts).toHaveLength(5)
    // 'Within 36 hours', not slaText's 'within 36h': options inside one dropdown read alike.
    expect(opts[0]).toEqual({ value: '36', label: 'Within 36 hours' })
  })

  it('leaves the four shipped options alone for a value already in the list', async () => {
    const opts = (await slaOptions())('24')
    expect(opts.map((o) => o.value)).toEqual(['0', '24', '48', '72'])
  })

  it('does not prepend for the no-deadline sentinel', async () => {
    const opts = (await slaOptions())('0')
    expect(opts).toHaveLength(4)
    expect(opts.filter((o) => o.value === '0')).toHaveLength(1)
  })
})
