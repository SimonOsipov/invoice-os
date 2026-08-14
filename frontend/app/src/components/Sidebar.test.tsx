// @vitest-environment jsdom
// First jsdom coverage of either nav badge. Mirrors DashboardActive.test.tsx's fetch-mock
// + ctx-cast idiom (single-endpoint mock: Sidebar fires only getRollup).
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { Counts, Rollup } from '../lib/dashboard'
import type { PlatformCtx } from '../types'
import { Sidebar } from './Sidebar'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

const ZERO_COUNTS: Counts = { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 }

// The three numbers below are kept MUTUALLY DISTINCT in every fixture: a badge wired to
// the wrong field then renders a different string, never a coincidentally equal one.
function rollup(f: { validated: number; awaitingApproval: number; needsAttention: number }): Rollup {
  return {
    totals: {
      counts: { ...ZERO_COUNTS, validated: f.validated },
      needs_attention: f.needsAttention,
      awaiting_approval: f.awaitingApproval,
      metrics: {},
      top_violations: [],
    },
    clients: [],
    top_violations: [],
  }
}

// mode 'inhouse' resolves scopedBucket straight to rollup.totals, and is the only mode
// whose nav carries the Approvals item at all.
function sidebarCtx(): PlatformCtx {
  const ctx = {
    mode: 'inhouse',
    active: { short: 'Acme', initials: 'AC', tin: '12345678-0001', entityId: null },
    clients: [],
    entities: [],
    user: { name: 'Ada Nwosu', initials: 'AN', verified: false, tenantName: null },
    view: 'dashboard',
    filter: '',
    switcherOpen: false,
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    toggleSwitcher: vi.fn(),
    switchClient: vi.fn(),
    nav: vi.fn(),
    signOut: vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

function mockRollupFetch(data: Rollup) {
  const fetchMock = vi.fn(() => Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(data) }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function navButton(label: string): HTMLElement {
  return screen.getByText(label).closest('button')!
}

// The badge span is the only `.mono` inside a nav button -- the glyph beside it is an
// inline <svg> carrying no text.
function badgeOf(label: string): HTMLElement | null {
  return navButton(label).querySelector('span.mono')
}

// Without this stub gatewayBase() returns null, useAsync never fires, `bucket` stays null
// and both badges stay absent -- every assertion below would pass against an empty nav.
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

// The Invoices badge is the settle signal: it reads the same one rollup fetch, so waiting
// on it proves `bucket` is populated before any Approvals assertion runs. Without it an
// absent-badge assertion would pass on a still-pending fetch.
async function renderSidebar(data: Rollup) {
  mockRollupFetch(data)
  render(<Sidebar ctx={sidebarCtx()} />)
  await within(navButton('Invoices')).findByText(String(data.totals.needs_attention))
}

describe('Sidebar nav badges', () => {
  it('the Approvals badge reads awaiting_approval, not counts.validated', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 2, needsAttention: 3 }))

    expect(badgeOf('Approvals')?.textContent).toBe('2')
  })

  it('awaiting_approval of 0 renders no Approvals badge even while validated is non-zero', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 0, needsAttention: 3 }))

    expect(badgeOf('Approvals')).toBeNull()
    expect(within(navButton('Approvals')).queryByText('0')).toBeNull()
  })

  it('the Invoices badge still reads needs_attention', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 2, needsAttention: 3 }))

    // The PAIRING is the point: asserted together, neither badge can be wired to the
    // other's field.
    expect(badgeOf('Invoices')?.textContent).toBe('3')
    expect(badgeOf('Approvals')?.textContent).toBe('2')
  })
})
