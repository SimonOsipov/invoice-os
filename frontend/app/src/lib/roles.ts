// Roles data model (MEMB-02-01) — STUB. Type signatures and seed shapes only; every
// function body is `not implemented` so lib/roles.test.ts compiles and runs RED.
// Real seed data and logic land in Stage 3.
//
// roles.ts type-imports Member and Policy and takes both as arguments — nothing imports
// roles.ts back, so the graph stays acyclic: roles -> members, roles -> workflows.

import type { Member } from './members'
import type { Policy, WorkflowMode } from './workflows'

export type Role = {
  key: string
  title: string
  desc: string
  members: string[]
}

export type RoleStore = Record<WorkflowMode, Role[]>

export type Resolved = { text: string; warn: boolean }
export type RoleSteps = { total: number; policies: { policyName: string; count: number }[] }

// Seed data — real rows land in Stage 3.
export const SEED_FIRM_ROLES: readonly Role[] = []
export const SEED_INHOUSE_ROLES: readonly Role[] = []

function notImplemented(): never {
  throw new Error('not implemented')
}

export function seedRoles(): RoleStore {
  return notImplemented()
}

export function replaceRole(_list: readonly Role[], _next: Role): Role[] {
  return notImplemented()
}

export function addRole(_list: readonly Role[], _next: Role): Role[] {
  return notImplemented()
}

export function removeRole(_list: readonly Role[], _key: string): Role[] {
  return notImplemented()
}

export function setRoleMembers(_list: readonly Role[], _key: string, _memberIds: readonly string[]): Role[] {
  return notImplemented()
}

export function pruneMember(_list: readonly Role[], _memberId: string): Role[] {
  return notImplemented()
}

export function newRoleKey(_list: readonly Role[], _title: string): string {
  return notImplemented()
}

export function roleOf(_list: readonly Role[], _key: string): Role & { deleted?: true } {
  return notImplemented()
}

export function holders(_list: readonly Role[], _members: readonly Member[], _key: string): Member[] {
  return notImplemented()
}

export function activeHolders(_list: readonly Role[], _members: readonly Member[], _key: string): Member[] {
  return notImplemented()
}

export function rolesOfMember(_list: readonly Role[], _memberId: string): Role[] {
  return notImplemented()
}

export function resolve(_list: readonly Role[], _members: readonly Member[], _key: string): Resolved {
  return notImplemented()
}

export function inspectorResolve(_list: readonly Role[], _members: readonly Member[], _key: string): Resolved {
  return notImplemented()
}

export function steps(_policies: readonly Policy[], _key: string): RoleSteps {
  return notImplemented()
}

export function stepsForMember(_policies: readonly Policy[], _roles: readonly Role[], _memberId: string): RoleSteps | null {
  return notImplemented()
}

export function unassignedRoles(_list: readonly Role[], _members: readonly Member[]): Role[] {
  return notImplemented()
}

export function roleUsage(_roleSteps: RoleSteps): string {
  return notImplemented()
}

export function holderCount(_n: number): string {
  return notImplemented()
}

export function unassignedNotice(_count: number): string {
  return notImplemented()
}

export function stepsWarning(_total: number): string {
  return notImplemented()
}

export function stepsNamedLine(_total: number): string {
  return notImplemented()
}

// NOT IN BRIEF placeholder — real copy (matches members.ts's SUSPENDED_STEPS_NOTE) lands in Stage 3.
export const SUSPENDED_STEPS_NOTE = ''
