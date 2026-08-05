// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// jsdom always reports scrollWidth === clientWidth === 0 -- real overflow geometry is
// proven only by e2e (invoice-surfaces.spec.ts / validation.spec.ts), never here.

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { Violation } from '../lib/validationApi'
import { TABLE_MIN_WIDTH, ViolationsTable } from './ViolationsTable'

function violation(over: Partial<Violation> = {}): Violation {
  return {
    rule_key: 'total.arithmetic',
    severity: 'error',
    message: 'Total does not equal subtotal plus VAT',
    path: '$.total',
    ...over,
  }
}

afterEach(() => {
  cleanup()
})

describe('ViolationsTable', () => {
  it('the wrapper scrolls horizontally instead of clipping', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    // data-testid="violations-scroll" is added by the fix (plan step 4) -- key off the
    // table's parent so this asserts the style value, not element presence, before then.
    const table = screen.getByRole('table')
    const wrapper = table.parentElement as HTMLElement
    expect(wrapper.style.overflowX).toBe('auto')
    expect(wrapper.style.overflow).not.toBe('hidden')
  })

  it('the table declares a minimum width alongside its 100% width', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const table = screen.getByRole('table')
    expect((table as HTMLElement).style.minWidth).toBe(`${TABLE_MIN_WIDTH}px`)
    expect((table as HTMLElement).style.width).toBe('100%')
  })

  it('the clean-pass block renders with no table and no scroll wrapper', () => {
    render(<ViolationsTable violations={[]} ruleSetVersion={3} />)

    expect(screen.getByText(/Passes all rules/)).toBeDefined()
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByTestId('violations-scroll')).toBeNull()
  })

  it('the five headers are present in the fixed column order', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const headers = screen.getAllByRole('columnheader').map((th) => th.textContent)
    expect(headers).toEqual(['Severity', 'Message', 'Rule key', 'Path', 'Rule-set version'])
  })
})
