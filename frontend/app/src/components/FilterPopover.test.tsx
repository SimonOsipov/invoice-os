// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// AUDIT-07-02 RED spec (Test-Spec, Mode A). FilterPopover.tsx is a stub that renders null
// -- every test below fails on the component not existing yet, never on setup/import.

import { dirname, join } from 'node:path'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
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
})
