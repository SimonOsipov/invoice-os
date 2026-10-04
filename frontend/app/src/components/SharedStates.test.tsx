// @vitest-environment jsdom
// Pins the shared Loading / ErrorState / EmptyState look on the components
// themselves. jsdom keeps var(...) strings as written, so rows assert tokens, not resolved colours.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, EmptyState, ErrorState, Loading } from '@invoice-os/api-client'

afterEach(cleanup)

// innerHTML of the no-new-prop call shapes, captured before the action/dense/messageMaxWidth props.
const TITLE_AND_MESSAGE =
  '<div style="background: var(--bg-2); border: 1px dashed var(--line-3); border-radius: var(--radius-md); padding: 56px; display: flex; flex-direction: column; align-items: center; text-align: center; font-family: var(--font-sans);"><span style="width: 44px; height: 44px; border-radius: var(--radius-md); background: var(--bg-3); color: var(--fg-3); display: grid; place-items: center; margin-bottom: 14px;"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="4" width="16" height="16" rx="2"></rect><path d="M4 9h16M9 4v16"></path></svg></span><div style="font-size: 16px; font-weight: 700; margin-bottom: 4px; color: var(--fg-1);">No entities yet</div><p style="font-size: 14px; color: var(--fg-3); margin: 0px; max-width: 340px;">Add your first business entity to get started.</p></div>'

const TITLE_ONLY =
  '<div style="background: var(--bg-2); border: 1px dashed var(--line-3); border-radius: var(--radius-md); padding: 56px; display: flex; flex-direction: column; align-items: center; text-align: center; font-family: var(--font-sans);"><span style="width: 44px; height: 44px; border-radius: var(--radius-md); background: var(--bg-3); color: var(--fg-3); display: grid; place-items: center; margin-bottom: 14px;"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="4" width="16" height="16" rx="2"></rect><path d="M4 9h16M9 4v16"></path></svg></span><div style="font-size: 16px; font-weight: 700; margin-bottom: 4px; color: var(--fg-1);">No invoice selected</div></div>'

const MESSAGE_ONLY =
  '<div style="background: var(--bg-2); border: 1px dashed var(--line-3); border-radius: var(--radius-md); padding: 56px; display: flex; flex-direction: column; align-items: center; text-align: center; font-family: var(--font-sans);"><span style="width: 44px; height: 44px; border-radius: var(--radius-md); background: var(--bg-3); color: var(--fg-3); display: grid; place-items: center; margin-bottom: 14px;"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="4" width="16" height="16" rx="2"></rect><path d="M4 9h16M9 4v16"></path></svg></span><p style="font-size: 14px; color: var(--fg-3); margin: 0px; max-width: 340px;">offline</p></div>'

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

  it('SS-06 props absent: markup is byte-identical (pin, green at write)', () => {
    const html = (el: ReactElement) => {
      const { container, unmount } = render(el)
      const out = container.innerHTML
      unmount()
      return out
    }
    expect(html(<EmptyState title="No entities yet" message="Add your first business entity to get started." />)).toBe(TITLE_AND_MESSAGE)
    expect(html(<EmptyState title="No invoice selected" />)).toBe(TITLE_ONLY)
    expect(html(<EmptyState message="offline" />)).toBe(MESSAGE_ONLY)
    expect(
      html(<EmptyState title="No entities yet" message="Add your first business entity to get started." dense={false} action={undefined} messageMaxWidth={undefined} />),
    ).toBe(TITLE_AND_MESSAGE)
    expect(html(<EmptyState title="No entities yet" message="Add your first business entity to get started." action={null} />)).toBe(TITLE_AND_MESSAGE)
  })

  it('SS-07 action renders inside the card, after the message', () => {
    const { container } = render(<EmptyState title="T" message="M" action={<button>Go</button>} />)
    const root = rootOf(container)
    const button = screen.getByRole('button', { name: 'Go' })
    const message = screen.getByText('M')

    expect(button.parentElement, 'the action is a child of the card').toBe(root)
    expect(root.lastElementChild, 'the action is the last child').toBe(button)
    expect(message.nextElementSibling, 'the action follows the message').toBe(button)
    expect(message.style.margin).toBe('0px 0px 20px')
    expect(root.style.padding, 'control: action is not dense').toBe('56px')
    expect(root.style.background).toBe('var(--bg-2)')
  })

  it('SS-08 dense is the prototype dense card', () => {
    const { container } = render(<EmptyState title="T" message="M" dense />)
    const root = rootOf(container)
    const tile = root.firstElementChild as HTMLElement
    expect(tile.querySelector('svg'), 'control: the first child is the icon tile').not.toBeNull()

    expect(root.style.padding).toBe('48px')
    expect(root.style.background).toBe('transparent')
    expect(root.style.border).toBe('1px dashed var(--line-3)')
    expect(root.style.borderRadius).toBe('var(--radius-md)')
    expect(tile.style.width).toBe('40px')
    expect(tile.style.height).toBe('40px')
    expect(tile.style.marginBottom).toBe('12px')
    const title = screen.getByText('T')
    expect(title.style.fontSize).toBe('15px')
    expect(title.style.fontWeight).toBe('700')
    const message = screen.getByText('M')
    expect(message.style.fontSize).toBe('13px')
    expect(message.style.lineHeight).toBe('1.55')
    expect(message.style.maxWidth).toBe('340px')
    expect(message.style.margin).toBe('0px')
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('SS-09 messageMaxWidth sets the message width only', () => {
    const { container } = render(<EmptyState title="T" message="M" messageMaxWidth={360} />)
    const message = screen.getByText('M')
    expect(message.style.maxWidth).toBe('360px')
    expect(message.style.fontSize).toBe('14px')
    expect(message.style.margin).toBe('0px')
    expect(rootOf(container).style.padding).toBe('56px')
  })

  it('SS-10 dense, action and width combine (boundary)', () => {
    const { container } = render(<EmptyState title="T" message="M" dense action={<button>Add</button>} messageMaxWidth={460} />)
    const root = rootOf(container)
    const message = screen.getByText('M')
    expect(message.style.maxWidth).toBe('460px')
    expect(message.style.margin).toBe('0px 0px 20px')
    expect(message.style.fontSize).toBe('13px')
    expect(root.lastElementChild).toBe(screen.getByRole('button', { name: 'Add' }))
    expect(root.style.padding).toBe('48px')
  })

  it('SS-11 action without a message (boundary)', () => {
    const { container } = render(<EmptyState title="T" action={<button>Go</button>} />)
    expect(container.querySelector('p'), 'no message element').toBeNull()
    const button = screen.getByRole('button', { name: 'Go' })
    expect(screen.getByText('T').nextElementSibling).toBe(button)
    expect(rootOf(container).lastElementChild).toBe(button)
  })
})
