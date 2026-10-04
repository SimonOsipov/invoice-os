// @vitest-environment jsdom
// jsdom keeps a var() border colour only in a lone `border` shorthand, so the spinner's
// 2px ring is read deployed (OV-03), not here.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { EmptyState, ErrorState, Loading } from './ViewStates'

afterEach(cleanup)

const style = (el: Element | null) => el?.getAttribute('style') ?? ''
const withStatus = new ApiError('http', 'boom', 503)
const noStatus = new ApiError('network', 'boom')

function child(el: Element | null, i = 0): HTMLElement {
  expect(el).not.toBeNull()
  const c = el!.children[i] as HTMLElement | undefined
  expect(c).toBeDefined()
  return c!
}

describe('Loading', () => {
  it('VS-01 Loading is an inline row with a teal spinner', () => {
    const { container } = render(<Loading label="Loading x…" />)
    const root = container.firstElementChild
    expect(root).not.toBeNull()
    for (const decl of ['display: flex', 'align-items: center', 'gap: 10px', 'padding: 40px 0px', 'color: var(--fg-3)', 'font-size: 13px']) {
      expect(style(root)).toContain(decl)
    }
    const spinner = child(root, 0)
    for (const decl of [
      'width: 16px',
      'height: 16px',
      'flex: 0 0 auto',
      'border-radius: 50%',
      'border-top-color: var(--action)',
      'animation: spin 700ms linear infinite',
    ]) {
      expect(style(spinner)).toContain(decl)
    }
    expect(screen.getByText('Loading x…')).toBeTruthy()
  })

  it('VS-02 Loading without a label draws only the spinner (boundary)', () => {
    const labelled = render(<Loading label="Loading x…" />)
    expect(labelled.container.firstElementChild?.children.length).toBe(1)
    expect(labelled.container.textContent).toBe('Loading x…')
    cleanup()

    const { container } = render(<Loading />)
    const root = container.firstElementChild
    expect(root).not.toBeNull()
    expect(root!.textContent).toBe('')
    expect(root!.children.length).toBe(1)
  })
})

describe('ErrorState', () => {
  it('VS-03 ErrorState is a white 6px card with the api-client copy', () => {
    const spy = vi.fn()
    const { container } = render(<ErrorState error={withStatus} onRetry={spy} />)
    const card = container.firstElementChild
    expect(card).not.toBeNull()
    for (const decl of ['background: var(--bg-2)', 'border: 1px solid var(--line-1)', 'border-radius: var(--radius-md)', 'padding: 28px', 'max-width: 520px']) {
      expect(style(card)).toContain(decl)
    }

    const title = screen.getByText('Something went wrong')
    expect(style(title)).toContain('font-size: 15px')
    expect(style(title)).toContain('font-weight: 700')

    const msg = screen.getByText('boom')
    expect(msg.tagName).toBe('P')
    expect(style(msg)).toContain('overflow-wrap: anywhere')

    expect(screen.getByText('HTTP 503').classList.contains('mono')).toBe(true)

    const retry = screen.getByRole('button', { name: 'Retry' })
    expect(retry.getAttribute('class')?.split(/\s+/).sort()).toEqual(['pf-btn', 'v2-btn', 'v2-btn-ghost'])
    expect(style(retry)).toContain('height: 34px')
    expect(spy).not.toHaveBeenCalled()
    fireEvent.click(retry)
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('VS-04 ErrorState drops the status line and the button when absent (error row)', () => {
    // Real ApiError carries status null; a bare object carries undefined. Both drop the line.
    for (const error of [noStatus, { message: 'boom' } as ApiError]) {
      const { container } = render(<ErrorState error={error} />)
      expect(screen.queryByText('Something went wrong')).not.toBeNull()
      expect(screen.queryByText('boom')).not.toBeNull()
      expect(screen.queryByText(/^HTTP /)).toBeNull()
      expect(screen.queryByRole('button')).toBeNull()
      expect(container.firstElementChild).not.toBeNull()
      cleanup()
    }

    render(<ErrorState error={withStatus} onRetry={() => {}} />)
    expect(screen.queryByText(/^HTTP /)).not.toBeNull()
    expect(screen.queryByRole('button')).not.toBeNull()
  })
})

describe('EmptyState', () => {
  it('VS-05 EmptyState list card with children', () => {
    const { container } = render(
      <EmptyState title="T" message="M">
        <button>Go</button>
      </EmptyState>,
    )
    const card = container.firstElementChild
    expect(card).not.toBeNull()
    for (const decl of ['background: var(--bg-2)', 'border: 1px dashed var(--line-3)', 'border-radius: var(--radius-md)', 'padding: 56px']) {
      expect(style(card)).toContain(decl)
    }

    const tile = child(card, 0)
    for (const decl of ['width: 44px', 'border-radius: var(--radius-md)', 'background: var(--bg-3)']) {
      expect(style(tile)).toContain(decl)
    }
    expect(tile.querySelector('svg')?.getAttribute('width')).toBe('18')

    const title = screen.getByText('T')
    expect(style(title)).toContain('font-size: 16px')
    expect(style(title)).toContain('font-weight: 700')
    const msg = screen.getByText('M')
    expect(style(msg)).toContain('font-size: 14px')
    expect(style(msg)).toContain('max-width: 360px')

    const button = screen.getByRole('button', { name: 'Go' })
    expect(card!.contains(button)).toBe(true)
    expect(msg.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('VS-06 EmptyState dense (dashboard idle, onboarding)', () => {
    const { container } = render(<EmptyState dense title="T" message="M" />)
    const card = container.firstElementChild
    expect(card).not.toBeNull()
    expect(style(card)).toContain('border: 1px dashed var(--line-3)')
    expect(style(card)).toContain('padding: 48px')
    expect(style(card)).not.toContain('background')
    expect(style(child(card, 0))).toContain('width: 40px')
    expect(style(screen.getByText('T'))).toContain('font-size: 15px')
    const msg = screen.getByText('M')
    for (const decl of ['font-size: 13px', 'max-width: 460px', 'line-height: 1.55']) {
      expect(style(msg)).toContain(decl)
    }
    expect(screen.queryByRole('button')).toBeNull()

    // Control: the non-dense card keeps its background and a child renders.
    cleanup()
    const list = render(
      <EmptyState title="T" message="M">
        <button>Go</button>
      </EmptyState>,
    )
    expect(style(list.container.firstElementChild)).toContain('background: var(--bg-2)')
    expect(screen.queryByRole('button')).not.toBeNull()
  })

  it('VS-07 a passed glyph replaces the default', () => {
    const def = render(<EmptyState title="T" />)
    expect(child(def.container.firstElementChild, 0).querySelector('svg')).not.toBeNull()
    cleanup()

    const { container } = render(<EmptyState title="T" glyph={<i data-testid="g" />} />)
    const tile = child(container.firstElementChild, 0)
    expect(tile.contains(screen.getByTestId('g'))).toBe(true)
    expect(tile.querySelector('svg')).toBeNull()
  })

  it('VS-09 EmptyState partial props (boundary)', () => {
    const titleOnly = render(<EmptyState title="T" />)
    expect(screen.queryByText('T')).not.toBeNull()
    expect(titleOnly.container.firstElementChild).not.toBeNull()
    expect(titleOnly.container.querySelector('p')).toBeNull()
    cleanup()

    const bold = (c: HTMLElement) => [...c.querySelectorAll('*')].filter((e) => style(e).includes('font-weight: 700'))
    const both = render(<EmptyState title="T" message="M" />)
    expect(bold(both.container).length).toBeGreaterThan(0)
    cleanup()
    const messageOnly = render(<EmptyState message="M" />)
    expect(screen.queryByText('M')).not.toBeNull()
    expect(bold(messageOnly.container).length).toBe(0)
    cleanup()

    const withChild = render(
      <EmptyState title="T">
        <button>Go</button>
      </EmptyState>,
    )
    const button = screen.getByRole('button', { name: 'Go' })
    expect(withChild.container.firstElementChild!.contains(button)).toBe(true)
    expect(screen.getByText('T').compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    cleanup()

    render(<EmptyState message="M" />)
    expect(style(screen.getByText('M'))).toContain('max-width: 360px')
    cleanup()
    render(<EmptyState message="M" messageMaxWidth={320} />)
    expect(style(screen.getByText('M'))).toContain('max-width: 320px')
    cleanup()
    render(<EmptyState dense message="M" messageMaxWidth={320} />)
    expect(style(screen.getByText('M'))).toContain('max-width: 320px')
  })
})

describe('v1 vocabulary', () => {
  it('VS-08 no v1 vocabulary (boundary)', () => {
    const html = [
      render(<Loading label="Loading x…" />).container.outerHTML,
      render(<ErrorState error={withStatus} onRetry={() => {}} />).container.outerHTML,
      render(
        <EmptyState title="T" message="M">
          <button>Go</button>
        </EmptyState>,
      ).container.outerHTML,
    ].join('')
    expect(html).toContain('var(--action)')
    for (const banned of ['oklch', '99px', '999px', '--radius-xl', '--radius-pill', 'var(--accent)', '--fg-4']) {
      expect(html).not.toContain(banned)
    }
  })
})
