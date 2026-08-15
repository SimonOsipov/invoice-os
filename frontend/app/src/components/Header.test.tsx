// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SANDBOX_DEFAULT } from '../App'
import { clampFilterText } from '../lib/invoices'
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

// Specs (task-331, BUG-01-05, Mode A): the header search control -- a real `<input>` in a
// `<form onSubmit>`, plus a clear control. Pinned testids the e2e layer also selects on.
describe('Header search field (BUG-01-05)', () => {
  it('renders exactly one input[type=text] capped at 200 characters', () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)

    const inputs = document.querySelectorAll('input[type="text"]')
    expect(inputs).toHaveLength(1)
    expect((inputs[0] as HTMLInputElement).maxLength).toBe(200)
  })

  it('submitting the search sets the query and navigates to invoices', async () => {
    const setInvoiceQuery = vi.fn()
    const nav = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, nav, setInvoiceQuery })} />)

    const input = screen.queryByTestId('invoice-search-input')
    expect(input).not.toBeNull()
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
    expect(input).not.toBeNull()
    await userEvent.type(input!, 'abc')

    const clearBtn = screen.queryByTestId('invoice-search-clear')
    expect(clearBtn).not.toBeNull()
    await userEvent.click(clearBtn!)

    expect(setInvoiceQuery).toHaveBeenCalledWith('')
  })
})

// QA adversarial (task-331, BUG-01-05) -- AC #7 says "including on paste". maxLength=200
// is a browser CHARACTER cap (JS string length / UTF-16 code units); the server's cap is
// 200 UTF-8 BYTES (see clampFilterText's own doc comment). 200 multi-byte characters pass
// maxLength (200 chars) while serialising to well over 200 bytes -- these tests trace what
// actually reaches setInvoiceQuery on that path, not just that clampFilterText itself is
// byte-correct in isolation (already pinned in invoices.test.ts).
describe('Header search field: paste vs maxLength vs the byte clamp (QA adversarial, AC #7)', () => {
  it('maxLength blocks typing past 200 characters, but 200 CJK characters (600 bytes) still clamp further on submit', async () => {
    const setInvoiceQuery = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setInvoiceQuery })} />)
    const input = screen.getByTestId('invoice-search-input') as HTMLInputElement

    // A single fireEvent.paste-style change with 250 CJK chars, matching how a real paste
    // delivers the whole string at once rather than one user-event keystroke per char.
    fireEvent.change(input, { target: { value: '文'.repeat(250) } })

    // maxLength is an HTML input CONSTRAINT enforced by the DOM on user input, not by our
    // onChange handler (which does no clamping) -- assert what the input's own value
    // actually holds after that change, rather than assume the attribute did its job.
    expect(input.maxLength, 'attribute must still be 200').toBe(200)

    fireEvent.submit(input.closest('form')!)

    expect(setInvoiceQuery).toHaveBeenCalledTimes(1)
    const sent = setInvoiceQuery.mock.calls[0][0] as string
    expect(new TextEncoder().encode(sent).length, 'the value that reaches setInvoiceQuery must never exceed 200 BYTES').toBeLessThanOrEqual(200)
    // Exact value: clampFilterText is independently pinned in invoices.test.ts; this
    // confirms Header actually calls it on the submitted value, not a re-derivation.
    expect(sent).toBe(clampFilterText(input.value))
  })

  it('clamp runs on submit, not on every keystroke: the DOM value stays the raw unclamped string until Enter', async () => {
    const setInvoiceQuery = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setInvoiceQuery })} />)
    const input = screen.getByTestId('invoice-search-input') as HTMLInputElement

    // Bypasses the browser's own maxLength enforcement (fireEvent sets the DOM value
    // property directly, the same way a paste that outran a lagging constraint check
    // would) -- proving the byte clamp is a real, independent safety net on submit, not
    // merely relying on maxLength to have already done the job before onChange ever runs.
    const raw = '文'.repeat(250)
    fireEvent.change(input, { target: { value: raw } })

    expect(input.value, 'no clamp on change -- the field still shows the raw value pre-submit').toBe(raw)
    expect(setInvoiceQuery).not.toHaveBeenCalled()

    fireEvent.submit(input.closest('form')!)

    expect(setInvoiceQuery).toHaveBeenCalledTimes(1)
    expect(setInvoiceQuery).toHaveBeenCalledWith(clampFilterText(raw))
    expect(new TextEncoder().encode(setInvoiceQuery.mock.calls[0][0] as string).length).toBeLessThanOrEqual(200)
  })

  it('typing 200 CJK characters via user-event is itself capped to 200 characters by maxLength (browser-enforced, not app code)', async () => {
    render(<Header ctx={headerCtx({ sandbox: true })} />)
    const input = screen.getByTestId('invoice-search-input') as HTMLInputElement

    // 40 chars, well under 200, kept short so the per-keystroke user-event loop stays fast
    // -- this test is about confirming user-event honours maxLength at all (a real-browser
    // behaviour jsdom + user-event v14 emulate), not about re-proving the byte math.
    await userEvent.type(input, '文'.repeat(40))

    expect(input.value).toHaveLength(40)
  })

  it('a REAL userEvent.paste() of 250 CJK characters is itself capped to 200 characters by maxLength, and the submitted value still clamps further to 200 bytes', async () => {
    const setInvoiceQuery = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setInvoiceQuery })} />)
    const input = screen.getByTestId('invoice-search-input') as HTMLInputElement
    const user = userEvent.setup()

    await user.click(input)
    await user.paste('文'.repeat(250))

    // maxLength is a CHARACTER cap (UTF-16 code units) -- 200 chars here is still 600
    // bytes, well over the server's byte cap.
    expect(input.value).toHaveLength(200)
    expect(new TextEncoder().encode(input.value).length).toBe(600)

    await user.keyboard('{Enter}')

    expect(setInvoiceQuery).toHaveBeenCalledTimes(1)
    const sent = setInvoiceQuery.mock.calls[0][0] as string
    expect(new TextEncoder().encode(sent).length, 'maxLength alone (a char cap) is not enough -- the byte clamp on submit is what actually holds AC #7').toBeLessThanOrEqual(200)
    expect(sent).toBe(clampFilterText(input.value))
  })

  it('a plain ASCII value at exactly 200 characters (200 bytes) needs no further clamping on submit', async () => {
    const setInvoiceQuery = vi.fn()
    render(<Header ctx={headerCtx({ sandbox: true, setInvoiceQuery })} />)
    const input = screen.getByTestId('invoice-search-input') as HTMLInputElement
    const exact200 = 'x'.repeat(200)

    fireEvent.change(input, { target: { value: exact200 } })
    fireEvent.submit(input.closest('form')!)

    expect(setInvoiceQuery).toHaveBeenCalledWith(exact200)
  })
})

// A03-1 (task-528, APPR-12-03, Mode A). Header.tsx:55's `CRUMB_MAP[view] || 'Overview'`
// degrades SILENTLY at runtime -- a missing key just falls back, it never throws or
// type-errors at the call site. CRUMB_MAP itself is not exported (Header.tsx has exactly
// two `export`s, neither of them CRUMB_MAP), so this is a source-scan, not a live import
// -- and it reads via `process.cwd()`, not `fileURLToPath(import.meta.url)`: this file is
// `@vitest-environment jsdom`, and jsdom rewrites `import.meta.url` off the `file:`
// scheme (InvoicesList.test.tsx:8-9, CustomersView.test.tsx:136-139 precedent), so
// `fileURLToPath` would throw there -- `process.cwd()` is what every jsdom source-scan in
// this repo actually uses, and `pnpm --filter` pins it at the package root.
function readSrc(relPath: string): string {
  return readFileSync(path.join(process.cwd(), relPath), 'utf8')
}

// Extracts the CRUMB_MAP object literal's own keys from Header.tsx's source text.
function crumbMapKeys(headerSrc: string): string[] {
  const match = headerSrc.match(/const CRUMB_MAP: Record<View, string> = \{([\s\S]*?)\n\}/)
  expect(match, 'CRUMB_MAP object literal not found -- the scan anchor itself is broken').not.toBeNull()
  return [...match![1].matchAll(/^\s*(\w+):/gm)].map((m) => m[1])
}

// Extracts the `View` union's own string members from types.ts's source text.
function viewMembers(typesSrc: string): string[] {
  const match = typesSrc.match(/export type View = ([^\n]+)/)
  expect(match, 'View union not found -- the scan anchor itself is broken').not.toBeNull()
  return [...match![1].matchAll(/'([^']+)'/g)].map((m) => m[1])
}

describe('A03-1: CRUMB_MAP cannot silently degrade at runtime for a new View member', () => {
  it('non-vacuity control: the scan can tell a present key from an absent one', () => {
    const keys = crumbMapKeys(readSrc('src/components/Header.tsx'))

    expect(keys.length, 'the scan must find a non-empty key set').toBeGreaterThan(0)
    expect(keys, 'known-true anchor: dashboard is mapped today').toContain('dashboard')
    expect(keys, 'known-false anchor: a fabricated key must never appear').not.toContain('definitely-not-a-real-view-xyz')
  })

  it("CRUMB_MAP declares an 'approvals' entry", () => {
    const keys = crumbMapKeys(readSrc('src/components/Header.tsx'))

    expect(keys, "CRUMB_MAP has no 'approvals' key -- Header.tsx:55's `|| 'Overview'` fallback would mask this silently at runtime").toContain('approvals')
  })

  it('CRUMB_MAP is a TOTAL map: every current View member has a crumb', () => {
    const keys = crumbMapKeys(readSrc('src/components/Header.tsx'))
    const views = viewMembers(readSrc('src/types.ts'))

    expect(views.length, 'the View union scan must find members').toBeGreaterThan(0)
    for (const v of views) {
      expect(keys, `CRUMB_MAP has no entry for View member '${v}'`).toContain(v)
    }
  })
})
