// @vitest-environment jsdom
// DEMO-06-03. DEMO_MODE binds at module scope (demo/flag.ts), so every case rebinds it via
// vi.stubEnv + vi.resetModules() + a dynamic import of Sidebar, same idiom as
// App.standIn.test.tsx. No VITE_GATEWAY_URL is stubbed, so gatewayBase() is null and
// Sidebar's rollup fetch never fires (immediate: false) -- no fetch mock needed.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { Member } from '../lib/members'
import type { PlatformCtx } from '../types'
import { POPOVER_HEADER } from './copy'

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
})

const SEAT: Member = {
  id: 'm-seat-001',
  name: 'Chinedu Okafor',
  initials: 'CO',
  email: 'chinedu@example.ng',
  role: 'admin',
  status: 'active',
  isYou: true,
}

// inhouse mode + no entities sidesteps the firm switcher's fetch/list wiring entirely --
// this file only exercises the footer.
function demoCtx(over: Record<string, unknown> = {}): PlatformCtx {
  const ctx = {
    mode: 'inhouse',
    active: { short: 'Acme', initials: 'AC', tin: '12345678-0001', entityId: null },
    clients: [],
    entities: [],
    user: { name: SEAT.name, initials: SEAT.initials, verified: true, tenantName: 'Okafor & Partners' },
    view: 'dashboard',
    switcherOpen: false,
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    toggleSwitcher: vi.fn(),
    switchClient: vi.fn(),
    nav: vi.fn(),
    signOut: vi.fn(),
    members: [SEAT],
    seatSubject: SEAT.id,
    ...over,
  }
  return ctx as unknown as PlatformCtx
}

async function renderDemoSidebar(ctx: PlatformCtx) {
  cleanup()
  vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { Sidebar } = await import('../components/Sidebar')
  render(<Sidebar ctx={ctx} />)
}

describe('PersonaFooter (flag on)', () => {
  it('flag on, the footer carries the marker line and the trigger', async () => {
    await renderDemoSidebar(demoCtx())

    expect(screen.getByText('DEMO BUILD')).not.toBeNull()
    expect(screen.getByText('OKAFOR & PARTNERS')).not.toBeNull()
    expect(screen.getByTestId('persona-trigger')).not.toBeNull()
  })

  it('the trigger truncates the 32-character name and keeps the role legible', async () => {
    const LONG_NAME = 'Oluwaseyifunmi Adebanjo-Ogunleye'
    const member: Member = { ...SEAT, id: 'm-long-001', name: LONG_NAME, role: 'preparer' }
    await renderDemoSidebar(
      demoCtx({
        user: { name: LONG_NAME, initials: 'OA', verified: true, tenantName: 'Okafor & Partners' },
        members: [member],
        seatSubject: member.id,
      }),
    )

    const name = screen.getByTestId('persona-name')
    expect(name.style.textOverflow).toBe('ellipsis')
    expect(name.style.whiteSpace).toBe('nowrap')
    expect(name.textContent).toBe(LONG_NAME)

    const role = screen.getByTestId('persona-role')
    expect(role.style.textOverflow).toBe('')
    expect(role.style.whiteSpace).toBe('')
    expect(role.textContent).toBe('PREPARER')
  })

  it('the dot is green on the seat and amber while standing in', async () => {
    await renderDemoSidebar(demoCtx({ seatSubject: SEAT.id }))
    expect(screen.getByTestId('persona-dot').style.background).toBe('var(--status-green-text)')

    await renderDemoSidebar(demoCtx({ seatSubject: 'someone-else-001' }))
    expect(screen.getByTestId('persona-dot').style.background).toBe('var(--status-amber-text)')
  })

  it('the persona trigger carries no pf-btn class', async () => {
    await renderDemoSidebar(demoCtx())

    const trigger = screen.getByTestId('persona-trigger')
    expect(trigger.className).not.toMatch(/\bpf-btn\b/)
    expect(trigger.style.borderRadius).toBe('var(--radius-sm)')
  })

  it('the verified marker survives the flag-on footer', async () => {
    await renderDemoSidebar(demoCtx())

    // Proves the assertion below runs against the flag-on branch, not the legacy one --
    // both branches carry the marker today, so this alone is what makes the test RED
    // pre-implementation instead of trivially green.
    expect(screen.getByTestId('persona-trigger')).not.toBeNull()

    const aside = document.querySelector('aside.pf-sidebar')!
    const marks = aside.querySelectorAll('[title="Tenant verified via /v1/me"]')
    expect(marks.length).toBe(1)
  })

  it('the trigger sets an explicit sans font', async () => {
    await renderDemoSidebar(demoCtx())
    expect(screen.getByTestId('persona-trigger').style.fontFamily).toBe('var(--font-sans)')
  })

  it('an unverified session keeps the org label on the marker row', async () => {
    await renderDemoSidebar(demoCtx({ user: { name: SEAT.name, initials: SEAT.initials, verified: false, tenantName: null } }))

    // Same guard as the verified-marker test: an unverified legacy footer ALSO renders
    // the org label with zero markers, so this proves the flag-on branch actually ran.
    expect(screen.getByTestId('persona-trigger')).not.toBeNull()

    expect(screen.getByText('ACME · FINANCE')).not.toBeNull()
    const aside = document.querySelector('aside.pf-sidebar')!
    expect(aside.querySelectorAll('[title="Tenant verified via /v1/me"]').length).toBe(0)
  })

  it('the role reads an em dash until the roster resolves', async () => {
    await renderDemoSidebar(demoCtx({ members: [] }))

    expect(screen.getByTestId('persona-role').textContent).toBe('—')
    expect(screen.getByTestId('persona-dot').style.background).toBe('var(--status-green-text)')
  })

  // QA: proves the hoisted const, not a lookalike -- Sidebar.tsx passes the same
  // signOutButton element to both branches.
  it('the flag-on footer renders the hoisted sign-out button and fires signOut', async () => {
    const ctx = demoCtx()
    await renderDemoSidebar(ctx)

    const btn = screen.getByRole('button', { name: 'Sign out' })
    expect(btn.className).toBe('pf-btn pf-signout')
    expect(btn.getAttribute('title')).toBe('Sign out')
    btn.click()
    expect(ctx.signOut).toHaveBeenCalledTimes(1)
  })

  // Row 8 (AC-9). Needs the <Sidebar/> harness (firm mode + one client) so both switchers
  // render on the same aside. Red today: the popover header string is nowhere.
  it("the two switchers' headers differ", async () => {
    const ctx = demoCtx({
      mode: 'firm',
      clients: [{ entityId: 'e1', name: 'Acme Ltd', short: 'Acme', initials: 'AC' }],
      entities: [{ id: 'e1', status: 'active' }],
      switcherOpen: true,
    })
    await renderDemoSidebar(ctx)
    fireEvent.click(screen.getByTestId('persona-trigger'))

    const aside = document.querySelector<HTMLElement>('aside.pf-sidebar')!
    expect(within(aside).getByText('Switch company')).not.toBeNull()
    expect(within(aside).getByText(POPOVER_HEADER)).not.toBeNull()
  })

  // AC-7. QA: was uncovered -- no row in the subtask's Test Specs exercised useDismiss.
  it('an outside mousedown closes the open popover', async () => {
    await renderDemoSidebar(demoCtx())
    fireEvent.click(screen.getByTestId('persona-trigger'))
    expect(screen.getByTestId('persona-popover')).not.toBeNull()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByTestId('persona-popover')).toBeNull()
  })

  // AC-7. QA: was uncovered.
  it('Escape closes the open popover', async () => {
    await renderDemoSidebar(demoCtx())
    fireEvent.click(screen.getByTestId('persona-trigger'))
    expect(screen.getByTestId('persona-popover')).not.toBeNull()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByTestId('persona-popover')).toBeNull()
  })

  // QA: proves wrapperRef wraps the trigger, not just the popover. A ref scoped to the
  // popover alone would let mousedown dismiss first and click reopen -- net effect stays
  // open. Fires the two events separately (fireEvent.click alone never dispatches mousedown).
  it('clicking the trigger while open closes it, and does not reopen it', async () => {
    await renderDemoSidebar(demoCtx())
    const trigger = screen.getByTestId('persona-trigger')
    fireEvent.click(trigger)
    expect(screen.getByTestId('persona-popover')).not.toBeNull()

    fireEvent.mouseDown(trigger)
    fireEvent.click(trigger)
    expect(screen.queryByTestId('persona-popover')).toBeNull()
  })
})
