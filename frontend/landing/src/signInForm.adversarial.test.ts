// AUTH-05-06 adversarial: the sign-in validator off its happy path.
import { describe, expect, it } from 'vitest'

import { validateDemoForm } from './components/demoForm'
import { validateSignInForm } from './signInForm'

const VALID_EMAIL = 'ada@okafor.ng'
const MISSING = 'Enter your work email.'
const MALFORMED = 'Enter a valid work email address.'
const NO_PASSWORD = 'Enter your password.'

describe('validateSignInForm adversarial', () => {
  it('checks the email with its surrounding spaces trimmed', () => {
    for (const email of [` ${VALID_EMAIL} `, `\t${VALID_EMAIL}\n`]) {
      expect(validateSignInForm({ email, password: 'pw' }), JSON.stringify(email)).toEqual({})
    }
    expect(validateSignInForm({ email: ' a b@c.d ', password: 'pw' }).email).toBe(MALFORMED)
  })

  it('treats a whitespace-only email as missing, not malformed', () => {
    for (const email of ['', ' ', '\t\n']) {
      expect(validateSignInForm({ email, password: 'pw' }).email, JSON.stringify(email)).toBe(MISSING)
    }
    expect(validateSignInForm({ email: 'a@', password: 'pw' }).email).toBe(MALFORMED)
  })

  it('reports the email and password errors together', () => {
    expect(validateSignInForm({ email: '', password: '' })).toEqual({ email: MISSING, password: NO_PASSWORD })
    expect(validateSignInForm({ email: 'nope', password: '' })).toEqual({ email: MALFORMED, password: NO_PASSWORD })
  })

  // D12/D7 say "password required, never trimmed": a non-empty whitespace password passes.
  it('passes a whitespace-only password because the password is never trimmed', () => {
    expect(validateSignInForm({ email: VALID_EMAIL, password: '   ' })).toEqual({})
    expect(validateSignInForm({ email: VALID_EMAIL, password: '' }).password).toBe(NO_PASSWORD)
  })

  it('judges emails exactly as the demo form does', () => {
    const emails = [VALID_EMAIL, 'a@b.c', 'a@b', '@b.c', 'a@.c', 'a@@b.c', 'a@b.c ', 'a b@c.d', 'a@b c.d', 'ab.c']
    const accepted = emails.filter((e) => !validateSignInForm({ email: e, password: 'pw' }).email)
    expect(accepted.length).toBeGreaterThan(0)
    expect(accepted.length).toBeLessThan(emails.length)
    for (const email of emails) {
      const demo = validateDemoForm({ name: 'n', email, company: 'c', consent: true }).email
      expect(validateSignInForm({ email, password: 'pw' }).email, email).toBe(demo)
    }
    expect(accepted).toEqual([VALID_EMAIL, 'a@b.c', 'a@b.c '])
  })
})
