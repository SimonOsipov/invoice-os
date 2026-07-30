// Members (Settings › Members) — types, constants, seed and read-only derivations
// (MEMB-01-01). STUB — the executor writes the seed and the bodies next; every constant
// below is empty and every function throws, so the specs in members.test.ts fail on a
// thrown `not implemented` / an assertion wrapping it, not on an import or type error.
//
// Mock-only, and permanently so for now: there is no members endpoint at all. The shipped
// `memberships` table has no status column, no inviter, no expiry and no `users` row, and
// nothing anywhere holds a department or an approval position — so Suspend, Remove and
// Axis B have no backend mechanism to call (MEMB-01 §Decisions). Everything here is seed
// data plus pure functions of their arguments, shaped so that swapping the seed for a
// fetch changes no component.
//
// Dependency direction is ONE-WAY: members.ts → workflows.ts. This module takes `RoleKey`,
// `WorkflowMode` and `Policy` from ./workflows and (once implemented) `WF_ROLES` from it
// too; workflows.ts never learns members exist — `stepsFor` receives the policy list as an
// argument rather than importing it. members.ts imports NOTHING from types.ts: types.ts
// already type-imports from ./lib/workflows, and MEMB-01-03 makes types.ts import THIS
// module, so a back-edge here would be a real cycle rather than a hypothetical one.
//
// Reducers (MEMB-01-02) are immutable, and `seedMembers` deep-clones. `SEED_*` are module
// constants that are readonly at the type level only — nothing freezes them at runtime —
// so a mutating reducer or a shallow clone would silently alias the seed across a mode
// switch. `clientAccess` is the only nested value in the whole module and is exactly where
// that bites: it is the same bug class lib/workflows.ts:15-18 records the Workflows port
// fixing.

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
export const ACCESS_ROLES: readonly { id: AccessRole; label: string; description: string }[] = []

export const DEPARTMENTS: readonly Department[] = []

/** The "What can each role do?" expander — eight capability rows × the three roles. */
export const CAPABILITY_ROWS: readonly { label: string; admin: boolean; preparer: boolean; reviewer: boolean }[] = []

/** The static client roster `clientAccess` indexes. Ids ARE the CFG indices (../data). */
export const CLIENT_ROSTER: readonly { id: number; name: string }[] = []

// ---------------------------------------------------------------------------
// Seed
// ---------------------------------------------------------------------------

export const SEED_FIRM_MEMBERS: readonly Member[] = []

export const SEED_INHOUSE_MEMBERS: readonly Member[] = []

/** Deep clone per call, mirroring `seedPolicies` (workflows.ts:206-216). */
export function seedMembers(): MemberStore {
  throw new Error('not implemented')
}

// ---------------------------------------------------------------------------
// Derivations — all pure, all taking their inputs as arguments
// ---------------------------------------------------------------------------

export function holders(_list: readonly Member[], _position: RoleKey): Member[] {
  throw new Error('not implemented')
}

export function activeHolders(_list: readonly Member[], _position: RoleKey): Member[] {
  throw new Error('not implemented')
}

/** Every `WF_ROLES` position with no holder at all, in `WF_ROLES` order. */
export function unassignedPositions(_list: readonly Member[]): RoleKey[] {
  throw new Error('not implemented')
}

/** Positions that HAVE holders but none active — a policy step that would block. */
export function blockedPositions(_list: readonly Member[]): RoleKey[] {
  throw new Error('not implemented')
}

/** Root lane + both `then`/`else` lanes; drafts included, zero-count policies omitted. */
export function stepsFor(_policies: readonly Policy[], _position: RoleKey): PolicySteps {
  throw new Error('not implemented')
}

export function resolvePosition(_list: readonly Member[], _position: RoleKey): PositionResolution {
  throw new Error('not implemented')
}

/** Never sees the SLA — the canvas joins this with `slaText(n.sla)` on ` · `. */
export function canvasApprovalLine(_res: PositionResolution): string {
  throw new Error('not implemented')
}

export function inspectorApprovalLine(_res: PositionResolution): string {
  throw new Error('not implemented')
}

export function departmentsInUse(_list: readonly Member[]): Department[] {
  throw new Error('not implemented')
}

/** Departments in use, then the standing committees and `Preparer`; `current` last if new. */
export function inhouseNotifyTargets(_list: readonly Member[], _current: string): string[] {
  throw new Error('not implemented')
}

/** Active members whose access role is `reviewer` — admins excluded, per §11.3. */
export function delegateCandidates(_list: readonly Member[]): string[] {
  throw new Error('not implemented')
}

export function clientAccessLabel(_access: 'all' | readonly number[]): string {
  throw new Error('not implemented')
}

export function clientAccessNames(_access: 'all' | readonly number[]): string[] {
  throw new Error('not implemented')
}

export function lastActiveLabel(_member: Member): string {
  throw new Error('not implemented')
}

export function activeAdmins(_list: readonly Member[]): Member[] {
  throw new Error('not implemented')
}
