import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type ApiFetchOptions, type AsyncStatus } from '@invoice-os/api-client'

import { APP_PERSONAS } from '../auth'
import { createAuthedFetch } from './authedFetch'
import { replaceMember, toMember, type Member, type MembershipWire } from './members'
import type { AuthedFetch } from './portfolio'
import {
  activeHolders,
  addRole,
  canSaveRole,
  createStaffedRole,
  createWorkflowRole,
  deleteRoleConfirm,
  deleteWorkflowRole,
  drawerRoleHelper,
  filterPickerMembers,
  filterRoles,
  hiddenInvitedFootnote,
  hiddenSelectionNote,
  holderCount,
  holders,
  inspectorResolve,
  intro,
  isApprover,
  listWorkflowRoles,
  pickerHiddenAmongSelected,
  pickerMembers,
  pickerSelectionCount,
  removeRole,
  replaceRole,
  resolve,
  roleOf,
  rolePatch,
  roleUsage,
  rolesOfMember,
  rolesSurface,
  rosterRoleCell,
  setRoleMembers,
  stepsForMember,
  stepsWarning,
  steps as roleSteps,
  unassignedNotice,
  unassignedRoles,
  updateWorkflowRole,
  type Role,
  type RoleSteps,
} from './roles'
import { SEED_FIRM_POLICIES, SEED_INHOUSE_POLICIES } from './policies.fixture'
import type { Policy } from './workflows'

// --- fixtures ---------------------------------------------------------------
// The mock roster and the role lists that staff it, copied here when lib/members.ts stopped
// shipping a seed and SEED_*_ROLES were re-pointed at the live membership subjects. The two
// travel together: a role's `members` ids only mean something against the directory they
// were written for. Specs that pin the SHIPPED re-point use the real constants — see the
// SHIPPED_FIRM_ROLES/SHIPPED_INHOUSE_ROLES blocks further down this file.
const MOCK_FIRM_MEMBERS: readonly Member[] = [
  {
    id: 'mf1',
    name: APP_PERSONAS.firm.name,
    initials: APP_PERSONAS.firm.initials,
    email: APP_PERSONAS.firm.email,
    role: 'admin',
    status: 'active',
    isYou: true,
  },
  {
    id: 'mf2',
    name: 'Folake Adesina',
    initials: 'FA',
    email: 'f.adesina@okafor.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mf3',
    name: 'Musa Danjuma',
    initials: 'MD',
    email: 'm.danjuma@okafor.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mf4',
    name: 'Chiamaka Nwosu',
    initials: 'CN',
    email: 'c.nwosu@okafor.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    // §10.9 — the deliberately long name + email row, guarding the column widths.
    id: 'mf5',
    name: 'Oluwaseyifunmi Adebanjo-Ogunleye',
    initials: 'OA',
    email: 'o.adebanjo-ogunleye@okaforandpartners.com.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mf6',
    name: 'Bature Suleiman',
    initials: 'BS',
    email: 'b.suleiman@okafor.ng',
    role: 'preparer',
    status: 'invited',
    isYou: false,
  },
  {
    id: 'mf7',
    name: 'Halima Yusuf',
    initials: 'HY',
    email: 'h.yusuf@okafor.ng',
    role: 'reviewer',
    status: 'suspended',
    isYou: false,
  },
]

const MOCK_INHOUSE_MEMBERS: readonly Member[] = [
  {
    id: 'mh1',
    name: APP_PERSONAS.inhouse.name,
    initials: APP_PERSONAS.inhouse.initials,
    email: APP_PERSONAS.inhouse.email,
    role: 'admin',
    status: 'active',
    isYou: true,
  },
  {
    id: 'mh2',
    name: 'Yetunde Fashola',
    initials: 'YF',
    email: 'y.fashola@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh3',
    name: 'Emeka Uzowulu',
    initials: 'EU',
    email: 'e.uzowulu@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh4',
    name: 'Tunde Adeyemi',
    initials: 'TA',
    email: 't.adeyemi@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh5',
    name: 'Ibrahim Bello',
    initials: 'IB',
    email: 'i.bello@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    // §2's headline frame: the only cfo holder, suspended, so both cfo approval steps block.
    id: 'mh6',
    name: 'Adebayo Ogunlesi',
    initials: 'AO',
    email: 'a.ogunlesi@honeywell.ng',
    role: 'reviewer',
    status: 'suspended',
    isYou: false,
  },
  {
    id: 'mh7',
    name: 'Zainab Lawal',
    initials: 'ZL',
    email: 'z.lawal@honeywell.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh8',
    name: 'Chidi Anyanwu',
    initials: 'CA',
    email: 'c.anyanwu@honeywell.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh9',
    name: 'Aisha Mohammed',
    initials: 'AM',
    email: 'a.mohammed@honeywell.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh10',
    name: 'Segun Oyelaran',
    initials: 'SO',
    email: 's.oyelaran@honeywell.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    // §13 check 11 runs in both modes, and in-house is the riskier one (7 columns to firm's
    // 6) — so in-house gets a long name/email row too (Decision `[inhouse-long-row]`).
    id: 'mh11',
    name: 'Oluwafunmilayo Ademola-Oyediran',
    initials: 'OA',
    email: 'o.ademola-oyediran@honeywellgroup.com.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh12',
    name: 'Kelechi Obi',
    initials: 'KO',
    email: 'k.obi@honeywell.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh13',
    name: 'Hauwa Abubakar',
    initials: 'HA',
    email: 'h.abubakar@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh14',
    name: 'Olumide Bakare',
    initials: 'OB',
    email: 'o.bakare@honeywell.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
  },
  {
    id: 'mh15',
    name: 'Nneka Chukwu',
    initials: 'NC',
    email: 'n.chukwu@honeywell.ng',
    role: 'preparer',
    status: 'invited',
    isYou: false,
  },
  {
    id: 'mh16',
    name: 'Sadiq Ibrahim',
    initials: 'SI',
    email: 's.ibrahim@honeywell.ng',
    role: 'reviewer',
    status: 'invited',
    isYou: false,
  },
]

const MOCK_FIRM_ROLES: readonly Role[] = [
  { key: 'preparer', title: 'Invoice Preparer', desc: 'Prepares and imports client invoices', members: ['mf2', 'mf5'] },
  { key: 'fin_mgr', title: 'Engagement Manager', desc: 'First sign-off on a client invoice', members: ['mf3'] },
  { key: 'fin_dir', title: 'Senior Manager', desc: 'Second sign-off above ₦250m', members: ['mf3'] },
  { key: 'compliance', title: 'Tax Reviewer', desc: 'Checks VAT, WHT and TIN detail before filing', members: ['mf4'] },
  { key: 'cfo', title: 'Engagement Partner', desc: 'Signs off invoices above ₦1bn', members: ['mf1'] },
  { key: 'quality_reviewer', title: 'Quality Reviewer', desc: 'Second-partner review on flagged engagements', members: [] },
]

// The eight shipped `position` values restaffed, each old department string kept as `desc`.
// mh6 is suspended and the only cfo holder, which is what makes the suspended-only state
// reachable without a user constructing it.
const MOCK_INHOUSE_ROLES: readonly Role[] = [
  { key: 'preparer', title: 'Preparer', desc: 'Accounts Payable', members: ['mh7'] },
  { key: 'line_mgr', title: 'Line Manager', desc: 'Requesting dept.', members: ['mh3'] },
  { key: 'fin_mgr', title: 'Finance Manager', desc: 'Finance', members: [] },
  { key: 'controller', title: 'Financial Controller', desc: 'Finance', members: ['mh4'] },
  { key: 'fin_dir', title: 'Finance Director', desc: 'Finance', members: ['mh1', 'mh2'] },
  { key: 'compliance', title: 'Compliance Officer', desc: 'Tax & Compliance', members: ['mh5'] },
  { key: 'cfo', title: 'CFO', desc: 'Executive', members: ['mh6'] },
  { key: 'ceo', title: 'CEO', desc: 'Executive', members: [] },
]

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

/** A hand-built policy, for traversal facts no seeded policy exercises (see QA note below). */
const testPolicy = (name: string, nodes: Policy['nodes']): Policy => ({ id: 'test', name, scope: 'test', status: 'published', version: 1, activeVersion: 1, nodes })

describe('AC-2 — seed keys, titles, descriptions', () => {
  // The three tautological seed-shape tests this block used to open with (key order, firm
  // titles/descs, inhouse descs) are superseded by the Go seed test asserting the SAME facts
  // against the live DB rows: internal/platform/db/seed_demo_test.go:4560,
  // TestSeedWorkflowRolesExistForBothDemoTenants.

  it('firm has exactly two unsignable roles — preparer is staffed but by no approver', () => {
    expect(unassignedRoles(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS).map((r) => r.key)).toEqual(['preparer', 'quality_reviewer'])
  })

  it('inhouse has four unsignable roles, one of them suspended-only', () => {
    expect(unassignedRoles(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS).map((r) => r.key)).toEqual(['preparer', 'fin_mgr', 'cfo', 'ceo'])
    expect(resolve(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS, 'cfo').text).toBe('Adebayo Ogunlesi')
  })

  it('every seeded approval step names a role that exists in that mode', () => {
    const firmKeys = new Set(MOCK_FIRM_ROLES.map((r) => r.key))
    const inhouseKeys = new Set(MOCK_INHOUSE_ROLES.map((r) => r.key))
    const firmRoles = approvalRoles(SEED_FIRM_POLICIES)
    const inhouseRoles = approvalRoles(SEED_INHOUSE_POLICIES)
    // Guards against a vacuous pass — SEED_FIRM_POLICIES/SEED_INHOUSE_POLICIES are already
    // shipped (policies.fixture.ts), so these lists are real and non-empty regardless of roles.ts.
    expect(firmRoles.length).toBeGreaterThan(0)
    expect(inhouseRoles.length).toBeGreaterThan(0)
    for (const key of firmRoles) expect(firmKeys.has(key)).toBe(true)
    for (const key of inhouseRoles) expect(inhouseKeys.has(key)).toBe(true)
  })

  it('every seeded holder id is a member of the same mode', () => {
    const cases = [
      [MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS],
      [MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS],
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
      [MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS],
      [MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS],
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
    for (const r of MOCK_FIRM_ROLES) for (const id of r.members) counts.set(id, (counts.get(id) ?? 0) + 1)
    const multi = [...counts.entries()].filter(([, n]) => n > 1).map(([id]) => id)
    expect(multi).toEqual(['mf3'])
  })
})

describe('AC-4 — reducers are immutable', () => {
  it('reducers return new arrays even on a miss', () => {
    const list = MOCK_FIRM_ROLES
    const unknownRole = role('nope', 'Nope', '', [])
    const result = replaceRole(list, unknownRole)
    expect(result).not.toBe(list)
    expect(result).toEqual(list)
  })
})

describe('AC-6 — roleOf', () => {
  it('roleOf returns a deleted sentinel for an absent key', () => {
    expect(roleOf(MOCK_FIRM_ROLES, 'nope')).toEqual({ key: 'nope', title: 'Deleted role', desc: '', members: [], deleted: true })
  })

  it('roleOf returns the role itself for a present key', () => {
    expect(roleOf(MOCK_FIRM_ROLES, 'cfo').title).toBe('Engagement Partner')
  })
})

describe('AC-7 — holders and rolesOfMember order', () => {
  it('rolesOfMember returns both roles for the two-role holder', () => {
    expect(rolesOfMember(MOCK_FIRM_ROLES, 'mf3').map((r) => r.title)).toEqual(['Engagement Manager', 'Senior Manager'])
  })

  it('rolesOfMember is empty for someone in no role', () => {
    expect(rolesOfMember(MOCK_FIRM_ROLES, 'mf7')).toEqual([])
  })

  it('holders returns members in role.members order, not roster order', () => {
    const roles = [role('preparer', 'Invoice Preparer', '', ['mf5', 'mf2'])]
    expect(holders(roles, MOCK_FIRM_MEMBERS, 'preparer').map((m) => m.id)).toEqual(['mf5', 'mf2'])
  })
})

describe('AC-8 — resolve and inspectorResolve', () => {
  it('resolve covers all five states', () => {
    // ok, one active holder
    expect(resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'fin_mgr')).toEqual({ text: 'Musa Danjuma', warn: false })
    // blocked despite two holders — neither is an approver
    expect(resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'preparer')).toEqual({ text: 'Folake Adesina +1', warn: true })
    // nobody holds it
    expect(resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'quality_reviewer')).toEqual({ text: 'Nobody assigned', warn: true })
    // only suspended holders
    expect(resolve(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS, 'cfo')).toEqual({ text: 'Adebayo Ogunlesi', warn: true })
    // key names no role
    expect(resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'nope')).toEqual({ text: 'Role no longer exists', warn: true })
  })

  it('resolve appends +N counting the other holders', () => {
    expect(resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'preparer').text).toBe('Folake Adesina +1')
  })

  it('resolve never appends the word suspended', () => {
    const text = resolve(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS, 'cfo').text
    expect(text).toBe('Adebayo Ogunlesi')
    expect(text).not.toContain('suspended')
  })

  it('resolve prefers the first ACTIVE holder, not the first listed', () => {
    // mh6 Adebayo Ogunlesi (suspended) listed first, mh1 Ngozi Balogun (active) second.
    const roles = [role('fin_dir', 'Finance Director', '', ['mh6', 'mh1'])]
    const result = resolve(roles, MOCK_INHOUSE_MEMBERS, 'fin_dir')
    expect(result.warn).toBe(false)
    expect(result.text).toBe('Ngozi Balogun +1')
  })

  it('warn is true for none, blocked and missing alike', () => {
    const none = resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'quality_reviewer')
    const blocked = resolve(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS, 'cfo')
    const missing = resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'nope')
    expect(none.warn).toBe(true)
    expect(blocked.warn).toBe(true)
    expect(missing.warn).toBe(true)
    expect(inspectorResolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'quality_reviewer').warn).toBe(true)
    expect(inspectorResolve(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS, 'cfo').warn).toBe(true)
    expect(inspectorResolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'nope').warn).toBe(true)
  })

  it("inspectorResolve uses its own three strings", () => {
    expect(inspectorResolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'cfo').text).toBe('Currently: Chinedu Okafor')
    expect(inspectorResolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'quality_reviewer').text).toBe(
      'Nobody holds this role — this step will block',
    )
    expect(inspectorResolve(MOCK_INHOUSE_ROLES, MOCK_INHOUSE_MEMBERS, 'cfo').text).toBe(
      'Currently: Adebayo Ogunlesi — this step will block',
    )
  })

  it('inspectorResolve omits +N', () => {
    const text = inspectorResolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'preparer').text
    expect(text).toBe('Currently: Folake Adesina — this step will block')
    expect(text).not.toContain('+1')
  })
})

// AC-8 — a role actually removed from the list (not just an arbitrary unknown key) still
// resolves to the deleted-role sentence, in both display functions. roles.ts is already
// shipped, so this pins existing behaviour rather than going red.
describe('AC-8 — a deleted role resolves to the deleted-role sentence, in both display functions', () => {
  it('resolve and inspectorResolve both flag a role removed from the list as missing', () => {
    const withoutCompliance = removeRole(MOCK_FIRM_ROLES, 'compliance')
    expect(resolve(withoutCompliance, MOCK_FIRM_MEMBERS, 'compliance')).toEqual({ text: 'Role no longer exists', warn: true })
    expect(inspectorResolve(withoutCompliance, MOCK_FIRM_MEMBERS, 'compliance')).toEqual({ text: 'Role no longer exists', warn: true })
  })
})

describe('AC-9 — unassignedRoles', () => {
  it('unassignedRoles is empty when every role has an active holder', () => {
    // mf1 admin, mf3 reviewer — both approvers. mf2 (preparer) would no longer keep this
    // test's own claim true, so the fixture stays on approvers rather than the expectation
    // flipping to contradict the test's name.
    const roles = [role('a', 'A', '', ['mf1']), role('b', 'B', '', ['mf3'])]
    expect(unassignedRoles(roles, MOCK_FIRM_MEMBERS)).toEqual([])
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
    expect(stepsForMember(SEED_FIRM_POLICIES, MOCK_FIRM_ROLES, 'mf3')?.total).toBe(5)
  })

  it('stepsForMember returns null when the total is zero', () => {
    // mf2 Folake Adesina holds only Invoice Preparer, which no firm policy references.
    expect(stepsForMember(SEED_FIRM_POLICIES, MOCK_FIRM_ROLES, 'mf2')).toBeNull()
  })

  it('stepsForMember returns null for a member holding no role', () => {
    expect(stepsForMember(SEED_FIRM_POLICIES, MOCK_FIRM_ROLES, 'mf6')).toBeNull()
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

// QA (task-344) — RolesView's search box and intro line, lifted here so a spec can reach them.
describe('QA — filterRoles', () => {
  it('matches case-insensitively on title', () => {
    // Not "INVOICE": that substring also hits fin_mgr's and cfo's descs.
    expect(filterRoles(MOCK_FIRM_ROLES, 'PREPARER').map((r) => r.key)).toEqual(['preparer'])
  })

  it('matches case-insensitively on desc when the title does not match', () => {
    // 'Tax Reviewer' never contains "vat" — only its desc does.
    const hits = filterRoles(MOCK_FIRM_ROLES, 'VAT')
    expect(hits.map((r) => r.key)).toEqual(['compliance'])
    expect(hits[0].title.toLowerCase()).not.toContain('vat')
  })

  it('returns nothing for a query no role matches', () => {
    expect(filterRoles(MOCK_FIRM_ROLES, 'zzz-nonexistent')).toEqual([])
  })

  it('an empty (or whitespace) query returns every role, as a copy', () => {
    const result = filterRoles(MOCK_FIRM_ROLES, '   ')
    expect(result).toEqual(MOCK_FIRM_ROLES)
    expect(result).not.toBe(MOCK_FIRM_ROLES)
  })
})

describe('QA — intro', () => {
  it('firm names its own first two role titles', () => {
    expect(intro(MOCK_FIRM_ROLES)).toBe(
      'A role is a named seat in your approval policies — Invoice Preparer, Engagement Manager. Workflow steps point at the role; the people here are who actually signs.',
    )
  })

  it('inhouse names its own first two role titles, not firm’s', () => {
    expect(intro(MOCK_INHOUSE_ROLES)).toBe(
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
// MOCK_FIRM_MEMBERS / MOCK_INHOUSE_MEMBERS directly, so a wrong body fails on OUR
// assertion, not a number this file invented.

describe('AC-4 — pickerMembers excludes invited people in both modes', () => {
  it('firm: 7 seeded, 1 invited (mf6) -> 6 rows, none invited', () => {
    expect(MOCK_FIRM_MEMBERS.filter((m) => m.status === 'invited').map((m) => m.id)).toEqual(['mf6']) // guard: pins the seed fact this test relies on
    const rows = pickerMembers(MOCK_FIRM_MEMBERS)
    expect(rows).toHaveLength(6)
    expect(rows.some((m) => m.status === 'invited')).toBe(false)
  })

  it('inhouse: 16 seeded, 2 invited (mh15, mh16) -> 14 rows, none invited', () => {
    expect(MOCK_INHOUSE_MEMBERS.filter((m) => m.status === 'invited').map((m) => m.id)).toEqual(['mh15', 'mh16']) // guard
    const rows = pickerMembers(MOCK_INHOUSE_MEMBERS)
    expect(rows).toHaveLength(14)
    expect(rows.some((m) => m.status === 'invited')).toBe(false)
  })
})

describe('AC-4 — filterPickerMembers matches name or email, case-insensitively, and trims', () => {
  // Built directly rather than via pickerMembers(), so this spec's own failure reason can
  // never be masked by the pickerMembers stub throwing first.
  const inhouseSelectable = MOCK_INHOUSE_MEMBERS.filter((m) => m.status !== 'invited')

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
    expect(MOCK_FIRM_MEMBERS).toHaveLength(7) // guard: pins the roster length this test means to NOT use
    expect(pickerSelectionCount(2, MOCK_FIRM_MEMBERS)).toBe('2 of 6 selected')
  })

  it('inhouse: 5 selected of 14 selectable', () => {
    expect(pickerSelectionCount(5, MOCK_INHOUSE_MEMBERS)).toBe('5 of 14 selected')
  })
})

describe('AC-5 — hiddenInvitedFootnote pluralises around one', () => {
  it('firm has exactly one invited person -> the singular residue string', () => {
    expect(hiddenInvitedFootnote(MOCK_FIRM_MEMBERS)).toBe('1 invited person is hidden until they accept the invite.')
  })

  it('inhouse has two invited people -> the plural form', () => {
    expect(hiddenInvitedFootnote(MOCK_INHOUSE_MEMBERS)).toBe('2 invited people are hidden until they accept the invite.')
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
    expect(MOCK_FIRM_ROLES.some((r) => r.title === 'Tax Reviewer')).toBe(true) // guard: the title really is a duplicate
    expect(canSaveRole('Tax Reviewer')).toBe(true)
  })
})

describe('AC-7 — deleteRoleConfirm names the role and its usage', () => {
  it('a used role names its usage sentence and warns those steps will block', () => {
    // 'compliance' (Tax Reviewer) is named once each in polF1/polF2/polF3 — three approval
    // steps across three policies (SEED_FIRM_POLICIES in policies.fixture.ts).
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
    removeRole(MOCK_FIRM_ROLES, 'compliance')
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

  it('a rename keeps the stored key, not a re-derived one', () => {
    const original = role('fin_mgr', 'Engagement Manager', 'First sign-off', ['mf3'])
    const renamedTitle = 'Chief Engagement Officer'
    expect({ ...original, title: renamedTitle }.key).toBe('fin_mgr')
  })
})

// Forward risk, now DECIDED: [invite-writes-both-stores] makes a role holding an invited
// member's id reachable (an invite used to put the fresh id straight into the chosen role), so
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
    const mh15 = MOCK_INHOUSE_MEMBERS.find((m) => m.id === 'mh15')!
    expect(mh15.status).toBe('invited') // guard
    expect(pickerMembers(MOCK_INHOUSE_MEMBERS).some((m) => m.id === 'mh15')).toBe(false)
  })

  it('pickerSelectionCount keeps echoing its numerator, uncrossed against the selectable set', () => {
    // Stand-in for role.members once a role holds an invited id: two real selectable holders
    // plus one invited id the picker will never render as a row.
    const inflatedSelected = ['mh1', 'mh2', 'mh15']
    expect(pickerSelectionCount(inflatedSelected.length, MOCK_INHOUSE_MEMBERS)).toBe('3 of 14 selected')
    const checkableRows = pickerMembers(MOCK_INHOUSE_MEMBERS).map((m) => m.id)
    const checkableOfSelected = inflatedSelected.filter((id) => checkableRows.includes(id))
    // The picker can only ever tick 2 of those 3 ids — the numerator above does not know that.
    expect(checkableOfSelected).toEqual(['mh1', 'mh2'])
    expect(checkableOfSelected.length).not.toBe(inflatedSelected.length)
  })

  it('pickerHiddenAmongSelected names the gap the count above cannot explain on its own', () => {
    const inflatedSelected = ['mh1', 'mh2', 'mh15']
    expect(pickerHiddenAmongSelected(inflatedSelected, MOCK_INHOUSE_MEMBERS)).toBe(1)
    expect(pickerHiddenAmongSelected(['mh1', 'mh2'], MOCK_INHOUSE_MEMBERS)).toBe(0)
  })

  it('hiddenSelectionNote renders the role-modal-count addendum for that gap', () => {
    const inflatedSelected = ['mh1', 'mh2', 'mh15']
    const n = pickerHiddenAmongSelected(inflatedSelected, MOCK_INHOUSE_MEMBERS)
    expect(hiddenSelectionNote(n)).toBe('+1 invited')
  })
})

// ============================================================================
// Members surfaces speak in workflow roles (Test Specs table, Mode A)
// ============================================================================

describe('rosterRoleCell — the roster column, first title plus N', () => {
  it('shows the first title plus N, with the full list newline-joined in the tooltip', () => {
    expect(rosterRoleCell(MOCK_FIRM_ROLES, 'mf3')).toEqual({
      text: 'Engagement Manager +1',
      tooltip: 'Engagement Manager\nSenior Manager',
    })
  })

  it('shows a bare title for a single role, with no +N and a one-line tooltip', () => {
    expect(rosterRoleCell(MOCK_FIRM_ROLES, 'mf4')).toEqual({ text: 'Tax Reviewer', tooltip: 'Tax Reviewer' })
  })

  it('is an em dash with an empty tooltip for nobody', () => {
    expect(rosterRoleCell(MOCK_FIRM_ROLES, 'mf6')).toEqual({ text: '—', tooltip: '' })
  })
})

describe('stepsForMember and stepsWarning — the suspended-row warning counts every held role', () => {
  it('the suspended in-house cfo holder still blocks the two cfo steps', () => {
    const result = stepsForMember(SEED_INHOUSE_POLICIES, MOCK_INHOUSE_ROLES, 'mh6')
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

describe('[remove-prunes-suspend-keeps] — a status write does not unstaff', () => {
  it('suspending does not unstaff — the role keeps the member, resolve just blocks', () => {
    expect(MOCK_FIRM_ROLES.find((r) => r.key === 'fin_mgr')?.members).toEqual(['mf3']) // guard
    const mf3 = MOCK_FIRM_MEMBERS.find((m) => m.id === 'mf3')!
    const suspended = replaceMember(MOCK_FIRM_MEMBERS, { ...mf3, status: 'suspended' })
    const result = resolve(MOCK_FIRM_ROLES, suspended, 'fin_mgr')
    expect(result.text).toBe('Musa Danjuma')
    expect(result.warn).toBe(true)
  })
})

describe('the firm workspace resolves every seeded approval step to a named person', () => {
  it('all ten seeded firm approval steps resolve without warn', () => {
    const stepRoles = approvalRoles(SEED_FIRM_POLICIES)
    expect(stepRoles.length).toBe(10) // guard against a vacuous pass
    for (const key of stepRoles) {
      const result = resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, key)
      expect(result.warn).toBe(false)
      expect(result.text).not.toBe('Nobody assigned')
      expect(result.text).not.toBe('Role no longer exists')
    }
  })
})

// ============================================================================
// APPR-04-02 — activeHolders/resolution gain the isApprover predicate
// ============================================================================
// Cases the re-derived assertions above don't reach on their own: activeHolders has no
// direct test anywhere else in this file, and no existing fixture mixes an active approver
// with an active non-approver under the same role.

describe('APPR-04-02 — the approver predicate', () => {
  it('activeHolders drops a preparer and keeps a reviewer', () => {
    const roles = [role('mixed', 'Mixed', '', ['mf2', 'mf3'])]
    expect(activeHolders(roles, MOCK_FIRM_MEMBERS, 'mixed').map((m) => m.id)).toEqual(['mf3'])
  })

  it('an admin holder still satisfies a seat (Q1 admits admins)', () => {
    expect(resolve(MOCK_FIRM_ROLES, MOCK_FIRM_MEMBERS, 'cfo')).toEqual({ text: 'Chinedu Okafor', warn: false })
  })

  it('+N still counts every other holder, approver or not', () => {
    const roles = [role('mixed', 'Mixed', '', ['mf3', 'mf2'])]
    expect(resolve(roles, MOCK_FIRM_MEMBERS, 'mixed')).toEqual({ text: 'Musa Danjuma +1', warn: false })
  })
})

// QA (Stage 4) gap-fill — the predicate is an AND of two independent halves, and three
// functions that must stay OUTSIDE it (holders, rolesOfMember/rosterRoleCell, stepsForMember).
describe('QA — APPR-04-02 adversarial coverage', () => {
  it('a suspended admin blocks on status alone, even though the role half passes', () => {
    const suspendedAdmin: Member = { ...MOCK_FIRM_MEMBERS[0], id: 'susp-admin', status: 'suspended' }
    const members = [...MOCK_FIRM_MEMBERS, suspendedAdmin]
    const roles = [role('lonely', 'Lonely', '', ['susp-admin'])]
    expect(activeHolders(roles, members, 'lonely')).toEqual([])
    expect(resolve(roles, members, 'lonely')).toEqual({ text: 'Chinedu Okafor', warn: true })
  })

  it('an active preparer and a suspended reviewer both fail the seat, for different reasons', () => {
    const suspendedReviewer: Member = { ...MOCK_FIRM_MEMBERS[2], id: 'susp-reviewer', status: 'suspended' }
    const members = [...MOCK_FIRM_MEMBERS, suspendedReviewer]
    const roles = [role('both-fail', 'Both fail', '', ['mf2', 'susp-reviewer'])]
    expect(activeHolders(roles, members, 'both-fail')).toEqual([])
    expect(resolve(roles, members, 'both-fail')).toEqual({ text: 'Folake Adesina +1', warn: true })
  })

  it('an active admin listed second still becomes primary over a preparer listed first', () => {
    const roles = [role('order', 'Order', '', ['mf2', 'mf1'])]
    expect(activeHolders(roles, MOCK_FIRM_MEMBERS, 'order').map((m) => m.id)).toEqual(['mf1'])
    expect(resolve(roles, MOCK_FIRM_MEMBERS, 'order')).toEqual({ text: 'Chinedu Okafor +1', warn: false })
  })

  it('holders stays unfiltered: an invited preparer still comes back', () => {
    const roles = [role('inv', 'Inv', '', ['mf6'])]
    expect(holders(roles, MOCK_FIRM_MEMBERS, 'inv').map((m) => m.id)).toEqual(['mf6'])
  })

  it('rolesOfMember and rosterRoleCell see a preparer holder exactly like any other', () => {
    expect(rolesOfMember(MOCK_FIRM_ROLES, 'mf2').map((r) => r.title)).toEqual(['Invoice Preparer'])
    expect(rosterRoleCell(MOCK_FIRM_ROLES, 'mf2')).toEqual({ text: 'Invoice Preparer', tooltip: 'Invoice Preparer' })
  })

  it('stepsForMember counts a preparer-held role: membership is the gate, not access role', () => {
    const p = testPolicy('P', [{ id: 'a1', type: 'approval', role: 'cfo', sla: '48', delegate: false }])
    const roles = [role('cfo', 'CFO', '', ['mf2'])]
    expect(stepsForMember([p], roles, 'mf2')).toEqual({ total: 1, policies: [{ policyName: 'P', count: 1 }] })
  })
})

// ============================================================================
// QA (Stage 4) — traversal/resolution facts Mode A flagged as dropped with
// stepsFor/resolvePosition and not portable from the seed alone: no seeded policy puts an
// approval node in an else lane, repeats one role twice within one policy, or blocks a role
// with more than one inactive holder. Hand-built fixtures, not seed data.
// ============================================================================

describe('steps — the else lane counts too, not just then', () => {
  it('an approval node sitting only in the else lane is counted', () => {
    const p = testPolicy('Else-only policy', [
      { id: 'c1', type: 'condition', field: 'amount', op: '>', value: 1, then: [], else: [{ id: 'a1', type: 'approval', role: 'cfo', sla: '48', delegate: false }] },
    ])
    expect(roleSteps([p], 'cfo')).toEqual({ total: 1, policies: [{ policyName: 'Else-only policy', count: 1 }] })
  })
})

describe('steps — notify and autoapprove nodes never count, only approval', () => {
  it('ignores notify/autoapprove at root and in both branch lanes, even a notify target matching the key', () => {
    const p = testPolicy('Notify-heavy policy', [
      { id: 'n1', type: 'notify', target: 'cfo', channel: 'Email' },
      { id: 'au1', type: 'autoapprove' },
      {
        id: 'c1',
        type: 'condition',
        field: 'amount',
        op: '>',
        value: 1,
        then: [{ id: 'n2', type: 'notify', target: 'cfo', channel: 'Email' }],
        else: [{ id: 'au2', type: 'autoapprove' }],
      },
    ])
    expect(roleSteps([p], 'cfo')).toEqual({ total: 0, policies: [] })
  })
})

describe('steps — one policy naming the same role twice reports 2, not 1', () => {
  it('counts every occurrence within the policy, root plus a branch lane', () => {
    const p = testPolicy('Belt-and-braces policy', [
      { id: 'a1', type: 'approval', role: 'cfo', sla: '48', delegate: false },
      { id: 'c1', type: 'condition', field: 'amount', op: '>', value: 1, then: [{ id: 'a2', type: 'approval', role: 'cfo', sla: '48', delegate: false }], else: [] },
    ])
    expect(roleSteps([p], 'cfo')).toEqual({ total: 2, policies: [{ policyName: 'Belt-and-braces policy', count: 2 }] })
  })
})

describe('resolve — blocked with more than one inactive holder still appends +N', () => {
  it('two inactive holders (suspended + invited): warn true, text carries +1', () => {
    // mh6 suspended, mh16 invited — neither active, so the role is blocked with two holders.
    const roles = [role('cfo', 'CFO', '', ['mh6', 'mh16'])]
    expect(resolve(roles, MOCK_INHOUSE_MEMBERS, 'cfo')).toEqual({ text: 'Adebayo Ogunlesi +1', warn: true })
  })
})

// QA (Stage 4): the two "SEED_*_ROLES re-pointed at the seeded subjects" blocks that used to
// live here were dropped by this subtask's own red-test commit, on the claim that
// TestSeedEveryStaffedUserHasAMembershipInThatTenant and TestSeedAllFiveHolderStatesAreReachable
// (internal/platform/db/seed_demo_test.go:4688,4852) supersede them. Checked fact-by-fact: those
// Go tests assert DB row state and never invoke unassignedRoles/resolve, so they cannot prove
// what those functions RENDER for real seed-shaped data (e.g. the exact 'Adebayo Ogunlesi +1'
// text) — that half of the coverage was a genuine loss, not a supersession. Restored here as a
// file-local fixture (SHIPPED_FIRM/INHOUSE_ROLES/MEMBERS, byte-identical to the deleted
// SEED_FIRM_ROLES/SEED_INHOUSE_ROLES and the deleted seedWire-built directory) rather than in
// production code, since roles are server-fetched now and there is no module constant to re-add.
const SHIPPED_FIRM_ROLES: readonly Role[] = [
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

const SHIPPED_INHOUSE_ROLES: readonly Role[] = [
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

describe('QA — the unstaffed and suspended-only states survive off the shipped role set', () => {
  it('every staffed role id in SHIPPED_FIRM_ROLES and SHIPPED_INHOUSE_ROLES is a seeded subject', () => {
    const SUBJECT_RE = /^c0000000-0000-0000-0000-0000000000\d{2}$/
    const ids = [...SHIPPED_FIRM_ROLES, ...SHIPPED_INHOUSE_ROLES].flatMap((r) => r.members)
    expect(ids.length).toBeGreaterThan(0) // guard against a vacuous pass
    for (const id of ids) expect(id).toMatch(SUBJECT_RE)
  })

  it('the unstaffed and suspended-only states survive the shipped role set', () => {
    expect(SHIPPED_FIRM_ROLES.find((r) => r.key === 'quality_reviewer')?.members).toEqual([])
    expect(SHIPPED_INHOUSE_ROLES.find((r) => r.key === 'fin_mgr')?.members).toEqual([])
    expect(SHIPPED_INHOUSE_ROLES.find((r) => r.key === 'ceo')?.members).toEqual([])
    expect(SHIPPED_INHOUSE_ROLES.find((r) => r.key === 'cfo')?.members).toEqual(['c0000000-0000-0000-0000-000000000012'])
  })
})

const seedWire = (userId: string, role: string, name: string, email: string, status: string): MembershipWire => ({
  user_id: userId,
  role,
  status,
  display_name: name,
  email,
})

const SHIPPED_FIRM_MEMBERS: readonly Member[] = [
  seedWire('c0000000-0000-0000-0000-000000000001', 'admin', 'Chinedu Okafor', 'c.okafor@okafor.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000003', 'preparer', 'Folake Adesina', 'f.adesina@okafor.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000004', 'reviewer', 'Musa Danjuma', 'm.danjuma@okafor.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000005', 'reviewer', 'Chiamaka Nwosu', 'c.nwosu@okafor.ng', 'active'),
  seedWire(
    'c0000000-0000-0000-0000-000000000006',
    'preparer',
    'Oluwaseyifunmi Adebanjo-Ogunleye',
    'o.adebanjo-ogunleye@okaforandpartners.com.ng',
    'active',
  ),
  seedWire('c0000000-0000-0000-0000-000000000007', 'reviewer', 'Halima Yusuf', 'h.yusuf@okafor.ng', 'suspended'),
].map((w) => toMember(w, 'nobody'))

const SHIPPED_INHOUSE_MEMBERS: readonly Member[] = [
  seedWire('c0000000-0000-0000-0000-000000000002', 'admin', 'Ngozi Balogun', 'n.balogun@honeywell.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000008', 'reviewer', 'Yetunde Fashola', 'y.fashola@honeywell.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000009', 'reviewer', 'Emeka Uzowulu', 'e.uzowulu@honeywell.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000010', 'reviewer', 'Tunde Adeyemi', 't.adeyemi@honeywell.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000011', 'reviewer', 'Ibrahim Bello', 'i.bello@honeywell.ng', 'active'),
  seedWire('c0000000-0000-0000-0000-000000000012', 'reviewer', 'Adebayo Ogunlesi', 'a.ogunlesi@honeywell.ng', 'suspended'),
  seedWire('c0000000-0000-0000-0000-000000000013', 'preparer', 'Zainab Lawal', 'z.lawal@honeywell.ng', 'active'),
].map((w) => toMember(w, 'nobody'))

describe('QA — unassignedRoles/resolve against the shipped role set and a live-shaped directory', () => {
  it('firm: preparer and quality_reviewer are unassigned, and every one of the ten seeded approval steps resolves without warn', () => {
    expect(unassignedRoles(SHIPPED_FIRM_ROLES, SHIPPED_FIRM_MEMBERS).map((r) => r.key)).toEqual(['preparer', 'quality_reviewer'])
    const stepRoles = approvalRoles(SEED_FIRM_POLICIES)
    expect(stepRoles.length).toBe(10) // guard against a vacuous pass
    for (const key of stepRoles) {
      const result = resolve(SHIPPED_FIRM_ROLES, SHIPPED_FIRM_MEMBERS, key)
      expect(result.warn).toBe(false)
      expect(result.text).not.toBe('Nobody assigned')
    }
  })

  it('inhouse: preparer/fin_mgr/cfo/ceo are unassigned, cfo blocks on its lone suspended holder, fin_dir resolves its active pair', () => {
    expect(unassignedRoles(SHIPPED_INHOUSE_ROLES, SHIPPED_INHOUSE_MEMBERS).map((r) => r.key)).toEqual(['preparer', 'fin_mgr', 'cfo', 'ceo'])
    expect(resolve(SHIPPED_INHOUSE_ROLES, SHIPPED_INHOUSE_MEMBERS, 'cfo')).toEqual({ text: 'Adebayo Ogunlesi', warn: true })
    expect(resolve(SHIPPED_INHOUSE_ROLES, SHIPPED_INHOUSE_MEMBERS, 'fin_dir')).toEqual({ text: 'Ngozi Balogun +1', warn: false })
  })
})

// [roles.ts:348] — a third `.toLowerCase()` on `email` no AC names (found in the plan pass).
// Same shape as members.test.ts's filterMembers/classifyInvites null-email specs.
describe('filterPickerMembers tolerates a null email', () => {
  it('tolerates a null email, both when the name matches and when nothing does', () => {
    const nullEmailRow = { ...MOCK_INHOUSE_MEMBERS[0], email: null as unknown as string }
    // The `||` short-circuits on the name match before `email` is ever touched — this half
    // is already true today and is pinned as a fact, not a red.
    expect(filterPickerMembers([nullEmailRow], 'ngozi').map((m) => m.id)).toEqual([nullEmailRow.id])
    // A query the name does NOT match forces evaluation of `m.email.toLowerCase()` — the
    // genuine red: today it throws instead of falling through to "no match".
    expect(() => filterPickerMembers([nullEmailRow], 'zzz-no-match')).not.toThrow()
    expect(filterPickerMembers([nullEmailRow], 'zzz-no-match')).toEqual([])
  })
})

// ============================================================================
// APPR-04-01 (task-460) — the wire layer over the APPR-02 workflow-role endpoints
// ============================================================================
// Two doubles, matching two different jobs:
//  - listWorkflowRoles/createWorkflowRole/updateWorkflowRole/deleteWorkflowRole/
//    setRoleMembers (the URL + exact-wire-body specs) use the REAL createAuthedFetch
//    with only `fetch` stubbed (invoices.test.ts's I6-I14 idiom) — a stubbed 4xx must
//    produce a genuine ApiError, not a re-implementation of apiFetch's own contract.
//  - The "rejects unchanged" and "composes two calls" specs inject a hand-rolled
//    AuthedFetch double directly (portfolio.test.ts's offboardEntity/onboardEntity
//    precedent): object IDENTITY can only be proven that way — apiFetch mints a fresh
//    ApiError from the response on every call, so a fetch-mock round trip could never
//    produce the "same instance" AC-4 requires.

interface RoleWireResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function mockFetchOnce(response: RoleWireResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const wireBase = 'https://gw'

const wireRoleA: Role = { key: 'cfo', title: 'Engagement Partner', desc: 'Signs off invoices above ₦1bn', members: ['u1'] }
const wireRoleB: Role = { key: 'compliance', title: 'Tax Reviewer', desc: 'Checks VAT, WHT and TIN detail before filing', members: [] }

describe('AC-1/AC-2 — listWorkflowRoles hits the workflow-roles path and unwraps the envelope', () => {
  it('GET .../workflow-roles resolves the .workflow_roles array with no init', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ workflow_roles: [wireRoleA, wireRoleB] }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listWorkflowRoles(af, wireBase)

    expect(result).toEqual([wireRoleA, wireRoleB])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles')
  })
})

describe('AC-1/AC-5 — createWorkflowRole POSTs title and desc in that key order', () => {
  it('the wire body is byte-exact: {"title":...,"desc":...}', async () => {
    const created: Role = { key: 'tax-reviewer-2', title: 'Tax Reviewer', desc: 'Checks VAT', members: [] }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(created) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await createWorkflowRole(af, wireBase, 'Tax Reviewer', 'Checks VAT')

    expect(result).toEqual(created)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ title: 'Tax Reviewer', desc: 'Checks VAT' }))
  })
})

describe('AC-1/AC-6 — updateWorkflowRole PATCHes only the keys the patch carries', () => {
  it('an absent title key stays absent on the wire — not sent as "" or null', async () => {
    const updated: Role = { ...wireRoleA, desc: '' }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await updateWorkflowRole(af, wireBase, 'cfo', { desc: '' })

    expect(result).toEqual(updated)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles/cfo')
    expect(init.method).toBe('PATCH')
    expect(init.body).toBe(JSON.stringify({ desc: '' }))
    expect(init.body).not.toContain('title')
  })
})

describe('AC-1 — deleteWorkflowRole sends DELETE and no body', () => {
  it('DELETE .../workflow-roles/{key} with init.body undefined', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wireRoleA) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await deleteWorkflowRole(af, wireBase, 'cfo')

    expect(result).toEqual(wireRoleA)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles/cfo')
    expect(init.method).toBe('DELETE')
    expect(init.body).toBeUndefined()
  })
})

describe('AC-1/AC-5 — setRoleMembers PUTs the members array under the members key', () => {
  it('PUT .../workflow-roles/{key}/members, body {"members":["u1","u2"]}', async () => {
    const staffed: Role = { ...wireRoleA, members: ['u1', 'u2'] }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(staffed) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await setRoleMembers(af, wireBase, 'cfo', ['u1', 'u2'])

    expect(result).toEqual(staffed)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles/cfo/members')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe(JSON.stringify({ members: ['u1', 'u2'] }))
  })

  it('an empty set PUTs an explicit {"members":[]}, never an absent key — the server 400s on {}', async () => {
    const unstaffed: Role = { ...wireRoleA, members: [] }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(unstaffed) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await setRoleMembers(af, wireBase, 'cfo', [])

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBe(JSON.stringify({ members: [] }))
  })
})

describe('AC-1 — a role key is percent-encoded into the path', () => {
  it('a key carrying a space and a slash never splits the URL path', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wireRoleA) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await deleteWorkflowRole(af, wireBase, 'tax reviewer/2')

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles/tax%20reviewer%2F2')
  })
})

describe('AC-4 — every wrapper rejects with the ApiError unchanged (not swallowed or rewrapped)', () => {
  // "unchanged" is proven by REFERENCE (portfolio.test.ts's offboardEntity/onboardEntity
  // precedent): a mocked AuthedFetch rejects with a specific ApiError instance, and each
  // wrapper must propagate that EXACT object — no try/catch to rebuild or reshape it.
  const boom = new ApiError('http', 'only an admin can change workflow roles', 403)

  it('listWorkflowRoles', async () => {
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    await expect(listWorkflowRoles(af, wireBase)).rejects.toBe(boom)
  })

  it('createWorkflowRole', async () => {
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    await expect(createWorkflowRole(af, wireBase, 'Tax Reviewer', '')).rejects.toBe(boom)
  })

  it('updateWorkflowRole', async () => {
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    await expect(updateWorkflowRole(af, wireBase, 'cfo', { desc: '' })).rejects.toBe(boom)
  })

  it('deleteWorkflowRole', async () => {
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    await expect(deleteWorkflowRole(af, wireBase, 'cfo')).rejects.toBe(boom)
  })

  it('setRoleMembers', async () => {
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    await expect(setRoleMembers(af, wireBase, 'cfo', ['u1'])).rejects.toBe(boom)
  })

  it('the rejected value carries .status and .message unchanged', async () => {
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    const caught = await deleteWorkflowRole(af, wireBase, 'cfo').catch((e: unknown) => e)
    expect(caught).toBe(boom)
    expect((caught as ApiError).status).toBe(403)
    expect((caught as ApiError).message).toBe('only an admin can change workflow roles')
  })
})

describe('AC-3 — createStaffedRole composes POST then PUT', () => {
  it('POSTs then PUTs the SERVER-returned key, in order — not a slug of the title', async () => {
    const created: Role = { key: 'e2e-seat', title: 'E2E seat', desc: '', members: [] }
    const staffed: Role = { ...created, members: ['u1'] }
    const af = vi.fn((_url: string, opts?: ApiFetchOptions) => Promise.resolve(opts?.method === 'POST' ? created : staffed))

    const result = await createStaffedRole(af as unknown as AuthedFetch, wireBase, 'E2E seat', '', ['u1'])

    expect(result).toEqual(staffed)
    expect(af).toHaveBeenCalledTimes(2)
    const [firstUrl, firstOpts] = af.mock.calls[0] as [string, ApiFetchOptions]
    const [secondUrl, secondOpts] = af.mock.calls[1] as [string, ApiFetchOptions]
    expect(firstUrl).toBe('https://gw/api/invoice/v1/workflow-roles')
    expect(firstOpts.method).toBe('POST')
    expect(secondUrl).toBe('https://gw/api/invoice/v1/workflow-roles/e2e-seat/members')
    expect(secondOpts.method).toBe('PUT')
  })

  it('makes no PUT for an unstaffed role — exactly one call, resolves the POST Role', async () => {
    const created: Role = { key: 'e2e-seat', title: 'E2E seat', desc: '', members: [] }
    const af = vi.fn().mockResolvedValue(created)

    const result = await createStaffedRole(af as unknown as AuthedFetch, wireBase, 'E2E seat', '', [])

    expect(result).toEqual(created)
    expect(af).toHaveBeenCalledTimes(1)
    const [, opts] = af.mock.calls[0] as [string, ApiFetchOptions]
    expect(opts.method).toBe('POST')
  })

  it('a failed PUT rejects and does not hide that the role was already created', async () => {
    const created: Role = { key: 'e2e-seat', title: 'E2E seat', desc: '', members: [] }
    const putFailure = new ApiError('http', 'bad member id', 400)
    const af = vi.fn((_url: string, opts?: ApiFetchOptions) => (opts?.method === 'POST' ? Promise.resolve(created) : Promise.reject(putFailure)))

    await expect(createStaffedRole(af as unknown as AuthedFetch, wireBase, 'E2E seat', '', ['u1'])).rejects.toBe(putFailure)
    expect(af).toHaveBeenCalledTimes(2) // the POST is still recorded as having happened
    const [, firstOpts] = af.mock.calls[0] as [string, ApiFetchOptions]
    expect(firstOpts.method).toBe('POST')
  })
})

// ============================================================================
// Story AC-4 — the ctx write verbs App.tsx composes over the wire layer above
// ============================================================================
// createRole/renameRole/staffRole/deleteRole stay INLINE closures in App.tsx, the
// setMemberStatus precedent (App.tsx:1041-1044) — never independently unit tested there.
// The harnesses below reproduce that composition exactly (wire call, then patch the
// mirror off the SERVER's own returned row) so these specs pin the contract Stage 3's
// App.tsx code has to satisfy. createRole/staffRole/the rejected-write case compose only
// pieces already proven above (createStaffedRole, setRoleMembers, addRole, replaceRole),
// so they hold today — their value is nailing down the exact composition, not driving new
// behaviour. renameRole's diff is the one piece with no home yet: `rolePatch` is a new
// export Stage 3 must add to lib/roles.ts, and these two specs genuinely fail until it does.
describe('AC-4 — the ctx write verbs', () => {
  async function harnessCreateRole(af: AuthedFetch, mirror: readonly Role[], title: string, desc: string, members: readonly string[]) {
    const created = await createStaffedRole(af, wireBase, title, desc, members)
    return { created, mirror: addRole(mirror, created) }
  }

  async function harnessStaffRole(af: AuthedFetch, mirror: readonly Role[], key: string, members: readonly string[]) {
    const updated = await setRoleMembers(af, wireBase, key, members)
    return { updated, mirror: replaceRole(mirror, updated) }
  }

  it('createRole stages the returned role in the mirror', async () => {
    const created: Role = { key: 'k1', title: 'T', desc: 'D', members: [] }
    const af = vi.fn().mockResolvedValue(created)

    const { created: result, mirror } = await harnessCreateRole(af as unknown as AuthedFetch, [], 'T', 'D', [])

    expect(result).toEqual(created)
    expect(mirror).toEqual([created])
  })

  it('staffRole resolves the SERVER-ordered members, and the mirror carries that order', async () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: '', members: [] }
    const staffed: Role = { ...stored, members: ['u2', 'u1'] } // the server's own order, not the caller's ['u1','u2']
    const af = vi.fn().mockResolvedValue(staffed)

    const { updated, mirror } = await harnessStaffRole(af as unknown as AuthedFetch, [stored], 'cfo', ['u1', 'u2'])

    expect(updated.members).toEqual(['u2', 'u1'])
    expect(mirror[0].members).toEqual(['u2', 'u1'])
  })

  it('a rejected write leaves the mirror untouched — nothing writes before the await', async () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: '', members: ['u1'] }
    const boom = new ApiError('http', 'only an admin can change workflow roles', 403)
    const af = vi.fn().mockRejectedValue(boom)

    await expect(harnessStaffRole(af as unknown as AuthedFetch, [stored], 'cfo', ['u2'])).rejects.toBe(boom)
    // The harness only computes a patched mirror AFTER the write settles — a rejection
    // never reaches replaceRole, so a real ctx's own role list is left exactly as it was.
  })

  it('renameRole PATCHes only the changed field — an unchanged desc is never sent', async () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'X', members: [] }
    const af = vi.fn().mockResolvedValue({ ...stored, title: 'Chief Financial Officer' })

    const patch = rolePatch(stored, 'Chief Financial Officer', 'X')
    expect(patch).toEqual({ title: 'Chief Financial Officer' })

    await updateWorkflowRole(af as unknown as AuthedFetch, wireBase, stored.key, patch)
    const [, init] = af.mock.calls[0] as [string, ApiFetchOptions]
    // `af` is a bare double, not the real apiFetch — it never JSON.stringifies opts.body
    // (only the real client.ts does, on the way to fetch), so the raw object is what lands.
    expect(init.body).toEqual({ title: 'Chief Financial Officer' })
  })

  it('renameRole makes no call when nothing changed', () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'X', members: [] }
    expect(rolePatch(stored, 'CFO', 'X')).toEqual({})
    // Stage 3's renameRole gates the call on Object.keys(patch).length -- an empty patch
    // is the signal to skip updateWorkflowRole and resolve the stored role directly.
  })
})

describe('QA — rolePatch adversarial coverage', () => {
  it('both title and desc changed: patch carries both, title-then-desc key order', () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'Old', members: [] }
    const patch = rolePatch(stored, 'Chief Financial Officer', 'New')
    expect(patch).toEqual({ title: 'Chief Financial Officer', desc: 'New' })
    // Body key order is load-bearing on the wire (apiFetch stringifies as-is) -- nothing
    // else in the suite pins it for a patch carrying both fields.
    expect(JSON.stringify(patch)).toBe('{"title":"Chief Financial Officer","desc":"New"}')
  })

  it('desc only changed: title stays absent from the patch', () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'Old', members: [] }
    const patch = rolePatch(stored, 'CFO', 'New')
    expect(patch).toEqual({ desc: 'New' })
    expect(patch.title).toBeUndefined()
  })

  it('a whitespace-only difference still counts as changed — rolePatch does not trim', () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'X', members: [] }
    expect(rolePatch(stored, 'CFO ', 'X')).toEqual({ title: 'CFO ' })
  })
})

// harnessCreateRole/harnessStaffRole above cover the created/staffed happy path and staffRole's
// rejection; these compose the same already-shipped pieces for the two verbs that block did not
// exercise on rejection.
describe('QA — AC-4 rejected writes for createRole, renameRole and deleteRole', () => {
  async function harnessRenameRole(af: AuthedFetch, mirror: readonly Role[], stored: Role, title: string, desc: string) {
    const patch = rolePatch(stored, title, desc)
    const updated = await updateWorkflowRole(af, wireBase, stored.key, patch)
    return { updated, mirror: replaceRole(mirror, updated) }
  }

  async function harnessDeleteRole(af: AuthedFetch, mirror: readonly Role[], key: string) {
    await deleteWorkflowRole(af, wireBase, key)
    return removeRole(mirror, key)
  }

  it('a rejected createRole never stages anything in the mirror', async () => {
    const boom = new ApiError('http', 'only an admin can create workflow roles', 403)
    const af = vi.fn().mockRejectedValue(boom)

    const created: Role = { key: 'k1', title: 'T', desc: 'D', members: [] }
    async function harnessCreateRole(mirror: readonly Role[]) {
      const role = await createStaffedRole(af as unknown as AuthedFetch, wireBase, created.title, created.desc, created.members)
      return addRole(mirror, role)
    }
    await expect(harnessCreateRole([])).rejects.toBe(boom)
  })

  it('a rejected renameRole leaves the mirror untouched', async () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'X', members: [] }
    const boom = new ApiError('http', 'only an admin can rename workflow roles', 403)
    const af = vi.fn().mockRejectedValue(boom)

    await expect(harnessRenameRole(af as unknown as AuthedFetch, [stored], stored, 'New Title', 'X')).rejects.toBe(boom)
  })

  it('a rejected deleteRole leaves the mirror untouched', async () => {
    const stored: Role = { key: 'cfo', title: 'CFO', desc: 'X', members: [] }
    const boom = new ApiError('http', 'only an admin can delete workflow roles', 403)
    const af = vi.fn().mockRejectedValue(boom)

    await expect(harnessDeleteRole(af as unknown as AuthedFetch, [stored], 'cfo')).rejects.toBe(boom)
  })
})

// Story AC-1 "no gateway means no network call" is not unit-tested here: rolesAsync's
// ternary (`base ? listWorkflowRoles(...) : Promise.reject(...)`) lives inline in App.tsx,
// mirroring membersAsync's own untested null-base gate (App.tsx:297-303) -- there is no
// separate shouldFetchRoles predicate to import (unlike shouldFetchEntities/
// shouldFetchInvoices), and this repo never renders App.tsx in tests. Structurally
// guaranteed instead: listWorkflowRoles's `base` parameter is typed `string`, not
// `string | null`, so the ternary is the only legal caller when base is absent, checked by
// Stage 3's `pnpm -r typecheck` gate.

describe('AC-7 — rolesSurface takes the worse of the two statuses', () => {
  // Hand-computed, independent of any ladder implementation: error worst, then loading,
  // then idle/empty (both read as 'empty'), and only both-ready reaches 'roster'.
  const CASES: readonly [AsyncStatus, AsyncStatus, ReturnType<typeof rolesSurface>][] = [
    ['idle', 'idle', 'empty'],
    ['idle', 'loading', 'loading'],
    ['idle', 'error', 'error'],
    ['idle', 'empty', 'empty'],
    ['idle', 'ready', 'empty'],
    ['loading', 'idle', 'loading'],
    ['loading', 'loading', 'loading'],
    ['loading', 'error', 'error'],
    ['loading', 'empty', 'loading'],
    ['loading', 'ready', 'loading'],
    ['error', 'idle', 'error'],
    ['error', 'loading', 'error'],
    ['error', 'error', 'error'],
    ['error', 'empty', 'error'],
    ['error', 'ready', 'error'],
    ['empty', 'idle', 'empty'],
    ['empty', 'loading', 'loading'],
    ['empty', 'error', 'error'],
    ['empty', 'empty', 'empty'],
    ['empty', 'ready', 'empty'],
    ['ready', 'idle', 'empty'],
    ['ready', 'loading', 'loading'],
    ['ready', 'error', 'error'],
    ['ready', 'empty', 'empty'],
    ['ready', 'ready', 'roster'],
  ]

  it('covers all 25 (rolesStatus, membersStatus) pairs', () => {
    expect(CASES.length).toBe(25) // guard against a vacuous pass
    for (const [rolesStatus, membersStatus, expected] of CASES) {
      expect(rolesSurface(rolesStatus, membersStatus)).toBe(expected)
    }
  })

  it('an errored members fetch can never render as a landed roles grid', () => {
    expect(rolesSurface('ready', 'error')).toBe('error')
    expect(rolesSurface('error', 'ready')).toBe('error')
  })
})

describe('AC-8 — isApprover admits reviewer and admin only', () => {
  it('mirrors the Go predicate: admin and reviewer approve, preparer does not', () => {
    expect(isApprover('admin')).toBe(true)
    expect(isApprover('reviewer')).toBe(true)
    expect(isApprover('preparer')).toBe(false)
  })
})

describe('AC-9 — the deleted reducers are gone', () => {
  it('setRoleMembers is the AuthedFetch-taking wrapper, not the old 3-arg reducer', () => {
    expect(typeof setRoleMembers).toBe('function')
    expect(setRoleMembers.length).toBe(4) // (f, base, key, memberIds) — the old reducer took 3
  })

  it('pruneMember and addRoleMembers are not exported from roles.ts', async () => {
    const rolesModule: Record<string, unknown> = await import('./roles')
    expect('pruneMember' in rolesModule).toBe(false)
    expect('addRoleMembers' in rolesModule).toBe(false)
  })
})

// ============================================================================
// QA gap-fill (task-460 Stage 4) — adversarial/edge coverage the Stage-2.5 table
// didn't include, plus one hole the composed-call spec above couldn't see.
// ============================================================================

describe('QA — createStaffedRole distinguishes the server key from a same-shaped title slug', () => {
  // The AC-3 "not a slug of the title" spec above uses title 'E2E seat', whose own slug
  // ('e2e-seat') is IDENTICAL to the fixture's server-returned key — so it cannot actually
  // tell "used created.key" apart from "used slug(title)". This fixture's slug and server
  // key deliberately diverge (a title collision bumps the server's key, not the slug).
  it('PUTs the collision-suffixed server key, never a fresh slug of the title', async () => {
    const created: Role = { key: 'tax-reviewer-2', title: 'Tax Reviewer', desc: '', members: [] }
    const staffed: Role = { ...created, members: ['u1'] }
    const af = vi.fn((_url: string, opts?: ApiFetchOptions) => Promise.resolve(opts?.method === 'POST' ? created : staffed))

    await createStaffedRole(af as unknown as AuthedFetch, wireBase, 'Tax Reviewer', '', ['u1'])

    const [secondUrl] = af.mock.calls[1] as [string, ApiFetchOptions]
    expect(secondUrl).toBe('https://gw/api/invoice/v1/workflow-roles/tax-reviewer-2/members')
  })
})

describe('QA — non-2xx surfaces the servers exact message and status through the real fetch path', () => {
  // The AC-4 block above proves reference identity via a hand-rolled AuthedFetch double.
  // These go through the REAL createAuthedFetch + apiFetch, so they also prove the
  // {error: "..."} envelope is actually read into ApiError.message end to end, per wrapper.
  // Messages are approval.go's statusForErr literals, not invented strings.
  it('listWorkflowRoles: 401 unauthorized', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const caught = await listWorkflowRoles(af, wireBase).catch((e: unknown) => e)
    expect((caught as ApiError).status).toBe(401)
    expect((caught as ApiError).message).toBe('unauthorized')
  })

  it('createWorkflowRole: 403 only an admin can change workflow roles', async () => {
    mockFetchOnce({ ok: false, status: 403, json: () => Promise.resolve({ error: 'only an admin can change workflow roles' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const caught = await createWorkflowRole(af, wireBase, 'Tax Reviewer', '').catch((e: unknown) => e)
    expect((caught as ApiError).status).toBe(403)
    expect((caught as ApiError).message).toBe('only an admin can change workflow roles')
  })

  it('updateWorkflowRole: 404 on a missing key', async () => {
    mockFetchOnce({ ok: false, status: 404, json: () => Promise.resolve({ error: 'workflow role not found' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const caught = await updateWorkflowRole(af, wireBase, 'ghost', { desc: 'x' }).catch((e: unknown) => e)
    expect((caught as ApiError).status).toBe(404)
    expect((caught as ApiError).message).toBe('workflow role not found')
  })

  it('deleteWorkflowRole: 404 on a missing key', async () => {
    mockFetchOnce({ ok: false, status: 404, json: () => Promise.resolve({ error: 'workflow role not found' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const caught = await deleteWorkflowRole(af, wireBase, 'ghost').catch((e: unknown) => e)
    expect((caught as ApiError).status).toBe(404)
    expect((caught as ApiError).message).toBe('workflow role not found')
  })

  it('setRoleMembers: 400 on a bad member id', async () => {
    mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'invalid request' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const caught = await setRoleMembers(af, wireBase, 'cfo', ['not-a-uuid']).catch((e: unknown) => e)
    expect((caught as ApiError).status).toBe(400)
    expect((caught as ApiError).message).toBe('invalid request')
  })
})

describe('QA — a unicode role key is percent-encoded byte-for-byte into the path', () => {
  it('non-ASCII and an embedded slash both survive encodeURIComponent, not a hand-rolled escape', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wireRoleA) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await deleteWorkflowRole(af, wireBase, 'café/💰')

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/workflow-roles/caf%C3%A9%2F%F0%9F%92%B0')
  })
})

describe('QA — setRoleMembers with an undefined array (a caller bypassing the type system)', () => {
  it('serializes to an absent key on the wire, and the servers own 400 for it propagates unchanged', async () => {
    const fetchMock = mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'members must be an array of member ids' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const caught = await setRoleMembers(af, wireBase, 'cfo', undefined as unknown as string[]).catch((e: unknown) => e)

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBe('{}') // JSON.stringify({members: undefined}) drops the key entirely
    expect((caught as ApiError).status).toBe(400)
    expect((caught as ApiError).message).toBe('members must be an array of member ids')
  })
})

describe('QA — updateWorkflowRole with no changed keys', () => {
  it('an empty patch still sends a literal {} body, not an absent one', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wireRoleA) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await updateWorkflowRole(af, wireBase, 'cfo', {})

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBe('{}')
  })
})

describe('QA — setRoleMembers resolves whatever order the server sends, not the callers input order', () => {
  it('the callers ["u1","u2"] input does not dictate the resolved arrays order', async () => {
    const reordered: Role = { ...wireRoleA, members: ['u2', 'u1'] }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(reordered) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await setRoleMembers(af, wireBase, 'cfo', ['u1', 'u2'])

    expect(result.members).toEqual(['u2', 'u1'])
  })
})
