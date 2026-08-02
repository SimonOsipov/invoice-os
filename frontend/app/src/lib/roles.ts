// Workflow roles (Settings › Roles) — the approval seat as one object owning both its title
// and its holders.
//
// Dependency direction is ONE-WAY and type-only: roles.ts → members.ts, roles.ts → workflows.ts.
// Member and policy lists arrive as ARGUMENTS (the shape `stepsFor` already uses), so nothing
// imports this module back and the graph stays acyclic.

import type { Member } from './members'
import type { Policy, WorkflowMode } from './workflows'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type Role = {
  /** Stable id, slug-generated on create and never re-derived on rename. Never shown in UI. */
  key: string
  title: string
  desc: string
  /** Member ids — the single source of truth for who holds this role. */
  members: string[]
}

/** Keyed per workspace mode, mirroring `PolicyStore` / `MemberStore` — NOT per client. */
export type RoleStore = Record<WorkflowMode, Role[]>

/** The card, the canvas and the inspector all need the amber tone to travel WITH the string. */
export type Resolved = { text: string; warn: boolean }

/** `stepsFor`'s shape with `name` renamed. Policies with a zero count are omitted. */
export type RoleSteps = { total: number; policies: { policyName: string; count: number }[] }

// ---------------------------------------------------------------------------
// Seed
// ---------------------------------------------------------------------------

// Practice vocabulary on the keys `SEED_FIRM_POLICIES` already references; `quality_reviewer`
// is the one net-new key and the one role nobody holds, which is what puts the blocking copy
// on screen at first load. mf3 Musa Danjuma is the seeded two-role holder.
export const SEED_FIRM_ROLES: readonly Role[] = [
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
export const SEED_INHOUSE_ROLES: readonly Role[] = [
  { key: 'preparer', title: 'Preparer', desc: 'Accounts Payable', members: ['mh7'] },
  { key: 'line_mgr', title: 'Line Manager', desc: 'Requesting dept.', members: ['mh3'] },
  { key: 'fin_mgr', title: 'Finance Manager', desc: 'Finance', members: [] },
  { key: 'controller', title: 'Financial Controller', desc: 'Finance', members: ['mh4'] },
  { key: 'fin_dir', title: 'Finance Director', desc: 'Finance', members: ['mh1', 'mh2'] },
  { key: 'compliance', title: 'Compliance Officer', desc: 'Tax & Compliance', members: ['mh5'] },
  { key: 'cfo', title: 'CFO', desc: 'Executive', members: ['mh6'] },
  { key: 'ceo', title: 'CEO', desc: 'Executive', members: [] },
]

/** Deep clone per call, mirroring `seedPolicies` / `seedMembers`. */
export function seedRoles(): RoleStore {
  return { firm: cloneRoles(SEED_FIRM_ROLES), inhouse: cloneRoles(SEED_INHOUSE_ROLES) }
}

/** `members` is the only nested value a Role carries, so a bare spread would alias the seed. */
function cloneRoles(list: readonly Role[]): Role[] {
  return list.map((r) => ({ ...r, members: r.members.slice() }))
}

// ---------------------------------------------------------------------------
// Reducers
// ---------------------------------------------------------------------------
// All five allocate unconditionally, so a miss returns a new array holding the same values
// rather than the input reference — the `replaceMember` / `replacePolicy` form.

export function replaceRole(list: readonly Role[], next: Role): Role[] {
  return list.map((r) => (r.key === next.key ? next : r))
}

export function addRole(list: readonly Role[], next: Role): Role[] {
  return [...list, next]
}

export function removeRole(list: readonly Role[], key: string): Role[] {
  return list.filter((r) => r.key !== key)
}

/** Copies the ids: one `memberIds` argument must not end up aliased by the role it staffs. */
export function setRoleMembers(list: readonly Role[], key: string, memberIds: readonly string[]): Role[] {
  return list.map((r) => (r.key === key ? { ...r, members: memberIds.slice() } : r))
}

/** Removing a member drops them from every role; suspending deliberately does not. */
export function pruneMember(list: readonly Role[], memberId: string): Role[] {
  return list.map((r) => ({ ...r, members: r.members.filter((id) => id !== memberId) }))
}

/**
 * Slug of the title, suffixed only on collision within the mode. Same slug form as rules.ts's
 * `tenantSlug` and deliberately not shared: a rules-domain edit there must not re-key roles.
 */
export function newRoleKey(list: readonly Role[], title: string): string {
  // A title of only punctuation slugifies to nothing, and Save gates on an empty NAME.
  const base =
    title
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '') || 'role'
  const taken = new Set(list.map((r) => r.key))
  if (!taken.has(base)) return base
  let n = 2
  while (taken.has(`${base}-${n}`)) n++
  return `${base}-${n}`
}

// ---------------------------------------------------------------------------
// Derivations — all pure, all taking their inputs as arguments
// ---------------------------------------------------------------------------

/** A step whose stored key names no role must render the truth rather than a raw id. */
export function roleOf(list: readonly Role[], key: string): Role & { deleted?: true } {
  return list.find((r) => r.key === key) ?? { key, title: 'Deleted role', desc: '', members: [], deleted: true }
}

/** In `Role.members` order, not roster order — the card's avatar stack renders straight off this. */
export function holders(list: readonly Role[], members: readonly Member[], key: string): Member[] {
  const role = list.find((r) => r.key === key)
  if (!role) return []
  return role.members.map((id) => members.find((m) => m.id === id)).filter((m): m is Member => m != null)
}

/**
 * Status only — deliberately NOT filtered by access role. The delegate picker is the one place
 * the Reviewer role is a hard gate; a holder who is an admin still resolves.
 */
export function activeHolders(list: readonly Role[], members: readonly Member[], key: string): Member[] {
  return holders(list, members, key).filter((m) => m.status === 'active')
}

export function rolesOfMember(list: readonly Role[], memberId: string): Role[] {
  return list.filter((r) => r.members.includes(memberId))
}

type Resolution =
  | { kind: 'missing' }
  | { kind: 'none' }
  | { kind: 'blocked' | 'ok'; primary: string; extra: number }

/** The five states, derived once so the card and the inspector cannot disagree about them. */
function resolution(list: readonly Role[], members: readonly Member[], key: string): Resolution {
  if (!list.some((r) => r.key === key)) return { kind: 'missing' }
  const all = holders(list, members, key)
  if (all.length === 0) return { kind: 'none' }
  const active = all.filter((m) => m.status === 'active')
  // `extra` counts the OTHER holders, active or not.
  const extra = all.length - 1
  if (active.length === 0) return { kind: 'blocked', primary: all[0].name, extra }
  return { kind: 'ok', primary: active[0].name, extra }
}

/**
 * The card's and the canvas's shared wording. No `— suspended` suffix: a blocked role reads as
 * the holder's name alone, and the blocking fact travels as `warn`.
 */
export function resolve(list: readonly Role[], members: readonly Member[], key: string): Resolved {
  const res = resolution(list, members, key)
  if (res.kind === 'missing') return { text: 'Role no longer exists', warn: true }
  if (res.kind === 'none') return { text: 'Nobody assigned', warn: true }
  return { text: res.extra > 0 ? `${res.primary} +${res.extra}` : res.primary, warn: res.kind === 'blocked' }
}

/** The inspector's own three strings. It omits `+N` deliberately. */
export function inspectorResolve(list: readonly Role[], members: readonly Member[], key: string): Resolved {
  const res = resolution(list, members, key)
  if (res.kind === 'missing') return { text: 'Role no longer exists', warn: true }
  if (res.kind === 'none') return { text: 'Nobody holds this role — this step will block', warn: true }
  if (res.kind === 'blocked') return { text: `Currently: ${res.primary} — this step will block`, warn: true }
  return { text: `Currently: ${res.primary}`, warn: false }
}

/** Nobody-at-all and nobody-active are one condition: the role cannot be signed. */
export function unassignedRoles(list: readonly Role[], members: readonly Member[]): Role[] {
  return list.filter((r) => activeHolders(list, members, r.key).length === 0)
}

/**
 * Root lane + both `then`/`else` lanes; drafts counted, zero-count policies omitted. One level
 * of descent is enough forever — a `BranchNode` cannot hold a `ConditionNode`.
 */
export function steps(policies: readonly Policy[], key: string): RoleSteps {
  return stepsForKeys(policies, new Set([key]))
}

function stepsForKeys(policies: readonly Policy[], keys: ReadonlySet<string>): RoleSteps {
  const named: { policyName: string; count: number }[] = []
  let total = 0
  for (const p of policies) {
    let count = 0
    for (const n of p.nodes) {
      if (n.type === 'approval') {
        if (keys.has(n.role)) count++
      } else if (n.type === 'condition') {
        for (const child of [...n.then, ...n.else]) {
          if (child.type === 'approval' && keys.has(child.role)) count++
        }
      }
    }
    if (count > 0) {
      named.push({ policyName: p.name, count })
      total += count
    }
  }
  return { total, policies: named }
}

/**
 * Every role they hold, unioned in ONE traversal — a policy naming two of their roles is one
 * row, not two. THE GATE IS THE COUNT: holding a role a policy never names answers `null`,
 * not a section reading "Named in 0 approval steps" above an empty list.
 */
export function stepsForMember(policies: readonly Policy[], roles: readonly Role[], memberId: string): RoleSteps | null {
  const keys = new Set(rolesOfMember(roles, memberId).map((r) => r.key))
  const found = stepsForKeys(policies, keys)
  return found.total > 0 ? found : null
}

/** Case-insensitive match on either card string — title or desc. Empty query returns a copy. */
export function filterRoles(list: readonly Role[], query: string): Role[] {
  const q = query.trim().toLowerCase()
  if (!q) return list.slice()
  return list.filter((r) => r.title.toLowerCase().includes(q) || r.desc.toLowerCase().includes(q))
}

// ---------------------------------------------------------------------------
// Copy
// ---------------------------------------------------------------------------
// Living in lib/ rather than in a component for the reason members.ts records: vitest is
// `environment: node`, so a string authored inside a component is a string no spec can hold.

/**
 * Names this mode's own first two roles — vocabulary the reader can already see on the
 * cards below — and drops the clause entirely below two roles.
 */
export function intro(list: readonly Role[]): string {
  const head = 'A role is a named seat in your approval policies'
  const tail = 'Workflow steps point at the role; the people here are who actually signs.'
  const named = list.slice(0, 2).map((r) => r.title)
  return named.length === 2 ? `${head} — ${named.join(', ')}. ${tail}` : `${head}. ${tail}`
}

/** Sentence case; the role card uppercases it in CSS. */
export function roleUsage(roleSteps: RoleSteps): string {
  if (roleSteps.total === 0) return 'not used in any policy'
  const p = roleSteps.policies.length
  // NOT IN BRIEF: both singulars, each noun singularising independently.
  return `${roleSteps.total} approval ${roleSteps.total === 1 ? 'step' : 'steps'} · ${p} ${p === 1 ? 'policy' : 'policies'}`
}

export function holderCount(n: number): string {
  // NOT IN BRIEF: the singular.
  return `${n} ${n === 1 ? 'person' : 'people'}`
}

/** One sentence, two banners — the Roles tab's and the Members tab's, so they cannot drift. */
export function unassignedNotice(count: number): string {
  // NOT IN BRIEF: the singular.
  return count === 1
    ? '1 role has nobody active assigned. Approval steps pointed at it will block.'
    : `${count} roles have nobody active assigned. Approval steps pointed at them will block.`
}

/** The roster row's sentence for a suspended holder — the count AND the consequence. */
export function stepsWarning(total: number): string {
  return total === 1 ? 'Named in 1 approval step · that step will block' : `Named in ${total} approval steps · those steps will block`
}

/** The drawer's bare count. NOT `stepsWarning`: the drawer carries the consequence separately. */
export function stepsNamedLine(total: number): string {
  return total === 1 ? 'Named in 1 approval step' : `Named in ${total} approval steps`
}

/** The drawer's amber note, and the half `stepsNamedLine` leaves unsaid. */
export const SUSPENDED_STEPS_NOTE = 'They are suspended, so those steps will block until someone else holds this role.'

// ---------------------------------------------------------------------------
// RoleModal (MEMB-02-03) — stubbed pure helpers, throw until the executor implements them
// ---------------------------------------------------------------------------

/** The picker's rows for one mode — an invited person holds no place in a role yet. */
export function pickerMembers(_list: readonly Member[]): Member[] {
  throw new Error('not implemented')
}

/** Right-aligned meta column: department in-house, access-role label in firm. */
export function pickerMeta(_mode: WorkflowMode, _member: Member): string {
  throw new Error('not implemented')
}

/** Case-insensitive name/email search over an already-selectable list; trims the query. */
export function filterPickerMembers(_list: readonly Member[], _query: string): Member[] {
  throw new Error('not implemented')
}

/** `X of Y selected` — Y is the selectable count, never the raw roster length. */
export function pickerSelectionCount(_selected: number, _list: readonly Member[]): string {
  throw new Error('not implemented')
}

/** The footnote naming how many invited people the picker hides. */
export function hiddenInvitedFootnote(_list: readonly Member[]): string {
  throw new Error('not implemented')
}

/** Save is inert on an empty or whitespace-only name; nothing else gates it — duplicate titles are allowed. */
export function canSaveRole(_name: string): boolean {
  throw new Error('not implemented')
}

/** The inline delete-confirm sentence, naming the role and its usage. */
export function deleteRoleConfirm(_title: string, _roleSteps: RoleSteps): string {
  throw new Error('not implemented')
}
