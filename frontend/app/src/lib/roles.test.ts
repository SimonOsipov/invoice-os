import { describe, expect, it } from 'vitest'

import { SEED_FIRM_MEMBERS, SEED_INHOUSE_MEMBERS } from './members'
import {
  holderCount,
  holders,
  inspectorResolve,
  newRoleKey,
  pruneMember,
  replaceRole,
  resolve,
  roleOf,
  roleUsage,
  rolesOfMember,
  SEED_FIRM_ROLES,
  SEED_INHOUSE_ROLES,
  seedRoles,
  setRoleMembers,
  stepsForMember,
  steps as roleSteps,
  unassignedNotice,
  unassignedRoles,
  type Role,
  type RoleSteps,
} from './roles'
import { roleOf as wfRoleOf, SEED_FIRM_POLICIES, SEED_INHOUSE_POLICIES, seedPolicies, WF_ROLES, type Policy } from './workflows'

// --- fixtures ---------------------------------------------------------------
// Every approval node in a seeded policy set, root lane and both branch lanes, one level
// deep — mirrors stepsFor's own traversal (members.ts:576-596).
function approvalRoles(policies: readonly Policy[]): string[] {
  const out: string[] = []
  for (const p of policies) {
    for (const n of p.nodes) {
      if (n.type === 'approval') out.push(n.role)
      else if (n.type === 'condition') {
        for (const child of [...n.then, ...n.else]) {
          if (child.type === 'approval') out.push(child.role)
        }
      }
    }
  }
  return out
}

const role = (key: string, title: string, desc: string, members: string[]): Role => ({ key, title, desc, members })

describe('AC-2 — seed keys, titles, descriptions', () => {
  it('seed firm has the six specified keys in brief order', () => {
    expect(SEED_FIRM_ROLES.map((r) => r.key)).toEqual(['preparer', 'fin_mgr', 'fin_dir', 'compliance', 'cfo', 'quality_reviewer'])
  })

  it('seed firm titles and descs are the brief strings', () => {
    expect(SEED_FIRM_ROLES.map((r) => r.title)).toEqual([
      'Invoice Preparer',
      'Engagement Manager',
      'Senior Manager',
      'Tax Reviewer',
      'Engagement Partner',
      'Quality Reviewer',
    ])
    expect(SEED_FIRM_ROLES.map((r) => r.desc)).toEqual([
      'Prepares and imports client invoices',
      'First sign-off on a client invoice',
      'Second sign-off above ₦250m',
      'Checks VAT, WHT and TIN detail before filing',
      'Signs off invoices above ₦1bn',
      'Second-partner review on flagged engagements',
    ])
  })

  it('seed inhouse descs are the old department strings', () => {
    expect(SEED_INHOUSE_ROLES.map((r) => r.desc)).toEqual([
      'Accounts Payable',
      'Requesting dept.',
      'Finance',
      'Finance',
      'Finance',
      'Tax & Compliance',
      'Executive',
      'Executive',
    ])
  })

  it('firm has exactly one unsignable role', () => {
    expect(unassignedRoles(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS).map((r) => r.key)).toEqual(['quality_reviewer'])
  })

  it('inhouse has three unsignable roles, one of them suspended-only', () => {
    expect(unassignedRoles(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS).map((r) => r.key)).toEqual(['fin_mgr', 'cfo', 'ceo'])
    expect(resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo').text).toBe('Adebayo Ogunlesi')
  })

  it('every seeded approval step names a role that exists in that mode', () => {
    const firmKeys = new Set(SEED_FIRM_ROLES.map((r) => r.key))
    const inhouseKeys = new Set(SEED_INHOUSE_ROLES.map((r) => r.key))
    const firmRoles = approvalRoles(SEED_FIRM_POLICIES)
    const inhouseRoles = approvalRoles(SEED_INHOUSE_POLICIES)
    // Guards against a vacuous pass — SEED_FIRM_POLICIES/SEED_INHOUSE_POLICIES are already
    // shipped (workflows.ts), so these lists are real and non-empty regardless of roles.ts.
    expect(firmRoles.length).toBeGreaterThan(0)
    expect(inhouseRoles.length).toBeGreaterThan(0)
    for (const key of firmRoles) expect(firmKeys.has(key)).toBe(true)
    for (const key of inhouseRoles) expect(inhouseKeys.has(key)).toBe(true)
  })

  it('every seeded holder id is a member of the same mode', () => {
    const cases = [
      [SEED_FIRM_ROLES, SEED_FIRM_MEMBERS],
      [SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS],
    ] as const
    for (const [roles, members] of cases) {
      const holderIds = roles.flatMap((r) => r.members)
      // Guards against a vacuous pass while SEED_*_ROLES is stubbed empty.
      expect(holderIds.length).toBeGreaterThan(0)
      for (const id of holderIds) expect(members.some((m) => m.id === id)).toBe(true)
    }
  })

  it('no invited member holds a role', () => {
    const cases = [
      [SEED_FIRM_ROLES, SEED_FIRM_MEMBERS],
      [SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS],
    ] as const
    for (const [roles, members] of cases) {
      const holderIds = roles.flatMap((r) => r.members)
      expect(holderIds.length).toBeGreaterThan(0)
      for (const id of holderIds) {
        const member = members.find((m) => m.id === id)
        expect(member?.status).not.toBe('invited')
      }
    }
  })

  it('firm seeds exactly one person in two roles', () => {
    const counts = new Map<string, number>()
    for (const r of SEED_FIRM_ROLES) for (const id of r.members) counts.set(id, (counts.get(id) ?? 0) + 1)
    const multi = [...counts.entries()].filter(([, n]) => n > 1).map(([id]) => id)
    expect(multi).toEqual(['mf3'])
  })
})

describe('AC-3 — seedRoles deep-clones', () => {
  it('seedRoles deep-clones so mutating one mode cannot reach the constant', () => {
    const a = seedRoles()
    const b = seedRoles()
    a.firm[0].members.push('zzz')
    expect(b.firm[0].members).not.toContain('zzz')
    expect(SEED_FIRM_ROLES[0].members).not.toContain('zzz')
  })
})

describe('AC-4 — reducers are immutable', () => {
  it('reducers return new arrays even on a miss', () => {
    const list = SEED_FIRM_ROLES
    const unknownRole = role('nope', 'Nope', '', [])
    const result = replaceRole(list, unknownRole)
    expect(result).not.toBe(list)
    expect(result).toEqual(list)
  })

  it('pruneMember drops one id from every role and leaves the rest', () => {
    const roles = [role('fin_mgr', 'Engagement Manager', '', ['mf3']), role('fin_dir', 'Senior Manager', '', ['mf3'])]
    const result = pruneMember(roles, 'mf3')
    for (const r of result) expect(r.members).not.toContain('mf3')
    expect(result.map((r) => r.key)).toEqual(['fin_mgr', 'fin_dir'])
  })

  it('setRoleMembers replaces the holder set immutably', () => {
    const roles = [role('preparer', 'Invoice Preparer', '', ['mf2'])]
    const result = setRoleMembers(roles, 'preparer', ['mf2', 'mf5'])
    expect(result).not.toBe(roles)
    expect(result.find((r) => r.key === 'preparer')?.members).toEqual(['mf2', 'mf5'])
    expect(roles[0].members).toEqual(['mf2']) // source untouched
  })
})

describe('AC-5 — newRoleKey', () => {
  it('newRoleKey slugifies the title', () => {
    expect(newRoleKey([], 'Engagement Partner')).toBe('engagement-partner')
  })

  it('newRoleKey suffixes on collision within the mode', () => {
    const roles = [role('tax-reviewer', 'Tax Reviewer', '', [])]
    expect(newRoleKey(roles, 'Tax Reviewer')).toBe('tax-reviewer-2')
  })

  it('newRoleKey never collides with a seeded key', () => {
    const firm = seedRoles().firm
    expect(firm.length).toBeGreaterThan(0) // guard against a vacuous pass
    const keys = new Set(firm.map((r) => r.key))
    for (const r of firm) expect(keys.has(newRoleKey(firm, r.title))).toBe(false)
  })
})

describe('AC-6 — roleOf', () => {
  it('roleOf returns a deleted sentinel for an absent key', () => {
    expect(roleOf(SEED_FIRM_ROLES, 'nope')).toEqual({ key: 'nope', title: 'Deleted role', desc: '', members: [], deleted: true })
  })

  it('roleOf returns the role itself for a present key', () => {
    expect(roleOf(SEED_FIRM_ROLES, 'cfo').title).toBe('Engagement Partner')
  })
})

describe('AC-7 — holders and rolesOfMember order', () => {
  it('rolesOfMember returns both roles for the two-role holder', () => {
    expect(rolesOfMember(SEED_FIRM_ROLES, 'mf3').map((r) => r.title)).toEqual(['Engagement Manager', 'Senior Manager'])
  })

  it('rolesOfMember is empty for someone in no role', () => {
    expect(rolesOfMember(SEED_FIRM_ROLES, 'mf7')).toEqual([])
  })

  it('holders returns members in role.members order, not roster order', () => {
    const roles = [role('preparer', 'Invoice Preparer', '', ['mf5', 'mf2'])]
    expect(holders(roles, SEED_FIRM_MEMBERS, 'preparer').map((m) => m.id)).toEqual(['mf5', 'mf2'])
  })
})

describe('AC-8 — resolve and inspectorResolve', () => {
  it('resolve covers all five states', () => {
    // ok, one active holder
    expect(resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'fin_mgr')).toEqual({ text: 'Musa Danjuma', warn: false })
    // ok, several
    expect(resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'preparer')).toEqual({ text: 'Folake Adesina +1', warn: false })
    // nobody holds it
    expect(resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'quality_reviewer')).toEqual({ text: 'Nobody assigned', warn: true })
    // only suspended holders
    expect(resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo')).toEqual({ text: 'Adebayo Ogunlesi', warn: true })
    // key names no role
    expect(resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'nope')).toEqual({ text: 'Role no longer exists', warn: true })
  })

  it('resolve appends +N counting the other holders', () => {
    expect(resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'preparer').text).toBe('Folake Adesina +1')
  })

  it('resolve never appends the word suspended', () => {
    const text = resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo').text
    expect(text).toBe('Adebayo Ogunlesi')
    expect(text).not.toContain('suspended')
  })

  it('resolve prefers the first ACTIVE holder, not the first listed', () => {
    // mh6 Adebayo Ogunlesi (suspended) listed first, mh1 Ngozi Balogun (active) second.
    const roles = [role('fin_dir', 'Finance Director', '', ['mh6', 'mh1'])]
    const result = resolve(roles, SEED_INHOUSE_MEMBERS, 'fin_dir')
    expect(result.warn).toBe(false)
    expect(result.text).toBe('Ngozi Balogun +1')
  })

  it('warn is true for none, blocked and missing alike', () => {
    const none = resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'quality_reviewer')
    const blocked = resolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo')
    const missing = resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'nope')
    expect(none.warn).toBe(true)
    expect(blocked.warn).toBe(true)
    expect(missing.warn).toBe(true)
    expect(inspectorResolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'quality_reviewer').warn).toBe(true)
    expect(inspectorResolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo').warn).toBe(true)
    expect(inspectorResolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'nope').warn).toBe(true)
  })

  it("inspectorResolve uses its own three strings", () => {
    expect(inspectorResolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'cfo').text).toBe('Currently: Chinedu Okafor')
    expect(inspectorResolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'quality_reviewer').text).toBe(
      'Nobody holds this role — this step will block',
    )
    expect(inspectorResolve(SEED_INHOUSE_ROLES, SEED_INHOUSE_MEMBERS, 'cfo').text).toBe(
      'Currently: Adebayo Ogunlesi — this step will block',
    )
  })

  it('inspectorResolve omits +N', () => {
    const text = inspectorResolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, 'preparer').text
    expect(text).toBe('Currently: Folake Adesina')
    expect(text).not.toContain('+1')
  })
})

describe('AC-9 — unassignedRoles', () => {
  it('unassignedRoles is empty when every role has an active holder', () => {
    const roles = [role('a', 'A', '', ['mf1']), role('b', 'B', '', ['mf2'])]
    expect(unassignedRoles(roles, SEED_FIRM_MEMBERS)).toEqual([])
  })
})

describe('AC-10 — steps', () => {
  it('steps counts root and both branch lanes and includes drafts', () => {
    expect(roleSteps(SEED_FIRM_POLICIES, 'cfo')).toEqual({
      total: 2,
      policies: [
        { policyName: 'Standard approval policy', count: 1 },
        { policyName: 'Government supply (B2G)', count: 1 },
      ],
    } satisfies RoleSteps)
  })

  it('steps omits policies with a zero count', () => {
    expect(roleSteps(SEED_FIRM_POLICIES, 'quality_reviewer')).toEqual({ total: 0, policies: [] })
  })
})

describe('AC-11 — stepsForMember', () => {
  it('stepsForMember unions every role the person holds', () => {
    expect(stepsForMember(SEED_FIRM_POLICIES, SEED_FIRM_ROLES, 'mf3')?.total).toBe(5)
  })

  it('stepsForMember returns null when the total is zero', () => {
    // mf2 Folake Adesina holds only Invoice Preparer, which no firm policy references.
    expect(stepsForMember(SEED_FIRM_POLICIES, SEED_FIRM_ROLES, 'mf2')).toBeNull()
  })

  it('stepsForMember returns null for a member holding no role', () => {
    expect(stepsForMember(SEED_FIRM_POLICIES, SEED_FIRM_ROLES, 'mf6')).toBeNull()
  })
})

describe('AC-12 — copy helpers', () => {
  it("roleUsage renders Core AC 3's string in sentence case", () => {
    const s: RoleSteps = { total: 2, policies: [{ policyName: 'A', count: 1 }, { policyName: 'B', count: 1 }] }
    expect(roleUsage(s)).toBe('2 approval steps · 2 policies')
  })

  it('roleUsage singularises both nouns independently', () => {
    const one: RoleSteps = { total: 1, policies: [{ policyName: 'A', count: 1 }] }
    const twoSteps: RoleSteps = { total: 2, policies: [{ policyName: 'A', count: 2 }] }
    expect(roleUsage(one)).toBe('1 approval step · 1 policy')
    expect(roleUsage(twoSteps)).toBe('2 approval steps · 1 policy')
  })

  it('roleUsage renders the unused sentence at zero', () => {
    expect(roleUsage({ total: 0, policies: [] })).toBe('not used in any policy')
  })

  it('unassignedNotice pluralises around one', () => {
    expect(unassignedNotice(1)).toBe('1 role has nobody active assigned. Approval steps pointed at it will block.')
    expect(unassignedNotice(3)).toBe('3 roles have nobody active assigned. Approval steps pointed at them will block.')
  })

  it('holderCount pluralises around one', () => {
    expect(holderCount(1)).toBe('1 person')
    expect(holderCount(3)).toBe('3 people')
  })
})

// AC-13's Test Spec row files this against src/lib/workflows.test.ts. Kept here instead —
// see the QA report: RoleKey's widening is type-only (zero runtime delta), so this cannot
// go RED in Mode A regardless of which file it lives in; it is a same-file regression guard.
describe('AC-13 — RoleKey widening leaves workflows.ts exports intact', () => {
  it('WF_ROLES, roleOf and seedPolicies are unchanged', () => {
    expect(WF_ROLES.map((r) => r.key)).toEqual(['preparer', 'line_mgr', 'fin_mgr', 'controller', 'fin_dir', 'compliance', 'cfo', 'ceo'])
    expect(wfRoleOf('cfo')).toEqual({ key: 'cfo', title: 'CFO', line: 'Executive' })
    expect(seedPolicies().firm.map((p) => p.id)).toEqual(SEED_FIRM_POLICIES.map((p) => p.id))
  })
})
