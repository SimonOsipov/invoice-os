// Members (Settings › Members) — types, constants, seed and read-only derivations
// (MEMB-01-01).
//
// Mock-only, and permanently so for now: there is no members endpoint at all. The shipped
// `memberships` table has no status column, no inviter, no expiry and no `users` row, and
// nothing anywhere holds a department or an approval position — so Suspend, Remove and
// Axis B have no backend mechanism to call (MEMB-01 §Decisions). Everything here is seed
// data plus pure functions of their arguments, shaped so that swapping the seed for a
// fetch changes no component.
//
// Dependency direction is ONE-WAY: members.ts → workflows.ts. This module takes `RoleKey`,
// `WorkflowMode`, `Policy` and `WF_ROLES` from ./workflows; workflows.ts never learns
// members exist — `stepsFor` receives the policy list as an argument rather than importing
// it. members.ts imports NOTHING from types.ts: types.ts already type-imports from
// ./lib/workflows, and MEMB-01-03 makes types.ts import THIS module, so a back-edge here
// would be a real cycle rather than a hypothetical one.
//
// Reducers (MEMB-01-02) are immutable, and `seedMembers` deep-clones. `SEED_*` are module
// constants that are readonly at the type level only — nothing freezes them at runtime —
// so a mutating reducer or a shallow clone would silently alias the seed across a mode
// switch. `clientAccess` is the only nested value in the whole module and is exactly where
// that bites: it is the same bug class lib/workflows.ts:15-18 records the Workflows port
// fixing.

import { CFG } from '../data'
import { APP_PERSONAS } from '../auth'
import { WF_ROLES } from './workflows'
import type { Policy, RoleKey, WorkflowMode } from './workflows'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Axis A — what a person can DO. Matches the shipped backend enum, three values only. */
export type AccessRole = 'admin' | 'preparer' | 'reviewer'

export type MemberStatus = 'active' | 'invited' | 'suspended'

/** In-house grouping primitive. There is no separate Teams/Groups entity. */
export type Department = 'Finance' | 'Tax & Compliance' | 'Accounts Payable' | 'Executive' | 'Procurement'

/**
 * One flat row per person, with the two mode-specific column sets declared optional
 * rather than split into a discriminated union — a firm row simply carries no
 * `department`/`position` and an in-house row carries no `clientAccess`.
 */
export type Member = {
  id: string
  name: string
  initials: string
  email: string
  role: AccessRole
  status: MemberStatus
  /** A human string ('2 hours ago'), not a date. `null` while invited. */
  lastActive: string | null
  /** `null` while invited. */
  joined: string | null
  invitedBy: string
  isYou: boolean
  /** FIRM mode only — `'all'`, or a subset of CLIENT_ROSTER ids (CFG indices). */
  clientAccess?: 'all' | number[]
  /** IN-HOUSE mode only. */
  department?: Department
  /** IN-HOUSE mode only — Axis B. `null` when the member holds no approval position. */
  position?: RoleKey | null
}

/** Keyed per workspace mode, mirroring `PolicyStore` — NOT per client. */
export type MemberStore = Record<WorkflowMode, Member[]>

/** `stepsFor`'s result. Policies with a zero count are omitted from `policies`. */
export type PolicySteps = { total: number; policies: { name: string; count: number }[] }

/**
 * How an approval position resolves to a person. Exported load-bearingly: MEMB-01-08 types
 * `WorkflowCanvas`/`WorkflowInspector`'s new `resolve?: (position: RoleKey) =>
 * PositionResolution` prop with it. Those components are barred from *value*-importing this
 * module, not from `import type` — the same erased-at-compile idiom types.ts:5-6 records.
 */
export type PositionResolution =
  | { kind: 'none' }
  | { kind: 'blocked'; primary: string; extra: number }
  | { kind: 'ok'; primary: string; extra: number }

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Axis A, with the descriptions rendered verbatim in the drawer and the invite modal. */
export const ACCESS_ROLES: readonly { id: AccessRole; label: string; description: string }[] = [
  { id: 'admin', label: 'Admin', description: 'Full access. Manages members, settings, connectors and certificates.' },
  { id: 'preparer', label: 'Preparer', description: 'Creates, imports and validates invoices. Cannot approve or transmit.' },
  {
    id: 'reviewer',
    label: 'Reviewer',
    description: 'Reviews and signs off on invoices in approval steps. Cannot manage members or settings.',
  },
]

export const DEPARTMENTS: readonly Department[] = ['Finance', 'Tax & Compliance', 'Accounts Payable', 'Executive', 'Procurement']

/**
 * The "What can each role do?" expander — eight capability rows × the three roles. Labels
 * are §6's copy, kept lowercase exactly as the story writes them; casing at render time is
 * MEMB-01-05's display call, not a fact to bake in here.
 */
export const CAPABILITY_ROWS: readonly { label: string; admin: boolean; preparer: boolean; reviewer: boolean }[] = [
  { label: 'create and edit invoices', admin: true, preparer: true, reviewer: true },
  { label: 'import from file or ERP', admin: true, preparer: true, reviewer: true },
  { label: 'run validation', admin: true, preparer: true, reviewer: true },
  { label: 'approve in approval steps', admin: true, preparer: false, reviewer: true },
  { label: 'transmit to FIRS/MBS', admin: true, preparer: false, reviewer: true },
  { label: 'invite and manage members', admin: true, preparer: false, reviewer: false },
  { label: 'manage ERP connectors', admin: true, preparer: false, reviewer: false },
  { label: 'manage signing certificates', admin: true, preparer: false, reviewer: false },
]

/**
 * The static client roster `clientAccess` indexes. Ids ARE the CFG indices (../data) —
 * deliberately the static mock roster and not the live portfolio entity list, whose ids are
 * UUID strings a `number[]` cannot address (Decision `[client-roster-is-static]`).
 */
export const CLIENT_ROSTER: readonly { id: number; name: string }[] = CFG.map((c, i) => ({ id: i, name: c.name }))

// ---------------------------------------------------------------------------
// Seed
// ---------------------------------------------------------------------------
// Only the two `isYou` rows take name/initials/email from APP_PERSONAS, so the row flagged
// YOU can never drift from the name the sidebar renders two inches away. Every other row
// carries hand-authored initials. `invitedBy` is an em dash on those two rows: they are the
// founding admins and nobody invited them.

export const SEED_FIRM_MEMBERS: readonly Member[] = [
  {
    id: 'mf1',
    name: APP_PERSONAS.firm.name,
    initials: APP_PERSONAS.firm.initials,
    email: APP_PERSONAS.firm.email,
    role: 'admin',
    status: 'active',
    lastActive: 'Just now',
    joined: '4 Feb 2024',
    invitedBy: '—',
    isYou: true,
    clientAccess: 'all',
  },
  {
    id: 'mf2',
    name: 'Folake Adesina',
    initials: 'FA',
    email: 'f.adesina@okafor.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '2 hours ago',
    joined: '18 Mar 2024',
    invitedBy: 'Chinedu Okafor',
    isYou: false,
    clientAccess: [0, 1, 3],
  },
  {
    id: 'mf3',
    name: 'Musa Danjuma',
    initials: 'MD',
    email: 'm.danjuma@okafor.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: 'Yesterday',
    joined: '6 May 2024',
    invitedBy: 'Chinedu Okafor',
    isYou: false,
    clientAccess: [2, 5],
  },
  {
    id: 'mf4',
    name: 'Chiamaka Nwosu',
    initials: 'CN',
    email: 'c.nwosu@okafor.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: '3 days ago',
    joined: '11 Sep 2024',
    invitedBy: 'Chinedu Okafor',
    isYou: false,
    clientAccess: 'all',
  },
  {
    // §10.9 — the deliberately long name + email row, guarding the column widths.
    id: 'mf5',
    name: 'Oluwaseyifunmi Adebanjo-Ogunleye',
    initials: 'OA',
    email: 'o.adebanjo-ogunleye@okaforandpartners.com.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '5 hours ago',
    joined: '2 Dec 2024',
    invitedBy: 'Chinedu Okafor',
    isYou: false,
    clientAccess: 'all',
  },
  {
    id: 'mf6',
    name: 'Bature Suleiman',
    initials: 'BS',
    email: 'b.suleiman@okafor.ng',
    role: 'preparer',
    status: 'invited',
    lastActive: null,
    joined: null,
    invitedBy: 'Chinedu Okafor',
    isYou: false,
    clientAccess: [0],
  },
  {
    id: 'mf7',
    name: 'Halima Yusuf',
    initials: 'HY',
    email: 'h.yusuf@okafor.ng',
    role: 'reviewer',
    status: 'suspended',
    lastActive: '1 month ago',
    joined: '22 Jan 2024',
    invitedBy: 'Chinedu Okafor',
    isYou: false,
    clientAccess: 'all',
  },
]

export const SEED_INHOUSE_MEMBERS: readonly Member[] = [
  {
    id: 'mh1',
    name: APP_PERSONAS.inhouse.name,
    initials: APP_PERSONAS.inhouse.initials,
    email: APP_PERSONAS.inhouse.email,
    role: 'admin',
    status: 'active',
    lastActive: 'Just now',
    joined: '9 Jan 2024',
    invitedBy: '—',
    isYou: true,
    department: 'Finance',
    // Both "you"/admin AND a fin_dir holder: the axes are orthogonal, and §6's matrix gives
    // Admin every capability including approve (Decision `[ngozi-holds-fin-dir]`).
    position: 'fin_dir',
  },
  {
    id: 'mh2',
    name: 'Yetunde Fashola',
    initials: 'YF',
    email: 'y.fashola@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: '1 hour ago',
    joined: '20 Feb 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Finance',
    // The second fin_dir holder — what makes §11.1's "+1" reachable.
    position: 'fin_dir',
  },
  {
    id: 'mh3',
    name: 'Emeka Uzowulu',
    initials: 'EU',
    email: 'e.uzowulu@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: '3 hours ago',
    joined: '20 Feb 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Procurement',
    position: 'line_mgr',
  },
  {
    id: 'mh4',
    name: 'Tunde Adeyemi',
    initials: 'TA',
    email: 't.adeyemi@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: 'Yesterday',
    joined: '4 Mar 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Finance',
    position: 'controller',
  },
  {
    id: 'mh5',
    name: 'Ibrahim Bello',
    initials: 'IB',
    email: 'i.bello@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: '2 days ago',
    joined: '4 Mar 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Tax & Compliance',
    position: 'compliance',
  },
  {
    // §2's headline frame: the only cfo holder, suspended, so both cfo approval steps block.
    id: 'mh6',
    name: 'Adebayo Ogunlesi',
    initials: 'AO',
    email: 'a.ogunlesi@honeywell.ng',
    role: 'reviewer',
    status: 'suspended',
    lastActive: '3 weeks ago',
    joined: '9 Jan 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Executive',
    position: 'cfo',
  },
  {
    id: 'mh7',
    name: 'Zainab Lawal',
    initials: 'ZL',
    email: 'z.lawal@honeywell.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '20 minutes ago',
    joined: '15 Apr 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Accounts Payable',
    position: 'preparer',
  },
  {
    id: 'mh8',
    name: 'Chidi Anyanwu',
    initials: 'CA',
    email: 'c.anyanwu@honeywell.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '4 hours ago',
    joined: '15 Apr 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Accounts Payable',
    position: null,
  },
  {
    id: 'mh9',
    name: 'Aisha Mohammed',
    initials: 'AM',
    email: 'a.mohammed@honeywell.ng',
    role: 'preparer',
    status: 'active',
    lastActive: 'Yesterday',
    joined: '2 Jun 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Accounts Payable',
    position: null,
  },
  {
    id: 'mh10',
    name: 'Segun Oyelaran',
    initials: 'SO',
    email: 's.oyelaran@honeywell.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '2 days ago',
    joined: '2 Jun 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Procurement',
    position: null,
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
    lastActive: '6 hours ago',
    joined: '19 Jul 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Tax & Compliance',
    position: null,
  },
  {
    id: 'mh12',
    name: 'Kelechi Obi',
    initials: 'KO',
    email: 'k.obi@honeywell.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '30 minutes ago',
    joined: '19 Jul 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Finance',
    position: null,
  },
  {
    id: 'mh13',
    name: 'Hauwa Abubakar',
    initials: 'HA',
    email: 'h.abubakar@honeywell.ng',
    role: 'reviewer',
    status: 'active',
    lastActive: '5 hours ago',
    joined: '3 Sep 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Executive',
    position: null,
  },
  {
    id: 'mh14',
    name: 'Olumide Bakare',
    initials: 'OB',
    email: 'o.bakare@honeywell.ng',
    role: 'preparer',
    status: 'active',
    lastActive: '3 days ago',
    joined: '3 Sep 2024',
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Procurement',
    position: null,
  },
  {
    id: 'mh15',
    name: 'Nneka Chukwu',
    initials: 'NC',
    email: 'n.chukwu@honeywell.ng',
    role: 'preparer',
    status: 'invited',
    lastActive: null,
    joined: null,
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Accounts Payable',
    position: null,
  },
  {
    id: 'mh16',
    name: 'Sadiq Ibrahim',
    initials: 'SI',
    email: 's.ibrahim@honeywell.ng',
    role: 'reviewer',
    status: 'invited',
    lastActive: null,
    joined: null,
    invitedBy: 'Ngozi Balogun',
    isYou: false,
    department: 'Finance',
    position: null,
  },
]

/** Deep clone per call, mirroring `seedPolicies` (workflows.ts:206-216). */
export function seedMembers(): MemberStore {
  return { firm: cloneMembers(SEED_FIRM_MEMBERS), inhouse: cloneMembers(SEED_INHOUSE_MEMBERS) }
}

/**
 * `clientAccess` is the only nested value a `Member` carries, so it is the only thing a
 * `{...m}` spread would leave aliased to the seed — mirroring `cloneNode`'s own
 * `then`/`else` copy (workflows.ts:214-216).
 */
function cloneMembers(list: readonly Member[]): Member[] {
  return list.map((m) => (Array.isArray(m.clientAccess) ? { ...m, clientAccess: m.clientAccess.slice() } : { ...m }))
}

// ---------------------------------------------------------------------------
// Derivations — all pure, all taking their inputs as arguments
// ---------------------------------------------------------------------------

export function holders(list: readonly Member[], position: RoleKey): Member[] {
  return list.filter((m) => m.position === position)
}

/**
 * Status only — deliberately NOT filtered by access role. §11.3's delegate picker is the one
 * place the Reviewer role is a hard gate (`delegateCandidates`); a position holder who is an
 * admin still resolves, per Decision `[delegates-are-reviewers-only]`.
 */
export function activeHolders(list: readonly Member[], position: RoleKey): Member[] {
  return holders(list, position).filter((m) => m.status === 'active')
}

/**
 * Every `WF_ROLES` position with no holder at all, in `WF_ROLES` order. Counts ALL positions,
 * not only those a policy uses — `fin_mgr` is used by zero in-house policies and §6 still
 * says "2 approval positions have nobody assigned" (Decision `[unassigned-counts-all]`).
 */
export function unassignedPositions(list: readonly Member[]): RoleKey[] {
  return WF_ROLES.filter((r) => holders(list, r.key).length === 0).map((r) => r.key)
}

/** Positions that HAVE holders but none active — a policy step that would block. */
export function blockedPositions(list: readonly Member[]): RoleKey[] {
  return WF_ROLES.filter((r) => holders(list, r.key).length > 0 && activeHolders(list, r.key).length === 0).map((r) => r.key)
}

/**
 * Root lane + both `then`/`else` lanes; drafts included, zero-count policies omitted.
 *
 * One level of descent is enough and always will be: `BranchNode` cannot hold a
 * `ConditionNode` (workflows.ts:75-82), so a nested condition is unrepresentable rather than
 * merely rejected — the tree is provably exactly two deep. Drafts are counted because a draft
 * still names the person: excluding `polH2` would make Adebayo's count 1 and contradict
 * §10.4's "Named in 2 approval steps" (Decision `[stepsfor-includes-drafts]`).
 */
export function stepsFor(policies: readonly Policy[], position: RoleKey): PolicySteps {
  const named: { name: string; count: number }[] = []
  let total = 0
  for (const p of policies) {
    let count = 0
    for (const n of p.nodes) {
      if (n.type === 'approval') {
        if (n.role === position) count++
      } else if (n.type === 'condition') {
        for (const child of [...n.then, ...n.else]) {
          if (child.type === 'approval' && child.role === position) count++
        }
      }
    }
    if (count > 0) {
      named.push({ name: p.name, count })
      total += count
    }
  }
  return { total, policies: named }
}

export function resolvePosition(list: readonly Member[], position: RoleKey): PositionResolution {
  const all = holders(list, position)
  if (all.length === 0) return { kind: 'none' }
  const active = all.filter((m) => m.status === 'active')
  const extra = all.length - 1
  if (active.length === 0) return { kind: 'blocked', primary: all[0].name, extra }
  return { kind: 'ok', primary: active[0].name, extra }
}

/** "Ngozi Balogun +1" — the "+n" counts the OTHER holders, active or not. */
function withExtra(primary: string, extra: number): string {
  return extra > 0 ? `${primary} +${extra}` : primary
}

/**
 * Never sees the SLA — the canvas joins this with `slaText(n.sla)` on ` · ` (§15.5). The
 * "+n" suffix applies to `blocked` as well as `ok`: the rule is per-line, not per-variant.
 * Unreachable in the seed, where the only blocked position has a single holder.
 */
export function canvasApprovalLine(res: PositionResolution): string {
  if (res.kind === 'none') return 'Nobody assigned'
  if (res.kind === 'blocked') return `${withExtra(res.primary, res.extra)} — suspended`
  return withExtra(res.primary, res.extra)
}

/** The inspector's read-only line omits "+n" deliberately — §11.2 spells it out that way. */
export function inspectorApprovalLine(res: PositionResolution): string {
  if (res.kind === 'none') return 'Nobody assigned — assign in Settings › Members'
  if (res.kind === 'blocked') return `Currently: ${res.primary} — suspended — this step will block`
  return `Currently: ${res.primary}`
}

/** Filtered in `DEPARTMENTS` order, NOT in the order the member list happens to reach them. */
export function departmentsInUse(list: readonly Member[]): Department[] {
  return DEPARTMENTS.filter((d) => list.some((m) => m.department === d))
}

/**
 * Departments in use, then the standing committees and `Preparer` — which here means
 * "whoever raised this document", a relationship rather than a position. A stored value the
 * list does not otherwise carry (`polH1`'s legacy `'Tax Team'`) is appended LAST: it stays
 * selectable without being promoted (Decision `[notify-target-order]`).
 */
export function inhouseNotifyTargets(list: readonly Member[], current: string): string[] {
  const out: string[] = [...departmentsInUse(list), 'Audit Committee', 'Board', 'Preparer']
  if (current && !out.includes(current)) out.push(current)
  return out
}

/** Active members whose access role is `reviewer` — admins excluded, per §11.3. */
export function delegateCandidates(list: readonly Member[]): string[] {
  return list.filter((m) => m.status === 'active' && m.role === 'reviewer').map((m) => m.name)
}

export function clientAccessLabel(access: 'all' | readonly number[]): string {
  if (access === 'all') return 'All clients'
  if (access.length === 0) return 'No clients'
  return `${access.length} ${access.length === 1 ? 'client' : 'clients'}`
}

/** Always in CLIENT_ROSTER (i.e. CFG) order, whatever order the stored ids are in. */
export function clientAccessNames(access: 'all' | readonly number[]): string[] {
  if (access === 'all') return CLIENT_ROSTER.map((c) => c.name)
  return CLIENT_ROSTER.filter((c) => access.includes(c.id)).map((c) => c.name)
}

/**
 * Invited rows read as the literal invite expiry (§10.1) — there is no per-member expiry
 * field and no clock behind one (Decision `[invite-expiry-literal]`).
 */
export function lastActiveLabel(member: Member): string {
  if (member.status === 'invited') return 'Expires in 6 days'
  return member.lastActive ?? '—'
}

/** The §9 last-admin lock reads this: at length 1, that admin cannot be demoted or suspended. */
export function activeAdmins(list: readonly Member[]): Member[] {
  return list.filter((m) => m.role === 'admin' && m.status === 'active')
}
