// @vitest-environment jsdom
// SignInLoading with and without a persona.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { SignIn, SignInLoading } from './SignIn'

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

describe('SignIn brand mark', () => {
  const markOf = (container: HTMLElement) => container.querySelector('img[aria-hidden="true"]')

  it('the picker card carries the 20px mark', () => {
    const { container } = render(<SignIn signingIn={null} onPick={() => {}} />)
    expect(markOf(container)?.getAttribute('width')).toBe('20')
    expect(markOf(container)?.getAttribute('height')).toBe('20')
  })

  it('the loading card carries the 20px mark', () => {
    const { container } = render(<SignInLoading />)
    expect(markOf(container)?.getAttribute('width')).toBe('20')
    expect(markOf(container)?.getAttribute('height')).toBe('20')
  })
})
