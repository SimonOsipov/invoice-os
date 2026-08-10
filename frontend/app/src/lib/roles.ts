// Workflow roles (Settings › Roles) — the approval seat as one object owning both its title
// and its holders.
//
// Dependency direction is ONE-WAY: roles.ts → members.ts, roles.ts → workflows.ts. Member and
// policy lists arrive as ARGUMENTS (the shape `stepsFor` already uses), so nothing imports this
// module back and the graph stays acyclic.
//
// Both edges are TYPE-ONLY: the picker's meta column moved to `accessRoleLabel` at its own
// call site when the in-house department fork went, so this module imports no runtime value.
//
// The `// Wire` section at the foot of this file adds five AuthedFetch wrappers over the
// APPR-02 workflow-role endpoints, on lib/members.ts's live-wire shape.

import type { AsyncStatus } from '@invoice-os/api-client'

import type { AccessRole, Member, MembersSurface } from './members'
import type { AuthedFetch } from './portfolio'
import type { Policy } from './workflows'

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

/** The card, the canvas and the inspector all need the amber tone to travel WITH the string. */
export type Resolved = { text: string; warn: boolean }

/** `stepsFor`'s shape with `name` renamed. Policies with a zero count are omitted. */
export type RoleSteps = { total: number; policies: { policyName: string; count: number }[] }

// ---------------------------------------------------------------------------
// Reducers
// ---------------------------------------------------------------------------
// All three allocate unconditionally, so a miss returns a new array holding the same values
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
 * Status AND access role: only an active admin/reviewer satisfies a seat. Staffing a preparer
 * stays legal server-side (`internal/approval/store.go:355-358`, `TestStaffing_PreparerMayBeStaffed`)
 * — the approver filter is a read-time concern, not a staffing-time one.
 */
export function activeHolders(list: readonly Role[], members: readonly Member[], key: string): Member[] {
  return holders(list, members, key).filter((m) => m.status === 'active' && isApprover(m.role))
}

export function rolesOfMember(list: readonly Role[], memberId: string): Role[] {
  return list.filter((r) => r.members.includes(memberId))
}

/**
 * The roster column's cell — the first title plus `+N`, every title in the tooltip. Newline-
 * joined for the reason the Client access tooltip is: a list of names on one line is a tooltip
 * nobody reads. An empty tooltip is the caller's own signal for "no roles".
 */
export function rosterRoleCell(list: readonly Role[], memberId: string): { text: string; tooltip: string } {
  const titles = rolesOfMember(list, memberId).map((r) => r.title)
  if (titles.length === 0) return { text: '—', tooltip: '' }
  const extra = titles.length - 1
  return { text: extra > 0 ? `${titles[0]} +${extra}` : titles[0], tooltip: titles.join('\n') }
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
  const active = all.filter((m) => m.status === 'active' && isApprover(m.role))
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

/**
 * Beneath the drawer's pill toggles. Forks on the ACCESS role, not on any workflow role: a
 * Preparer cannot act on an approval step at all, so for them a workflow role is inert until
 * the cards above change — which is the one thing the general sentence does not say.
 */
export function drawerRoleHelper(role: AccessRole): string {
  return role === 'preparer'
    ? 'Preparers cannot approve. Give them the Reviewer access role above before a workflow role means anything.'
    : 'Roles decide which approval steps this person can act on.'
}

/** Beneath the invite modal's `Workflow role` select, in both modes. */
export const INVITE_ROLE_HELPER =
  'The workflow role decides which approval steps they can sign. You can change it later in Settings › Roles.'

// ---------------------------------------------------------------------------
// Role modal — the picker's derivations, and the modal's own copy
// ---------------------------------------------------------------------------

/** The picker's rows for one mode — an invited person holds no place in a role yet. */
export function pickerMembers(list: readonly Member[]): Member[] {
  return list.filter((m) => m.status !== 'invited')
}

/** Case-insensitive name/email search over an already-selectable list; trims the query. */
export function filterPickerMembers(list: readonly Member[], query: string): Member[] {
  const q = query.trim().toLowerCase()
  if (!q) return list.slice()
  // `q` is non-empty here, so a null email's `''` never matches.
  return list.filter((m) => m.name.toLowerCase().includes(q) || (m.email ?? '').toLowerCase().includes(q))
}

/**
 * `X of Y selected` — `clientSelectionCount`'s idiom, but Y is the SELECTABLE count and never
 * the roster length: a person the picker will not show cannot be one of the Y you may pick.
 */
export function pickerSelectionCount(selected: number, list: readonly Member[]): string {
  return `${selected} of ${pickerMembers(list).length} selected`
}

/**
 * How many of a selection the picker will never render a row for — the gap the count above
 * cannot explain on its own, now that an invite can put a fresh (still `invited`) id straight
 * into a role. Additive: `pickerMembers` and `pickerSelectionCount` are unchanged, and a caller
 * that ignores this renders exactly what it rendered before.
 */
export function pickerHiddenAmongSelected(selected: readonly string[], list: readonly Member[]): number {
  const shown = new Set(pickerMembers(list).map((m) => m.id))
  return selected.filter((id) => !shown.has(id)).length
}

/** The `role-modal-count` addendum for that gap. Callers gate on a non-zero count. */
export function hiddenSelectionNote(n: number): string {
  return `+${n} invited`
}

/** The footnote naming how many invited people the picker hides. Callers gate on a zero count. */
export function hiddenInvitedFootnote(list: readonly Member[]): string {
  const n = list.length - pickerMembers(list).length
  // NOT IN BRIEF: the singular.
  return n === 1
    ? '1 invited person is hidden until they accept the invite.'
    : `${n} invited people are hidden until they accept the invite.`
}

/** Save is inert on an empty or whitespace-only name; nothing else gates it — duplicate titles are allowed. */
export function canSaveRole(name: string): boolean {
  return name.trim() !== ''
}

/**
 * The inline delete-confirm sentence, naming the role and its usage. The blocking clause is
 * dropped entirely on an unused role: nothing points at it, so nothing can block.
 */
export function deleteRoleConfirm(title: string, roleSteps: RoleSteps): string {
  const usage = roleUsage(roleSteps)
  // NOT IN BRIEF: the unused half. The used half is the brief's sentence.
  if (roleSteps.total === 0) return `Delete ${title}? It is ${usage}.`
  return `Delete ${title}? ${usage}. Those steps will block until you point them somewhere else.`
}

// NOT IN BRIEF: §2 asks the header for "a one-line subtitle" and supplies neither line.
export const NEW_ROLE_SUBTITLE = 'Name the seat and say who fills it.'
export const EDIT_ROLE_SUBTITLE = 'Rename the seat, or change who fills it.'

/** The toolbar flash after a save — §2's string. `invitedNotice`'s posture on the Members tab. */
export function savedNotice(title: string): string {
  return `${title} saved`
}

/** NOT IN BRIEF: the delete half of the pair, so the two cannot drift. */
export function deletedNotice(title: string): string {
  return `${title} deleted`
}

// ---------------------------------------------------------------------------
// Wire
// ---------------------------------------------------------------------------
// The wire object IS `Role` (approval.Role, field-for-field) — no separate wire type or
// projection. `base` is a parameter, never `gatewayBase()` called here; no wrapper catches,
// so a non-2xx rejects with the underlying ApiError unchanged.

/** The one-key envelope; the other four wrappers below resolve a bare `Role`. */
export async function listWorkflowRoles(f: AuthedFetch, base: string): Promise<Role[]> {
  const body = await f<{ workflow_roles: Role[] }>(`${base}/api/invoice/v1/workflow-roles`)
  return body.workflow_roles
}

export async function createWorkflowRole(f: AuthedFetch, base: string, title: string, desc: string): Promise<Role> {
  return f<Role>(`${base}/api/invoice/v1/workflow-roles`, { method: 'POST', body: { title, desc } })
}

/** Go pointer fields: an absent key here must stay absent on the wire, never `""` or `null`. */
export type RolePatch = { title?: string; desc?: string }

/** Only the changed fields — an unchanged one is omitted, never sent back as its old value. */
export function rolePatch(current: Role, title: string, desc: string): RolePatch {
  const patch: RolePatch = {}
  if (title !== current.title) patch.title = title
  if (desc !== current.desc) patch.desc = desc
  return patch
}

export async function updateWorkflowRole(f: AuthedFetch, base: string, key: string, patch: RolePatch): Promise<Role> {
  return f<Role>(`${base}/api/invoice/v1/workflow-roles/${encodeURIComponent(key)}`, { method: 'PATCH', body: patch })
}

export async function deleteWorkflowRole(f: AuthedFetch, base: string, key: string): Promise<Role> {
  return f<Role>(`${base}/api/invoice/v1/workflow-roles/${encodeURIComponent(key)}`, { method: 'DELETE' })
}

/** An empty set still PUTs an explicit `{"members":[]}` — the server 400s on an absent key. */
export async function setRoleMembers(f: AuthedFetch, base: string, key: string, memberIds: readonly string[]): Promise<Role> {
  return f<Role>(`${base}/api/invoice/v1/workflow-roles/${encodeURIComponent(key)}/members`, {
    method: 'PUT',
    body: { members: memberIds },
  })
}

/** POSTs then PUTs the SERVER's own returned key, never a slug of the title; skips the PUT unstaffed. */
export async function createStaffedRole(
  f: AuthedFetch,
  base: string,
  title: string,
  desc: string,
  members: readonly string[],
): Promise<Role> {
  const created = await createWorkflowRole(f, base, title, desc)
  if (members.length === 0) return created
  return setRoleMembers(f, base, created.key, members)
}

/** Worst-of ladder over BOTH fetches the Roles screen needs landed — not `membersSurface`'s one-argument form. */
export function rolesSurface(rolesStatus: AsyncStatus, membersStatus: AsyncStatus): MembersSurface {
  if (rolesStatus === 'error' || membersStatus === 'error') return 'error'
  if (rolesStatus === 'loading' || membersStatus === 'loading') return 'loading'
  if (rolesStatus === 'idle' || rolesStatus === 'empty' || membersStatus === 'idle' || membersStatus === 'empty') return 'empty'
  return 'roster'
}

/** Mirrors internal/invoice/store.go's isApprover. */
export function isApprover(role: AccessRole): boolean {
  return role === 'admin' || role === 'reviewer'
}
