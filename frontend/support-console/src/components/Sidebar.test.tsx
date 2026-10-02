import { isValidElement, type ReactElement, type ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { signOut } from '../auth'
import { Sidebar } from './Sidebar'

vi.mock('@invoice-os/console-session', () => ({ StaffGate: () => null, signOutConsole: vi.fn() }))

type ButtonEl = ReactElement<Record<string, unknown>>

// Sidebar is called as a function, so only host elements are walked.
function buttons(node: ReactNode, out: ButtonEl[] = []): ButtonEl[] {
  if (Array.isArray(node)) node.forEach((n) => buttons(n, out))
  else if (isValidElement<Record<string, unknown>>(node)) {
    if (node.type === 'button') out.push(node)
    buttons(node.props.children as ReactNode, out)
  }
  return out
}

describe('Sidebar', () => {
  it('Sidebar_signOutButtonKeepsItsAttributesAndIsWiredToSignOut', () => {
    const all = buttons(Sidebar({ screen: 'submissions', onNavigate: () => undefined, deadLetterCount: 0 }))
    expect(all.length, 'sidebar buttons').toBeGreaterThan(1)

    const wired = all.filter((b) => b.props.onClick === signOut)
    expect(wired, 'buttons whose onClick is signOut').toHaveLength(1)
    const btn = wired[0]
    expect(btn.props['aria-label']).toBe('Sign out')
    expect(btn.props.title).toBe('Sign out')
    expect(btn.props.className).toBe('ops-btn ops-hide-narrow')
    expect(btn.props.type).toBe('button')
    expect(all.filter((b) => b.props['aria-label'] === 'Sign out')).toHaveLength(1)
  })
})
