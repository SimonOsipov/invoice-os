// AC-6 (DEMO-06-02). Pure -- node environment, no jsdom.
import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import type { Member } from '../lib/members'
import { isSeat, personaFromMember } from './identity'

const SEAT = APP_PERSONAS.firm

const ACTIVE_MEMBER: Member = {
  id: 'm-active-001',
  name: 'Amaka Eze',
  initials: 'AE',
  email: 'amaka@example.ng',
  role: 'reviewer',
  status: 'active',
  isYou: false,
}

describe('personaFromMember', () => {
  it('takes the tenant from the seat, never the row', () => {
    const persona = personaFromMember(ACTIVE_MEMBER, SEAT)

    // seat-sourced
    expect(persona.id).toBe(SEAT.id)
    expect(persona.org).toBe(SEAT.org)
    expect(persona.access).toBe(SEAT.access)
    expect(persona.mode).toBe(SEAT.mode)
    expect(persona.tenantId).toBe(SEAT.tenantId)
    expect(persona.role).toBe(SEAT.role)

    // member-sourced
    expect(persona.name).toBe(ACTIVE_MEMBER.name)
    expect(persona.initials).toBe(ACTIVE_MEMBER.initials)
    expect(persona.subject).toBe(ACTIVE_MEMBER.id)
  })

  it('refuses a non-active member', () => {
    const suspended: Member = { ...ACTIVE_MEMBER, status: 'suspended' }
    expect(() => personaFromMember(suspended, SEAT)).toThrow(/suspended/i)
  })

  // 'invited' is unreachable through the demo roster today (Stage 1 note 3), but the
  // guard is `!== 'active'`, not a suspended-only check -- prove it holds for both.
  it('refuses an invited member', () => {
    const invited: Member = { ...ACTIVE_MEMBER, status: 'invited' }
    expect(() => personaFromMember(invited, SEAT)).toThrow(/invited/i)
  })
})

describe('isSeat', () => {
  it.each([
    ['matching subject', SEAT.subject, SEAT.subject, true],
    ['different subject', 'someone-else', SEAT.subject, false],
    ['no seat subject (flag off)', SEAT.subject, undefined, false],
  ])('%s -> %s', (_label, subject, seatSubject, expected) => {
    expect(isSeat(subject, seatSubject)).toBe(expected)
  })
})
