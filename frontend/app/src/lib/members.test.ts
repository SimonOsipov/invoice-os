import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { CFG } from '../data'
import {
  ACCESS_ROLES,
  activeAdmins,
  activeHolders,
  blockedPositions,
  canvasApprovalLine,
  CAPABILITY_ROWS,
  clientAccessLabel,
  clientAccessNames,
  CLIENT_ROSTER,
  delegateCandidates,
  DEPARTMENTS,
  departmentsInUse,
  holders,
  inhouseNotifyTargets,
  inspectorApprovalLine,
  lastActiveLabel,
  resolvePosition,
  SEED_FIRM_MEMBERS,
  seedMembers,
  stepsFor,
  unassignedPositions,
  type Member,
  type MemberStatus,
  type PositionResolution,
} from './members'
import { SEED_INHOUSE_POLICIES, WF_ROLES, type Policy, type RoleKey } from './workflows'

// --- fixtures ---------------------------------------------------------------
// Every spec starts from a fresh clone, never from a SEED_* constant — except T1.2/T1.2b,
// where the aliasing between the clone and the constant IS what is under test.
const firm = () => seedMembers().firm
const inhouse = () => seedMembers().inhouse
const names = (list: readonly Member[]) => list.map((m) => m.name)
const you = (list: readonly Member[]): Member => list.filter((m) => m.isYou)[0]
const resolved = (position: RoleKey): PositionResolution => resolvePosition(inhouse(), position)

/** Reads a subset-scoped `clientAccess` as the array it must be — T1.2b's clone probe. */
const scopedIds = (list: readonly Member[], index: number): number[] => {
  const access = list[index].clientAccess
  if (!Array.isArray(access)) throw new Error(`expected a subset-scoped clientAccess at index ${index}`)
  return access
}

/** A hand-built in-house row, for the frames the shipped seed deliberately cannot reach. */
const inhouseRow = (name: string, status: MemberStatus, position: RoleKey | null): Member => ({
  id: `m-${name.split(' ')[0].toLowerCase()}`,
  name,
  initials: name
    .split(' ')
    .map((t) => t[0])
    .join(''),
  email: `${name.split(' ')[0].toLowerCase()}@honeywell.ng`,
  role: 'reviewer',
  status,
  lastActive: status === 'active' ? '2 hours ago' : null,
  joined: status === 'invited' ? null : '12 Mar 2025',
  invitedBy: 'Ngozi Balogun',
  isYou: false,
  department: 'Finance',
  position,
})

/** The seed's only non-empty `else` lane holds an autoapprove, so T1.18 builds its own. */
const elseLanePolicy = (): Policy => ({
  id: 'polX',
  name: 'Else-lane policy',
  scope: 'All invoices',
  status: 'published',
  updated: 'just now',
  nodes: [
    {
      id: 'xn1',
      type: 'condition',
      field: 'amount',
      op: '>',
      value: 1_000_000_000,
      then: [],
      else: [{ id: 'xn2', type: 'approval', role: 'controller', sla: '48', delegate: false }],
    },
  ],
})

const noApprovalPolicy = (): Policy => ({
  id: 'polY',
  name: 'Notify-only policy',
  scope: 'All invoices',
  status: 'published',
  updated: 'just now',
  nodes: [
    { id: 'yn1', type: 'notify', target: 'Board', channel: 'Email' },
    { id: 'yn2', type: 'autoapprove' },
  ],
})

const NOTIFY_BASE = [
  'Finance',
  'Tax & Compliance',
  'Accounts Payable',
  'Executive',
  'Procurement',
  'Audit Committee',
  'Board',
  'Preparer',
]

describe('seed shape (T1.1–T1.6, §15.6)', () => {
  it('ships 7 firm members and 16 in-house members (T1.1)', () => {
    const store = seedMembers()
    expect(store.firm).toHaveLength(7)
    expect(store.inhouse).toHaveLength(16)
  })

  it('clones the rows, so a mutated row leaks into neither the other clone nor the seed (T1.2)', () => {
    const a = seedMembers()
    const b = seedMembers()

    expect(a.firm[0]).not.toBe(b.firm[0])
    expect(a.firm[0]).not.toBe(SEED_FIRM_MEMBERS[0])

    a.firm[0].name = 'Mutated Row'
    expect(a.firm[0].name).toBe('Mutated Row')
    expect(b.firm[0].name).toBe('Chinedu Okafor')
    expect(SEED_FIRM_MEMBERS[0].name).toBe('Chinedu Okafor')
  })

  it('clones the nested clientAccess array too, not just the row object (T1.2b)', () => {
    const a = seedMembers()
    const b = seedMembers()

    // The subset-scoped row. T1.2 mutates a scalar on an `'all'` row and would pass against
    // a `{...m}` shallow copy; `clientAccess` is the module's only nested value, so this is
    // the assertion that actually pins the deep clone.
    expect(a.firm[1].name).toBe('Folake Adesina')
    expect(a.firm[1].clientAccess).not.toBe(b.firm[1].clientAccess)
    expect(a.firm[1].clientAccess).not.toBe(SEED_FIRM_MEMBERS[1].clientAccess)

    scopedIds(a.firm, 1).push(4)
    expect(scopedIds(a.firm, 1)).toEqual([0, 1, 3, 4])
    expect(scopedIds(b.firm, 1)).toEqual([0, 1, 3])
    expect(scopedIds(SEED_FIRM_MEMBERS, 1)).toEqual([0, 1, 3])
  })

  it('the firm you-row is the shipped firm persona (T1.3)', () => {
    const row = you(firm())
    expect(row).toMatchObject({
      name: 'Chinedu Okafor',
      initials: 'CO',
      email: 'c.okafor@okafor.ng',
      role: 'admin',
      clientAccess: 'all',
    })

    // The three-field subset only: `Persona.role` is the JWT role 'authenticated'
    // (auth.ts:46), not an AccessRole, so a whole-object match would fail for the wrong
    // reason. This is the assertion that stops the seed drifting from the sidebar.
    const { name, initials, email } = APP_PERSONAS.firm
    expect([row.name, row.initials, row.email]).toEqual([name, initials, email])
  })

  it('the in-house you-row is the shipped in-house persona, and holds fin_dir (T1.4)', () => {
    const row = you(inhouse())
    expect(row).toMatchObject({
      name: 'Ngozi Balogun',
      initials: 'NB',
      role: 'admin',
      position: 'fin_dir',
    })

    const { name, initials, email } = APP_PERSONAS.inhouse
    expect([row.name, row.initials, row.email]).toEqual([name, initials, email])
  })

  it('marks exactly one member as you in each mode (T1.5)', () => {
    expect(firm().filter((m) => m.isYou)).toHaveLength(1)
    expect(inhouse().filter((m) => m.isYou)).toHaveLength(1)
  })

  it('leaks no columns across modes — firm has no position, in-house no client access (T1.6)', () => {
    const f = firm()
    const h = inhouse()
    expect(f).toHaveLength(7)
    expect(h).toHaveLength(16)

    for (const m of f) {
      expect(m.department).toBeUndefined()
      expect(m.position).toBeUndefined()
    }
    for (const m of h) {
      expect(m.clientAccess).toBeUndefined()
    }
  })
})

describe('holders / activeHolders (T1.7–T1.10, §5)', () => {
  it('lists every holder of a position, in seed order (T1.7)', () => {
    expect(names(holders(inhouse(), 'fin_dir'))).toEqual(['Ngozi Balogun', 'Yetunde Fashola'])
  })

  it('returns nothing for a position nobody holds (T1.8)', () => {
    expect(holders(inhouse(), 'fin_mgr')).toEqual([])
  })

  it('excludes a suspended holder, while `holders` still sees them (T1.9)', () => {
    expect(names(holders(inhouse(), 'cfo'))).toEqual(['Adebayo Ogunlesi'])
    expect(activeHolders(inhouse(), 'cfo')).toEqual([])
  })

  it('excludes an invited holder (T1.10)', () => {
    // Hand-built: no seeded row is a position-holding *invited* member.
    const list = [inhouseRow('Sadiq Ibrahim', 'invited', 'fin_mgr')]
    expect(names(holders(list, 'fin_mgr'))).toEqual(['Sadiq Ibrahim'])
    expect(activeHolders(list, 'fin_mgr')).toEqual([])
  })
})

describe('unassignedPositions / blockedPositions (T1.11–T1.13, §6)', () => {
  it('reports exactly fin_mgr and ceo as unassigned, in WF_ROLES order (T1.11)', () => {
    expect(unassignedPositions(inhouse())).toEqual(['fin_mgr', 'ceo'])
  })

  it('reports exactly cfo as blocked (T1.12)', () => {
    expect(blockedPositions(inhouse())).toEqual(['cfo'])
  })

  it('keeps blocked and unassigned disjoint (T1.13)', () => {
    const list = inhouse()
    const unassigned = unassignedPositions(list)
    const blocked = blockedPositions(list)

    expect(unassigned.length).toBeGreaterThan(0)
    expect(blocked.length).toBeGreaterThan(0)
    expect(blocked.filter((p) => unassigned.includes(p))).toEqual([])
  })
})

describe('stepsFor (T1.14–T1.20, §15.5)', () => {
  it('counts across the root lane and a then lane (T1.14)', () => {
    expect(stepsFor(SEED_INHOUSE_POLICIES, 'fin_dir')).toEqual({
      total: 2,
      policies: [
        { name: 'Company approval policy', count: 1 },
        { name: 'Capital expenditure', count: 1 },
      ],
    })
  })

  it('counts a position whose every step sits inside a then lane (T1.15)', () => {
    const steps = stepsFor(SEED_INHOUSE_POLICIES, 'cfo')
    expect(steps.total).toBe(2)
    expect(steps.policies).toEqual([
      { name: 'Company approval policy', count: 1 },
      { name: 'Capital expenditure', count: 1 },
    ])
  })

  it('lists a single policy for a position named once (T1.16)', () => {
    expect(stepsFor(SEED_INHOUSE_POLICIES, 'ceo')).toEqual({
      total: 1,
      policies: [{ name: 'Capital expenditure', count: 1 }],
    })
  })

  it('omits zero-count policies entirely (T1.17)', () => {
    expect(stepsFor(SEED_INHOUSE_POLICIES, 'compliance')).toEqual({ total: 0, policies: [] })
  })

  it('reads else lanes as well as then lanes (T1.18)', () => {
    // Hand-built: `h1n4.else` is the seed's only non-empty else lane and it holds an
    // autoapprove, so no seeded approval node is else-lane reachable.
    const steps = stepsFor([elseLanePolicy()], 'controller')
    expect(steps.total).toBe(1)
    expect(steps.policies).toEqual([{ name: 'Else-lane policy', count: 1 }])
  })

  it('ignores notify and autoapprove nodes (T1.19)', () => {
    expect(stepsFor([noApprovalPolicy()], 'fin_dir')).toEqual({ total: 0, policies: [] })
  })

  it('includes draft policies (T1.20)', () => {
    expect(SEED_INHOUSE_POLICIES[1].status).toBe('draft')
    expect(SEED_INHOUSE_POLICIES[1].name).toBe('Capital expenditure')
    expect(stepsFor(SEED_INHOUSE_POLICIES, 'fin_dir').policies.map((p) => p.name)).toContain('Capital expenditure')
  })
})

describe('resolvePosition (T1.21–T1.24, §15.5)', () => {
  it('resolves to the first active holder and counts the rest (T1.21)', () => {
    expect(resolved('fin_dir')).toEqual({ kind: 'ok', primary: 'Ngozi Balogun', extra: 1 })
  })

  it('resolves a sole active holder with no extra (T1.22)', () => {
    expect(resolved('line_mgr')).toEqual({ kind: 'ok', primary: 'Emeka Uzowulu', extra: 0 })
  })

  it('reports blocked when every holder is inactive (T1.23)', () => {
    expect(resolved('cfo')).toEqual({ kind: 'blocked', primary: 'Adebayo Ogunlesi', extra: 0 })
  })

  it('reports none when nobody holds the position (T1.24)', () => {
    expect(resolved('ceo')).toEqual({ kind: 'none' })
  })
})

describe('approval lines (T1.25–T1.26, §11.1/§11.2)', () => {
  it('formats the canvas sub-line for ok / blocked / none (T1.25)', () => {
    expect(canvasApprovalLine(resolved('fin_dir'))).toBe('Ngozi Balogun +1')
    expect(canvasApprovalLine(resolved('cfo'))).toBe('Adebayo Ogunlesi — suspended')
    expect(canvasApprovalLine(resolved('ceo'))).toBe('Nobody assigned')
  })

  it('formats the inspector line for ok / blocked / none (T1.26)', () => {
    expect(inspectorApprovalLine(resolved('line_mgr'))).toBe('Currently: Emeka Uzowulu')
    expect(inspectorApprovalLine(resolved('cfo'))).toBe('Currently: Adebayo Ogunlesi — suspended — this step will block')
    expect(inspectorApprovalLine(resolved('ceo'))).toBe('Nobody assigned — assign in Settings › Members')
  })
})

describe('departments and notify targets (T1.27–T1.31, §11.4)', () => {
  it('lists all five departments, in DEPARTMENTS order (T1.27)', () => {
    expect(departmentsInUse(inhouse())).toEqual(['Finance', 'Tax & Compliance', 'Accounts Payable', 'Executive', 'Procurement'])
    expect(departmentsInUse(inhouse())).toEqual([...DEPARTMENTS])
  })

  it('drops a department once no member sits in it (T1.28)', () => {
    const list = inhouse().filter((m) => m.department !== 'Procurement')
    expect(list.length).toBeLessThan(inhouse().length)
    expect(departmentsInUse(list)).toEqual(['Finance', 'Tax & Compliance', 'Accounts Payable', 'Executive'])
  })

  it('offers departments, then the standing committees, then Preparer (T1.29)', () => {
    expect(inhouseNotifyTargets(inhouse(), 'Finance')).toEqual(NOTIFY_BASE)
  })

  it('keeps a legacy stored target selectable, appended last (T1.30)', () => {
    // polH1's seeded notify target (workflows.ts:179) is not a department or a committee.
    expect(inhouseNotifyTargets(inhouse(), 'Tax Team')).toEqual([...NOTIFY_BASE, 'Tax Team'])
  })

  it('does not duplicate a current value the list already carries (T1.31)', () => {
    const targets = inhouseNotifyTargets(inhouse(), 'Board')
    expect(targets).toEqual(NOTIFY_BASE)
    expect(targets.filter((t) => t === 'Board')).toHaveLength(1)
  })
})

describe('delegateCandidates (T1.32, §11.3)', () => {
  it('lists the six active in-house reviewers and nobody else (T1.32)', () => {
    const candidates = delegateCandidates(inhouse())
    expect(candidates).toEqual([
      'Yetunde Fashola',
      'Emeka Uzowulu',
      'Tunde Adeyemi',
      'Ibrahim Bello',
      'Oluwafunmilayo Ademola-Oyediran',
      'Hauwa Abubakar',
    ])
    expect(candidates).not.toContain('Adebayo Ogunlesi') // suspended
    expect(candidates).not.toContain('Sadiq Ibrahim') // invited
    expect(candidates).not.toContain('Ngozi Balogun') // admin, not reviewer
    expect(candidates).not.toContain('Zainab Lawal') // preparer
  })
})

describe('client access (T1.33–T1.34, §6)', () => {
  it('labels all / a subset / none (T1.33)', () => {
    expect(clientAccessLabel('all')).toBe('All clients')
    expect(clientAccessLabel([0, 1, 3])).toBe('3 clients')
    expect(clientAccessLabel([2, 5])).toBe('2 clients')
    expect(clientAccessLabel([])).toBe('No clients')
  })

  it('resolves ids to CFG client names, in CFG order (T1.34)', () => {
    expect(clientAccessNames([0, 1, 3])).toEqual([
      'Lagos Freight & Logistics Ltd',
      'Sahara Foods Distribution Ltd',
      'Adeyemi & Sons Trading',
    ])
  })
})

describe('lastActiveLabel (T1.35–T1.37, §10.1)', () => {
  it('reads as the invite expiry for an invited member (T1.35)', () => {
    expect(lastActiveLabel(inhouseRow('Nneka Chukwu', 'invited', null))).toBe('Expires in 6 days')
  })

  it('reads the stored value for an active member (T1.36)', () => {
    const row = inhouseRow('Kelechi Obi', 'active', null)
    expect(row.lastActive).toBe('2 hours ago')
    expect(lastActiveLabel(row)).toBe('2 hours ago')
  })

  it('reads as an em dash for a non-invited member with no last-active value (T1.37)', () => {
    const row = inhouseRow('Halima Yusuf', 'suspended', null)
    expect(row.lastActive).toBeNull()
    expect(lastActiveLabel(row)).toBe('—')
  })
})

describe('activeAdmins (T1.38, §9)', () => {
  it('finds exactly one active admin in each mode, so the last-admin lock is reachable (T1.38)', () => {
    expect(activeAdmins(firm())).toHaveLength(1)
    expect(activeAdmins(inhouse())).toHaveLength(1)
  })
})

describe('constants (T1.39–T1.40, §3/§6)', () => {
  it('carries the three access roles with §3 copy verbatim (T1.39)', () => {
    expect(ACCESS_ROLES.map((r) => [r.id, r.label, r.description])).toEqual([
      ['admin', 'Admin', 'Full access. Manages members, settings, connectors and certificates.'],
      ['preparer', 'Preparer', 'Creates, imports and validates invoices. Cannot approve or transmit.'],
      ['reviewer', 'Reviewer', 'Reviews and signs off on invoices in approval steps. Cannot manage members or settings.'],
    ])
  })

  it('carries eight capability rows: admin all, reviewer the first five, preparer the first three (T1.40)', () => {
    expect(CAPABILITY_ROWS).toHaveLength(8)
    expect(CAPABILITY_ROWS.map((r) => r.admin)).toEqual([true, true, true, true, true, true, true, true])
    expect(CAPABILITY_ROWS.map((r) => r.reviewer)).toEqual([true, true, true, true, true, false, false, false])
    expect(CAPABILITY_ROWS.map((r) => r.preparer)).toEqual([true, true, true, false, false, false, false, false])
  })
})

// ============================================================================
// QA1–QA17 — adversarial / edge coverage (MEMB-01-01 QA, Mode B)
// ============================================================================
// T1.1–T1.40 are the architect's acceptance-criteria specs, authored before the
// implementation. Each spec below was chosen because a mutation of the shipped code
// SURVIVED that set: it names a behaviour §5/§6/§15.5/§15.6 states but nothing yet
// pins. Nothing here re-asserts a T1 spec.

/** A holder list the seed cannot produce: mixed statuses on one position. */
const cfoHolders = (statuses: readonly MemberStatus[]): Member[] =>
  statuses.map((s, i) => ({ ...inhouseRow(`Holder${i} Person`, s, 'cfo'), id: `m-cfo-${i}`, name: `Holder ${i}` }))

describe('CLIENT_ROSTER (QA1, §14.5)', () => {
  it('is the six CFG companies, ids equal to their CFG index, in CFG order (QA1)', () => {
    // The roster had no spec of its own. Its ids ARE the CFG indices — every stored
    // `clientAccess` subset in the seed indexes into this array by position.
    expect(CLIENT_ROSTER).toHaveLength(6)
    expect(CLIENT_ROSTER.map((c) => c.id)).toEqual([0, 1, 2, 3, 4, 5])
    expect(CLIENT_ROSTER.map((c) => c.name)).toEqual(CFG.map((c) => c.name))
    expect(CLIENT_ROSTER[0].name).toBe('Lagos Freight & Logistics Ltd')
    expect(CLIENT_ROSTER[5].name).toBe('Kano Textile Mills Plc')
  })
})

describe('client access — decided-but-unpinned behaviour (QA2–QA5, §6)', () => {
  it("resolves 'all' to every roster name rather than to an empty list (QA2)", () => {
    // A judgement call the story does not make: `'all'` could equally have returned []
    // and pushed the special case onto callers. It does not — pinned so MEMB-01-04's
    // tooltip and MEMB-01-07's drawer can rely on it.
    expect(clientAccessNames('all')).toEqual(CFG.map((c) => c.name))
    expect(clientAccessNames('all')).toHaveLength(6)
  })

  it('returns names in CFG order whatever order the stored ids are in (QA3)', () => {
    // T1.34 passes [0,1,3] — already sorted — so it cannot see an implementation that
    // simply maps the stored order.
    expect(clientAccessNames([3, 0])).toEqual(['Lagos Freight & Logistics Ltd', 'Adeyemi & Sons Trading'])
    expect(clientAccessNames([5, 2, 0])).toEqual([
      'Lagos Freight & Logistics Ltd',
      'Nigerian Delta Supplies Co.',
      'Kano Textile Mills Plc',
    ])
  })

  it('resolves an empty subset to nothing and ignores an id off the roster (QA4)', () => {
    expect(clientAccessNames([])).toEqual([])
    expect(clientAccessNames([99])).toEqual([])
    expect(clientAccessNames([0, 99])).toEqual(['Lagos Freight & Logistics Ltd'])
  })

  it('labels a single-client subset in the singular — the seeded invited firm row (QA5)', () => {
    // Bature Suleiman ships `[0]`, so '1 client' is reachable on the shipped screen;
    // T1.33 only exercises 'all' / 3 / 2 / 0.
    const bature = firm().find((m) => m.name === 'Bature Suleiman')
    expect(bature?.clientAccess).toEqual([0])
    expect(clientAccessLabel([0])).toBe('1 client')
    expect(clientAccessLabel([0, 1])).toBe('2 clients')
  })
})

describe('resolvePosition and the two approval lines — frames the seed cannot reach (QA6–QA9, §15.5)', () => {
  it('picks the first ACTIVE holder, not the first holder, and counts every holder as extra (QA6)', () => {
    // No seeded position has an inactive holder ahead of an active one, so T1.21/T1.22
    // pass just as happily against `primary = all[0].name`.
    const list = cfoHolders(['suspended', 'active', 'invited'])
    expect(names(list)).toEqual(['Holder 0', 'Holder 1', 'Holder 2'])
    expect(resolvePosition(list, 'cfo')).toEqual({ kind: 'ok', primary: 'Holder 1', extra: 2 })
  })

  it('reports a position held only by an invited member as blocked, never as unassigned (QA7)', () => {
    // §6's notice counts positions with NO holder; an invite that has not been accepted
    // is a holder, so this belongs in the blocked bucket instead.
    const list = [inhouseRow('Sadiq Ibrahim', 'invited', 'cfo')]
    expect(resolvePosition(list, 'cfo')).toEqual({ kind: 'blocked', primary: 'Sadiq Ibrahim', extra: 0 })
    expect(blockedPositions(list)).toEqual(['cfo'])
    expect(unassignedPositions(list)).not.toContain('cfo')
  })

  it('keeps the "+n" suffix on a blocked canvas line (QA8)', () => {
    // Decided, not specified: the "+n" rule is per-line, not per-variant. Unreachable
    // from the seed, where the one blocked position has a single holder.
    const res = resolvePosition(cfoHolders(['suspended', 'suspended', 'invited']), 'cfo')
    expect(res).toEqual({ kind: 'blocked', primary: 'Holder 0', extra: 2 })
    expect(canvasApprovalLine(res)).toBe('Holder 0 +2 — suspended')
  })

  it('omits "+n" from the inspector line even when the canvas shows it (QA9)', () => {
    // §11.2's worked example is Ngozi with a second fin_dir holder and NO "+1". T1.26
    // only ever passes resolutions with extra === 0, so the asymmetry was unpinned.
    const res = resolved('fin_dir')
    expect(res).toEqual({ kind: 'ok', primary: 'Ngozi Balogun', extra: 1 })
    expect(canvasApprovalLine(res)).toBe('Ngozi Balogun +1')
    expect(inspectorApprovalLine(res)).toBe('Currently: Ngozi Balogun')
  })
})

describe('stepsFor — counting and empty input (QA10–QA11, §15.5)', () => {
  it('counts every matching approval in a policy, not merely whether one exists (QA10)', () => {
    // No seeded policy names one position twice, so `count` is never observed above 1.
    const policy: Policy = {
      id: 'polZ',
      name: 'Twice-named policy',
      scope: 'All invoices',
      status: 'published',
      updated: 'just now',
      nodes: [
        { id: 'zn1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
        { id: 'zn2', type: 'approval', role: 'fin_mgr', sla: '48', delegate: false },
        {
          id: 'zn3',
          type: 'condition',
          field: 'amount',
          op: '>',
          value: 1_000_000,
          then: [{ id: 'zn4', type: 'approval', role: 'fin_mgr', sla: '72', delegate: false }],
          else: [],
        },
      ],
    }
    expect(stepsFor([policy], 'fin_mgr')).toEqual({ total: 3, policies: [{ name: 'Twice-named policy', count: 3 }] })
  })

  it('returns an empty result for an empty policy list (QA11)', () => {
    expect(stepsFor([], 'fin_dir')).toEqual({ total: 0, policies: [] })
  })
})

describe('empty-list boundaries (QA12, §5)', () => {
  it('every list derivation degrades to empty — except unassignedPositions, which reports all eight (QA12)', () => {
    const none: Member[] = []
    expect(holders(none, 'cfo')).toEqual([])
    expect(activeHolders(none, 'cfo')).toEqual([])
    expect(blockedPositions(none)).toEqual([])
    expect(departmentsInUse(none)).toEqual([])
    expect(delegateCandidates(none)).toEqual([])
    expect(activeAdmins(none)).toEqual([])
    expect(resolvePosition(none, 'cfo')).toEqual({ kind: 'none' })

    // The one asymmetry: with nobody in the workspace, every approval position is vacant.
    expect(unassignedPositions(none)).toEqual(WF_ROLES.map((r) => r.key))
    expect(unassignedPositions(none)).toHaveLength(8)
  })
})

describe('inhouseNotifyTargets — empty current (QA13, §11.4)', () => {
  it('appends nothing when the node carries no stored target (QA13)', () => {
    // A new notify node's target starts empty; an unguarded push would put a blank
    // option at the bottom of the select.
    expect(inhouseNotifyTargets(inhouse(), '')).toEqual(NOTIFY_BASE)
    expect(inhouseNotifyTargets([], '')).toEqual(['Audit Committee', 'Board', 'Preparer'])
  })
})

describe('activeAdmins and lastActiveLabel — status precedence (QA14–QA15, §9/§10.1)', () => {
  it('counts only ACTIVE admins — a suspended or invited admin does not hold the lock (QA14)', () => {
    // Both seeds ship exactly one admin, so T1.38 reads 1 whether or not status filters.
    const list = [
      { ...inhouseRow('Ngozi Balogun', 'active', 'fin_dir'), role: 'admin' as const },
      { ...inhouseRow('Suspended Admin', 'suspended', null), role: 'admin' as const },
      { ...inhouseRow('Invited Admin', 'invited', null), role: 'admin' as const },
    ]
    expect(names(activeAdmins(list))).toEqual(['Ngozi Balogun'])
  })

  it('reads the invite expiry even when an invited row carries a last-active value (QA15)', () => {
    // §10.1 makes the Last active column read the expiry for invited rows, full stop —
    // status wins over the stored string. Every seeded invited row has `null`, so the
    // precedence was unobservable.
    const row: Member = { ...inhouseRow('Nneka Chukwu', 'invited', null), lastActive: '5 minutes ago' }
    expect(lastActiveLabel(row)).toBe('Expires in 6 days')
  })
})

describe('seed invariants the reducers will depend on (QA16–QA17, §15.6)', () => {
  it('gives every member a unique id and a unique email within its mode (QA16)', () => {
    // MEMB-01-02's replaceMember / removeMember key off `id`, and classifyInvites
    // compares lower-cased emails; a duplicate in either would break them silently.
    for (const list of [firm(), inhouse()]) {
      const ids = list.map((m) => m.id)
      const emails = list.map((m) => m.email.toLowerCase())
      expect(new Set(ids).size).toBe(list.length)
      expect(new Set(emails).size).toBe(list.length)
    }
  })

  it('marks the two founding admins as invited by nobody, and everyone else by a name (QA17)', () => {
    // Decided, not specified: inventing an inviter for the founding admin would invent
    // a fact. §8's drawer renders this string as-is.
    for (const list of [firm(), inhouse()]) {
      for (const m of list) {
        if (m.isYou) expect(m.invitedBy).toBe('—')
        else expect(m.invitedBy).not.toBe('—')
      }
    }
    expect(you(firm()).invitedBy).toBe('—')
    expect(you(inhouse()).invitedBy).toBe('—')
    expect(firm().find((m) => m.name === 'Folake Adesina')?.invitedBy).toBe('Chinedu Okafor')
    expect(inhouse().find((m) => m.name === 'Yetunde Fashola')?.invitedBy).toBe('Ngozi Balogun')
  })
})

describe('CAPABILITY_ROWS copy (QA18, §6)', () => {
  it('carries §6\'s eight capability labels, in §6\'s order (QA18)', () => {
    // T1.40 pins the row count and the three boolean columns but never looks at a label,
    // so the copy MEMB-01-05 renders was entirely unenforced.
    expect(CAPABILITY_ROWS.map((r) => r.label)).toEqual([
      'create and edit invoices',
      'import from file or ERP',
      'run validation',
      'approve in approval steps',
      'transmit to FIRS/MBS',
      'invite and manage members',
      'manage ERP connectors',
      'manage signing certificates',
    ])
  })
})
