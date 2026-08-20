// Demo-only. A stand-in persona wears the roster row's name and subject over the SEAT's
// tenant: GET /v1/memberships returns no tenant_id per row, so the seat is the only
// non-guessed source for tenantId/role/mode.
import type { Persona } from '../auth'
import { accessRoleLabel, type Member } from '../lib/members'

export function personaFromMember(member: Member, seat: Persona): Persona {
  if (member.status !== 'active') {
    throw new Error(`personaFromMember: member ${member.id} is ${member.status}, not active`)
  }
  return {
    id: seat.id,
    name: member.name,
    title: accessRoleLabel(member.role),
    initials: member.initials,
    org: seat.org,
    email: '',
    access: seat.access,
    mode: seat.mode,
    // Member.id IS the auth subject — toMember maps it from w.user_id.
    subject: member.id,
    tenantId: seat.tenantId,
    role: seat.role,
  }
}

/** Takes the seat's SUBJECT, not a Persona: PlatformCtx exposes only seatSubject. */
export function isSeat(subject: string, seatSubject: string | undefined): boolean {
  return seatSubject !== undefined && subject === seatSubject
}
