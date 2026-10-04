// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { accessRoleLabel } from '../lib/members'
import type { Member } from '../lib/members'
import { BLOCKED_BY_ROLE_ACTION, BLOCKED_BY_ROLE_PREFIX } from './copy'
import { BlockedByRoleNote } from './BlockedByRoleNote'

afterEach(() => {
  cleanup()
})

const FOLAKE: Member = {
  id: 'm-folake-001',
  name: 'Folake Adesina',
  initials: 'FA',
  email: 'folake@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: true,
}

describe('BlockedByRoleNote (task-594, DEMO-06-06)', () => {
  it('the note names the person and their access-role label', () => {
    render(<BlockedByRoleNote member={FOLAKE} />)

    const expected = `${BLOCKED_BY_ROLE_PREFIX.replace('{name}', 'Folake Adesina').replace('{role}', 'Preparer')} ${BLOCKED_BY_ROLE_ACTION}`
    expect(screen.getByTestId('persona-blocked-note').textContent).toBe(expected)
  })

  it('every copy token is substituted', () => {
    render(<BlockedByRoleNote member={FOLAKE} />)

    const text = screen.getByTestId('persona-blocked-note').textContent ?? ''
    expect(text).not.toContain('{')
    expect(text).not.toContain('}')
    // Pins the role through accessRoleLabel, not a re-cased m.role -- a forgotten
    // {role} replace would leave the literal token, caught by the assertions above.
    expect(text).toContain(accessRoleLabel('preparer'))
  })

  // DM-08. The corner is red at write; the lock glyph and text colours are pins (Q3: the icon
  // keeps --fg-4, the sentence is enabled text on --fg-3).
  it('DM-08 the blocked note is a 6px panel', () => {
    render(<BlockedByRoleNote member={FOLAKE} />)

    const note = screen.getByTestId('persona-blocked-note')
    const [glyph, text] = Array.from(note.children) as HTMLElement[]
    expect(glyph).not.toBeUndefined()
    expect(text).not.toBeUndefined()
    expect(text.textContent).toContain('Folake Adesina')
    expect(glyph.style.color).toBe('var(--fg-4)')
    expect(text.style.color).toBe('var(--fg-3)')
    expect(note.style.borderRadius).toBe('var(--radius-md)')
    expect(note.outerHTML).not.toContain('oklch')
  })
})
