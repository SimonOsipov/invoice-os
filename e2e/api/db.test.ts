// RED-then-GREEN spec (M4-21-10, AC-6) — pins requireDbInCI's existing hard-fail-in-CI
// contract (M4-04-08) so this story's target-discovery rework can't silently regress it.
import { afterEach, describe, expect, it } from 'vitest'

import { requireDbInCI } from './db'

afterEach(() => {
  delete process.env.CI
  delete process.env.DATABASE_SUPERUSER_URL_DEV
})

describe('requireDbInCI', () => {
  it('still throws in CI when the DSN is absent', () => {
    process.env.CI = '1'
    delete process.env.DATABASE_SUPERUSER_URL_DEV

    expect(() => requireDbInCI()).toThrow(/DATABASE_SUPERUSER_URL_DEV/)
  })

  it('returns silently when the DSN is set', () => {
    process.env.CI = '1'
    process.env.DATABASE_SUPERUSER_URL_DEV = 'postgres://example'

    expect(() => requireDbInCI()).not.toThrow()
  })
})
