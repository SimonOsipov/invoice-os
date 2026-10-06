// @vitest-environment jsdom
// SignInLoading reads "Opening your workspace…".
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { SignIn, SignInLoading } from './SignIn'

afterEach(cleanup)

describe('SignInLoading (AUTH-05 D9)', () => {
  it('reads "Opening your workspace…"', () => {
    render(<SignInLoading />)
    expect(screen.getByText('Opening your workspace…')).toBeTruthy()
    expect(screen.queryByText(/Signing in as/)).toBeNull()
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
