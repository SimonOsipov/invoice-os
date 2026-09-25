// AUTH-05-06 AC-5: the sign-in validator.
import { describe, expect, it } from 'vitest'

import { validateSignInForm, type SignInFormValues } from './signInForm'

const VALID_EMAIL = 'ada@okafor.ng'

describe('validateSignInForm', () => {
  it('validateSignInForm refuses empty and malformed email', () => {
    const bad = ['', 'a@', 'a b@c.d']
    expect(bad.length).toBeGreaterThan(0)
    for (const email of bad) {
      const errors = validateSignInForm({ email, password: 'pw' })
      expect(errors.email, `email ${JSON.stringify(email)}`).toEqual(expect.any(String))
      expect(errors.email, `email ${JSON.stringify(email)}`).not.toBe('')
    }
    expect(validateSignInForm({ email: VALID_EMAIL, password: 'pw' })).toEqual({})
  })

  it('validateSignInForm refuses an empty password only', () => {
    const empty = validateSignInForm({ email: VALID_EMAIL, password: '' })
    expect(empty.password).toEqual(expect.any(String))
    expect(empty.password).not.toBe('')
    expect(empty.email).toBeUndefined()

    const spaced: SignInFormValues = { email: VALID_EMAIL, password: '  x  ' }
    expect(validateSignInForm(spaced)).toEqual({})
    expect(spaced.password).toBe('  x  ')
  })
})
