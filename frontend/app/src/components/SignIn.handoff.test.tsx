// @vitest-environment jsdom
// SignInLoading reads "Opening your workspace…".
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { SignInLoading } from './SignIn'

afterEach(cleanup)

describe('SignInLoading (AUTH-05 D9)', () => {
  it('reads "Opening your workspace…"', () => {
    render(<SignInLoading />)
    expect(screen.getByText('Opening your workspace…')).toBeTruthy()
    expect(screen.queryByText(/Signing in as/)).toBeNull()
  })
})
