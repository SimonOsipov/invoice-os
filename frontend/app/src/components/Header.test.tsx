// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SANDBOX_DEFAULT } from '../App'
import type { PlatformCtx } from '../types'
import { Header } from './Header'

afterEach(cleanup)

// Header reads exactly five ctx fields. Typing them against the real PlatformCtx keeps a
// rename breaking the typecheck; the cast then stands in for the ~90 fields it never reads.
// `nav` is a real PlatformCtx field (App.tsx) so it joins the Pick; `setInvoiceQuery`
// (task-331, BUG-01-05) doesn't exist on PlatformCtx yet, so it's added as its own
// intersection member instead -- same idiom `active` below already uses -- which is what
// lets this widen without touching types.ts.
type HeaderCtx = Pick<PlatformCtx, 'view' | 'sandbox' | 'setSandbox' | 'openCreate' | 'nav'> & {
  active: Pick<PlatformCtx['active'], 'initials'>
  setInvoiceQuery: (q: string) => void
}

function headerCtx(over: {
  sandbox: boolean
  setSandbox?: () => void
  nav?: PlatformCtx['nav']
  setInvoiceQuery?: (q: string) => void
}) {
  const ctx: HeaderCtx = {
    active: { initials: 'OP' },
    view: 'dashboard',
    openCreate: () => {},
    setSandbox: over.setSandbox ?? vi.fn(),
    sandbox: over.sandbox,
    nav: over.nav ?? vi.fn(),
    setInvoiceQuery: over.setInvoiceQuery ?? vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

// Same accessible names the Playwright spec locates by. RTL matches `name` as an exact
// full string where Playwright matches a substring, so green here means green there.
const sandboxSeg = () => screen.getByRole('button', { name: 'SANDBOX' })
const newInvoiceBtn = () => screen.getByRole('button', { name: 'New invoice' })
const liveSeg = () => screen.getByTestId('env-pill-live')

describe('Header environment pill', () => {
  it('the app default renders SANDBOX pressed and LIVE not', () => {
    render(<Header ctx={headerCtx({ sandbox: SANDBOX_DEFAULT })} />)

    expect(sandboxSeg().getAttribute('aria-pressed')).toBe('true')
    expect(liveSeg().getAttribute('aria-pressed')).toBe('false')
  })

  it('the LIVE segment carries the real disabled attribute', () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)

    expect((liveSeg() as HTMLButtonElement).disabled).toBe(true)
  })

  it('clicking LIVE never calls setSandbox', async () => {
    const setSandbox = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setSandbox })} />)

    // pointerEventsCheck off: user-event otherwise refuses to dispatch at a disabled
    // control, which would pass the assertion without ever exercising the click.
    await userEvent.setup({ pointerEventsCheck: 0 }).click(liveSeg())

    expect(setSandbox).not.toHaveBeenCalled()
    expect(sandboxSeg().getAttribute('aria-pressed')).toBe('true')
  })

  it('Enter and Space on LIVE never call setSandbox', () => {
    const setSandbox = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setSandbox })} />)
    const live = liveSeg()

    live.focus()
    for (const key of ['Enter', ' ']) {
      fireEvent.keyDown(live, { key })
      fireEvent.keyUp(live, { key })
    }

    expect(setSandbox).not.toHaveBeenCalled()
    expect(document.activeElement).not.toBe(live)
  })

  it('Tab from SANDBOX skips LIVE and lands on New invoice', async () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)
    sandboxSeg().focus()

    await userEvent.setup().tab()

    expect(document.activeElement).not.toBe(liveSeg())
    expect(document.activeElement).toBe(newInvoiceBtn())
  })

  it('aria-pressed tracks the mode on both segments', () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)
    expect(sandboxSeg().getAttribute('aria-pressed')).toBe('true')
    expect(liveSeg().getAttribute('aria-pressed')).toBe('false')

    cleanup()

    render(<Header ctx={headerCtx({ sandbox: false })} />)
    expect(sandboxSeg().getAttribute('aria-pressed')).toBe('false')
    expect(liveSeg().getAttribute('aria-pressed')).toBe('true')
  })

  it('the LIVE dot is muted to the disabled foreground', () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)

    // jsdom's getComputedStyle does not resolve var(); el.style round-trips it.
    const dot = liveSeg().querySelector('span')
    expect(dot).not.toBeNull()
    expect(dot!.style.background).toBe('var(--fg-4)')
  })

  it('the LIVE segment exposes the forward-looking reason on title', () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)

    expect(liveSeg().getAttribute('title')).toContain('accreditation')
  })
})

// RED specs (task-331, BUG-01-05, Mode A): the search control is a `<span>` today, not an
// `<input>` -- every spec below fails on a real DOM assertion (an absent element, or a
// spy never called), never a compile error. Pinned testids the e2e layer also selects on.
describe('Header search field (BUG-01-05)', () => {
  it('renders exactly one input[type=text] capped at 200 characters', () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)

    const inputs = document.querySelectorAll('input[type="text"]')
    expect(inputs, 'zero text inputs render today').toHaveLength(1)
    expect((inputs[0] as HTMLInputElement).maxLength).toBe(200)
  })

  it('submitting the search sets the query and navigates to invoices', async () => {
    const setInvoiceQuery = vi.fn()
    const nav = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, nav, setInvoiceQuery })} />)

    const input = screen.queryByTestId('invoice-search-input')
    expect(input, 'invoice-search-input does not exist yet').not.toBeNull()
    await userEvent.type(input!, 'INV-9001{Enter}')

    expect(setInvoiceQuery).toHaveBeenCalledTimes(1)
    expect(setInvoiceQuery).toHaveBeenCalledWith('INV-9001')
    expect(nav).toHaveBeenCalledTimes(1)
    expect(nav).toHaveBeenCalledWith('invoices')
  })

  it('clicking clear emits an empty query', async () => {
    const setInvoiceQuery = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setInvoiceQuery })} />)

    const input = screen.queryByTestId('invoice-search-input')
    expect(input, 'invoice-search-input does not exist yet').not.toBeNull()
    await userEvent.type(input!, 'abc')

    const clearBtn = screen.queryByTestId('invoice-search-clear')
    expect(clearBtn, 'invoice-search-clear does not exist yet').not.toBeNull()
    await userEvent.click(clearBtn!)

    expect(setInvoiceQuery).toHaveBeenCalledWith('')
  })
})
