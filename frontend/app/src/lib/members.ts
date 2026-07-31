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
 * How an approval position resolves to a person. Its readers are the two copy helpers below
 * and `WorkflowBuilder` — the one Workflows component that imports this module at all.
 *
 * It is deliberately NOT the `resolve?:` prop type MEMB-01-08 was sketched with. The canvas
 * and the inspector word the same resolution differently, so a prop carrying the raw variant
 * would force one of them to re-derive copy this module already owns; and the AMBER tone
 * (`kind !== 'ok'`) has to travel WITH the string. They therefore take an already-rendered
 * `{ line, amber }` (WorkflowParts' `ResolvedLine`) and this type never leaves here.
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

/**
 * The `ACCESS_ROLES` display label for one role id — the Access role column, the drawer and
 * the invite modal all want it and none of them may re-case `m.role`, which happens to
 * produce the right string today and diverges silently the first time a label changes.
 *
 * The fallback exists for the same reason `roleOf`'s does (workflows.ts:39-41): a persisted
 * id this build does not know must render as SOMETHING rather than crash the row. It is a
 * branch no screenshot can reach, which is exactly why it is spec'd here and not inlined.
 */
export function accessRoleLabel(role: AccessRole): string {
  return ACCESS_ROLES.find((r) => r.id === role)?.label ?? role
}

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
 * §6's footnote under the capability matrix, and §6's copy for the firm-only `Client users`
 * placeholder. Both are rendered verbatim by MemberRoleMatrix — MEMB-01-05's AC#1 says so
 * for the footnote and AC#3 for the card — and neither is a derivation of anything.
 *
 * They live here, beside the eight labels QA18 already pins, for the reason task-297's QA
 * moved three display strings out of components: `environment: node` cannot mount a
 * component, so a string authored inside one is a string no spec can hold (§15.8). The
 * footnote is the longest passage in the story, two sentences whose clauses depend on each
 * other, and a fluent paraphrase of it is exactly what a screenshot gate cannot catch.
 *
 * The expander's own header label stays in the component: §6 names it as the affordance,
 * not as prose, and AC#1's verbatim clause covers the rows and the footnote only.
 */
export const CAPABILITY_FOOTNOTE =
  'Approving also requires an approval position in the policy that routes the invoice. A person needs the Reviewer role to act on approval steps at all, and an approval position to decide which steps.'

export const CLIENT_USERS_COPY =
  'Give a contact at one of your clients read-only access, or approval rights on their own invoices.'

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
 * `then`/`else` copy (workflows.ts:214-216). Shared with the row-building reducers
 * (`setMemberRole`/`setMemberStatus`), which face exactly the same hazard.
 */
function copyMember(m: Member): Member {
  return Array.isArray(m.clientAccess) ? { ...m, clientAccess: m.clientAccess.slice() } : { ...m }
}

function cloneMembers(list: readonly Member[]): Member[] {
  return list.map((m) => copyMember(m))
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

/**
 * §6's copy for the in-house unassigned-positions notice, pluralised — the sentence only,
 * never the position titles, which the caller renders separately so it can weight them.
 *
 * §6 supplies the PLURAL verbatim ("2 approval positions have nobody assigned. Policies that
 * use them will block."); the singular is a reconstruction, written to match it phrase for
 * phrase (positions→position, have→has, them→it) and pinned here rather than in a component
 * so the invented half is an artifact a spec can hold. It is reachable: MEMB-01-07's drawer
 * can assign a position and drive the count to 1.
 *
 * Lives beside `clientAccessLabel` because it is the same kind of thing — a member-domain
 * count rendered as a sentence, and one `environment: node` can actually reach.
 */
export function unassignedNotice(count: number): string {
  return count === 1
    ? '1 approval position has nobody assigned. Policies that use it will block.'
    : `${count} approval positions have nobody assigned. Policies that use them will block.`
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

/**
 * §10.4's copy for the suspended-member row warning, pluralised over `stepsFor(...).total`.
 *
 * §10.4 supplies the PLURAL verbatim ("Named in 2 approval steps · those steps will block");
 * the singular is a reconstruction (steps→step, those→that) and is reachable the moment a
 * Workflows edit leaves one step naming the position. Same reason as `unassignedNotice`: the
 * invented half belongs where a spec can hold it, not inside a component vitest cannot mount.
 *
 * Call it only for a member who HOLDS a position — the guard is `m.position != null`, not a
 * mode check, since firm rows carry no position at all (T1.6).
 */
export function stepsWarning(total: number): string {
  return total === 1
    ? 'Named in 1 approval step · that step will block'
    : `Named in ${total} approval steps · those steps will block`
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

// ---------------------------------------------------------------------------
// MEMB-01-02 — invite pipeline, reducers, filter and the last-admin guard
// ---------------------------------------------------------------------------

/** One verdict per pasted address, in input order. */
export type InviteVerdict = 'ok' | 'member' | 'invited' | 'malformed'

/**
 * Discriminated on `mode`, so the fork is a compile-time fact and a firm invite's
 * `clientAccess` is REQUIRED rather than optional: `clientAccessLabel`/`clientAccessNames`
 * read `access.length` on anything that is not `'all'` and would throw on `undefined` in
 * MEMB-01-04's table and MEMB-01-07's drawer. `Member` itself stays flat (see its
 * docblock) — this is the argument, not the result.
 */
export type InviteOptions =
  | { mode: 'firm'; role: AccessRole; clientAccess: 'all' | number[] }
  | { mode: 'inhouse'; role: AccessRole; department: Department; position: RoleKey | null }

/**
 * Deliberately minimal (Decision `[email-validation-minimal]`): one `@`, a dot in the
 * domain, no whitespace on either side. Same shape as the pattern rules.ts:189 shows the
 * user as display copy, so what the invite box accepts cannot drift from what the rules
 * screen advertises.
 */
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/** Comma, semicolon and any whitespace — newlines included — all separate addresses. */
const ADDRESS_SEPARATORS = /[,;\s]+/

/** `. _ - +` all read as word breaks inside the local part. */
const LOCAL_PART_SEPARATORS = /[._+-]+/

/** Splits on comma / semicolon / whitespace / newline; trims, drops empties, de-dupes. */
export function parseEmailInput(raw: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  // `\s` inside the separator class does the trimming for us: whitespace can never survive
  // into a fragment, so the only cleanup left is dropping the empties that repeated or
  // leading/trailing separators leave behind.
  for (const address of raw.split(ADDRESS_SEPARATORS)) {
    if (!address) continue
    // De-duped case-insensitively but stored VERBATIM — the first spelling seen is the one
    // the chip renders, and the address is what will be mailed.
    const key = address.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(address)
  }
  return out
}

export function isValidEmail(value: string): boolean {
  return EMAIL_RE.test(value)
}

/** The local part, split into words. Shared by `nameFromEmail` and `initialsFrom`. */
function localTokens(email: string): string[] {
  return email.split('@')[0].split(LOCAL_PART_SEPARATORS).filter(Boolean)
}

/** §7's "name derived from the local part" — local part, split, capitalised, space-joined. */
export function nameFromEmail(email: string): string {
  return localTokens(email)
    .map((t) => t[0].toUpperCase() + t.slice(1).toLowerCase())
    .join(' ')
}

/**
 * MEMB-01-06's extra chip gate — a local part of only separators derives no name.
 *
 * `isValidEmail` is deliberately minimal, so `'...@x.ng'` and `'-@x.ng'` pass it, classify
 * `ok`, and mint a row whose `name` and `initials` are both empty: a blank Person cell and
 * an empty initials circle in the table. QA35 pins that as RECORDED, NOT ENDORSED, and
 * routes the product call here — the invite modal rejects the chip as `Not a valid email`.
 *
 * Deliberately a SIBLING rather than a fourth branch inside `classifyInvites`: QA35 pins
 * that function returning `['ok']` for exactly this address, and that spec stays literally
 * true — the classifier still says `ok`, the modal declines to mint. Widening `EMAIL_RE`
 * instead is barred twice over: T2.7/T2.8 pin its accept/reject sets, and Decision
 * `[email-validation-minimal]` keeps it matching the pattern rules.ts:189 advertises.
 *
 * Not a validator on its own — `'nope'` has a derivable name and is not an address. It is
 * only ever applied ON TOP of an `ok` verdict, so the composition is strictly stricter than
 * the classifier alone and can never let a bad row through.
 */
export function hasDerivableName(email: string): boolean {
  return localTokens(email).length > 0
}

/**
 * Takes an EMAIL, not a name — a deliberate FORK of `initials(name)` (customers.ts:60-69)
 * rather than a reuse, and emphatically not a silent third variant. Two reasons, both
 * structural: that helper reads a NAME and strips non-alpha, so an email fed to it returns
 * garbage; and Decision `[name-from-local-part]` wants a single-token local part to yield
 * its first TWO letters (`zainab` → `ZA`), where one-letter-per-token gives `Z`. Composing
 * it as `initials(nameFromEmail(x))` fails for that same second reason.
 */
export function initialsFrom(email: string): string {
  const tokens = localTokens(email)
  const source = tokens.length === 1 ? tokens[0] : tokens.map((t) => t[0]).join('')
  return source.slice(0, 2).toUpperCase()
}

export function classifyInvites(existing: readonly Member[], addresses: readonly string[]): InviteVerdict[] {
  return addresses.map((address): InviteVerdict => {
    if (!isValidEmail(address)) return 'malformed'
    // Lower-cased on BOTH sides: the stored spelling is whatever was typed when the row was
    // created, so a raw `===` would let the same person be invited twice.
    const match = existing.find((m) => m.email.toLowerCase() === address.toLowerCase())
    if (!match) return 'ok'
    return match.status === 'invited' ? 'invited' : 'member'
  })
}

// Fresh ids, mirroring `newNodeId`/`newPolicyId` (workflows.ts:287-296): a module-level
// counter started ABOVE the hand-written seed range (`mf1`…`mf7` / `mh1`…`mh16`) so a minted
// id can never collide with one, prefixed per mode so ids stay unique across a mode switch
// too (QA16). Deliberately NOT `Date.now()`: MEMB-01-06 maps `memberFromInvite` over a whole
// chip list in one tick, which would emit duplicate ids that `replaceMember`/`removeMember`
// would then apply to the wrong row.
let memberSeq = 100

function newMemberId(mode: WorkflowMode): string {
  return `${mode === 'firm' ? 'mf' : 'mh'}${memberSeq++}`
}

/** The inviter's name arrives as an ARGUMENT — this module never reaches for ctx. */
export function memberFromInvite(email: string, opts: InviteOptions, inviterName: string): Member {
  const base: Member = {
    id: newMemberId(opts.mode),
    name: nameFromEmail(email),
    initials: initialsFrom(email),
    email,
    role: opts.role,
    status: 'invited',
    lastActive: null,
    joined: null,
    // Always the real inviter, never the em dash: QA17 pins `invitedBy === '—'` IFF `isYou`,
    // and an invited row is never `isYou`.
    invitedBy: inviterName,
    isYou: false,
  }
  // A branch, not `department: opts.department` on one flat object — the latter leaves the
  // key PRESENT holding `undefined`, which ships an in-house column into the firm table.
  if (opts.mode === 'firm') {
    // Copied rather than aliased: MEMB-01-06 maps this over a chip list from a SINGLE `opts`,
    // which would otherwise leave every minted row sharing one array.
    return { ...base, clientAccess: Array.isArray(opts.clientAccess) ? opts.clientAccess.slice() : opts.clientAccess }
  }
  return { ...base, department: opts.department, position: opts.position }
}

// Reducers. All five allocate UNCONDITIONALLY (`map` / `filter` / spread), so a miss returns
// a new array holding the same values rather than the input reference — that is the
// `replacePolicy`/`removePolicy` form (workflows.ts:396-402), picked over rules.ts:253-268's
// `return rules as CustomRule[]` because the two house precedents disagree and only the
// always-allocate one satisfies §15.1. Every reducer keys off `id`, never email.

export function replaceMember(list: readonly Member[], next: Member): Member[] {
  return list.map((m) => (m.id === next.id ? next : m))
}

export function addMembers(list: readonly Member[], added: readonly Member[]): Member[] {
  return [...list, ...added]
}

export function removeMember(list: readonly Member[], id: string): Member[] {
  return list.filter((m) => m.id !== id)
}

// `setMemberRole`/`setMemberStatus` build their new row through `copyMember` because a bare
// `{...m}` would leave it sharing `clientAccess` with the row it replaced. `replaceMember`
// cannot do the same — its row is caller-built, so the caller owns that copy.

export function setMemberRole(list: readonly Member[], id: string, role: AccessRole): Member[] {
  return list.map((m) => (m.id === id ? { ...copyMember(m), role } : m))
}

export function setMemberStatus(list: readonly Member[], id: string, status: MemberStatus): Member[] {
  return list.map((m) => (m.id === id ? { ...copyMember(m), status } : m))
}

// The ONE definition of "a filter is active". MembersView reads it to choose between the
// roster-of-one empty state and the filtered-to-zero row, and `filterMembers` short-circuits
// on it, so the two cannot disagree about what an empty query is. They did once, and the two
// empty surfaces shadowed each other.
export function isFiltering(query: string, roleFilter: AccessRole | 'all'): boolean {
  return query.trim() !== '' || roleFilter !== 'all'
}

/** Name OR email, case-insensitive substring. `roleFilter: 'all'` disables the role predicate. */
export function filterMembers(list: readonly Member[], query: string, roleFilter: AccessRole | 'all'): Member[] {
  // Not a fast path — this is the pin. Neither predicate live means nobody is excluded, and
  // saying so via `isFiltering` is what stops a caller re-deriving that rule. Copies, per §15.1.
  if (!isFiltering(query, roleFilter)) return list.slice()
  const q = query.trim().toLowerCase()
  return list.filter((m) => {
    if (roleFilter !== 'all' && m.role !== roleFilter) return false
    if (!q) return true
    return m.name.toLowerCase().includes(q) || m.email.toLowerCase().includes(q)
  })
}

/** §9 — true iff `member` is an active admin and they are the only one. */
export function isProtectedAdmin(list: readonly Member[], member: Member): boolean {
  if (member.role !== 'admin' || member.status !== 'active') return false
  return activeAdmins(list).length === 1
}

// ---------------------------------------------------------------------------
// MEMB-01-06 — the invite modal's copy and its client-picker derivations
// ---------------------------------------------------------------------------
// Rendered by InviteMembersModal and nothing else, but living here rather than in it for
// §15.8's reason: vitest is `environment: node`, so a string authored inside a component is
// a string no spec can hold. Same argument that moved CAPABILITY_FOOTNOTE / CLIENT_USERS_COPY
// (T5.1) and the three display derivations (QA40-QA46) into this module.
//
// The modal's own two title strings stay in the component, matching MATRIX_HEADING's posture
// (MemberRoleMatrix.tsx:19-22): §7 names them as the surface's chrome, not as its content.

/**
 * §3 verbatim — the hint beneath the in-house Approval position select (AC#5).
 *
 * It is the sentence that keeps the two axes verbally distinct, which §3 calls load-bearing:
 * `reviewer` means "may act on approval steps at all", a position means "which steps". A
 * paraphrase reads as correct and quietly collapses the distinction, which is exactly the
 * failure a screenshot gate cannot catch.
 */
export const REVIEWER_HINT =
  'Only members with the Reviewer role can act on approval steps. The position decides which steps.'

/**
 * §7's three inline chip errors, keyed by the verdict that produces them. `ok` is excluded at
 * the TYPE level: a chip that classified `ok` has no error to render, and a `Record` over the
 * whole union would demand a fourth string that means nothing.
 *
 * `malformed` also carries the `hasDerivableName` downgrade. Both failures map to the same
 * string deliberately — to the person typing, "this address cannot become a member" is one
 * fact, and §7 supplies exactly three strings rather than a fourth for the rarer case.
 */
export const INVITE_ERROR: Record<Exclude<InviteVerdict, 'ok'>, string> = {
  member: 'Already a member',
  invited: 'Already invited',
  malformed: 'Not a valid email',
}

/**
 * The chip list's own de-duplication — §7's "an address already chipped does not chip twice",
 * and the duty NOTHING upstream performs. `parseEmailInput` de-dupes only WITHIN one paste
 * (T2.5) and `classifyInvites` has no batch memory at all (QA33), so the chip list is the only
 * place this state exists and this is the only function that enforces it.
 *
 * Keyed on `toLowerCase()`, keeping the FIRST spelling seen — the same key `parseEmailInput`
 * uses and the same case-insensitive comparison `classifyInvites` makes against stored rows. A
 * raw `===` would let `a@x.ng` and `A@x.ng` both chip and both classify `ok`, and the roster
 * would gain the same person twice.
 *
 * It lives here rather than in the modal against that subtask's own "everything else stays
 * component-side" rule, because that rule's stated reason — "its oracle is the deploy gate" —
 * is false for exactly this function: a lower-cased-key collision is invisible in a screenshot.
 * Same argument that pulled QA40-QA46's three derivations out of MEMB-01-04's components.
 */
export function mergeChips(current: readonly string[], added: readonly string[]): string[] {
  const seen = new Set(current.map((c) => c.toLowerCase()))
  const out = [...current]
  for (const address of added) {
    const key = address.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(address)
  }
  return out
}

/**
 * §7's zero-selected state. `Selected clients` with nothing ticked is an invite that grants
 * access to nothing, so `Send invites` is disabled and this says why — INVENTED COPY, since §7
 * specifies the behaviour and supplies no sentence for it.
 */
export const NO_CLIENTS_NOTE = 'Pick at least one client, or switch to All clients.'

/**
 * The client picker's filtered-to-zero line. Deliberately the roster search's sentence with one
 * noun changed (MembersView.tsx:141) — two search boxes on one tab that phrased "nothing
 * matched" differently would read as two different failures.
 */
export const NO_CLIENT_MATCH = 'No clients match this search.'

/**
 * The invite modal's client-picker search. Same rule as `filterMembers` — trimmed,
 * case-insensitive substring (QA36) — so the two search boxes this tab ships cannot disagree
 * about what a trailing space means.
 *
 * Narrows what is SHOWN and nothing else. The ticked set is the caller's state and this
 * function never sees it: a filter that unticked a hidden client would silently revoke access
 * the user had already granted, and the user would never see it happen.
 */
export function filterClientRoster(query: string): { id: number; name: string }[] {
  const q = query.trim().toLowerCase()
  // Copies rather than handing back the module constant, per §15.1 and this module's own
  // docblock: `CLIENT_ROSTER` is readonly at the type level only, nothing freezes it.
  if (!q) return CLIENT_ROSTER.slice()
  return CLIENT_ROSTER.filter((c) => c.name.toLowerCase().includes(q))
}

/**
 * §7's running count under the picker, in the shape the story writes it ("3 of 6 selected").
 *
 * The denominator is the ROSTER's length, never the filtered length: the sentence is about how
 * much access this invite grants, and a search box cannot change that. Typing "lagos" must not
 * make a 3-client invite read "1 of 1 selected".
 */
export function clientSelectionCount(selected: number): string {
  return `${selected} of ${CLIENT_ROSTER.length} selected`
}

/**
 * The flash MembersView raises after a successful send. INVENTED COPY — §7 says the modal
 * closes and says nothing about what confirms it — so it is pinned here rather than left in a
 * component, the same shape and the same reason as `unassignedNotice`'s singular.
 *
 * It exists because every other action on this tab already flashes (MembersTable's Resend
 * invite / Copy invite link), and the one action that actually changes the roster must not be
 * the silent one.
 */
export function invitedNotice(count: number): string {
  return `Invited ${count} ${count === 1 ? 'person' : 'people'}.`
}

// ---------------------------------------------------------------------------
// MEMB-01-07 — the member drawer's copy, and the three facts it must not derive
// ---------------------------------------------------------------------------
// §8's danger-zone copy is the single most important text in this story, and none of it
// existed in the repo before this subtask. It lives here for §15.8's reason and for no
// other: vitest is `environment: node`, so a sentence authored inside `MemberDrawer.tsx`
// is a sentence no spec can hold, and the drawer's oracle is a screenshot — which is
// exactly the gate a fluent paraphrase walks straight through. Same argument that moved
// CAPABILITY_FOOTNOTE (T5.1), REVIEWER_HINT / NO_CLIENTS_NOTE (T6.12) and QA40-QA46's
// three display derivations into this module.
//
// Every string below was byte-checked against §8/§9 in the vault, not against the
// subtask description that quotes them.

/**
 * §8's `Suspend` explanation, verbatim.
 *
 * Rendered under the suspend control in BOTH of its states. It describes what suspension
 * IS — the state — not the direction of travel, so it is equally true beside `Reactivate`,
 * which undoes exactly what it describes. §8 supplies no second sentence for reactivation
 * and AC#6 is a verbatim gate, so inventing one would add an unspec'd string to satisfy a
 * requirement nothing states.
 */
export const SUSPEND_EXPLANATION = 'Blocks sign-in and keeps all history. Their name stays on every invoice they touched.'

/**
 * §8's `Remove` explanation, verbatim.
 *
 * The semicolon is load-bearing: the second clause is what makes the first survivable, and
 * splitting it into two sentences (or dropping "audit history is never rewritten") is the
 * paraphrase this constant exists to prevent.
 */
export const REMOVE_EXPLANATION =
  'Revokes access permanently. Their name stays on every invoice they touched; audit history is never rewritten.'

/**
 * §8's confirm sentence. A FUNCTION, not a constant — §8 writes it with the member's name
 * interpolated ("Remove {name}? …"), so there is no name-free form of this string.
 */
export function removeConfirmQuestion(name: string): string {
  return `Remove ${name}? Access is revoked immediately. Their name stays on every invoice they touched.`
}

/** §8/§10.4's amber note beneath the drawer's Approval involvement section, verbatim. */
export const SUSPENDED_STEPS_NOTE = 'They are suspended, so those steps will block until someone else holds this position.'

/**
 * §9's explanation, and the tab's most-repeated sentence: the `⋯` menu, the drawer's role
 * cards and the drawer's danger zone all carry it.
 *
 * MOVED here from `MembersTable.tsx:91`, where it was a component-local constant no spec
 * could see. Four call sites across two components is exactly the divergence QA41 caught
 * for `accessRoleLabel` — one copy edited, the other left behind, and a screenshot of
 * either one looking right.
 */
export const PROTECTED_ADMIN_NOTE = "You're the only admin. Promote someone else first."

/**
 * §8's Approval-involvement gate: the steps the section renders, or `null` for "render
 * nothing at all".
 *
 * THE GATE IS THE COUNT, NOT THE POSITION. §8 and AC#5 both phrase the trigger as "when the
 * member holds a position", and three shipped in-house rows hold a position no policy names
 * (T7.6) — Tunde Adeyemi, Ibrahim Bello and Zainab Lawal. Taken literally, their drawers
 * would read "Named in 0 approval steps" above an empty policy list. Holding a position is
 * necessary, not sufficient.
 *
 * BOTH halves of that rule live here rather than in `MemberDrawer`, where the count half was
 * first written. §15.8's reason and this module's own header's: vitest is `environment:
 * node`, so a rule derived inside a component is a rule no spec can hold — and this is the
 * one that decides whether three shipped drawers read correctly or read "0". Same argument
 * that moved QA40-QA46's three display derivations out of MEMB-01-04's components.
 *
 * Hands back `stepsFor`'s result unchanged when it answers at all, rather than a boolean: the
 * drawer renders `total` AND every policy name off it, so a predicate would only make the
 * caller run `stepsFor` a second time to get what this call already computed.
 *
 * `position` is absent in TWO shapes and both mean the same thing here — `undefined` on every
 * firm row, which carries no such column (T1.6), and `null` on an in-house row holding no
 * approval position. `stepsFor` stays exported and separately called: MembersTable reads it
 * under §10.4's DIFFERENT gate, which fires only for a suspended row.
 */
export function approvalInvolvement(policies: readonly Policy[], position: RoleKey | null | undefined): PolicySteps | null {
  if (position == null) return null
  const steps = stepsFor(policies, position)
  return steps.total > 0 ? steps : null
}

/**
 * §8's Approval-involvement line — the bare count, with no consequence clause.
 *
 * NOT `stepsWarning`. That one is §10.4's ROW sentence and carries the ` · those steps
 * will block` suffix; in the drawer the blocking fact is carried separately, and only when
 * the member is actually suspended, by `SUSPENDED_STEPS_NOTE`. Rendering the row's
 * sentence here would state the consequence twice for a suspended member and state it
 * wrongly for an active one, who blocks nothing.
 *
 * §8 supplies the PLURAL verbatim ("Named in 2 approval steps"); the singular is the same
 * steps→step reconstruction `stepsWarning` and `unassignedNotice` already make, and is
 * reachable the moment a Workflows edit leaves one step naming the position.
 */
export function stepsNamedLine(total: number): string {
  return total === 1 ? 'Named in 1 approval step' : `Named in ${total} approval steps`
}

/**
 * The drawer's Activity section is the first and only consumer of `joined`, which is
 * `null` on every invited row. Returns the same `'—'` `lastActiveLabel` returns for a
 * missing value, so the three Activity cells cannot disagree about how absence renders.
 */
export function joinedLabel(member: Member): string {
  return member.joined ?? '—'
}

/**
 * §7's zero-selected rule, as a predicate over the value the client picker EMITS.
 *
 * It moved out of `InviteMembersModal` when MEMB-01-07 extracted `ClientAccessPicker`: the
 * rule now has two readers — the picker, which shows `NO_CLIENTS_NOTE`, and the modal,
 * which disables `Send invites` — and the modal can no longer see the picker's internal
 * `scope`. It does not need to. `'all' | number[]` is a discriminated union, not a
 * convention: `'all'` IS scope-all and an array IS scope-selected, so `access !== 'all'`
 * answers the question the modal used to answer with its own state.
 *
 * `[]` is representable and meaningful — `clientAccessLabel([])` renders 'No clients'. It
 * is simply not sendable.
 */
export function needsClientPick(access: 'all' | readonly number[]): boolean {
  return access !== 'all' && access.length === 0
}
