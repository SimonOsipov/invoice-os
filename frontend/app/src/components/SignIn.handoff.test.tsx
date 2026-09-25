// @vitest-environment jsdom
// SignInLoading with and without a persona.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { SignInLoading } from './SignIn'

afterEach(cleanup)

describe('SignInLoading (AUTH-05 D9)', () => {
  it('with no persona reads "Opening your workspace…"', () => {
    render(<SignInLoading />)
    expect(screen.getByText('Opening your workspace…')).toBeTruthy()
    expect(screen.queryByText(/Signing in as/)).toBeNull()
  })

  it('with a persona reads "Signing in as <name>…"', () => {
    render(<SignInLoading persona={APP_PERSONAS.inhouse} />)
    expect(screen.getByText(`Signing in as ${APP_PERSONAS.inhouse.name}…`)).toBeTruthy()
    expect(screen.queryByText('Opening your workspace…')).toBeNull()
  })
})
