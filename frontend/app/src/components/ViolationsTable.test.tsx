// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// jsdom always reports scrollWidth === clientWidth === 0 -- real overflow geometry is
// proven only by e2e (invoice-surfaces.spec.ts / validation.spec.ts), never here.

import { cleanup, render, screen, within } from '@testing-library/react'
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

  // QA adversarial: the two style tests above key off table.parentElement, which stays
  // correct even if overflowX landed on the wrong div. Pin the testid itself to that same
  // element so a future refactor can't move data-testid="violations-scroll" onto a decoy
  // ancestor (e.g. the InvoiceDetail.tsx "violations-table" wrapper, which has no overflow
  // property of its own) without failing here.
  it('the violations-scroll testid is on the element that actually gets overflowX, not a decoy ancestor', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const scrollEl = screen.getByTestId('violations-scroll')
    const table = screen.getByRole('table')
    expect(scrollEl).toBe(table.parentElement)
    expect(scrollEl.style.overflowX).toBe('auto')
  })

  // QA adversarial: every prior case used exactly one violation. Multiple rows and an
  // absent path (v.path ?? '—') had zero coverage before this file existed.
  it('renders one row per violation in order, with a long message intact and a missing path falling back to the placeholder', () => {
    const longMessage = 'Total does not equal subtotal plus VAT. '.repeat(20).trim()
    render(
      <ViolationsTable
        violations={[
          violation({ rule_key: 'total.arithmetic', path: '$.total', message: longMessage }),
          violation({ rule_key: 'vat-standard-rate', path: undefined, message: 'second violation' }),
        ]}
        ruleSetVersion={3}
      />,
    )

    const table = screen.getByRole('table')
    const [firstRow, secondRow] = within(table).getAllByRole('row').slice(1)

    expect(within(firstRow).getByText(longMessage)).toBeDefined()
    expect(within(firstRow).getAllByRole('cell')[3].textContent).toBe('$.total')
    expect(within(secondRow).getAllByRole('cell')[3].textContent).toBe('—')
  })

  // CodeRabbit (PR #138): browsers don't universally make an overflowing container
  // focusable, so a keyboard-only user couldn't reach Path/Rule-set version at the
  // detail-rail width (Core AC #1's "reachable" includes by keyboard).
  it('the scroll wrapper is keyboard-focusable with an accessible name', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const scrollEl = screen.getByTestId('violations-scroll')
    expect(scrollEl.tabIndex).toBe(0)
    expect(scrollEl.getAttribute('aria-label')).toBeTruthy()
  })

  // role="region" + aria-label registers as a landmark, which ValidationView.tsx's
  // full-width (never-overflowing) mount would also pick up -- AC-5 bars any playground
  // presentation change, and a spurious landmark is one screen readers surface. "group"
  // supports an author-supplied name without landmark status.
  it('the scroll wrapper uses a non-landmark role, so the playground gets no spurious landmark', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const scrollEl = screen.getByTestId('violations-scroll')
    expect(scrollEl.getAttribute('role')).not.toBe('region')
  })
})
