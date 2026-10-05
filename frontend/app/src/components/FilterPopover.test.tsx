// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { dirname, join } from 'node:path'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { FilterPopover } from './FilterPopover'

afterEach(cleanup)

const COMPONENTS_DIR = dirname(fileURLToPath(import.meta.url))

function renderPopover(open: boolean) {
  const onOpen = vi.fn()
  const onClose = vi.fn()
  const utils = render(
    <div>
      <div data-testid="outside-node">outside</div>
      <FilterPopover testId="fp" label="Test filter" open={open} onOpen={onOpen} onClose={onClose}>
        <div data-testid="fp-child">child</div>
      </FilterPopover>
    </div>,
  )
  return { ...utils, onOpen, onClose }
}

describe('FilterPopover', () => {
  it('filterPopover_chevronRotatesAndIsInlineSvg', () => {
    renderPopover(true)
    const openChevron = screen.getByTestId('fp-chevron')
    const svg = openChevron.querySelector('svg')
    expect(svg, 'chevron must be an inline svg').not.toBeNull()
    const openTransform = openChevron.style.transform
    cleanup()

    renderPopover(false)
    const closedChevron = screen.getByTestId('fp-chevron')
    const closedTransform = closedChevron.style.transform
    expect(openTransform, 'transform must differ between open and closed').not.toBe(closedTransform)

    // Source scan, floor first: prove the right file was read before asserting an absence.
    const src = readFileSync(join(COMPONENTS_DIR, 'FilterPopover.tsx'), 'utf8')
    expect(src.length, 'FilterPopover.tsx must be non-empty').toBeGreaterThan(0)
    expect(src, 'must contain chevDownGlyph').toContain('chevDownGlyph')
    expect(src, 'no background-image chevron').not.toMatch(/background-image/)
  })

  it('PR-02 the filter panel floats on shadow-card', () => {
    const { container } = renderPopover(true)
    const panel = screen.getByTestId('fp-panel')

    expect(panel.style.minWidth, 'control: the panel style is read').toBe('240px')
    expect(panel.style.boxShadow).toBe('var(--shadow-card)')
    expect(panel.style.borderRadius).toBe('var(--radius-md)')
    expect(screen.getByTestId('fp-trigger').classList.contains('pf-btn')).toBe(true)
    expect(container.innerHTML).not.toContain('oklch')
  })

  it('filterPopover_blockTriggerIsFullWidthAndForty', () => {
    render(
      <FilterPopover testId="fp" label="Company" open={false} onOpen={vi.fn()} onClose={vi.fn()} block>
        <div>body</div>
      </FilterPopover>,
    )
    const trigger = screen.getByTestId('fp-trigger')
    expect([trigger.style.width, trigger.style.height, trigger.style.justifyContent]).toEqual(['100%', '40px', ''])
    cleanup()

    renderPopover(false)
    const plain = screen.getByTestId('fp-trigger')
    expect([plain.style.width, plain.style.height, plain.style.padding]).toEqual(['', '34px', '0px 11px'])
  })

  it('filterPopover_triggerValuesFollowD9WithAndWithoutBlock', () => {
    render(
      <FilterPopover testId="fp" label="Company" summary="Acme" open={false} onOpen={vi.fn()} onClose={vi.fn()}>
        <div>body</div>
      </FilterPopover>,
    )
    const trigger = screen.getByTestId('fp-trigger')
    expect([trigger.style.gap, trigger.style.padding, trigger.style.fontSize, trigger.style.fontWeight]).toEqual(['8px', '0px 11px', '13px', '500'])
    const summary = screen.getByText('Acme')
    expect([summary.style.fontWeight, summary.style.color]).toEqual(['400', 'var(--fg-3)'])
    expect(screen.getByTestId('fp-chevron').style.color).toBe('var(--fg-3)')
    expect(trigger.style.justifyContent, 'without block the trigger packs left').toBe('')
    cleanup()

    render(
      <FilterPopover testId="fp" label="Company" summary="Acme" open={false} onOpen={vi.fn()} onClose={vi.fn()} block>
        <div>body</div>
      </FilterPopover>,
    )
    const block = screen.getByTestId('fp-trigger')
    expect([block.style.gap, block.style.padding, block.style.fontSize, block.style.fontWeight]).toEqual(['10px', '0px 12px', '13.5px', '400'])
    const blockSummary = screen.getByText('Acme')
    expect([blockSummary.style.flex, blockSummary.style.textOverflow]).toEqual(['1 1 0%', 'ellipsis'])
    const label = screen.getByText('Company')
    expect(block.contains(label), 'block draws the label above the trigger, not inside it').toBe(false)
    expect(block.getAttribute('aria-labelledby')).toBe('fp-label fp-summary')
  })

  it('filterPopover_enabledTriggerCarriesNoDisabledPaint', () => {
    renderPopover(false)
    const trigger = screen.getByTestId('fp-trigger')
    expect(trigger.style.cursor, 'control needle: an enabled trigger is a pointer').toBe('pointer')
    expect([trigger.style.opacity, trigger.style.filter]).toEqual(['', ''])
  })

  it('filterPopover_disabledKeepsPaintAndDims', () => {
    render(
      <FilterPopover testId="fp" label="Company" open={false} onOpen={vi.fn()} onClose={vi.fn()} disabled>
        <div>body</div>
      </FilterPopover>,
    )
    const trigger = screen.getByTestId('fp-trigger')
    expect([trigger.style.opacity, trigger.style.cursor, trigger.style.filter]).toEqual(['0.45', 'not-allowed', 'none'])
  })

  it('filterPopover_escapeCloses', () => {
    const { onClose } = renderPopover(true)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('filterPopover_outsideMousedownCloses', () => {
    const { onClose } = renderPopover(true)
    fireEvent.mouseDown(screen.getByTestId('outside-node'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('filterPopover_triggerClickClosesAnOpenPanel', () => {
    const { onClose } = renderPopover(true)
    fireEvent.click(screen.getByTestId('fp-trigger'))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('fp-panel'), 'panel must be gone after the trigger closes it').toBeNull()
  })

  // fireEvent.click above fires only 'click', never 'mousedown' -- it cannot see the bug
  // useDismiss.ts's own doc comment warns about (ref on the panel alone: mousedown-outside
  // dismisses, then the trigger's own click re-opens). userEvent.click fires the full
  // pointer sequence, so this is the oracle that actually depends on the ref wrapping the
  // trigger, per AC#3's "(the ref wraps the trigger)".
  it('filterPopover_triggerClickViaRealPointerSequenceClosesCleanly', async () => {
    const user = userEvent.setup()
    const { onClose } = renderPopover(true)
    await user.click(screen.getByTestId('fp-trigger'))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('fp-panel'), 'panel must not reappear from the click after mousedown dismissed it').toBeNull()
  })

  it('filterPopover_escapeWhenAlreadyClosedIsNoOp', () => {
    const { onClose } = renderPopover(false)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose, 'no listener is attached while closed').not.toHaveBeenCalled()
  })

  // AC#2 says "no background-image anywhere in the new files" (plural). The RED spec's
  // scan only covers FilterPopover.tsx and only the kebab-case CSS string -- every style
  // in both files is a JSX style object, whose property is camelCase `backgroundImage`,
  // which the kebab-case regex cannot see. This scan covers both files and both spellings.
  it('filterPopover_noBackgroundImageInEitherFileEitherSpelling', () => {
    const filterPopoverSrc = readFileSync(join(COMPONENTS_DIR, 'FilterPopover.tsx'), 'utf8')
    const auditFilterCardSrc = readFileSync(join(COMPONENTS_DIR, 'AuditFilterCard.tsx'), 'utf8')
    for (const [name, src] of [
      ['FilterPopover.tsx', filterPopoverSrc],
      ['AuditFilterCard.tsx', auditFilterCardSrc],
    ] as const) {
      expect(src.length, `${name} must be non-empty`).toBeGreaterThan(0)
      expect(src, `${name}: no kebab-case background-image`).not.toMatch(/background-image/)
      expect(src, `${name}: no camelCase backgroundImage`).not.toMatch(/backgroundImage/)
    }
  })
})
