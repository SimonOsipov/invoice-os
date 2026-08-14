import { describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncStatus } from '@invoice-os/api-client'

import { APP_PERSONAS } from '../auth'
import { CFG } from '../data'
import type { AuthedFetch } from './portfolio'
import * as membersModule from './members'
import {
  ACCESS_ROLES,
  accessRoleLabel,
  activeAdmins,
  addMembers,
  CAPABILITY_FOOTNOTE,
  CAPABILITY_ROWS,
  classifyInvites,
  clientSelectionCount,
  CLIENT_ROSTER,
  CLIENT_USERS_COPY,
  delegateCandidates,
  emailLabel,
  filterClientRoster,
  filterMembers,
  hasDerivableName,
  inhouseNotifyTargets,
  initialsFrom,
  INVITE_ERROR,
  invitedNotice,
  isFiltering,
  isProtectedAdmin,
  isValidEmail,
  listMembers,
  memberInitials,
  MEMBER_UNBACKED,
  membersSurface,
  membersViewState,
  mergeChips,
  nameFromEmail,
  needsClientPick,
  NO_CLIENT_MATCH,
  NO_CLIENTS_NOTE,
  parseEmailInput,
  PROTECTED_ADMIN_NOTE,
  removeConfirmQuestion,
  removeMember,
  REMOVE_EXPLANATION,
  replaceMember,
  setMembershipStatus,
  SUSPEND_EXPLANATION,
  toMember,
  type Member,
  type MemberStatus,
  type MembershipWire,
} from './members'
// Legal in a spec: roles.ts imports members.ts, and the test graph has no cycle. The
// approver pair is transcribed inline in members.ts; T3-6 below pins the two agree.
import { isApprover } from './roles'

// --- fixtures ---------------------------------------------------------------
// The mock roster, moved here verbatim when lib/members.ts stopped shipping a seed. It is a
// FIXTURE now, not a shipped constant: it exercises the reducers and the derivations over
// rows the live directory (identity + access role + status only) cannot express — invited
// rows, departments, per-person client access, `invitedBy`.
const SEED_FIRM_MEMBERS: readonly Member[] = [
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

const SEED_INHOUSE_MEMBERS: readonly Member[] = [
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

/** The deleted `seedMembers`, over the fixture. Composes the module's own deep clone. */
const seedMembers = () => ({ firm: SEED_FIRM_MEMBERS.map((m) => ({ ...m })), inhouse: SEED_INHOUSE_MEMBERS.map((m) => ({ ...m })) })

// Every spec starts from a fresh copy, never from a SEED_* constant. A `Member` is flat, so
// a spread is a full copy — the nested `clientAccess` the old deep clone existed for is gone.
const firm = () => seedMembers().firm
const inhouse = () => seedMembers().inhouse
const names = (list: readonly Member[]) => list.map((m) => m.name)
const you = (list: readonly Member[]): Member => list.filter((m) => m.isYou)[0]

/** A hand-built in-house row, for the frames the shipped seed deliberately cannot reach. */
const inhouseRow = (name: string, status: MemberStatus): Member => ({
  id: `m-${name.split(' ')[0].toLowerCase()}`,
  name,
  initials: name
    .split(' ')
    .map((t) => t[0])
    .join(''),
  email: `${name.split(' ')[0].toLowerCase()}@honeywell.ng`,
  role: 'reviewer',
  status,
  isYou: false,
})

/**
 * The firm mirror of `inhouseRow`. The T2 reducer specs need a row they can hand to
 * `addMembers`/`replaceMember` that is NOT in the seed.
 */
const firmRow = (name: string, status: MemberStatus): Member => ({
  id: `m-${name.split(' ')[0].toLowerCase()}`,
  name,
  initials: name
    .split(' ')
    .map((t) => t[0])
    .join(''),
  email: `${name.split(' ')[0].toLowerCase()}@okafor.ng`,
  role: 'preparer',
  status,
  isYou: false,
})

const NOTIFY_BASE = ['Audit Committee', 'Board', 'Preparer']

describe('seed shape (T1.1–T1.5, §15.6)', () => {
  it('ships 7 firm members and 16 in-house members (T1.1)', () => {
    const store = seedMembers()
    expect(store.firm).toHaveLength(7)
    expect(store.inhouse).toHaveLength(16)
  })

  it('copies the rows, so a mutated row leaks into neither the other copy nor the fixture (T1.2)', () => {
    const a = seedMembers()
    const b = seedMembers()

    expect(a.firm[0]).not.toBe(b.firm[0])
    expect(a.firm[0]).not.toBe(SEED_FIRM_MEMBERS[0])

    a.firm[0].name = 'Mutated Row'
    expect(a.firm[0].name).toBe('Mutated Row')
    expect(b.firm[0].name).toBe('Chinedu Okafor')
    expect(SEED_FIRM_MEMBERS[0].name).toBe('Chinedu Okafor')
  })

  it('the firm you-row is the shipped firm persona (T1.3)', () => {
    const row = you(firm())
    expect(row).toMatchObject({
      name: 'Chinedu Okafor',
      initials: 'CO',
      email: 'c.okafor@okafor.ng',
      role: 'admin',
    })

    // The three-field subset only: `Persona.role` is the JWT role 'authenticated'
    // (auth.ts:46), not an AccessRole, so a whole-object match would fail for the wrong
    // reason. This is the assertion that stops the seed drifting from the sidebar.
    const { name, initials, email } = APP_PERSONAS.firm
    expect([row.name, row.initials, row.email]).toEqual([name, initials, email])
  })

  it('the in-house you-row is the shipped in-house persona (T1.4)', () => {
    const row = you(inhouse())
    expect(row).toMatchObject({
      name: 'Ngozi Balogun',
      initials: 'NB',
      role: 'admin',
    })

    const { name, initials, email } = APP_PERSONAS.inhouse
    expect([row.name, row.initials, row.email]).toEqual([name, initials, email])
  })

  it('marks exactly one member as you in each mode (T1.5)', () => {
    expect(firm().filter((m) => m.isYou)).toHaveLength(1)
    expect(inhouse().filter((m) => m.isYou)).toHaveLength(1)
  })
})

// AC-1 — Member.position is gone from the type and from both seeds.
describe('AC-1 — Member.position is gone', () => {
  it('no seeded member, in either mode, carries a position field', () => {
    for (const row of [...SEED_FIRM_MEMBERS, ...SEED_INHOUSE_MEMBERS]) {
      expect('position' in row).toBe(false)
    }
  })

  it('the in-house seed is otherwise byte-identical — ids, names, statuses', () => {
    // Not expected to go red on its own: nothing here touches `position`, so this holds
    // today too. Pinned as the regression guard that catches the deletion taking anything
    // else with it, the same shape as AC-13's RoleKey-widening guard above.
    expect(SEED_INHOUSE_MEMBERS.map((m) => [m.id, m.name, m.status])).toEqual([
      ['mh1', 'Ngozi Balogun', 'active'],
      ['mh2', 'Yetunde Fashola', 'active'],
      ['mh3', 'Emeka Uzowulu', 'active'],
      ['mh4', 'Tunde Adeyemi', 'active'],
      ['mh5', 'Ibrahim Bello', 'active'],
      ['mh6', 'Adebayo Ogunlesi', 'suspended'],
      ['mh7', 'Zainab Lawal', 'active'],
      ['mh8', 'Chidi Anyanwu', 'active'],
      ['mh9', 'Aisha Mohammed', 'active'],
      ['mh10', 'Segun Oyelaran', 'active'],
      ['mh11', 'Oluwafunmilayo Ademola-Oyediran', 'active'],
      ['mh12', 'Kelechi Obi', 'active'],
      ['mh13', 'Hauwa Abubakar', 'active'],
      ['mh14', 'Olumide Bakare', 'active'],
      ['mh15', 'Nneka Chukwu', 'invited'],
      ['mh16', 'Sadiq Ibrahim', 'invited'],
    ])
  })

})

describe('notify targets (T1.29–T1.31, §11.4)', () => {

  it('offers the standing committees, then Preparer (T1.29)', () => {
    // The five departments that used to lead this list went with the field. `toMember` never
    // set one, so the dropdown had already lost them.
    expect(inhouseNotifyTargets('Finance')).toEqual([...NOTIFY_BASE, 'Finance'])
  })

  it('keeps a legacy stored target selectable, appended last (T1.30)', () => {
    // polH1's seeded notify target (h1n7 in the policies.fixture.ts seed) is not a department or a committee.
    expect(inhouseNotifyTargets('Tax Team')).toEqual([...NOTIFY_BASE, 'Tax Team'])
  })

  it('does not duplicate a current value the list already carries (T1.31)', () => {
    const targets = inhouseNotifyTargets('Board')
    expect(targets).toEqual(NOTIFY_BASE)
    expect(targets.filter((t) => t === 'Board')).toHaveLength(1)
  })
})

describe('delegateCandidates (T1.32, APPR-00 Q1)', () => {
  it('lists every active in-house approver — the admin first, then the six active reviewers (T1.32)', () => {
    const candidates = delegateCandidates(inhouse())
    expect(candidates).toEqual([
      'Ngozi Balogun', // admin — mh1 leads the roster, and filter/map keep input order
      'Yetunde Fashola',
      'Emeka Uzowulu',
      'Tunde Adeyemi',
      'Ibrahim Bello',
      'Oluwafunmilayo Ademola-Oyediran',
      'Hauwa Abubakar',
    ])
    expect(candidates).not.toContain('Adebayo Ogunlesi') // suspended
    expect(candidates).not.toContain('Sadiq Ibrahim') // invited
    expect(candidates).toContain('Ngozi Balogun') // admin — INVERTED by APPR-00 Q1, was not.toContain
    expect(candidates).not.toContain('Zainab Lawal') // preparer
  })
})

// --- APPR-10-03: the approver set widens to {admin, reviewer} ----------------

describe('APPR-00 Q1 — the widened set stops at the approver pair', () => {
  it('an active preparer is not a delegate candidate, in either mode', () => {
    // KEEP-GREEN both sides of the widening: over-widening to "any active member" is the
    // only change this catches.
    expect(names(inhouse())).toContain('Zainab Lawal') // in the roster, so the exclusion is not vacuous
    expect(names(firm())).toContain('Folake Adesina')
    expect(delegateCandidates(inhouse())).not.toContain('Zainab Lawal')
    expect(delegateCandidates(firm())).not.toContain('Folake Adesina')
  })
})

describe('APPR-00 Q1 — the inline approver pair agrees with lib/roles.ts', () => {
  it('delegateCandidates admits exactly the active members isApprover accepts', () => {
    // members.ts cannot import roles.ts (one-way, members.ts:8-11), so the pair is
    // transcribed twice. This is the pin that catches the two drifting apart.
    const rows = inhouse()
    const viaIsApprover = rows.filter((m) => m.status === 'active' && isApprover(m.role)).map((m) => m.name)
    expect(viaIsApprover.length, 'isApprover admitted nobody, so the comparison below is vacuous').toBeGreaterThan(0)
    expect(delegateCandidates(rows)).toEqual(viaIsApprover)
  })
})

describe('APPR-00 Q1 — status still gates the newly-admitted admin role', () => {
  /** Neither fixture carries a suspended or an invited ADMIN, so these rows are built here. */
  const adminRow = (name: string, status: MemberStatus): Member => ({ ...inhouseRow(name, status), role: 'admin' })

  it('excludes a suspended admin and an invited admin, and admits the active one', () => {
    // The active admin is the positive control for the ROLE half — without it the two
    // exclusions are satisfied by any predicate that refuses every admin, which is the rule
    // Q1 retired. The reviewer is the control for the STATUS half.
    const candidates = delegateCandidates([
      adminRow('Amara Eze', 'suspended'),
      adminRow('Bola Adewale', 'invited'),
      adminRow('Dele Okonkwo', 'active'),
      inhouseRow('Chika Obi', 'active'),
    ])
    expect(candidates).toEqual(['Dele Okonkwo', 'Chika Obi'])
    expect(candidates).not.toContain('Amara Eze')
    expect(candidates).not.toContain('Bola Adewale')
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

describe('inhouseNotifyTargets — empty current (QA13, §11.4)', () => {
  it('appends nothing when the node carries no stored target (QA13)', () => {
    // A new notify node's target starts empty; an unguarded push would put a blank
    // option at the bottom of the select.
    expect(inhouseNotifyTargets('')).toEqual(NOTIFY_BASE)
  })
})

describe('activeAdmins — status precedence (QA14, §9)', () => {
  it('counts only ACTIVE admins — a suspended or invited admin does not hold the lock (QA14)', () => {
    // Both seeds ship exactly one admin, so T1.38 reads 1 whether or not status filters.
    const list = [
      { ...inhouseRow('Ngozi Balogun', 'active'), role: 'admin' as const },
      { ...inhouseRow('Suspended Admin', 'suspended'), role: 'admin' as const },
      { ...inhouseRow('Invited Admin', 'invited'), role: 'admin' as const },
    ]
    expect(names(activeAdmins(list))).toEqual(['Ngozi Balogun'])
  })

})

describe('seed invariants the reducers depend on (QA16, §15.6)', () => {
  it('gives every member a unique id and a unique email within its mode (QA16)', () => {
    // MEMB-01-02's replaceMember / removeMember key off `id`, and classifyInvites
    // compares lower-cased emails; a duplicate in either would break them silently.
    for (const list of [firm(), inhouse()]) {
      const ids = list.map((m) => m.id)
      const emails = list.map((m) => (m.email ?? '').toLowerCase())
      expect(new Set(ids).size).toBe(list.length)
      expect(new Set(emails).size).toBe(list.length)
    }
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
      'transmit to NRS/MBS',
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

describe('reducers — immutability and misses (T2.25–T2.28, T2.32, §15.1)', () => {
  it('appends without touching the input list (T2.25)', () => {
    const list = firm()
    const added = firmRow('Tosin Okonkwo', 'invited')
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
    const ghost = firmRow('Ghost Person', 'active')
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

  it('never touches its input list or the seed, in either mode (T2.32)', () => {
    const seedFirmBefore = structuredClone(SEED_FIRM_MEMBERS)
    const seedInhouseBefore = structuredClone(SEED_INHOUSE_MEMBERS)

    const cases = [
      { list: firm(), pristine: firm(), id: 'mf2', extra: firmRow('Tosin Okonkwo', 'invited') },
      { list: inhouse(), pristine: inhouse(), id: 'mh4', extra: inhouseRow('Tosin Okonkwo', 'invited') },
    ]

    for (const c of cases) {
      const untouched = () => expect(c.list).toEqual(c.pristine)

      replaceMember(c.list, { ...c.list[1], role: 'admin' })
      untouched()
      addMembers(c.list, [c.extra])
      untouched()
      removeMember(c.list, c.id)
      untouched()
    }

    expect(SEED_FIRM_MEMBERS).toEqual(seedFirmBefore)
    expect(SEED_INHOUSE_MEMBERS).toEqual(seedInhouseBefore)
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
    const base = inhouse()
    const list = replaceMember(base, { ...base[3], role: 'admin' })
    expect(activeAdmins(list)).toHaveLength(2)
    expect(isProtectedAdmin(list, list[0])).toBe(false)
    expect(isProtectedAdmin(list, list[3])).toBe(false)
  })

  it('re-locks the first admin once the second is suspended (T2.43)', () => {
    const base = inhouse()
    const two = replaceMember(base, { ...base[3], role: 'admin' })
    const list = replaceMember(two, { ...two[3], status: 'suspended' })

    expect(list[3].status).toBe('suspended')
    expect(isProtectedAdmin(list, list[0])).toBe(true)
    expect(isProtectedAdmin(list, list[3])).toBe(false)
  })

  it('does not count an invited admin (T2.44)', () => {
    const base = inhouse()
    const list = replaceMember(base, { ...base[15], role: 'admin' })

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

describe('reducer misses — the leaves T2.27 does not reach (QA19–QA20, §15.1)', () => {
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

describe('the seed literals themselves, as an oracle no snapshot can launder (QA25, §15.6)', () => {
  it('holds its hand-authored values after every reducer runs over the CONSTANTS (QA25)', () => {
    // T2.32 snapshots SEED_* with structuredClone at the top of its own body, so corruption
    // committed by an earlier spec in this file is captured by the snapshot and the
    // comparison passes vacuously. This spec asserts the literals directly — and runs the
    // reducers over the constants THEMSELVES (not a clone) first, which is the one call
    // shape T2.32 never makes.
    const extraFirm = firmRow('Tosin Okonkwo', 'invited')
    const extraInhouse = inhouseRow('Tosin Okonkwo', 'invited')

    replaceMember(SEED_FIRM_MEMBERS, { ...SEED_FIRM_MEMBERS[1], role: 'admin' })
    addMembers(SEED_FIRM_MEMBERS, [extraFirm])
    removeMember(SEED_FIRM_MEMBERS, 'mf2')

    replaceMember(SEED_INHOUSE_MEMBERS, { ...SEED_INHOUSE_MEMBERS[3], role: 'admin' })
    addMembers(SEED_INHOUSE_MEMBERS, [extraInhouse])
    removeMember(SEED_INHOUSE_MEMBERS, 'mh4')

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

describe('invite-pipeline contracts MEMB-01-06 has to honour (QA33–QA35, §7)', () => {
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

  it('accepts a separators-only local part, and derives neither a name nor initials (QA35)', () => {
    // RECORDED, NOT ENDORSED. `isValidEmail` is deliberately minimal, so a local part made
    // only of separators passes it and classifies `ok`. `hasDerivableName` is the gate that
    // stops it becoming a row with a blank Person cell.
    expect(isValidEmail('...@x.ng')).toBe(true)
    expect(classifyInvites(firm(), ['...@x.ng'])).toEqual(['ok'])
    expect(nameFromEmail('...@x.ng')).toBe('')
    expect(initialsFrom('...@x.ng')).toBe('')
    expect(hasDerivableName('...@x.ng')).toBe(false)
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
    const row = firmRow('Ghost Person', 'active')

    expect(classifyInvites(empty, ['a@x.ng', 'bad'])).toEqual(['ok', 'malformed'])
    expect(filterMembers(empty, 'ngozi', 'all')).toEqual([])
    expect(addMembers(empty, [row])).toEqual([row])
    expect(replaceMember(empty, row)).toEqual([])
    expect(removeMember(empty, 'mf1')).toEqual([])

    for (const out of [addMembers(empty, []), replaceMember(empty, row), removeMember(empty, 'mf1')]) {
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

    const detached: Member = { ...firmRow('Ghost Admin', 'active'), role: 'admin' }
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
// Pulled into this module — which already owns `emailLabel` and
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
    // The `roleOf` fallback's twin (roles.ts): a persisted id from a newer build
    // must render as something rather than crash the row. Unreachable from the seed, which
    // is exactly why the screenshot gate cannot stand in for this spec.
    expect(accessRoleLabel('auditor' as never)).toBe('auditor')
    expect(ACCESS_ROLES.map((r) => r.id)).not.toContain('auditor')
  })
})

// AC-12 — [two-banners]: both banners render the SAME roles.ts constant now.
describe('AC-12 — the members banner string is the roles-tab string', () => {
  it('unassignedNotice is exported from roles.ts only; members.ts exports no such constant', () => {
    expect('unassignedNotice' in membersModule).toBe(false)
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
  it("carries §6's Client users copy verbatim (T5.1)", () => {
    expect(CLIENT_USERS_COPY).toBe(
      'Give a contact at one of your clients read-only access, or approval rights on their own invoices.',
    )
  })
})

// AC-13 — the footnote and the drawer helper both speak in workflow roles now.
describe('AC-13 — the capability footnote says workflow role, not position', () => {
  it('CAPABILITY_FOOTNOTE mentions a workflow role and no longer a position; REVIEWER_HINT is gone', () => {
    expect(CAPABILITY_FOOTNOTE).toContain('workflow role')
    expect(CAPABILITY_FOOTNOTE).not.toContain('position')
    expect('REVIEWER_HINT' in membersModule).toBe(false)
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

describe('MEMB-01-06 invite modal copy (T6.2, §7)', () => {
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

    const verdicts = classifyInvites(list, [active.email ?? '', pending.email ?? '', 'not-an-email', 'new@x.ng'])
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
    for (const m of [...firm(), ...inhouse()]) expect(hasDerivableName(m.email ?? '')).toBe(true)

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

describe('mergeChips — the de-dupe nothing upstream performs (T6.9-T6.11, §7)', () => {
  it('de-dupes on the LOWER-CASED address, keeping the first spelling (T6.9)', () => {
    // The whole reason this function exists. QA33 pins `classifyInvites` returning ['ok','ok']
    // for ['new@x.ng', 'NEW@x.ng'] and QA34 pins `addMembers` appending both — so if the chip
    // list compared with `===`, one person would be invited twice with two ids and two rows.
    expect(mergeChips([], ['a@x.ng', 'A@x.ng'])).toEqual(['a@x.ng'])
    expect(mergeChips(['a@x.ng'], ['A@X.NG'])).toEqual(['a@x.ng'])
    // The FIRST spelling survives, because that is the address that will be mailed — the same
    // rule `parseEmailInput` follows within a single paste (T2.5).
    expect(mergeChips(['MiXeD@x.ng'], ['mixed@x.ng'])).toEqual(['MiXeD@x.ng'])
  })

  it('is the CROSS-paste half that parseEmailInput cannot do (T6.10)', () => {
    // `parseEmailInput` runs per paste and sees a fresh string each time, so pasting the same
    // list twice yields two identical arrays that only this function can reconcile.
    const first = mergeChips([], parseEmailInput('a@x.ng, b@x.ng'))
    expect(first).toEqual(['a@x.ng', 'b@x.ng'])
    const second = mergeChips(first, parseEmailInput('b@x.ng; c@x.ng'))
    expect(second).toEqual(['a@x.ng', 'b@x.ng', 'c@x.ng'])
    // Order is append-only: a re-pasted address does NOT jump to the end, so the chips do not
    // reshuffle under the user between pastes.
    expect(mergeChips(second, parseEmailInput('a@x.ng'))).toEqual(['a@x.ng', 'b@x.ng', 'c@x.ng'])
  })

  it('degrades cleanly and never returns its input reference (T6.11)', () => {
    const cur = ['a@x.ng']
    expect(mergeChips(cur, [])).toEqual(['a@x.ng'])
    expect(mergeChips(cur, parseEmailInput(''))).toEqual(['a@x.ng'])
    expect(mergeChips(cur, parseEmailInput('   '))).toEqual(['a@x.ng'])
    expect(mergeChips([], [])).toEqual([])
    // Always allocates, per §15.1 — a caller that mutated the result must not reach the state
    // array React is still holding.
    expect(mergeChips(cur, [])).not.toBe(cur)
  })
})

describe('the client picker\'s two invented sentences (T6.12, §7)', () => {
  it('explains the disabled Send, and echoes the roster search word for word (T6.12)', () => {
    // INVENTED COPY. §7 gives the zero-selected state a BEHAVIOUR (nothing to grant, nothing to
    // send) and no sentence, so the sentence is pinned here rather than left in the component.
    expect(NO_CLIENTS_NOTE).toBe('Pick at least one client, or switch to All clients.')
    // Deliberately the roster search's line with one noun changed. Pinned as a PAIR, so the two
    // cannot drift into phrasing the same failure two ways on one tab.
    expect(NO_CLIENT_MATCH).toBe('No clients match this search.')
    expect(NO_CLIENT_MATCH).toBe('No members match this search.'.replace('members', 'clients'))
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

// ---------------------------------------------------------------------------
// QA47–QA48 — MEMB-01-06 QA (Mode B), the two node-reachable gaps T6.1–T6.12 left
// ---------------------------------------------------------------------------
// Mutation-tested: all ten mutations of the T6 batch's own subjects were caught, but
// EIGHT mutations of the modal's WIRING survived all three gates, because vitest is
// `environment: node` and the repo has no DOM component layer (docs/e2e-convention.md
// also puts Settings out of scope for browser E2E). These two are what remains inside
// this module's reach — everything else on that list is a Phase 3.5 gate item.

describe('MEMB-01-06 QA — the picker filter is literal (QA47)', () => {
  it('matches a LITERAL substring, not a pattern (QA47)', () => {
    // T6.5 pins the trim / case / substring rule, but every one of its queries is a plain
    // word, so `includes(q)` and `new RegExp(q).test(name)` agree on all of them and the
    // spec cannot tell them apart. These two can:
    //   '.' — a literal dot matches ONE roster name; as a pattern it matches all six.
    expect(filterClientRoster('.').map((c) => c.name)).toEqual(['Nigerian Delta Supplies Co.'])
    //   '(' — a literal paren matches nothing; as a pattern it THROWS SyntaxError, taking
    //   the modal down mid-keystroke (AC#10) rather than showing NO_CLIENT_MATCH.
    expect(filterClientRoster('(')).toEqual([])
    expect(filterClientRoster('[a-z]')).toEqual([])
    // And the ampersand two roster names actually contain, so "literal" is proven in both
    // directions rather than only by what it refuses.
    expect(filterClientRoster('&').map((c) => c.id)).toEqual([0, 3])
  })
})

// The `none` sentinel risk QA48 named, retargeted from WF_ROLES to Role.key. The invite
// modal's Workflow role select draws from BOTH tenant modes' seeded roles, and `Role.key` is
// a free-form, server-minted slug, not a closed union — a role titled "None" collides with
// the sentinel on its own, with no widening required. This makes the risk MORE reachable
// than QA48 described it, not less.
//
// File-local mirror of lib/roles.ts's former SEED_FIRM_ROLES/SEED_INHOUSE_ROLES key sets
// (subtask 04 deleted the module-level seed; the DB seed's Go-side test is the source of
// truth now) — only the keys matter here, not title/desc/members.
const MOCK_FIRM_ROLE_KEYS = ['preparer', 'fin_mgr', 'fin_dir', 'compliance', 'cfo', 'quality_reviewer']
const MOCK_INHOUSE_ROLE_KEYS = ['preparer', 'line_mgr', 'fin_mgr', 'controller', 'fin_dir', 'compliance', 'cfo', 'ceo']

describe("the invite modal's `none` sentinel stays un-collided (QA48, updated)", () => {
  it('no seeded role, in either mode, is keyed `none`', () => {
    expect(MOCK_FIRM_ROLE_KEYS).not.toContain('none')
    expect(MOCK_INHOUSE_ROLE_KEYS).not.toContain('none')
  })
})

// ---------------------------------------------------------------------------
// T7.1–T7.8 — MEMB-01-07, the member drawer
// ---------------------------------------------------------------------------
// The drawer itself is unreachable from here (`environment: node`), so what these specs
// hold is the half that is NOT rendering: §8/§9's copy, and the three facts the drawer is
// forbidden to derive inside itself. Every literal below was byte-checked against the
// story in the vault.

describe("§8's danger-zone copy — the most important text in the story (T7.1–T7.3)", () => {
  it('reproduces both explanations verbatim (T7.1)', () => {
    // §8's two bullets, character for character. This is the text a buyer reads to decide
    // whether removing someone destroys their audit trail, and the answer is no — which is
    // a promise the product keeps only if the sentence keeps saying it.
    expect(SUSPEND_EXPLANATION).toBe(
      'Removes their approver rights and keeps all history. Sign-in is not blocked yet. Their name stays on every invoice they touched.',
    )
    expect(REMOVE_EXPLANATION).toBe(
      'Revokes access permanently. Their name stays on every invoice they touched; audit history is never rewritten.',
    )

    // The shared clause, pinned as a PAIR. §8 says it twice on purpose: whichever button
    // you are looking at, the reassurance is in front of you. A copy edit that "de-duplicates"
    // it would silently make one of the two acts look like it erases history.
    const shared = 'Their name stays on every invoice they touched'
    expect(SUSPEND_EXPLANATION).toContain(shared)
    expect(REMOVE_EXPLANATION).toContain(shared)

    // The semicolon, not a full stop. `Remove`'s second clause is what makes the first
    // survivable, and a sentence break demotes it to an afterthought.
    expect(REMOVE_EXPLANATION).toContain('touched; audit history is never rewritten.')
  })

  // AC8 [suspend-copy-is-true]: suspension only pulls approver rights, it does not block
  // sign-in — nothing in the auth path reads the status column.
  it('AC8: SUSPEND_EXPLANATION stops claiming to block sign-in', () => {
    expect(SUSPEND_EXPLANATION).not.toContain('Blocks sign-in')
    expect(SUSPEND_EXPLANATION).toContain('Sign-in is not blocked yet.')
  })

  it('AC8: the shared audit-trail clause survives the rewrite in both explanations', () => {
    // SUSPEND_EXPLANATION's clause ends the sentence with a full stop, both before and
    // after the rewrite -- this guards the rewrite from dropping it.
    expect(SUSPEND_EXPLANATION).toContain('Their name stays on every invoice they touched.')
    // REMOVE_EXPLANATION continues past the clause with a semicolon, never a full stop (its
    // own docblock: "The semicolon is load-bearing") -- so this half is period-less.
    expect(REMOVE_EXPLANATION).toContain('Their name stays on every invoice they touched')
  })

  it("interpolates the member's real name into the confirm question (T7.2)", () => {
    // §8 writes this one with the name inside it, which is why it is a function. The
    // template placeholder must never survive into the rendered string.
    expect(removeConfirmQuestion('Folake Adesina')).toBe(
      'Remove Folake Adesina? Access is revoked immediately. Their name stays on every invoice they touched.',
    )
    expect(removeConfirmQuestion('Adebayo Ogunlesi')).toBe(
      'Remove Adebayo Ogunlesi? Access is revoked immediately. Their name stays on every invoice they touched.',
    )
    expect(removeConfirmQuestion('Folake Adesina')).not.toContain('{name}')

    // The confirm carries the SAME reassurance as the button it guards, and a DIFFERENT
    // consequence clause ('revoked immediately' vs 'permanently') — both deliberate.
    expect(removeConfirmQuestion('X')).toContain('Their name stays on every invoice they touched.')
    expect(removeConfirmQuestion('X')).toContain('Access is revoked immediately.')

    // Names in the shipped seed carry hyphens; nothing escapes or truncates them.
    expect(removeConfirmQuestion('Oluwafunmilayo Ademola-Oyediran')).toContain('Remove Oluwafunmilayo Ademola-Oyediran?')
  })

  it("pins §9's note, verbatim (T7.3)", () => {
    // MOVED out of MembersTable.tsx:91, where no spec could see it, because MEMB-01-07
    // needs the same sentence at three more places. A straight apostrophe, as the story
    // writes it — a smart quote here renders identically and fails a text assertion.
    expect(PROTECTED_ADMIN_NOTE).toBe("You're the only admin. Promote someone else first.")
    expect(PROTECTED_ADMIN_NOTE).toContain("You're")
    expect(PROTECTED_ADMIN_NOTE).not.toContain('’')
    // §8's amber note (SUSPENDED_STEPS_NOTE) moved to roles.ts, reworded "role" not
    // "position" — no longer this module's to pin.
  })
})

describe('needsClientPick — §7\'s zero-selected rule, now read by two components (T7.8)', () => {
  it("treats 'all' and [] as the discriminated union they are (T7.8)", () => {
    // It moved into this module when MEMB-01-07 extracted `ClientAccessPicker`: the picker
    // shows NO_CLIENTS_NOTE and the modal disables `Send invites` off ONE rule, and the
    // modal can no longer see the picker's internal scope.
    expect(needsClientPick('all')).toBe(false)
    expect(needsClientPick([])).toBe(true)
    expect(needsClientPick([0])).toBe(false)
    expect(needsClientPick([0, 1, 3])).toBe(false)

    // `[]` is representable and meaningful — it is simply not sendable.
    const scoped = [0, 1, 3]
    expect(needsClientPick(scoped)).toBe(false)
    expect(needsClientPick(scoped.slice(0, 1))).toBe(false)
    expect(needsClientPick(scoped.slice(0, 0))).toBe(true)
  })
})

// APPR-00 Q1 — the approver set is {admin, reviewer} in BOTH modes, and firm mode had no
// spec of its own (T1.32 only ever ran in-house).
describe('APPR-00 Q1 — delegateCandidates admits the admin in firm mode too', () => {
  it('lists the active firm admin first, then the two active firm reviewers', () => {
    const candidates = delegateCandidates(SEED_FIRM_MEMBERS)
    expect(candidates).toEqual(['Chinedu Okafor', 'Musa Danjuma', 'Chiamaka Nwosu'])
    expect(candidates).toContain('Chinedu Okafor') // admin — INVERTED by APPR-00 Q1, was not.toContain
    expect(candidates).not.toContain('Halima Yusuf') // reviewer, suspended
    expect(candidates).not.toContain('Folake Adesina') // preparer
  })
})

// ============================================================================
// APPR-15-05 — the live member wire and projection
// ============================================================================
// Authored as Mode A RED specs, and that RED phase is over: listMembers,
// setMembershipStatus, toMember, memberInitials, emailLabel and membersViewState are
// implemented, MEMBER_UNBACKED ships populated, and filterMembers/classifyInvites both
// guard a null email with `(m.email ?? '').toLowerCase()`. Every spec below is green
// against the shipped implementations.

/** authedFetch.test.ts's / portfolio.test.ts's own helper, for the ApiError rethrow specs. */
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

const wire = (overrides: Partial<MembershipWire> = {}): MembershipWire => ({
  user_id: 'c0000000-0000-0000-0000-000000000003',
  role: 'reviewer',
  status: 'active',
  display_name: 'Folake Adesina',
  email: 'f.adesina@okafor.ng',
  ...overrides,
})

const SELF_SUBJECT = 'c0000000-0000-0000-0000-000000000001'

/** A Member row with a wire-null email, forced past the (still-required) `email: string` field. */
const nullEmailMember = (name: string): Member => ({ ...inhouseRow(name, 'active'), email: null as unknown as string })

describe('AC-1 — listMembers targets the memberships endpoint', () => {
  it('GETs <base>/api/tenancy/v1/memberships via the injected authedFetch, no token option of its own', async () => {
    const af = vi.fn().mockResolvedValue({ memberships: [wire()] }) as unknown as AuthedFetch
    const base = 'https://gw'

    await listMembers(af, base)

    expect(af).toHaveBeenCalledTimes(1)
    expect(af).toHaveBeenCalledWith(`${base}/api/tenancy/v1/memberships`)
  })

  it('rethrows a given ApiError unreshaped — the same instance', async () => {
    const boom = new ApiError('http', 'unauthorized', 401, { error: 'unauthorized' })
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch

    const caught = await captureRejection(() => listMembers(af, 'https://gw'))
    expect(caught).toBe(boom)
  })
})

describe('AC-1 — setMembershipStatus PATCHes memberships/<id>', () => {
  it('sends PATCH with body {status} to the member-scoped URL', async () => {
    const af = vi.fn().mockResolvedValue(wire({ status: 'suspended' })) as unknown as AuthedFetch
    const base = 'https://gw'

    await setMembershipStatus(af, base, 'u1', 'suspended')

    expect(af).toHaveBeenCalledTimes(1)
    expect(af).toHaveBeenCalledWith(`${base}/api/tenancy/v1/memberships/u1`, {
      method: 'PATCH',
      body: { status: 'suspended' },
    })
  })

  it('rethrows a 409 ApiError with status and body intact', async () => {
    const boom = new ApiError('http', 'suspended approver', 409, { error: 'suspended approver' })
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch

    const caught = await captureRejection(() => setMembershipStatus(af, 'https://gw', 'u1', 'suspended'))
    expect(caught).toBe(boom)
    expect((caught as ApiError).status).toBe(409)
    expect((caught as ApiError).body).toEqual({ error: 'suspended approver' })
  })
})

describe('AC-2 — toMember maps the wire row to a Member', () => {
  it('maps the five wire fields to {id, name, email, role, status}', () => {
    const w = wire({
      user_id: 'c0000000-0000-0000-0000-000000000005',
      display_name: 'Chiamaka Nwosu',
      email: 'c.nwosu@okafor.ng',
      role: 'reviewer',
      status: 'active',
    })
    const m = toMember(w, SELF_SUBJECT)
    expect(m.id).toBe(w.user_id)
    expect(m.name).toBe('Chiamaka Nwosu')
    expect(m.email).toBe('c.nwosu@okafor.ng')
    expect(m.role as string).toBe('reviewer')
    expect(m.status).toBe('active')
  })

  it('keeps role VERBATIM — reviewer stays reviewer, an unexpected Auditor is kept, not defaulted or lower-cased', () => {
    expect(toMember(wire({ role: 'reviewer' }), SELF_SUBJECT).role as string).toBe('reviewer')
    expect(toMember(wire({ role: 'Auditor' }), SELF_SUBJECT).role as string).toBe('Auditor')
  })

  it('falls back the name display_name -> email -> user_id', () => {
    expect(toMember(wire({ display_name: 'Folake Adesina', email: 'f.adesina@okafor.ng' }), SELF_SUBJECT).name).toBe(
      'Folake Adesina',
    )
    expect(toMember(wire({ display_name: null, email: 'f.adesina@okafor.ng' }), SELF_SUBJECT).name).toBe('f.adesina@okafor.ng')
    expect(
      toMember(wire({ display_name: null, email: null, user_id: 'c0000000-0000-0000-0000-000000000009' }), SELF_SUBJECT).name,
    ).toBe('c0000000-0000-0000-0000-000000000009')
  })

  it('resolves isYou from the passed self subject', () => {
    expect(toMember(wire({ user_id: SELF_SUBJECT }), SELF_SUBJECT).isYou).toBe(true)
    expect(toMember(wire({ user_id: 'c0000000-0000-0000-0000-000000000099' }), SELF_SUBJECT).isYou).toBe(false)
  })
})

describe('AC-2/[APPR-10 trap] — delegateCandidates survives the live projection', () => {
  it('a projected active reviewer AND a projected active admin are both delegate candidates', () => {
    // Roster order, not role order: these rows are built reviewer-first, so the admin lands
    // SECOND here and first in the two seed blocks above.
    const rows = [
      wire({ user_id: 'c1', role: 'reviewer', status: 'active', display_name: 'Musa Danjuma' }),
      wire({ user_id: 'c2', role: 'admin', status: 'active', display_name: 'Chinedu Okafor' }),
    ].map((w) => toMember(w, SELF_SUBJECT))
    expect(delegateCandidates(rows)).toEqual(['Musa Danjuma', 'Chinedu Okafor'])
  })
})

describe('AC-3 — memberInitials composes initials()/initialsFrom(), no third variant', () => {
  it('takes initials from the display name when present', () => {
    expect(memberInitials('Folake Adesina', 'f.adesina@x.ng', 'u1')).toBe('FA')
  })

  it('falls back to initialsFrom(email) when there is no display name', () => {
    expect(memberInitials(null, 'f.adesina@x.ng', 'u1')).toBe('FA')
  })

  it('falls back to the first two characters of the subject, upper-cased, when both are absent', () => {
    expect(memberInitials(null, null, 'zzuser')).toBe('ZZ')
  })
})

describe('AC-5 — emailLabel', () => {
  it('renders a missing email as an em dash', () => {
    expect(emailLabel(nullEmailMember('Nomail Person'))).toBe('—')
  })

  it('renders a real address verbatim', () => {
    expect(emailLabel({ ...inhouseRow('Has Mail', 'active'), email: 'a@b.ng' })).toBe('a@b.ng')
  })
})

describe('AC-5 — filterMembers and classifyInvites tolerate a null email', () => {
  it('filterMembers tolerates a null email, both when the name matches and when nothing does', () => {
    const row = nullEmailMember('Nomail Person')
    // The `||` short-circuits on the name match before `email` is ever touched — this half
    // is already true today and is pinned as a fact, not a red.
    expect(names(filterMembers([row], 'nomail', 'all'))).toEqual(['Nomail Person'])
    // A query the name does NOT match forces evaluation of `m.email.toLowerCase()` — this
    // is the genuine red: today it throws instead of falling through to "no match".
    expect(() => filterMembers([row], 'zzz-no-match', 'all')).not.toThrow()
    expect(filterMembers([row], 'zzz-no-match', 'all')).toEqual([])
  })

  it('classifyInvites treats a null-email row as no match, and does not throw', () => {
    const row = nullEmailMember('Nomail Person')
    expect(() => classifyInvites([row], ['fresh@x.ng'])).not.toThrow()
    expect(classifyInvites([row], ['fresh@x.ng'])).toEqual(['ok'])
  })
})

describe('AC-7 — MEMBER_UNBACKED', () => {
  it('supplies one distinct, non-empty sentence per unbacked control', () => {
    const keys = ['invite', 'remove', 'role', 'department', 'clientAccess'] as const
    for (const k of keys) expect(MEMBER_UNBACKED[k]).toBeTruthy()
    const values = keys.map((k) => MEMBER_UNBACKED[k])
    expect(new Set(values).size).toBe(keys.length)
  })
})

describe('AC-8 — membersViewState never turns an error into empty', () => {
  it('null base -> idle', () => {
    expect(membersViewState(null, 'ready')).toBe('idle')
  })

  it('an error status passes through as error, never empty', () => {
    expect(membersViewState('https://gw', 'error')).toBe('error')
  })

  it('an empty status passes through as empty', () => {
    expect(membersViewState('https://gw', 'empty')).toBe('empty')
  })
})

// Expected to RED against the still-present exports — this subtask does not delete
// them ([two-step-type-narrowing]; App.tsx:299 still calls seedMembers at this commit).
describe('AC-9 — the member seed is gone from the app path', () => {
  it('module exports carry no SEED_FIRM_MEMBERS, SEED_INHOUSE_MEMBERS or seedMembers', () => {
    expect('SEED_FIRM_MEMBERS' in membersModule).toBe(false)
    expect('SEED_INHOUSE_MEMBERS' in membersModule).toBe(false)
    expect('seedMembers' in membersModule).toBe(false)
  })
})

// ============================================================================
// QA (Stage 4) — adversarial coverage over the live wire and projection
// ============================================================================

describe('AC-2 — toMember keeps status VERBATIM too, and an unfamiliar one is simply inert', () => {
  it('an unrecognised status is kept as-is, not defaulted or dropped', () => {
    expect(toMember(wire({ status: 'pending_kyc' }), SELF_SUBJECT).status as string).toBe('pending_kyc')
  })

  it('an unrecognised status matches no status-gated derivation rather than crashing one', () => {
    const m = toMember(wire({ status: 'pending_kyc', role: 'admin' }), SELF_SUBJECT)
    expect(() => activeAdmins([m])).not.toThrow()
    expect(activeAdmins([m])).toEqual([]) // neither 'active' nor anything else it compares against
  })
})

describe('AC-3 — memberInitials, adversarial shapes', () => {
  it('an empty-string display name is falsy, so it falls through to email like a missing one', () => {
    expect(memberInitials('', 'f.adesina@okafor.ng', 'u1')).toBe('FA')
  })

  it('a punctuation-only display name strips to nothing — unreachable from the seed, deliberately unguarded', () => {
    expect(memberInitials('---', 'f.adesina@okafor.ng', 'u1')).toBe('')
  })

  it('a single-word display name yields one letter, not a two-word pair', () => {
    expect(memberInitials('Zainab', null, 'u1')).toBe('Z')
  })

  it('diacritics do not crash or mojibake — the underlying initials() strips non-ASCII letters rather than transliterating them', () => {
    expect(() => memberInitials('Adébáyọ̀ Ògúnlẹ́sì', null, 'u1')).not.toThrow()
    // 'Ò' is stripped along with the other accented characters, so the second word's leading
    // letter is its first PLAIN one ('g'), not the accented 'Ò' a person would read as the name's
    // initial — recorded fact, not a regression this subtask introduced (initials() is customers.ts's).
    expect(memberInitials('Adébáyọ̀ Ògúnlẹ́sì', null, 'u1')).toBe('AG')
  })
})

describe('AC-5 — emailLabel does not conflate an empty string with a null email', () => {
  it('null renders the em dash; an empty string renders as itself — `??` only catches null/undefined', () => {
    const nullRow = nullEmailMember('Nomail Person')
    const emptyRow = { ...inhouseRow('Blank Mail', 'active'), email: '' }
    expect(emailLabel(nullRow)).toBe('—')
    expect(emailLabel(emptyRow)).toBe('')
    expect(emailLabel(emptyRow)).not.toBe(emailLabel(nullRow))
  })
})

describe('AC-8 — membersViewState over every AsyncStatus value, not just the three the AC names', () => {
  const ALL_STATUSES: AsyncStatus[] = ['idle', 'loading', 'error', 'empty', 'ready']

  it('a non-null base passes every status through unchanged, including loading and idle itself', () => {
    for (const s of ALL_STATUSES) expect(membersViewState('https://gw', s)).toBe(s)
  })

  it('a null base collapses every status to idle, including one that is already idle', () => {
    for (const s of ALL_STATUSES) expect(membersViewState(null, s)).toBe('idle')
  })
})

describe('activeAdmins/isProtectedAdmin — a short LIVE-shaped roster (4 rows) with a suspended admin', () => {
  const live = [
    wire({ user_id: 'c1', role: 'admin', status: 'active', display_name: 'Sole Admin' }),
    wire({ user_id: 'c2', role: 'admin', status: 'suspended', display_name: 'Suspended Admin' }),
    wire({ user_id: 'c3', role: 'reviewer', status: 'active', display_name: 'Reviewer One' }),
    wire({ user_id: 'c4', role: 'preparer', status: 'active', display_name: 'Preparer One' }),
  ].map((w) => toMember(w, SELF_SUBJECT))

  it('a suspended admin is excluded from activeAdmins', () => {
    expect(activeAdmins(live).map((m) => m.name)).toEqual(['Sole Admin'])
  })

  it('the sole active admin is still protected with a suspended second admin present', () => {
    const solo = live.find((m) => m.name === 'Sole Admin')!
    expect(isProtectedAdmin(live, solo)).toBe(true)
  })

  it('the suspended admin is never protected — the guard reads status, not role alone', () => {
    const suspended = live.find((m) => m.name === 'Suspended Admin')!
    expect(isProtectedAdmin(live, suspended)).toBe(false)
  })

  it('a non-admin over the same roster is never protected', () => {
    const reviewer = live.find((m) => m.name === 'Reviewer One')!
    expect(isProtectedAdmin(live, reviewer)).toBe(false)
  })
})

describe('QA APPR-00 Q1 — delegateCandidates, adversarial shapes', () => {
  // Reviewer BEFORE admin, so this roster's ordering disagrees with both seed blocks.
  const live = [
    wire({ user_id: 'c1', role: 'preparer', status: 'active', display_name: 'Preparer One' }),
    wire({ user_id: 'c2', role: 'reviewer', status: 'active', display_name: 'Reviewer One' }),
    wire({ user_id: 'c3', role: 'admin', status: 'active', display_name: 'Admin One' }),
    wire({ user_id: 'c4', role: 'reviewer', status: 'suspended', display_name: 'Reviewer Two' }),
  ].map((w) => toMember(w, SELF_SUBJECT))

  it('an off-union role is refused — the widened set is a closed pair, not a not-preparer test', () => {
    // `toMember` keeps an unfamiliar role verbatim (see the VERBATIM spec above), so a role
    // outside `AccessRole` really does reach this predicate.
    const odd = toMember(wire({ user_id: 'c5', role: 'Auditor', status: 'active', display_name: 'Auditor One' }), SELF_SUBJECT)
    const rows = [...live, odd]
    expect(rows.map((m) => m.role as string), 'the projection dropped the odd role, so the exclusion below is vacuous').toContain('Auditor')
    expect(delegateCandidates(rows)).not.toContain('Auditor One')
    expect(delegateCandidates(rows)).toEqual(['Reviewer One', 'Admin One'])
  })

  it('order tracks the roster, not the role — reversing the rows reverses the candidates', () => {
    const forward = delegateCandidates(live)
    expect(forward.length, 'fewer than two candidates, so the ordering assertions are vacuous').toBeGreaterThan(1)
    expect(forward).toEqual(['Reviewer One', 'Admin One'])
    expect(delegateCandidates([...live].reverse())).toEqual(['Admin One', 'Reviewer One'])
  })

  it('an empty roster and an all-ineligible roster both give an empty list, not a throw', () => {
    expect(() => delegateCandidates([])).not.toThrow()
    expect(delegateCandidates([])).toEqual([])
    const ineligible = live.filter((m) => !delegateCandidates(live).includes(m.name))
    expect(ineligible.length, 'nothing ineligible in the fixture, so the empty result below is vacuous').toBeGreaterThan(0)
    expect(delegateCandidates(ineligible)).toEqual([])
  })
})

describe('membersSurface — the one derivation both tabs branch on, over every AsyncStatus', () => {
  it('loading stays loading', () => {
    expect(membersSurface('loading')).toBe('loading')
  })

  it('idle (no gateway configured) reads as empty, the roster-of-nothing surface', () => {
    expect(membersSurface('idle')).toBe('empty')
  })

  it('empty (a landed, zero-row roster) reads as empty', () => {
    expect(membersSurface('empty')).toBe('empty')
  })

  it('ready (a landed, non-empty roster) reads as roster', () => {
    expect(membersSurface('ready')).toBe('roster')
  })

  // The whole point of AC1: a fetch that FAILED must never render as an empty SUCCESS. If
  // this ever regressed to 'empty', both tabs would paint "just you" / "no roles yet" over
  // a roster that never loaded, which is indistinguishable on screen from the truth.
  it('error reads as error, never as empty or roster', () => {
    expect(membersSurface('error')).toBe('error')
    expect(membersSurface('error')).not.toBe('empty')
    expect(membersSurface('error')).not.toBe('roster')
  })

  it('every status maps to exactly one surface, and only ready reaches roster', () => {
    const ALL: AsyncStatus[] = ['idle', 'loading', 'error', 'empty', 'ready']
    const bySurface = ALL.map((s) => [s, membersSurface(s)] as const)
    expect(bySurface).toEqual([
      ['idle', 'empty'],
      ['loading', 'loading'],
      ['error', 'error'],
      ['empty', 'empty'],
      ['ready', 'roster'],
    ])
  })
})
