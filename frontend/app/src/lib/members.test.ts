import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { CFG } from '../data'
import {
  ACCESS_ROLES,
  accessRoleLabel,
  activeAdmins,
  activeHolders,
  addMembers,
  blockedPositions,
  canvasApprovalLine,
  CAPABILITY_FOOTNOTE,
  CAPABILITY_ROWS,
  classifyInvites,
  clientAccessLabel,
  clientAccessNames,
  clientSelectionCount,
  CLIENT_ROSTER,
  CLIENT_USERS_COPY,
  delegateCandidates,
  DEPARTMENTS,
  departmentsInUse,
  filterClientRoster,
  filterMembers,
  hasDerivableName,
  holders,
  inhouseNotifyTargets,
  initialsFrom,
  inspectorApprovalLine,
  INVITE_ERROR,
  invitedNotice,
  isFiltering,
  isProtectedAdmin,
  isValidEmail,
  lastActiveLabel,
  memberFromInvite,
  nameFromEmail,
  parseEmailInput,
  removeMember,
  replaceMember,
  resolvePosition,
  REVIEWER_HINT,
  SEED_FIRM_MEMBERS,
  SEED_INHOUSE_MEMBERS,
  seedMembers,
  setMemberRole,
  setMemberStatus,
  stepsFor,
  stepsWarning,
  unassignedNotice,
  unassignedPositions,
  type InviteOptions,
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

/**
 * The firm mirror of `inhouseRow` — the one fixture MEMB-01-01 did not need. The T2 reducer
 * specs need a row they can hand to `addMembers`/`replaceMember` that is NOT in the seed,
 * and `clientAccess` (firm-only) is the module's single nested value, so it is the whole
 * point of having it.
 */
const firmRow = (name: string, status: MemberStatus, clientAccess: 'all' | number[]): Member => ({
  id: `m-${name.split(' ')[0].toLowerCase()}`,
  name,
  initials: name
    .split(' ')
    .map((t) => t[0])
    .join(''),
  email: `${name.split(' ')[0].toLowerCase()}@okafor.ng`,
  role: 'preparer',
  status,
  lastActive: status === 'active' ? '2 hours ago' : null,
  joined: status === 'invited' ? null : '12 Mar 2025',
  invitedBy: 'Chinedu Okafor',
  isYou: false,
  clientAccess,
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

// ============================================================================
// T2.1–T2.44 — MEMB-01-02 acceptance-criteria specs (authored RED, test-first)
// ============================================================================
// Transcribed from task-295's Test Specs table BEFORE the invite pipeline, the reducers,
// `filterMembers` and the last-admin guard exist. members.ts carries signature-only stubs
// that throw, so every spec below fails today — none of them can pass by accident.
//
// T2.15–T2.32b run over FIRM (the only mode carrying `clientAccess`); T2.33–T2.44 run over
// IN-HOUSE (the only mode with enough rows to make a filter narrow). Fixtures are the ones
// MEMB-01-01 shipped, plus `firmRow`.

describe('parseEmailInput (T2.1–T2.6, §7)', () => {
  it('splits on a comma (T2.1)', () => {
    expect(parseEmailInput('a@x.ng, b@x.ng')).toEqual(['a@x.ng', 'b@x.ng'])
  })

  it('splits on semicolons, newlines and spaces too, keeping input order (T2.2)', () => {
    expect(parseEmailInput('a@x.ng;b@x.ng\nc@x.ng d@x.ng')).toEqual(['a@x.ng', 'b@x.ng', 'c@x.ng', 'd@x.ng'])
  })

  it('trims each address (T2.3)', () => {
    expect(parseEmailInput('  a@x.ng  ,  b@x.ng ')).toEqual(['a@x.ng', 'b@x.ng'])
  })

  it('drops empty fragments left by repeated separators (T2.4)', () => {
    expect(parseEmailInput('a@x.ng,,;  ,b@x.ng')).toEqual(['a@x.ng', 'b@x.ng'])
  })

  it('de-dupes case-insensitively, keeping the first spelling seen (T2.5)', () => {
    expect(parseEmailInput('A@x.ng, a@X.ng')).toEqual(['A@x.ng'])
  })

  it('returns nothing for an empty paste (T2.6)', () => {
    expect(parseEmailInput('')).toEqual([])
  })
})

describe('isValidEmail (T2.7–T2.8, §7)', () => {
  it('accepts a plain address, a long dotted/hyphenated one and a plus tag (T2.7)', () => {
    for (const value of ['a@b.co', 'o.adebanjo-ogunleye@okaforandpartners.com.ng', 'x+y@z.ng']) {
      expect(isValidEmail(value)).toBe(true)
    }
  })

  it('rejects a bare domain, a missing @, an embedded space, a missing side and a double @ (T2.8)', () => {
    for (const value of ['a@b', 'ab.co', 'a b@c.co', '@b.co', 'a@', '', 'a@@b.co']) {
      expect(isValidEmail(value)).toBe(false)
    }
  })
})

describe('nameFromEmail / initialsFrom (T2.9–T2.14, §7)', () => {
  it('reads a dotted local part as two capitalised tokens (T2.9)', () => {
    expect(nameFromEmail('t.okonkwo@honeywell.ng')).toBe('T Okonkwo')
  })

  it('reads underscore and dash as separators too (T2.10)', () => {
    expect(nameFromEmail('ada_eze@x.ng')).toBe('Ada Eze')
    expect(nameFromEmail('ada-eze@x.ng')).toBe('Ada Eze')
  })

  it('reads a single-token local part as one capitalised word (T2.11)', () => {
    expect(nameFromEmail('zainab@x.ng')).toBe('Zainab')
  })

  it('takes one letter per token when the local part has two or more (T2.12)', () => {
    expect(initialsFrom('t.okonkwo@x.ng')).toBe('TO')
  })

  it('takes the first TWO letters of a single-token local part (T2.13)', () => {
    // The reason `initialsFrom` is a deliberate fork of customers.ts's `initials(name)`,
    // which would return one character here.
    expect(initialsFrom('zainab@x.ng')).toBe('ZA')
  })

  it('caps at two characters however many tokens there are (T2.14)', () => {
    expect(initialsFrom('a.b.c@x.ng')).toBe('AB')
  })
})

describe('classifyInvites (T2.15–T2.20, §7)', () => {
  it('reports an existing active member, matching case-insensitively (T2.15)', () => {
    // mf1, upper-cased: the comparison is on lower-cased emails, not on the stored string.
    expect(classifyInvites(firm(), ['C.OKAFOR@OKAFOR.NG'])).toEqual(['member'])
  })

  it('reports an already-invited address as invited, not as a member (T2.16)', () => {
    expect(classifyInvites(firm(), ['b.suleiman@okafor.ng'])).toEqual(['invited'])
  })

  it('reports a suspended member as a member (T2.17)', () => {
    expect(classifyInvites(firm(), ['h.yusuf@okafor.ng'])).toEqual(['member'])
  })

  it('reports an unparseable address as malformed (T2.18)', () => {
    expect(classifyInvites(firm(), ['not-an-email'])).toEqual(['malformed'])
  })

  it('reports a fresh valid address as ok (T2.19)', () => {
    expect(classifyInvites(firm(), ['t.okonkwo@okafor.ng'])).toEqual(['ok'])
  })

  it('returns one verdict per address, in input order (T2.20)', () => {
    expect(classifyInvites(firm(), ['new@x.ng', 'c.okafor@okafor.ng', 'bad'])).toEqual(['ok', 'member', 'malformed'])
  })
})

describe('memberFromInvite (T2.21–T2.24, §7)', () => {
  it('mints an invited row with the derived name, the passed inviter and no activity (T2.21)', () => {
    const m = memberFromInvite(
      't.okonkwo@x.ng',
      { mode: 'inhouse', role: 'reviewer', department: 'Finance', position: null },
      'Ngozi Balogun',
    )
    expect(m).toMatchObject({
      email: 't.okonkwo@x.ng',
      name: 'T Okonkwo',
      initials: 'TO',
      role: 'reviewer',
      status: 'invited',
      lastActive: null,
      joined: null,
      isYou: false,
      invitedBy: 'Ngozi Balogun',
    })
    // QA17 pins `invitedBy === '—'` IFF `isYou`, and an invited row is never `isYou`.
    expect(m.invitedBy).not.toBe('—')
  })

  it('mints unique ids that collide with no seeded id, five in one call chain (T2.22)', () => {
    const minted = ['a1@x.ng', 'a2@x.ng', 'a3@x.ng', 'a4@x.ng', 'a5@x.ng'].map((e) =>
      memberFromInvite(e, { mode: 'firm', role: 'preparer', clientAccess: 'all' }, 'Chinedu Okafor'),
    )
    const ids = minted.map((m) => m.id)
    expect(new Set(ids).size).toBe(5)

    const store = seedMembers()
    const seeded = new Set([...store.firm, ...store.inhouse].map((m) => m.id))
    for (const id of ids) expect(seeded.has(id)).toBe(false)
  })

  it('gives a firm invite its clientAccess and NO in-house keys at all (T2.23)', () => {
    const m = memberFromInvite('t.okonkwo@x.ng', { mode: 'firm', role: 'preparer', clientAccess: 'all' }, 'Chinedu Okafor')
    expect(m.clientAccess).toBe('all')
    // Key ABSENT, not merely undefined: `{department: undefined}` would satisfy a
    // `toBeUndefined()` check while still shipping the column into the firm table.
    expect('department' in m).toBe(false)
    expect('position' in m).toBe(false)
  })

  it('gives an in-house invite its department and position and NO clientAccess key (T2.24)', () => {
    const m = memberFromInvite(
      't.okonkwo@x.ng',
      { mode: 'inhouse', role: 'reviewer', department: 'Finance', position: null },
      'Ngozi Balogun',
    )
    expect(m.department).toBe('Finance')
    expect(m.position).toBeNull()
    expect('clientAccess' in m).toBe(false)
  })
})

describe('reducers — immutability and misses (T2.25–T2.32b, §15.1)', () => {
  it('appends without touching the input list (T2.25)', () => {
    const list = firm()
    const added = firmRow('Tosin Okonkwo', 'invited', [0])
    const out = addMembers(list, [added])

    expect(out).not.toBe(list)
    expect(out).toHaveLength(8)
    expect(out[7]).toEqual(added)
    expect(list).toHaveLength(7)
    expect(list).toEqual(firm())
  })

  it('swaps the row with the matching id and leaves the input alone (T2.26)', () => {
    const list = firm()
    const next: Member = { ...list[1], role: 'admin' }
    const out = replaceMember(list, next)

    expect(out).not.toBe(list)
    expect(out[1].role).toBe('admin')
    expect(list[1].role).toBe('preparer')
    expect(out.map((m) => m.role)).toEqual(['admin', 'admin', 'reviewer', 'reviewer', 'preparer', 'preparer', 'reviewer'])
    expect(list).toEqual(firm())
  })

  it('returns a NEW array of the same values when the id is not present (T2.27)', () => {
    const list = firm()
    const ghost = firmRow('Ghost Person', 'active', 'all')
    expect(list.map((m) => m.id)).not.toContain(ghost.id)

    const out = replaceMember(list, ghost)
    expect(out).not.toBe(list)
    expect(out).toEqual(list)
    expect(list).toEqual(firm())
  })

  it('drops exactly the removed id, preserving order (T2.28)', () => {
    const list = firm()
    const out = removeMember(list, 'mf3')

    expect(out).toHaveLength(6)
    expect(out.map((m) => m.id)).toEqual(['mf1', 'mf2', 'mf4', 'mf5', 'mf6', 'mf7'])
    expect(list).toEqual(firm())
  })

  it('builds a NEW member object rather than assigning into the old one (T2.29)', () => {
    const list = firm()
    const out = setMemberRole(list, 'mf2', 'admin')

    expect(out[1]).not.toBe(list[1])
    expect(out[1].role).toBe('admin')
    expect(list[1].role).toBe('preparer')
  })

  it('flips active to suspended and changes nothing else on the row (T2.30)', () => {
    const list = firm()
    const out = setMemberStatus(list, 'mf2', 'suspended')

    expect(out[1].status).toBe('suspended')
    expect(list[1].status).toBe('active')
    expect({ ...out[1], status: 'active' }).toEqual(list[1])
  })

  it('flips suspended back to active (T2.31)', () => {
    const list = firm()
    expect(list[6].id).toBe('mf7')
    expect(list[6].status).toBe('suspended')

    const out = setMemberStatus(list, 'mf7', 'active')
    expect(out[6].status).toBe('active')
    expect(list[6].status).toBe('suspended')
  })

  it('never touches its input list or the seed, in either mode (T2.32)', () => {
    const seedFirmBefore = structuredClone(SEED_FIRM_MEMBERS)
    const seedInhouseBefore = structuredClone(SEED_INHOUSE_MEMBERS)

    const cases = [
      { list: firm(), pristine: firm(), id: 'mf2', extra: firmRow('Tosin Okonkwo', 'invited', [0]) },
      { list: inhouse(), pristine: inhouse(), id: 'mh4', extra: inhouseRow('Tosin Okonkwo', 'invited', null) },
    ]

    for (const c of cases) {
      const untouched = () => expect(c.list).toEqual(c.pristine)

      replaceMember(c.list, { ...c.list[1], role: 'admin' })
      untouched()
      addMembers(c.list, [c.extra])
      untouched()
      removeMember(c.list, c.id)
      untouched()
      setMemberRole(c.list, c.id, 'admin')
      untouched()
      setMemberStatus(c.list, c.id, 'suspended')
      untouched()
    }

    expect(SEED_FIRM_MEMBERS).toEqual(seedFirmBefore)
    expect(SEED_INHOUSE_MEMBERS).toEqual(seedInhouseBefore)
    expect(scopedIds(SEED_FIRM_MEMBERS, 1)).toEqual([0, 1, 3])
    expect(scopedIds(SEED_FIRM_MEMBERS, 2)).toEqual([2, 5])
  })

  it('copies the nested clientAccess when it builds a row, it does not alias it (T2.32b)', () => {
    // T2.32 alone cannot see this: it compares by VALUE, and an aliased array holds the
    // right value until something writes through it. `clientAccess` is the module's only
    // nested value and exists only on firm rows, so the probe has to sit on a
    // subset-scoped firm row — mf2's [0,1,3]. Same hole T1.2b closed for `seedMembers`.
    const list = firm()
    expect(list[1].id).toBe('mf2')
    expect(scopedIds(list, 1)).toEqual([0, 1, 3])

    for (const out of [setMemberRole(list, 'mf2', 'admin'), setMemberStatus(list, 'mf2', 'suspended')]) {
      expect(out[1].clientAccess).not.toBe(list[1].clientAccess)
      expect(out[1].clientAccess).not.toBe(SEED_FIRM_MEMBERS[1].clientAccess)

      scopedIds(out, 1).push(4)
      expect(scopedIds(out, 1)).toEqual([0, 1, 3, 4])
      expect(scopedIds(list, 1)).toEqual([0, 1, 3])
      expect(scopedIds(SEED_FIRM_MEMBERS, 1)).toEqual([0, 1, 3])
    }
  })
})

describe('filterMembers (T2.33–T2.39, §6)', () => {
  it('matches on the name when the email does not carry it (T2.33)', () => {
    // Ngozi's email is n.balogun@honeywell.ng, so 'ngozi' is a name-only match.
    expect(names(filterMembers(inhouse(), 'ngozi', 'all'))).toEqual(['Ngozi Balogun'])
  })

  it('matches on the email as a plain substring, not a domain split (T2.34)', () => {
    // 15, not 16: Oluwafunmilayo sits on honeywellgroup.com.ng, which contains 'honeywell'
    // but not 'honeywell.ng'. The pair is what pins substring matching.
    const domain = filterMembers(inhouse(), 'honeywell.ng', 'all')
    expect(domain).toHaveLength(15)
    expect(names(domain)).not.toContain('Oluwafunmilayo Ademola-Oyediran')
    expect(filterMembers(inhouse(), 'honeywell', 'all')).toHaveLength(16)
  })

  it('ignores case in the query (T2.35)', () => {
    expect(names(filterMembers(inhouse(), 'ADEBAYO', 'all'))).toEqual(['Adebayo Ogunlesi'])
  })

  it('returns nothing when nothing matches (T2.36)', () => {
    expect(filterMembers(inhouse(), 'zzzz', 'all')).toEqual([])
  })

  it('applies the role filter on its own (T2.37)', () => {
    expect(names(filterMembers(inhouse(), '', 'admin'))).toEqual(['Ngozi Balogun'])
  })

  it("treats roleFilter 'all' as no role predicate at all (T2.38)", () => {
    expect(filterMembers(inhouse(), '', 'all')).toHaveLength(16)
  })

  it('ANDs the two predicates, and the AND actually narrows (T2.39)', () => {
    expect(names(filterMembers(inhouse(), 'z', 'reviewer'))).toEqual(['Emeka Uzowulu'])

    // Neither predicate alone can produce that result: 'z' spans all three roles, and
    // the reviewer filter alone returns eight. ('o' would prove nothing — every in-house
    // row matches it via @honeywell.)
    expect(names(filterMembers(inhouse(), 'z', 'all'))).toEqual(['Ngozi Balogun', 'Emeka Uzowulu', 'Zainab Lawal'])
    expect(filterMembers(inhouse(), '', 'reviewer')).toHaveLength(8)
  })
})

describe('isFiltering (QA39, §6/§10.7)', () => {
  // Extracted so MembersView cannot re-derive filterMembers' emptiness rule across a module
  // boundary. The answer picks between the roster-of-one EmptyState (§10.7) and the
  // filtered-to-zero row; when the two rules drifted, those surfaces shadowed each other.
  // Each case asserts the PAIR — the predicate's answer and what filterMembers actually did.

  it('is false when neither predicate is live, and nobody is excluded', () => {
    expect(isFiltering('', 'all')).toBe(false)
    expect(filterMembers(inhouse(), '', 'all')).toHaveLength(16)
  })

  it('reads a whitespace-only query as no query at all — the case that already bit', () => {
    expect(isFiltering('   ', 'all')).toBe(false)
    expect(isFiltering(' \n\t ', 'all')).toBe(false)
    expect(filterMembers(inhouse(), '   ', 'all')).toHaveLength(16)
  })

  it('is true on a non-empty query alone, padding notwithstanding', () => {
    expect(isFiltering('ngozi', 'all')).toBe(true)
    expect(isFiltering('  ngozi  ', 'all')).toBe(true)
    expect(names(filterMembers(inhouse(), '  ngozi  ', 'all'))).toEqual(['Ngozi Balogun'])
  })

  it('is true on a role filter alone, with no query at all', () => {
    expect(isFiltering('', 'admin')).toBe(true)
    expect(isFiltering('   ', 'reviewer')).toBe(true)
    expect(names(filterMembers(inhouse(), '', 'admin'))).toEqual(['Ngozi Balogun'])
  })

  it('still allocates on the not-filtering path', () => {
    // The short-circuit must not hand back the caller's own array — the always-allocate rule
    // the reducers above follow, which a bare `return list` would quietly break.
    const list = inhouse()
    const out = filterMembers(list, '  ', 'all')
    expect(out).not.toBe(list)
    expect(out).toEqual(list)
  })
})

describe('isProtectedAdmin (T2.40–T2.44, §9)', () => {
  it('protects the sole active admin (T2.40)', () => {
    const list = inhouse()
    expect(list[0].name).toBe('Ngozi Balogun')
    expect(isProtectedAdmin(list, list[0])).toBe(true)
  })

  it('does not protect a non-admin (T2.41)', () => {
    const list = inhouse()
    expect(list[3].name).toBe('Tunde Adeyemi')
    expect(list[3].role).toBe('reviewer')
    expect(isProtectedAdmin(list, list[3])).toBe(false)
  })

  it('protects neither admin once there are two active ones (T2.42)', () => {
    const list = setMemberRole(inhouse(), 'mh4', 'admin')
    expect(activeAdmins(list)).toHaveLength(2)
    expect(isProtectedAdmin(list, list[0])).toBe(false)
    expect(isProtectedAdmin(list, list[3])).toBe(false)
  })

  it('re-locks the first admin once the second is suspended (T2.43)', () => {
    const two = setMemberRole(inhouse(), 'mh4', 'admin')
    const list = setMemberStatus(two, 'mh4', 'suspended')

    expect(list[3].status).toBe('suspended')
    expect(isProtectedAdmin(list, list[0])).toBe(true)
    expect(isProtectedAdmin(list, list[3])).toBe(false)
  })

  it('does not count an invited admin (T2.44)', () => {
    const list = setMemberRole(inhouse(), 'mh16', 'admin')

    expect(list[15].name).toBe('Sadiq Ibrahim')
    expect(list[15].status).toBe('invited')
    expect(isProtectedAdmin(list, list[0])).toBe(true)
    expect(isProtectedAdmin(list, list[15])).toBe(false)
  })
})

// ============================================================================
// QA19–QA35 — adversarial / edge coverage (MEMB-01-02 QA, Mode B)
// ============================================================================
// The T2 batch above is the acceptance-criteria transcription, authored RED before the
// implementation existed. This batch is what mutation-testing the shipped implementation
// showed those specs CANNOT see: every spec below was written against a mutant that
// survived the whole T2 suite. The mutation it kills is named in each spec's comment.

describe('reducer misses — the four AC#6 leaves T2.27 does not reach (QA19–QA22, §15.1)', () => {
  // AC#6 covers a miss for ALL FIVE reducers, but T2.27 covers `replaceMember` alone. The
  // superseded rules.ts:253-268 reading (`return rules as CustomRule[]`, the input
  // REFERENCE) survives the entire T2 suite in the other four. These are the guards.

  it('addMembers allocates a new array even when nothing is added (QA19)', () => {
    const list = firm()
    const out = addMembers(list, [])

    expect(out).not.toBe(list)
    expect(out).toEqual(list)
    expect(out).toHaveLength(7)
    // A miss reallocates the ARRAY, not the rows — untouched rows pass through by reference.
    expect(out[0]).toBe(list[0])
    expect(list).toEqual(firm())
  })

  it('removeMember allocates a new array when the id is not present (QA20)', () => {
    const list = firm()
    expect(list.map((m) => m.id)).not.toContain('mf99')

    const out = removeMember(list, 'mf99')
    expect(out).not.toBe(list)
    expect(out).toEqual(list)
    expect(out).toHaveLength(7)
    expect(out[0]).toBe(list[0])
    expect(list).toEqual(firm())
  })

  it('setMemberRole allocates a new array when the id is not present (QA21)', () => {
    const list = firm()
    const out = setMemberRole(list, 'mf99', 'admin')

    expect(out).not.toBe(list)
    expect(out).toEqual(list)
    expect(out.map((m) => m.role)).toEqual(list.map((m) => m.role))
    expect(out[0]).toBe(list[0])
    expect(list).toEqual(firm())
  })

  it('setMemberStatus allocates a new array when the id is not present (QA22)', () => {
    const list = firm()
    const out = setMemberStatus(list, 'mf99', 'suspended')

    expect(out).not.toBe(list)
    expect(out).toEqual(list)
    expect(out.map((m) => m.status)).toEqual(list.map((m) => m.status))
    expect(out[0]).toBe(list[0])
    expect(list).toEqual(firm())
  })
})

describe('reducers key off id, never email (QA23, §15.1)', () => {
  it('routes by next.id even when next carries a DIFFERENT row\'s email (QA23)', () => {
    // T2.26/T2.27 cannot see this: T2.26 builds `next` by spreading the target row, so its
    // id AND email both match, and T2.27's ghost matches neither. An email-keyed
    // `replaceMember` therefore passes both. This is the spec that separates them.
    const list = firm()
    expect(list[1].id).toBe('mf2')
    expect(list[2].id).toBe('mf3')

    // mf2's email on a row whose id is mf3 — the id must win.
    const next: Member = { ...list[1], id: 'mf3', name: 'Impostor' }
    const out = replaceMember(list, next)

    expect(out[2]).toBe(next)
    expect(out[1]).toBe(list[1])
    expect(out[1].name).toBe('Folake Adesina')
    expect(out.map((m) => m.id)).toEqual(['mf1', 'mf2', 'mf3', 'mf4', 'mf5', 'mf6', 'mf7'])

    // And the converse: rewriting the email alone still lands on the same row.
    const renamed: Member = { ...list[1], email: 'nobody@nowhere.ng' }
    const out2 = replaceMember(list, renamed)
    expect(out2[1]).toBe(renamed)
    expect(out2[2]).toBe(list[2])
  })
})

describe('minted ids are unique ACROSS modes, not only within one (QA24, §7)', () => {
  it('prefixes per mode over one shared counter, colliding with no seeded id (QA24)', () => {
    // T2.22 mints five FIRM invites only, so the mf/mh fork — the actual mechanism for
    // cross-mode uniqueness — is never observed there: an `mf`-only implementation passes it.
    const firmInvites = ['q1@x.ng', 'q2@x.ng'].map((e) =>
      memberFromInvite(e, { mode: 'firm', role: 'preparer', clientAccess: 'all' }, 'Chinedu Okafor'),
    )
    const inhouseInvites = ['q3@x.ng', 'q4@x.ng'].map((e) =>
      memberFromInvite(e, { mode: 'inhouse', role: 'reviewer', department: 'Finance', position: null }, 'Ngozi Balogun'),
    )

    for (const m of firmInvites) expect(m.id.startsWith('mf')).toBe(true)
    for (const m of inhouseInvites) expect(m.id.startsWith('mh')).toBe(true)

    const ids = [...firmInvites, ...inhouseInvites].map((m) => m.id)
    expect(new Set(ids).size).toBe(4)

    // Against BOTH seeded modes, not just the minting one.
    const store = seedMembers()
    const seeded = new Set([...store.firm, ...store.inhouse].map((m) => m.id))
    for (const id of ids) expect(seeded.has(id)).toBe(false)
  })
})

describe('the seed literals themselves, as an oracle no snapshot can launder (QA25, §15.6)', () => {
  it('holds its hand-authored values after every reducer runs over the CONSTANTS (QA25)', () => {
    // T2.32 snapshots SEED_* with structuredClone at the top of its own body, so corruption
    // committed by an earlier spec in this file is captured by the snapshot and the
    // comparison passes vacuously. This spec asserts the literals directly — and runs the
    // five reducers over the constants THEMSELVES (not a clone) first, which is the one
    // call shape T2.32 never makes.
    const extraFirm = firmRow('Tosin Okonkwo', 'invited', [0])
    const extraInhouse = inhouseRow('Tosin Okonkwo', 'invited', null)

    replaceMember(SEED_FIRM_MEMBERS, { ...SEED_FIRM_MEMBERS[1], role: 'admin' })
    addMembers(SEED_FIRM_MEMBERS, [extraFirm])
    removeMember(SEED_FIRM_MEMBERS, 'mf2')
    setMemberRole(SEED_FIRM_MEMBERS, 'mf2', 'admin')
    setMemberStatus(SEED_FIRM_MEMBERS, 'mf2', 'suspended')

    replaceMember(SEED_INHOUSE_MEMBERS, { ...SEED_INHOUSE_MEMBERS[3], role: 'admin' })
    addMembers(SEED_INHOUSE_MEMBERS, [extraInhouse])
    removeMember(SEED_INHOUSE_MEMBERS, 'mh4')
    setMemberRole(SEED_INHOUSE_MEMBERS, 'mh4', 'admin')
    setMemberStatus(SEED_INHOUSE_MEMBERS, 'mh4', 'suspended')

    expect(SEED_FIRM_MEMBERS.map((m) => m.id)).toEqual(['mf1', 'mf2', 'mf3', 'mf4', 'mf5', 'mf6', 'mf7'])
    expect(SEED_FIRM_MEMBERS.map((m) => m.role)).toEqual([
      'admin',
      'preparer',
      'reviewer',
      'reviewer',
      'preparer',
      'preparer',
      'reviewer',
    ])
    expect(SEED_FIRM_MEMBERS.map((m) => m.status)).toEqual([
      'active',
      'active',
      'active',
      'active',
      'active',
      'invited',
      'suspended',
    ])
    expect(SEED_FIRM_MEMBERS.map((m) => m.clientAccess)).toEqual(['all', [0, 1, 3], [2, 5], 'all', 'all', [0], 'all'])

    expect(SEED_INHOUSE_MEMBERS.map((m) => m.id)).toEqual([
      'mh1',
      'mh2',
      'mh3',
      'mh4',
      'mh5',
      'mh6',
      'mh7',
      'mh8',
      'mh9',
      'mh10',
      'mh11',
      'mh12',
      'mh13',
      'mh14',
      'mh15',
      'mh16',
    ])
    expect(SEED_INHOUSE_MEMBERS.map((m) => m.role)).toEqual([
      'admin',
      'reviewer',
      'reviewer',
      'reviewer',
      'reviewer',
      'reviewer',
      'preparer',
      'preparer',
      'preparer',
      'preparer',
      'reviewer',
      'preparer',
      'reviewer',
      'preparer',
      'preparer',
      'reviewer',
    ])
    expect(SEED_INHOUSE_MEMBERS.map((m) => m.status)).toEqual([
      'active',
      'active',
      'active',
      'active',
      'active',
      'suspended',
      'active',
      'active',
      'active',
      'active',
      'active',
      'active',
      'active',
      'active',
      'invited',
      'invited',
    ])
    expect(SEED_INHOUSE_MEMBERS.map((m) => m.position)).toEqual([
      'fin_dir',
      'fin_dir',
      'line_mgr',
      'controller',
      'compliance',
      'cfo',
      'preparer',
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
    ])
    // The one row T2.34's 15-vs-16 split turns on.
    expect(SEED_INHOUSE_MEMBERS[10].email).toBe('o.ademola-oyediran@honeywellgroup.com.ng')
    // Exactly one active admin per mode — the frame every §9 spec is read against.
    expect(activeAdmins(SEED_FIRM_MEMBERS)).toHaveLength(1)
    expect(activeAdmins(SEED_INHOUSE_MEMBERS)).toHaveLength(1)
  })
})

describe('name and initials — the branches no acceptance criterion names (QA26–QA31, §7)', () => {
  it('returns ONE character for a single-LETTER local part, not two (QA26)', () => {
    // Records that the plan's "always 2 chars" is really "at MOST 2": there is no second
    // letter to take, and no seeded row reaches this. `slice(0, 2)` is the pinned behaviour.
    expect(initialsFrom('a@x.ng')).toBe('A')
    expect(nameFromEmail('a@x.ng')).toBe('A')
  })

  it('TITLE-cases each token — it does not preserve the typed casing (QA27)', () => {
    // An underspecified pick: the plan says only "capitalised". Pinned so a later subtask
    // cannot flip it silently — invited rows render beside Title Case seeded names.
    expect(nameFromEmail('ADEBAYO.X@y.ng')).toBe('Adebayo X')
    expect(nameFromEmail('MiXeD.CaSe@x.ng')).toBe('Mixed Case')
    expect(nameFromEmail('T.OKONKWO@honeywell.ng')).toBe('T Okonkwo')
  })

  it("treats '+' as a local-part separator, exactly like . _ and - (QA28)", () => {
    // T2.9/T2.10 pin . _ and -, and T2.7 only validates a plus-tagged address; nothing pins
    // what a plus tag DERIVES. A plus tag is the likeliest real address to hit this.
    expect(nameFromEmail('c+tag@x.ng')).toBe('C Tag')
    expect(initialsFrom('c+tag@x.ng')).toBe('CT')
    expect(nameFromEmail('chinedu+okafor@x.ng')).toBe('Chinedu Okafor')
  })

  it('stores the address VERBATIM and normalises only the derived name (QA29)', () => {
    // `parseEmailInput` keeps the first spelling seen because that is what gets mailed;
    // `memberFromInvite` has to agree, or the chip and the row disagree on the address.
    const m = memberFromInvite('MiXeD.CaSe@X.NG', { mode: 'firm', role: 'preparer', clientAccess: 'all' }, 'Chinedu Okafor')
    expect(m.email).toBe('MiXeD.CaSe@X.NG')
    expect(m.name).toBe('Mixed Case')
    expect(m.initials).toBe('MC')
  })

  it('does not trim — the pipeline trims in parseEmailInput, not in the validator (QA30)', () => {
    expect(isValidEmail(' a@b.co')).toBe(false)
    expect(isValidEmail('a@b.co ')).toBe(false)
    // …and parseEmailInput is why that is safe: it strips whitespace before validation.
    expect(parseEmailInput(' a@b.co ')).toEqual(['a@b.co'])
  })

  it('survives a Windows CRLF paste without a stray carriage return (QA31)', () => {
    // `\s` in the separator class is load-bearing: a `[,;\t\n ]` class would leave the `\r`
    // glued to the address, and every chip from a Windows paste would read Not a valid email.
    const out = parseEmailInput('a@x.ng\r\nb@x.ng\r\n')
    expect(out).toEqual(['a@x.ng', 'b@x.ng'])
    for (const address of out) expect(isValidEmail(address)).toBe(true)
  })
})

describe('invite-pipeline contracts MEMB-01-06 has to honour (QA32–QA35, §7)', () => {
  it('copies a firm invite\'s clientAccess so minted rows never share one array (QA32)', () => {
    // MEMB-01-06 maps `memberFromInvite` over a whole chip list from a SINGLE `opts`. If the
    // array were assigned through, every invited row would alias it — and the drawer's
    // scoping editor would then edit all of them at once.
    const opts: Extract<InviteOptions, { mode: 'firm' }> = { mode: 'firm', role: 'preparer', clientAccess: [0, 2] }
    const a = memberFromInvite('a@x.ng', opts, 'Chinedu Okafor')
    const b = memberFromInvite('b@x.ng', opts, 'Chinedu Okafor')

    expect(a.clientAccess).toEqual([0, 2])
    expect(b.clientAccess).toEqual([0, 2])
    expect(a.clientAccess).not.toBe(b.clientAccess)
    expect(a.clientAccess).not.toBe(opts.clientAccess)
    ;(a.clientAccess as number[]).push(4)
    expect(b.clientAccess).toEqual([0, 2])
    expect(opts.clientAccess).toEqual([0, 2])
  })

  it('classifies each address independently — no memory of earlier verdicts (QA33)', () => {
    // `parseEmailInput` de-dupes within ONE paste. `classifyInvites` de-dupes nothing, so a
    // second paste of the same address reads `ok` again. MEMB-01-06 owns that de-dupe.
    expect(classifyInvites(firm(), ['new@x.ng', 'NEW@x.ng'])).toEqual(['ok', 'ok'])
  })

  it('appends duplicates verbatim — addMembers de-dupes nothing (QA34)', () => {
    // Pinned as a contract, not an accident: MEMB-01-06 must drop non-`ok` classifications
    // and de-dupe chips across successive pastes itself, because this funnel will not.
    const list = firm()
    const out = addMembers(list, [list[1], list[1]])

    expect(out).toHaveLength(9)
    expect(out.map((m) => m.id)).toEqual(['mf1', 'mf2', 'mf3', 'mf4', 'mf5', 'mf6', 'mf7', 'mf2', 'mf2'])
    expect(list).toEqual(firm())
  })

  it('accepts a separators-only local part and mints a NAMELESS row (QA35)', () => {
    // RECORDED, NOT ENDORSED. `isValidEmail` is deliberately minimal, so a local part made
    // only of separators passes it, classifies `ok`, and mints a row whose name and initials
    // are both empty — a blank Person cell in MEMB-01-04's table. Pinned so the behaviour is
    // a visible decision rather than an accident; the UX call (reject the chip) belongs to
    // MEMB-01-06 and is recorded in its notes.
    expect(isValidEmail('...@x.ng')).toBe(true)
    expect(classifyInvites(firm(), ['...@x.ng'])).toEqual(['ok'])

    const m = memberFromInvite('...@x.ng', { mode: 'firm', role: 'preparer', clientAccess: 'all' }, 'Chinedu Okafor')
    expect(m.name).toBe('')
    expect(m.initials).toBe('')
  })
})

describe('filter and guard — boundaries the T2 batch does not reach (QA36–QA38, §6/§9)', () => {
  it('TRIMS the query before matching, so trailing whitespace does not zero the box (QA36)', () => {
    // An underspecified pick: the plan says only "case-insensitive substring". Pinned
    // because MEMB-01-03 wires this straight to a text input, where a trailing space is
    // the difference between one result and none.
    expect(names(filterMembers(inhouse(), '  ngozi  ', 'all'))).toEqual(['Ngozi Balogun'])
    expect(names(filterMembers(inhouse(), 'ngozi ', 'admin'))).toEqual(['Ngozi Balogun'])
    // A whitespace-only query is an EMPTY query, not a query that matches nothing.
    expect(filterMembers(inhouse(), '   ', 'all')).toHaveLength(16)
    expect(names(filterMembers(inhouse(), ' \n\t ', 'admin'))).toEqual(['Ngozi Balogun'])
  })

  it('degrades to empty on an empty list, and still allocates (QA37)', () => {
    // QA12 covered MEMB-01-01's derivations at the empty boundary; none of MEMB-01-02's
    // exports had that coverage.
    const empty: readonly Member[] = []
    const row = firmRow('Ghost Person', 'active', 'all')

    expect(classifyInvites(empty, ['a@x.ng', 'bad'])).toEqual(['ok', 'malformed'])
    expect(filterMembers(empty, 'ngozi', 'all')).toEqual([])
    expect(addMembers(empty, [row])).toEqual([row])
    expect(replaceMember(empty, row)).toEqual([])
    expect(removeMember(empty, 'mf1')).toEqual([])
    expect(setMemberRole(empty, 'mf1', 'admin')).toEqual([])
    expect(setMemberStatus(empty, 'mf1', 'active')).toEqual([])

    for (const out of [
      addMembers(empty, []),
      replaceMember(empty, row),
      removeMember(empty, 'mf1'),
      setMemberRole(empty, 'mf1', 'admin'),
      setMemberStatus(empty, 'mf1', 'active'),
    ]) {
      expect(out).not.toBe(empty)
    }
  })

  it('reads the LIST, never the member alone — including for a detached row (QA38)', () => {
    // An active admin who is not in the list still reads as protected when that list holds
    // exactly one active admin: `isProtectedAdmin` does no identity lookup. Recorded so
    // MEMB-01-04's ⋯ menu and MEMB-01-07's drawer pass the CURRENT row, never a stale one.
    const sole = inhouse()[0]
    expect(sole.role).toBe('admin')
    expect(isProtectedAdmin([], sole)).toBe(false)

    const detached: Member = { ...firmRow('Ghost Admin', 'active', 'all'), role: 'admin' }
    expect(inhouse().map((m) => m.id)).not.toContain(detached.id)
    expect(isProtectedAdmin(inhouse(), detached)).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// MEMB-01-04 QA — the three display derivations the table would otherwise inline
// ---------------------------------------------------------------------------
// All three shipped inside MembersTable/MembersView, where `environment: node` cannot reach
// them: two of them are INVENTED COPY (a singular §6 and a singular §10.4 that the story
// supplies only in the plural) and the third has a fallback branch no screenshot can hit.
// Pulled into this module — which already owns `clientAccessLabel`, `lastActiveLabel` and
// CAPABILITY_ROWS' copy — so the reconstructions are artifacts a spec holds rather than
// strings only a reviewer's eye guards.

describe('accessRoleLabel (QA40–QA41, §6)', () => {
  it('returns the ACCESS_ROLES label for every role, never a re-cased id (QA40)', () => {
    expect(accessRoleLabel('admin')).toBe('Admin')
    expect(accessRoleLabel('preparer')).toBe('Preparer')
    expect(accessRoleLabel('reviewer')).toBe('Reviewer')

    // The pin, not a restatement: every seed row's label must come off the constant, so a
    // label edit in ACCESS_ROLES cannot leave the table rendering the old word. Both modes,
    // because the column is the one cell §15.7 gives both grids.
    for (const m of [...firm(), ...inhouse()]) {
      const declared = ACCESS_ROLES.find((r) => r.id === m.role)
      expect(declared).toBeDefined()
      expect(accessRoleLabel(m.role)).toBe(declared?.label)
    }
  })

  it('falls back to the raw id for a role this build does not know (QA41)', () => {
    // The `roleOf` fallback's twin (workflows.ts:39-41): a persisted id from a newer build
    // must render as something rather than crash the row. Unreachable from the seed, which
    // is exactly why the screenshot gate cannot stand in for this spec.
    expect(accessRoleLabel('auditor' as never)).toBe('auditor')
    expect(ACCESS_ROLES.map((r) => r.id)).not.toContain('auditor')
  })
})

describe('unassignedNotice — §6 copy, plural verbatim and singular reconstructed (QA42–QA44)', () => {
  it('renders §6 verbatim at the seed count of 2 (QA42)', () => {
    // The count the in-house seed actually produces: WF_ROLES holds 8 positions and the
    // roster holds 6 of them, so `fin_mgr` and `ceo` are the two §6 names.
    const unassigned = unassignedPositions(inhouse())
    expect(unassigned).toEqual(['fin_mgr', 'ceo'])
    expect(unassignedNotice(unassigned.length)).toBe(
      '2 approval positions have nobody assigned. Policies that use them will block.',
    )
  })

  it('reconstructs the singular phrase for phrase (QA43)', () => {
    // INVENTED COPY, pinned so the reconstruction is reviewable as text rather than as a
    // ternary buried in a component. Three agreements move together: positions→position,
    // have→has, them→it. Reachable via MEMB-01-07's drawer.
    expect(unassignedNotice(1)).toBe('1 approval position has nobody assigned. Policies that use it will block.')
  })

  it('takes the plural for every count that is not 1, zero included (QA44)', () => {
    // Zero is not rendered — MembersView gates on `unassigned.length > 0` — but the branch
    // must not be the singular if that gate is ever loosened, because "0 approval position
    // has" is the one wording no reading of §6 permits.
    expect(unassignedNotice(0)).toBe('0 approval positions have nobody assigned. Policies that use them will block.')
    expect(unassignedNotice(8)).toBe('8 approval positions have nobody assigned. Policies that use them will block.')
    // Firm mode's number, and the reason the notice is gated on MODE and not on this count:
    // firm rows carry no position at all (T1.6), so every position reads as unassigned.
    expect(unassignedPositions(firm()).length).toBe(WF_ROLES.length)
  })
})

describe('stepsWarning — §10.4 copy over stepsFor().total (QA45–QA46)', () => {
  it('renders §10.4 verbatim for the suspended CFO the seed is built around (QA45)', () => {
    // The §2 headline frame end to end: the only cfo holder is suspended, and both cfo
    // steps across the in-house policies are what "those steps will block" refers to.
    const cfo = inhouse().find((m) => m.position === 'cfo')
    expect(cfo?.status).toBe('suspended')
    const steps = stepsFor(SEED_INHOUSE_POLICIES, 'cfo')
    expect(steps.total).toBe(2)
    expect(stepsWarning(steps.total)).toBe('Named in 2 approval steps · those steps will block')
  })

  it('reconstructs the singular, and keeps the plural everywhere else (QA46)', () => {
    // INVENTED COPY. steps→step and those→that move together; the middle dot and its
    // spacing are §10.4's, not a renderer's.
    expect(stepsWarning(1)).toBe('Named in 1 approval step · that step will block')
    expect(stepsWarning(3)).toBe('Named in 3 approval steps · those steps will block')
    // 0 is never rendered (the row warning is gated on `total > 0`), but if it ever were,
    // "Named in 0 approval step" would be wrong in the same way "0 positions has" is.
    expect(stepsWarning(0)).toBe('Named in 0 approval steps · those steps will block')
  })
})

// ---------------------------------------------------------------------------
// MEMB-01-05 — the two §6 strings the expander renders verbatim
// ---------------------------------------------------------------------------
// Authored WITH the constants rather than before them: MEMB-01-05 is `Test-first: no` and
// everything else it ships is rendering, whose oracle is the deploy gate. Copy is the one
// part a screenshot cannot audit — a reviewer reads a fluent paraphrase as correct — and
// AC#1 requires the footnote VERBATIM. QA18 already pins the eight row labels; this pins
// the other half of that obligation, plus the `Client users` card's sentence.

describe('MEMB-01-05 expander copy (T5.1, §6)', () => {
  it("carries §6's footnote and Client users copy verbatim (T5.1)", () => {
    expect(CAPABILITY_FOOTNOTE).toBe(
      'Approving also requires an approval position in the policy that routes the invoice. A person needs the Reviewer role to act on approval steps at all, and an approval position to decide which steps.',
    )
    expect(CLIENT_USERS_COPY).toBe(
      'Give a contact at one of your clients read-only access, or approval rights on their own invoices.',
    )
  })
})

// ---------------------------------------------------------------------------
// MEMB-01-06 — the invite modal's copy, its chip gate and its picker derivations
// ---------------------------------------------------------------------------
// MEMB-01-06 is `Test-first: no` and most of what it ships is rendering, whose oracle is the
// deploy gate. These six are the exceptions — the parts that are FACTS rather than pixels:
// three verbatim §3/§7 strings, one invented sentence, one search rule that must agree with
// `filterMembers`, and the chip gate that settles DEFECT D1. All of them would otherwise live
// inside a component `environment: node` cannot mount (§15.8).

describe('MEMB-01-06 invite modal copy (T6.1-T6.2, §3/§7)', () => {
  it("carries §3's Reviewer hint verbatim (T6.1)", () => {
    // §3 calls this vocabulary load-bearing: role means "may act on approval steps at all",
    // position means "which steps". A fluent paraphrase collapses that distinction and reads
    // as correct to a reviewer's eye, which is the whole reason it is pinned as text.
    expect(REVIEWER_HINT).toBe(
      'Only members with the Reviewer role can act on approval steps. The position decides which steps.',
    )
  })

  it("carries §7's three chip errors, one per non-ok verdict (T6.2)", () => {
    expect(INVITE_ERROR.member).toBe('Already a member')
    expect(INVITE_ERROR.invited).toBe('Already invited')
    expect(INVITE_ERROR.malformed).toBe('Not a valid email')

    // The pin, not a restatement: every verdict `classifyInvites` can actually produce for a
    // FAILING chip must have a string here, so a fourth verdict cannot ship a blank error line
    // under a red chip. Driven through the real classifier rather than asserted as a key list.
    const list = firm()
    const active = list[1]
    const pending = list[5]
    expect(active.status).toBe('active')
    expect(pending.status).toBe('invited')

    const verdicts = classifyInvites(list, [active.email, pending.email, 'not-an-email', 'new@x.ng'])
    expect(verdicts).toEqual(['member', 'invited', 'malformed', 'ok'])
    for (const v of verdicts) {
      if (v === 'ok') continue
      expect(INVITE_ERROR[v]).toBeTruthy()
    }
    // …and nothing else, so `ok` never acquires a string that would render as an error.
    expect(Object.keys(INVITE_ERROR).sort()).toEqual(['invited', 'malformed', 'member'])
  })
})

describe('hasDerivableName — DEFECT D1 settled without moving QA35 (T6.3-T6.4, §7)', () => {
  it('rejects a separators-only local part, and leaves classifyInvites untouched (T6.3)', () => {
    expect(hasDerivableName('...@x.ng')).toBe(false)
    expect(hasDerivableName('-@x.ng')).toBe(false)
    expect(hasDerivableName('._+-@x.ng')).toBe(false)

    // The two facts it reconciles, restated here so the three move together. QA35 pins the
    // classifier saying `ok` for this address; that stays LITERALLY true, because the gate is
    // a sibling the modal applies on top of the verdict, not a new branch inside it.
    expect(isValidEmail('...@x.ng')).toBe(true)
    expect(classifyInvites(firm(), ['...@x.ng'])).toEqual(['ok'])

    // And the gate is exactly "would this mint a nameless row?" — nothing wider.
    expect(nameFromEmail('...@x.ng')).toBe('')
    expect(initialsFrom('...@x.ng')).toBe('')
  })

  it('accepts every address the pipeline is meant to mint, and is not a validator (T6.4)', () => {
    // Every seeded address in both modes, so the gate can never reject a real member's shape.
    for (const m of [...firm(), ...inhouse()]) expect(hasDerivableName(m.email)).toBe(true)

    // The awkward shapes the QA26-QA31 batch already pinned name/initials for.
    for (const address of ['a@x.ng', 'c+tag@x.ng', 'MiXeD.CaSe@X.NG', 'o.adebanjo-ogunleye@okaforandpartners.com.ng']) {
      expect(hasDerivableName(address)).toBe(true)
      expect(nameFromEmail(address)).not.toBe('')
    }

    // NOT a validator on its own — `'nope'` derives a name and is not an address. The contract
    // is about the COMPOSITION: `ok && hasDerivableName` is strictly narrower than `ok`, so
    // applying it can only ever reject more, never admit something the classifier refused.
    expect(hasDerivableName('nope')).toBe(true)
    expect(isValidEmail('nope')).toBe(false)
  })
})

describe('the firm client picker (T6.5-T6.7, §7)', () => {
  it("matches filterMembers' rule — trimmed, case-insensitive substring (T6.5)", () => {
    // Deliberately the same rule as the roster search box eight inches away (QA36). Two search
    // inputs on one tab that disagreed about a trailing space would be indefensible.
    expect(filterClientRoster('lagos').map((c) => c.id)).toEqual([0])
    expect(filterClientRoster('LAGOS').map((c) => c.id)).toEqual([0])
    expect(filterClientRoster('  foods  ').map((c) => c.id)).toEqual([1])
    // A substring, not a prefix — and one that spans two rows, so the filter is really running.
    expect(filterClientRoster('ltd').map((c) => c.name)).toEqual([
      'Lagos Freight & Logistics Ltd',
      'Sahara Foods Distribution Ltd',
    ])
    expect(filterClientRoster('zzz')).toEqual([])
  })

  it('treats a whitespace-only query as an EMPTY one, and always allocates (T6.6)', () => {
    expect(filterClientRoster('')).toHaveLength(CLIENT_ROSTER.length)
    expect(filterClientRoster('   ')).toHaveLength(CLIENT_ROSTER.length)
    expect(filterClientRoster(' \n\t ').map((c) => c.id)).toEqual([0, 1, 2, 3, 4, 5])

    // Never the module constant itself: `CLIENT_ROSTER` is readonly at the TYPE level only, and
    // a caller handed the real array could sort or splice the roster for the whole session.
    const all = filterClientRoster('')
    expect(all).not.toBe(CLIENT_ROSTER)
    all.length = 0
    expect(CLIENT_ROSTER).toHaveLength(6)
  })

  it('counts against the ROSTER, not against what the search left showing (T6.7)', () => {
    // §7's shape verbatim. The seed's roster is 6, which is the number §7 itself writes.
    expect(CLIENT_ROSTER).toHaveLength(6)
    expect(clientSelectionCount(3)).toBe('3 of 6 selected')

    // The one that matters: filtering must not restate the denominator. A 3-client invite with
    // "lagos" typed into the box still grants 3 of 6, not 1 of 1.
    expect(filterClientRoster('lagos')).toHaveLength(1)
    expect(clientSelectionCount(3)).toBe('3 of 6 selected')

    // Both ends, including the zero that disables `Send invites` and the all-ticked case that
    // is deliberately NOT collapsed into `All clients` — the user picked six, not "everyone".
    expect(clientSelectionCount(0)).toBe('0 of 6 selected')
    expect(clientSelectionCount(6)).toBe('6 of 6 selected')
  })
})

describe('invitedNotice — the send confirmation (T6.8)', () => {
  it('pluralises the person it invited, one and many (T6.8)', () => {
    // INVENTED COPY, both halves. §7 says the modal closes when everything sends; it says
    // nothing about what confirms it, so the sentence is a reconstruction and belongs where a
    // spec holds it — same argument as `unassignedNotice`/`stepsWarning`'s singulars.
    expect(invitedNotice(1)).toBe('Invited 1 person.')
    expect(invitedNotice(3)).toBe('Invited 3 people.')

    // Reachable: a partial send flashes the count that ACTUALLY landed while the failed chips
    // stay in the modal, so this string is rendered for counts below the chip total too.
    expect(invitedNotice(2)).toBe('Invited 2 people.')
    // 0 is never rendered — the modal raises no flash when nothing sent — but the branch must
    // not read "Invited 0 person." if that gate is ever loosened.
    expect(invitedNotice(0)).toBe('Invited 0 people.')
  })
})
