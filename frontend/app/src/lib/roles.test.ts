import { describe, expect, it } from 'vitest'

import { SEED_FIRM_MEMBERS, SEED_INHOUSE_MEMBERS, setMemberStatus } from './members'
import {
  canSaveRole,
  deleteRoleConfirm,
  drawerRoleHelper,
  filterPickerMembers,
  filterRoles,
  hiddenInvitedFootnote,
  holderCount,
  holders,
  inspectorResolve,
  intro,
  newRoleKey,
  pickerHiddenAmongSelected,
  pickerMembers,
  pickerMeta,
  pickerSelectionCount,
  pruneMember,
  removeRole,
  replaceRole,
  resolve,
  roleOf,
  roleUsage,
  rolesOfMember,
  rosterRoleCell,
  SEED_FIRM_ROLES,
  SEED_INHOUSE_ROLES,
  seedRoles,
  setRoleMembers,
  stepsForMember,
  stepsWarning,
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

// QA — DELETE this describe block in subtask 05/06, alongside WF_ROLES: it exists only to
// catch SEED_INHOUSE_ROLES drifting from WF_ROLES, which typecheck cannot see. The holder-list
// half of this block — checked against the shipped Member.position groupings — is gone: that
// subtask deletes Member.position, so there is nothing left to compare holders against.
describe('QA — SEED_INHOUSE_ROLES agrees with the seed source it is replacing', () => {
  it('titles and descs match WF_ROLES key-for-key', () => {
    const byKey = new Map(WF_ROLES.map((r) => [r.key, r]))
    expect(SEED_INHOUSE_ROLES.length).toBe(WF_ROLES.length) // guard against a vacuous pass
    for (const r of SEED_INHOUSE_ROLES) {
      const wf = byKey.get(r.key)
      expect(wf).toBeDefined()
      expect(r.title).toBe(wf?.title)
      expect(r.desc).toBe(wf?.line)
    }
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

describe("QA — newRoleKey's empty-slug fallback (Save gates on name, not on slug legality)", () => {
  it("falls back to the literal key 'role' when the title slugifies to nothing", () => {
    expect(newRoleKey([], '###')).toBe('role')
  })

  it('composes the fallback with the ordinary collision suffix', () => {
    const withRole = [role('role', 'Role', '', [])]
    expect(newRoleKey(withRole, '###')).toBe('role-2')
    const withRole2 = [...withRole, role('role-2', 'Role', '', [])]
    expect(newRoleKey(withRole2, '###')).toBe('role-3')
  })

  it('an emoji-only title hits the same fallback, not a different one', () => {
    expect(newRoleKey([], '🎉🎉🎉')).toBe('role')
  })

  it('a title mixing non-latin letters with ascii keeps only the ascii', () => {
    // 'Ω' is stripped like any other non a-z0-9 character — no unicode-aware slugifier here.
    expect(newRoleKey([], 'Ω Reviewer')).toBe('reviewer')
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

// QA (task-344) — RolesView's search box and intro line, lifted here so a spec can reach them.
describe('QA — filterRoles', () => {
  it('matches case-insensitively on title', () => {
    // Not "INVOICE": that substring also hits fin_mgr's and cfo's descs.
    expect(filterRoles(SEED_FIRM_ROLES, 'PREPARER').map((r) => r.key)).toEqual(['preparer'])
  })

  it('matches case-insensitively on desc when the title does not match', () => {
    // 'Tax Reviewer' never contains "vat" — only its desc does.
    const hits = filterRoles(SEED_FIRM_ROLES, 'VAT')
    expect(hits.map((r) => r.key)).toEqual(['compliance'])
    expect(hits[0].title.toLowerCase()).not.toContain('vat')
  })

  it('returns nothing for a query no role matches', () => {
    expect(filterRoles(SEED_FIRM_ROLES, 'zzz-nonexistent')).toEqual([])
  })

  it('an empty (or whitespace) query returns every role, as a copy', () => {
    const result = filterRoles(SEED_FIRM_ROLES, '   ')
    expect(result).toEqual(SEED_FIRM_ROLES)
    expect(result).not.toBe(SEED_FIRM_ROLES)
  })
})

describe('QA — intro', () => {
  it('firm names its own first two role titles', () => {
    expect(intro(SEED_FIRM_ROLES)).toBe(
      'A role is a named seat in your approval policies — Invoice Preparer, Engagement Manager. Workflow steps point at the role; the people here are who actually signs.',
    )
  })

  it('inhouse names its own first two role titles, not firm’s', () => {
    expect(intro(SEED_INHOUSE_ROLES)).toBe(
      'A role is a named seat in your approval policies — Preparer, Line Manager. Workflow steps point at the role; the people here are who actually signs.',
    )
  })

  it('drops the named-roles clause entirely with one role', () => {
    const text = intro([role('cfo', 'Engagement Partner', '', [])])
    expect(text).toBe('A role is a named seat in your approval policies. Workflow steps point at the role; the people here are who actually signs.')
    expect(text).not.toContain('—')
    expect(text).not.toContain('Engagement Partner')
  })

  it('drops the named-roles clause entirely with zero roles', () => {
    const text = intro([])
    expect(text).toBe('A role is a named seat in your approval policies. Workflow steps point at the role; the people here are who actually signs.')
    expect(text).not.toContain('—')
  })
})

// RoleModal's picker and delete-confirm helpers. Seed counts are pinned against
// SEED_FIRM_MEMBERS / SEED_INHOUSE_MEMBERS directly, so a wrong body fails on OUR
// assertion, not a number this file invented.

describe('AC-4 — pickerMembers excludes invited people in both modes', () => {
  it('firm: 7 seeded, 1 invited (mf6) -> 6 rows, none invited', () => {
    expect(SEED_FIRM_MEMBERS.filter((m) => m.status === 'invited').map((m) => m.id)).toEqual(['mf6']) // guard: pins the seed fact this test relies on
    const rows = pickerMembers(SEED_FIRM_MEMBERS)
    expect(rows).toHaveLength(6)
    expect(rows.some((m) => m.status === 'invited')).toBe(false)
  })

  it('inhouse: 16 seeded, 2 invited (mh15, mh16) -> 14 rows, none invited', () => {
    expect(SEED_INHOUSE_MEMBERS.filter((m) => m.status === 'invited').map((m) => m.id)).toEqual(['mh15', 'mh16']) // guard
    const rows = pickerMembers(SEED_INHOUSE_MEMBERS)
    expect(rows).toHaveLength(14)
    expect(rows.some((m) => m.status === 'invited')).toBe(false)
  })
})

describe('AC-4 — pickerMeta reads department in-house and the access-role label in firm', () => {
  it("inhouse reads the member's department", () => {
    const mh4 = SEED_INHOUSE_MEMBERS.find((m) => m.id === 'mh4')!
    expect(mh4.department).toBe('Finance') // guard: pins the fixture fact, independent of pickerMeta
    expect(pickerMeta('inhouse', mh4)).toBe('Finance')
  })

  it("firm reads the member's access-role LABEL, not the raw lowercase id", () => {
    const mf3 = SEED_FIRM_MEMBERS.find((m) => m.id === 'mf3')!
    expect(mf3.role).toBe('reviewer') // guard: pins the fixture fact — pickerMeta must NOT just echo this
    expect(pickerMeta('firm', mf3)).toBe('Reviewer')
  })
})

describe('AC-4 — filterPickerMembers matches name or email, case-insensitively, and trims', () => {
  // Built directly rather than via pickerMembers(), so this spec's own failure reason can
  // never be masked by the pickerMembers stub throwing first.
  const inhouseSelectable = SEED_INHOUSE_MEMBERS.filter((m) => m.status !== 'invited')

  it('a padded, wrong-case query matches on name', () => {
    expect(filterPickerMembers(inhouseSelectable, '  LAWAL ').map((m) => m.id)).toEqual(['mh7'])
  })

  it('a query matches on email when it does not match the name', () => {
    // 'honeywellgroup' only appears in mh11's email domain, not in any name.
    expect(filterPickerMembers(inhouseSelectable, 'honeywellgroup').map((m) => m.id)).toEqual(['mh11'])
  })

  it('an empty (or whitespace) query returns the full input list, copied', () => {
    const result = filterPickerMembers(inhouseSelectable, '   ')
    expect(result).toEqual(inhouseSelectable)
    expect(result).not.toBe(inhouseSelectable)
  })

  it('a query matching nobody returns an empty array, not the full list', () => {
    expect(filterPickerMembers(inhouseSelectable, 'zzz-nonexistent')).toEqual([])
  })
})

describe('AC-5 — pickerSelectionCount denominators on the selectable count, not the roster', () => {
  it('firm: 2 selected of 6 selectable (mf6, invited, excluded from the denominator)', () => {
    expect(SEED_FIRM_MEMBERS).toHaveLength(7) // guard: pins the roster length this test means to NOT use
    expect(pickerSelectionCount(2, SEED_FIRM_MEMBERS)).toBe('2 of 6 selected')
  })

  it('inhouse: 5 selected of 14 selectable', () => {
    expect(pickerSelectionCount(5, SEED_INHOUSE_MEMBERS)).toBe('5 of 14 selected')
  })
})

describe('AC-5 — hiddenInvitedFootnote pluralises around one', () => {
  it('firm has exactly one invited person -> the singular residue string', () => {
    expect(hiddenInvitedFootnote(SEED_FIRM_MEMBERS)).toBe('1 invited person is hidden until they accept the invite.')
  })

  it('inhouse has two invited people -> the plural form', () => {
    expect(hiddenInvitedFootnote(SEED_INHOUSE_MEMBERS)).toBe('2 invited people are hidden until they accept the invite.')
  })
})

describe('AC-6 — canSaveRole gates on the name alone', () => {
  it('an empty name cannot save', () => {
    expect(canSaveRole('')).toBe(false)
  })

  it('a whitespace-only name cannot save', () => {
    expect(canSaveRole('   ')).toBe(false)
  })

  it('a non-empty name can save even when that title already exists — duplicates are allowed', () => {
    expect(SEED_FIRM_ROLES.some((r) => r.title === 'Tax Reviewer')).toBe(true) // guard: the title really is a duplicate
    expect(canSaveRole('Tax Reviewer')).toBe(true)
  })
})

describe('AC-7 — deleteRoleConfirm names the role and its usage', () => {
  it('a used role names its usage sentence and warns those steps will block', () => {
    // 'compliance' (Tax Reviewer) is named once each in polF1/polF2/polF3 — three approval
    // steps across three policies (workflows.ts:162-183).
    const usage = roleSteps(SEED_FIRM_POLICIES, 'compliance')
    expect(usage).toEqual({
      total: 3,
      policies: [
        { policyName: 'Standard approval policy', count: 1 },
        { policyName: 'Cross-border & FX', count: 1 },
        { policyName: 'Government supply (B2G)', count: 1 },
      ],
    }) // guard: pins the exact usage this test's assertion depends on
    const text = deleteRoleConfirm('Tax Reviewer', usage)
    expect(text).toContain('Tax Reviewer')
    expect(text).toContain('3 approval steps · 3 policies')
    expect(text).toContain('will block')
  })

  it('an unused role names "not used in any policy" and claims no blocking', () => {
    const usage = roleSteps(SEED_FIRM_POLICIES, 'quality_reviewer')
    expect(usage).toEqual({ total: 0, policies: [] }) // guard
    const text = deleteRoleConfirm('Quality Reviewer', usage)
    expect(text).toContain('Quality Reviewer')
    expect(text).toContain('not used in any policy')
    expect(text).not.toContain('will block')
  })
})

// This one is NOT expected to go red: removeRole takes only a role list and a key — it has
// no policies parameter to touch one through, so "removeRole leaves every policy object
// untouched" is true by construction today. Pinned anyway, the same shape as AC-13 above,
// as the regression guard for `[delete-does-not-demote]`: the day removeRole's signature
// grows a policies argument, THIS is the test that must catch a write into it.
describe('AC-7 — removeRole never touches a policy object ([delete-does-not-demote])', () => {
  it('policy references and their published status are unchanged after removing a role', () => {
    const published = SEED_FIRM_POLICIES.filter((p) => p.status === 'published')
    expect(published.length).toBeGreaterThan(0) // guard against a vacuous pass
    const before = published.map((p) => p)
    removeRole(SEED_FIRM_ROLES, 'compliance')
    const after = SEED_FIRM_POLICIES.filter((p) => p.status === 'published')
    expect(after).toEqual(before)
    for (let i = 0; i < before.length; i++) expect(after[i]).toBe(before[i]) // same object references
    for (const p of after) expect(p.status).toBe('published')
  })
})

describe('[key-is-a-slug] — renaming a role never re-derives its key', () => {
  it('replaceRole matches by key and updates the rest, on a real hit not just a miss', () => {
    const original = role('fin_mgr', 'Engagement Manager', 'First sign-off', ['mf3'])
    const renamed = { ...original, title: 'Chief Engagement Officer' }
    const result = replaceRole([original], renamed)
    expect(result[0]).toEqual(renamed)
    expect(result[0].key).toBe('fin_mgr')
  })

  it('the key a rename keeps is not what newRoleKey would derive from the new title', () => {
    // The shipped edit path spreads the stored role and overrides title/desc/members only —
    // this is the assertion that would catch RoleModal ever switching to re-deriving the key
    // on save, the way it already does on create.
    const original = role('fin_mgr', 'Engagement Manager', 'First sign-off', ['mf3'])
    const renamedTitle = 'Chief Engagement Officer'
    expect(newRoleKey([original], renamedTitle)).not.toBe('fin_mgr')
    expect({ ...original, title: renamedTitle }.key).toBe('fin_mgr')
  })
})

// Forward risk, now DECIDED: [invite-writes-both-stores] makes a role holding an invited
// member's id reachable (inviteMembers puts the fresh id straight into the chosen role), so
// the picker's "X of Y selected" contract needs an explicit call rather than a silent one.
// Decision: (a) — the numerator keeps counting the invited id (pickerMembers/pickerSelectionCount
// are UNCHANGED, both still correct in isolation), and a NEW additive export names how many of
// a selection are invited-and-hidden, so a caller can render the gap instead of leaving it
// unexplained. Rejected: filtering the numerator down to the visible set (breaks the moment
// `selected` is later used to save — an untickable row would either silently survive a save
// nobody asked for, or vanish from a save that never touched it) and showing invited rows as
// disabled (widens pickerMembers' return shape for every caller, not just this one).
describe('the invited-holder picker contract, pinned on purpose', () => {
  it('pickerMembers keeps excluding the invited person regardless of any role membership', () => {
    const mh15 = SEED_INHOUSE_MEMBERS.find((m) => m.id === 'mh15')!
    expect(mh15.status).toBe('invited') // guard
    expect(pickerMembers(SEED_INHOUSE_MEMBERS).some((m) => m.id === 'mh15')).toBe(false)
  })

  it('pickerSelectionCount keeps echoing its numerator, uncrossed against the selectable set', () => {
    // Stand-in for role.members once a role holds an invited id: two real selectable holders
    // plus one invited id the picker will never render as a row.
    const inflatedSelected = ['mh1', 'mh2', 'mh15']
    expect(pickerSelectionCount(inflatedSelected.length, SEED_INHOUSE_MEMBERS)).toBe('3 of 14 selected')
    const checkableRows = pickerMembers(SEED_INHOUSE_MEMBERS).map((m) => m.id)
    const checkableOfSelected = inflatedSelected.filter((id) => checkableRows.includes(id))
    // The picker can only ever tick 2 of those 3 ids — the numerator above does not know that.
    expect(checkableOfSelected).toEqual(['mh1', 'mh2'])
    expect(checkableOfSelected.length).not.toBe(inflatedSelected.length)
  })

  it('pickerHiddenAmongSelected names the gap the count above cannot explain on its own', () => {
    const inflatedSelected = ['mh1', 'mh2', 'mh15']
    expect(pickerHiddenAmongSelected(inflatedSelected, SEED_INHOUSE_MEMBERS)).toBe(1)
    expect(pickerHiddenAmongSelected(['mh1', 'mh2'], SEED_INHOUSE_MEMBERS)).toBe(0)
  })
})

// ============================================================================
// task-346 — Members surfaces speak in workflow roles (Test Specs table, Mode A)
// ============================================================================

describe('rosterRoleCell — the roster column, first title plus N', () => {
  it('shows the first title plus N, with the full list newline-joined in the tooltip', () => {
    expect(rosterRoleCell(SEED_FIRM_ROLES, 'mf3')).toEqual({
      text: 'Engagement Manager +1',
      tooltip: 'Engagement Manager\nSenior Manager',
    })
  })

  it('shows a bare title for a single role, with no +N and a one-line tooltip', () => {
    expect(rosterRoleCell(SEED_FIRM_ROLES, 'mf4')).toEqual({ text: 'Tax Reviewer', tooltip: 'Tax Reviewer' })
  })

  it('is an em dash with an empty tooltip for nobody', () => {
    expect(rosterRoleCell(SEED_FIRM_ROLES, 'mf6')).toEqual({ text: '—', tooltip: '' })
  })
})

describe('stepsForMember and stepsWarning — the suspended-row warning counts every held role', () => {
  it('the suspended in-house cfo holder still blocks the two cfo steps', () => {
    const result = stepsForMember(SEED_INHOUSE_POLICIES, SEED_INHOUSE_ROLES, 'mh6')
    expect(result?.total).toBe(2)
    expect(result?.policies.map((p) => p.policyName)).toEqual(['Company approval policy', 'Capital expenditure'])
  })

  it('stepsWarning pluralises around one', () => {
    expect(stepsWarning(1)).toBe('Named in 1 approval step · that step will block')
    expect(stepsWarning(2)).toBe('Named in 2 approval steps · those steps will block')
  })
})

describe('drawerRoleHelper — forks on the access role, not the workflow role', () => {
  it('preparers get the Reviewer-first sentence; reviewers and admins get the general one', () => {
    expect(drawerRoleHelper('preparer')).toBe(
      'Preparers cannot approve. Give them the Reviewer access role above before a workflow role means anything.',
    )
    expect(drawerRoleHelper('reviewer')).toBe('Roles decide which approval steps this person can act on.')
    expect(drawerRoleHelper('admin')).toBe('Roles decide which approval steps this person can act on.')
  })
})

describe('[remove-prunes-suspend-keeps] — pruneMember vs setMemberStatus', () => {
  it('pruning a removed member empties the role they solely held', () => {
    const pruned = pruneMember(SEED_INHOUSE_ROLES, 'mh6')
    const result = resolve(pruned, SEED_INHOUSE_MEMBERS, 'cfo')
    expect(result.text).toBe('Nobody assigned')
    expect(result.warn).toBe(true)
  })

  it('suspending does not unstaff — the role keeps the member, resolve just blocks', () => {
    expect(SEED_FIRM_ROLES.find((r) => r.key === 'fin_mgr')?.members).toEqual(['mf3']) // guard
    const suspended = setMemberStatus(SEED_FIRM_MEMBERS, 'mf3', 'suspended')
    const result = resolve(SEED_FIRM_ROLES, suspended, 'fin_mgr')
    expect(result.text).toBe('Musa Danjuma')
    expect(result.warn).toBe(true)
  })
})

describe('the firm workspace resolves every seeded approval step to a named person', () => {
  it('all ten seeded firm approval steps resolve without warn', () => {
    const stepRoles = approvalRoles(SEED_FIRM_POLICIES)
    expect(stepRoles.length).toBe(10) // guard against a vacuous pass
    for (const key of stepRoles) {
      const result = resolve(SEED_FIRM_ROLES, SEED_FIRM_MEMBERS, key)
      expect(result.warn).toBe(false)
      expect(result.text).not.toBe('Nobody assigned')
      expect(result.text).not.toBe('Role no longer exists')
    }
  })
})
