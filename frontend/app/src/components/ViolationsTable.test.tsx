// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// jsdom always reports scrollWidth === clientWidth === 0 -- real overflow geometry is
// proven only by e2e (invoice-surfaces.spec.ts), never here.
//
// RED specs (BUG-13-01, Mode A). Deliberately green today, and said so rather than
// discovered: violationsTable_longMessageRendersInFull (AC-4's only no-truncation
// oracle -- D6 was deleted because this holds it), violationsTable_wrapperStill-
// DeclaresOverflowXAuto ([narrow-band]'s failsafe pin) and violationsTable_cleanPass-
// BlockIsUnchanged (an Out of Scope guard). The other five fail on their assertions.

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Violation } from '../lib/validationApi'
import { ViolationsTable } from './ViolationsTable'

// 104 chars of [A-Za-z0-9_]: no space, hyphen, dot or bracket, so UAX #14 offers no break
// opportunity and the string can only wrap under overflow-wrap:anywhere. BUG-13-03's e2e
// stub carries its own copy -- different package, no import path.
const UNBREAKABLE_MESSAGE =
  'total_arithmetic_violation_subtotal_plus_vat_must_equal_total_computed_1234567_declared_1234599_delta_32'

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
  // [narrow-band]: the scroll recipe goes, this one declaration stays. Below 1180px the
  // card's overflow:hidden would turn the overflow into an unrecoverable clip.
  it('violationsTable_wrapperStillDeclaresOverflowXAuto', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const table = screen.getByRole('table')
    const wrapper = table.parentElement as HTMLElement
    expect(wrapper.style.overflowX).toBe('auto')
    expect(wrapper.style.overflow).not.toBe('hidden')
  })

  it('violationsTable_declaresNoMinimumWidth', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const table = screen.getByRole('table') as HTMLElement
    expect(table.style.minWidth).toBe('')
    expect(table.style.width).toBe('100%')
  })

  it('violationsTable_cleanPassBlockIsUnchanged', () => {
    render(<ViolationsTable violations={[]} ruleSetVersion={4} />)

    expect(screen.getByText(/Passes all rules/).textContent).toBe('Passes all rules — no violations. Evaluated against rule-set v4.')
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('violationsTable_rendersFourHeadersInOrder', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const headers = screen.getAllByRole('columnheader').map((th) => th.textContent)
    expect(headers).toEqual(['Severity', 'Message', 'Rule key', 'Path'])
  })

  it('violationsTable_everyRowHasFourCells', () => {
    render(
      <ViolationsTable
        violations={[violation(), violation({ rule_key: 'vat-standard-rate', path: undefined, message: 'second violation' })]}
        ruleSetVersion={3}
      />,
    )

    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    for (const row of rows) expect(within(row).getAllByRole('cell')).toHaveLength(4)
  })

  it('violationsTable_messageCellDeclaresLineHeight', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const cell = screen.getByText('Total does not equal subtotal plus VAT').closest('td')
    expect(cell).not.toBeNull()
    expect((cell as HTMLElement).style.lineHeight).toBe('1.5')
  })

  it('violationsTable_longMessageRendersInFull', () => {
    expect(UNBREAKABLE_MESSAGE).toHaveLength(104)
    expect(UNBREAKABLE_MESSAGE).toMatch(/^[A-Za-z0-9_]+$/)
    render(<ViolationsTable violations={[violation({ message: UNBREAKABLE_MESSAGE })]} ruleSetVersion={3} />)

    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(1)
    expect(within(rows[0]).getByText(UNBREAKABLE_MESSAGE).textContent).toBe(UNBREAKABLE_MESSAGE)
  })

  it('violationsTable_scrollAffordanceIsGone', () => {
    render(<ViolationsTable violations={[violation()]} ruleSetVersion={3} />)

    const wrapper = screen.getByRole('table').parentElement as HTMLElement
    expect(screen.queryByTestId('violations-scroll')).toBeNull()
    // hasAttribute, not .tabIndex: a plain <div> reports -1 with or without the attribute.
    expect(wrapper.hasAttribute('tabindex')).toBe(false)
    expect(wrapper.hasAttribute('role')).toBe(false)
    expect(wrapper.hasAttribute('aria-label')).toBe(false)
    expect(wrapper.classList.contains('pf-scroll-x')).toBe(false)
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

  // QA Mode B (BUG-13-01). violationsTable_longMessageRendersInFull reads textContent, so
  // it survives a CSS truncation: adding whiteSpace:'nowrap' + overflow:'hidden' +
  // textOverflow:'ellipsis' to this cell reddens NOTHING in jsdom (measured), yet it is
  // exactly the "never truncated with an ellipsis or clipped" Core AC-4 forbids. jsdom
  // applies no CSS, so declaration is the only thing this layer can observe. e2e D7
  // (BUG-13-03) is the rendered oracle; this is the cheap one that fires first.
  it('violationsTable_messageCellDeclaresNoTruncation', () => {
    render(<ViolationsTable violations={[violation({ message: UNBREAKABLE_MESSAGE })]} ruleSetVersion={3} />)

    const cell = screen.getByText(UNBREAKABLE_MESSAGE).closest('td') as HTMLElement
    expect(cell).not.toBeNull()
    expect(cell.style.textOverflow).toBe('')
    expect(cell.style.whiteSpace).not.toBe('nowrap')
    expect(cell.style.overflow).not.toBe('hidden')
    expect(cell.style.maxHeight).toBe('')
    expect(cell.style.getPropertyValue('-webkit-line-clamp')).toBe('')
  })

  // QA Mode B (BUG-13-01). severityStyle falls back to MUTED_STYLE on an unmapped value.
  // Every prior case used a mapped severity, so the fallback row's shape was unpinned:
  // a row that lost its badge would still have four cells, but not four RENDERED ones.
  it('violationsTable_unknownSeverityStillRendersAFullRow', () => {
    render(
      <ViolationsTable
        violations={[violation({ severity: 'critical' as unknown as Violation['severity'], rule_key: 'unmapped.rule', path: '$.unmapped' })]}
        ruleSetVersion={3}
      />,
    )

    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(1)
    const cells = within(rows[0]).getAllByRole('cell')
    expect(cells).toHaveLength(4)
    expect(cells[0].textContent).toBe('Info')
    expect(cells[1].textContent).toBe('Total does not equal subtotal plus VAT')
    expect(cells[2].textContent).toBe('unmapped.rule')
    expect(cells[3].textContent).toBe('$.unmapped')
  })

  // QA Mode B (BUG-13-01). The file's only overflow-wrap oracle: every text cell of every
  // row of a wide set, so a declaration applied per-index rather than per-row still fails.
  // The length floor stops the loop passing vacuously.
  it('violationsTable_everyRowOfManyDeclaresTheWrapOnAllThreeTextCells', () => {
    const many = Array.from({ length: 25 }, (_, i) =>
      violation({ rule_key: `rule_${i}_unbroken_key`, path: i % 3 === 0 ? undefined : `$_line_${i}_total`, message: `${UNBREAKABLE_MESSAGE}_${i}` }),
    )
    render(<ViolationsTable violations={many} ruleSetVersion={3} />)

    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(25)
    for (const row of rows) {
      const cells = within(row).getAllByRole('cell') as HTMLElement[]
      expect(cells).toHaveLength(4)
      // Severity (index 0) is a badge, not free text -- it is deliberately not in this set.
      for (const cell of cells.slice(1)) expect(cell.style.overflowWrap).toBe('anywhere')
    }
    // The em-dash placeholder rides the same wrapping cell as a real path.
    const placeholderCells = rows.map((r) => within(r).getAllByRole('cell')[3]).filter((c) => c.textContent === '—')
    expect(placeholderCells.length).toBeGreaterThan(0)
    for (const cell of placeholderCells) expect((cell as HTMLElement).style.overflowWrap).toBe('anywhere')
  })

  // Table B. Soft assertions: every new value shows red on its own line.
  it('compliance pass and not-validated are plain text', () => {
    render(<ViolationsTable violations={[]} ruleSetVersion={4} />)

    const verdict = screen.getByText(/Passes all rules/)
    expect.soft(verdict.style.background, 'no green fill').toBe('')
    expect.soft(verdict.style.border, 'no green border').toBe('')
    expect.soft(verdict.style.borderRadius, 'no corner').toBe('')
    expect.soft(verdict.style.fontSize).toBe('13px')
    expect.soft(verdict.style.color).toBe('var(--fg-2)')
  })

  it('a severity badge is a 4px badge with no dot; rows follow v2', () => {
    render(
      <ViolationsTable
        violations={[
          violation(),
          violation({ severity: 'warning', rule_key: 'vat-standard-rate', message: 'second violation', path: '$.vat' }),
          violation({ severity: 'critical' as unknown as Violation['severity'], rule_key: 'unmapped.rule', message: 'third violation', path: '$.unmapped' }),
        ]}
        ruleSetVersion={3}
      />,
    )

    const headers = screen.getAllByRole('columnheader') as HTMLElement[]
    expect(headers).toHaveLength(4)
    for (const th of headers) {
      expect.soft(th.className.split(' '), `${th.textContent} header is a .label`).toContain('label')
      expect.soft(th.style.padding, `${th.textContent} header padding`).toBe('8px 12px')
    }

    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(3)
    for (const row of rows) {
      const cells = within(row).getAllByRole('cell') as HTMLElement[]
      expect(cells).toHaveLength(4)
      const badge = cells[0].firstElementChild as HTMLElement
      expect(badge, 'the severity cell holds a badge').not.toBeNull()
      const label = badge.textContent
      expect(['Error', 'Warning', 'Info'], `badge reads its severity, got ${label}`).toContain(label)
      expect.soft(badge.style.borderRadius, `${label} badge radius`).toBe('var(--radius-sm)')
      expect.soft(badge.style.padding, `${label} badge padding`).toBe('2px 7px')
      expect.soft(
        Array.from(badge.children).filter((c) => (c.textContent ?? '') === ''),
        `${label} badge has no empty dot element`,
      ).toHaveLength(0)
      for (const cell of cells) expect.soft(cell.style.padding, `${label} row cell padding`).toBe('10px 12px')
      expect.soft(cells[1].style.color, `${label} message colour`).toBe('var(--fg-1)')
      expect.soft(cells[1].style.fontSize, `${label} message size`).toBe('12.5px')
      const key = cells[2].firstElementChild as HTMLElement
      expect.soft(key.style.color, `${label} rule key colour`).toBe('var(--fg-2)')
    }
  })
})

describe('ViolationsTable line links (ENGI-16-03)', () => {
  const linePath = { path: 'line_items[2].unit_price' }

  it('violationsTable_linePathRendersAButton', () => {
    const onOpenLine = vi.fn()
    render(<ViolationsTable violations={[violation(linePath)]} ruleSetVersion={3} onOpenLine={onOpenLine} />)

    const btn = screen.getByTestId('violation-open-line')
    expect(btn.textContent).toBe('Line 2')
    fireEvent.click(btn)
    expect(onOpenLine).toHaveBeenCalledWith({ line: 2, field: 'unit_price' })
  })

  it('violationsTable_disabledLineButton', () => {
    const onOpenLine = vi.fn()
    render(<ViolationsTable violations={[violation(linePath)]} ruleSetVersion={3} onOpenLine={onOpenLine} lineDisabled />)

    const btn = screen.getByTestId('violation-open-line') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(btn.style.opacity).toBe('0.45')
    expect(btn.style.cursor).toBe('not-allowed')
    fireEvent.click(btn)
    expect(onOpenLine).not.toHaveBeenCalled()
  })

  it('violationsTable_nonLinePathsStayText', () => {
    render(
      <ViolationsTable
        violations={[violation({ path: 'line_items' }), violation({ path: 'subtotal' }), violation({ path: undefined })]}
        ruleSetVersion={3}
        onOpenLine={vi.fn()}
      />,
    )

    expect(screen.queryByTestId('violation-open-line')).toBeNull()
    const cells = screen.getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell')[3].textContent)
    expect(cells).toEqual(['line_items', 'subtotal', '—'])
  })

  it('violationsTable_noHandlerKeepsText', () => {
    render(<ViolationsTable violations={[violation({ path: 'line_items[2]' })]} ruleSetVersion={3} />)

    expect(screen.queryByTestId('violation-open-line')).toBeNull()
    expect(within(screen.getAllByRole('row')[1]).getAllByRole('cell')[3].textContent).toBe('line_items[2]')
  })
})
