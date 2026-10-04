// @vitest-environment jsdom
// Pins the shared Loading / ErrorState / EmptyState look on the components
// themselves. jsdom keeps var(...) strings as written, so rows assert tokens, not resolved colours.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, EmptyState, ErrorState, Loading } from '@invoice-os/api-client'

afterEach(cleanup)

function rootOf(container: HTMLElement): HTMLElement {
  return container.firstElementChild as HTMLElement
}

describe('SharedStates: Loading', () => {
  it('SS-01 Loading is an inline row with a 16px circle on an --action arc', () => {
    const { container } = render(<Loading label="Loading entities…" />)
    screen.getByText('Loading entities…')

    const root = rootOf(container)
    expect(root.style.display).toBe('flex')
    expect(root.style.alignItems).toBe('center')
    expect(root.style.gap).toBe('10px')
    expect(root.style.padding).toBe('40px 0px')
    expect(root.style.flexDirection, 'an inline row, not a column').toBe('')

    const spin = container.querySelector('.apic-loading-spin') as HTMLElement
    expect(spin, 'the .apic-loading-spin hook stays').not.toBeNull()
    expect(spin.style.width).toBe('16px')
    expect(spin.style.borderRadius).toBe('50%')
    expect(spin.style.borderTopColor).toBe('var(--action)')

    const label = screen.getByText('Loading entities…')
    expect(label.style.fontSize).toBe('13px')
    expect(label.style.color).toBe('var(--fg-3)')
  })
})

describe('SharedStates: ErrorState', () => {
  function renderError(onRetry?: () => void) {
    return render(<ErrorState error={new ApiError('http', 'The entity list could not be loaded.', 503)} onRetry={onRetry} />)
  }

  it('SS-02 ErrorState is a 6px --bg-2 card with no icon tile', () => {
    const { container } = renderError(vi.fn())
    screen.getByText('The entity list could not be loaded.')

    const root = rootOf(container)
    expect(root.style.background).toBe('var(--bg-2)')
    expect(root.style.border).toBe('1px solid var(--line-1)')
    expect(root.style.borderRadius).toBe('var(--radius-md)')
    expect(root.style.maxWidth).toBe('520px')
    expect(root.style.padding).toBe('28px')
    expect(container.querySelector('svg'), 'no icon tile').toBeNull()

    const title = screen.getByText('Something went wrong')
    expect(title.style.fontSize).toBe('15px')
    expect(title.style.fontWeight).toBe('700')
    expect(title.style.color, 'title reads --fg-1, as the prototype card inherits it').toBe('var(--fg-1)')

    const message = screen.getByText('The entity list could not be loaded.')
    expect(message.tagName).toBe('P')
    expect(message.style.fontSize).toBe('13px')
    expect(message.style.color).toBe('var(--fg-2)')

    const http = screen.getByText('HTTP 503')
    expect(http.classList.contains('mono')).toBe(true)
    expect(http.style.fontSize).toBe('11px')
    expect(http.style.color).toBe('var(--fg-3)')
  })

  it('SS-03 Retry is a ghost pf-btn that still retries', () => {
    const fn = vi.fn()
    renderError(fn)

    const retry = screen.getByRole('button', { name: 'Retry' })
    expect(retry.className).toBe('v2-btn v2-btn-ghost pf-btn')
    expect(retry.style.height).toBe('34px')
    fireEvent.click(retry)
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('SS-04 an error without a status renders no HTTP line (boundary)', () => {
    const { container } = render(<ErrorState error={new ApiError('network', 'offline')} />)
    screen.getByText('offline')

    expect(container.textContent).not.toMatch(/HTTP/)
    expect(screen.queryByRole('button')).toBeNull()
  })
})

describe('SharedStates: EmptyState', () => {
  it('SS-05 EmptyState is a 6px card with a 700 title', () => {
    const { container } = render(<EmptyState title="No entities yet" message="Add your first business entity to get started." />)
    const message = screen.getByText('Add your first business entity to get started.')

    const root = rootOf(container)
    const tile = root.firstElementChild as HTMLElement
    expect(tile.querySelector('svg'), 'control: the first child is the icon tile').not.toBeNull()
    expect(root.style.borderRadius).toBe('var(--radius-md)')
    expect(tile.style.borderRadius).toBe('var(--radius-md)')
    expect(screen.getByText('No entities yet').style.fontWeight).toBe('700')
    expect(message.style.maxWidth).toBe('340px')
    expect(root.style.border, 'the empty card stays dashed').toBe('1px dashed var(--line-3)')
    expect(root.style.padding).toBe('56px')
  })
})
