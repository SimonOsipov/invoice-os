// @vitest-environment jsdom
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { SeverityPill, TypePill } from './RulePills'

afterEach(cleanup)

describe('RP-01: rule pills take the v2 corners (D-5)', () => {
  it('SeverityPill is radius-sm and keeps its tone border; TypePill is radius-md', () => {
    const { container } = render(
      <>
        <SeverityPill severity="error" />
        <TypePill type="format" />
      </>,
    )
    const [severity, type] = Array.from(container.children) as HTMLElement[]
    const sev = severity.getAttribute('style') ?? ''
    expect(sev).toContain('border-radius: var(--radius-sm)')
    expect(sev).toContain('border: 1px solid var(--status-red-border)')
    expect(sev).not.toContain('999px')
    const typ = type.getAttribute('style') ?? ''
    expect(typ).toContain('border-radius: var(--radius-md)')
    expect(typ).not.toContain('999px')
  })
})
