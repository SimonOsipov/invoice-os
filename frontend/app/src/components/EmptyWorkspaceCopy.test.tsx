// @vitest-environment jsdom
// AUTH-10-04: emptyClient() feeds every surface that renders active.short / active.name in a
// zero-entity workspace. Real emptyClient(), real components: each must read mode-neutral.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { emptyClient } from '../lib/clients'
import type { Mode, PlatformCtx } from '../types'
import { AddCompanyTask } from './AddCompanyTask'
import { CreateForm } from './CreateForm'
import { CreateMapping } from './CreateMapping'
import { CreateUpload } from './CreateUpload'
import { CustomersView } from './CustomersView'
import { ReportsView } from './ReportsView'
import { RulesView } from './RulesView'
import { Sidebar } from './Sidebar'
import { WorkflowsView } from './WorkflowsView'

const MODES: Mode[] = ['inhouse', 'firm']
// Word boundary: the firm task's own `No clients yet` is a true empty state.
const BROKEN = /\bno client\b/i

function baseCtx(mode: Mode, over: Record<string, unknown> = {}): PlatformCtx {
  return {
    mode,
    active: emptyClient(),
    activeEntity: null,
    entitiesState: 'empty',
    entities: [],
    clients: [],
    user: { name: 'Ada', initials: 'A', verified: false, tenantName: null },
    view: 'dashboard',
    switcherOpen: false,
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    nav: vi.fn(),
    toggleSwitcher: vi.fn(),
    switchClient: vi.fn(),
    signOut: vi.fn(),
    members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
    membersState: 'ready',
    membersError: null,
    refetchMembers: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
}

function pageText(): string {
  return document.body.textContent ?? ''
}

function expectEmptyWorkspaceCopy(containing: string[]) {
  const text = pageText()
  // Positive first: an unrendered surface passes every negative.
  expect(text.length).toBeGreaterThan(0)
  for (const want of containing) expect(text).toContain(want)
  expect(text).not.toMatch(BROKEN)
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            invoices: [],
            pagination: { limit: 100, offset: 0, total: 0 },
            totals: { counts: {}, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
            clients: [],
            top_violations: [],
          }),
      }),
    ),
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('emptyClient() in a zero-entity workspace reads mode-neutral on every surface (AUTH-10-04)', () => {
  it('the placeholder is what the surfaces below are fed', () => {
    const c = emptyClient()
    expect(c.short).toBe('your company')
    expect(c.name).toBe('No company yet')
  })

  it('Sidebar in-house: org label and company chip', () => {
    render(<Sidebar ctx={baseCtx('inhouse')} />)
    expectEmptyWorkspaceCopy(['YOUR COMPANY · FINANCE', 'your company'])
    expect(screen.getAllByText('your company').length).toBeGreaterThan(0)
  })

  it('Sidebar firm: client group and switcher', () => {
    render(<Sidebar ctx={baseCtx('firm', { switcherOpen: true })} />)
    expectEmptyWorkspaceCopy(['your company'])
    expect(pageText()).not.toContain('YOUR COMPANY · FINANCE')
  })

  it.each(MODES)('CreateUpload header and amber panel (%s)', (mode) => {
    render(
      <CreateUpload
        ctx={baseCtx(mode, { pickedFiles: [], filesRefusal: null, importError: null, runKind: null, skipUpload: vi.fn() })}
      />,
    )
    expectEmptyWorkspaceCopy(['Import invoices · your company', 'before you file'])
  })

  it.each(MODES)('CreateMapping header and supplier sentence (%s)', (mode) => {
    render(
      <CreateMapping
        ctx={baseCtx(mode, {
          preview: { columns: ['Invoice No'], sample_rows: [['INV-1']] },
          mapping: {},
          armedField: null,
          dragField: null,
          run: { files: [], cursor: 0, status: 'idle' },
          importError: null,
          entityId: null,
          groups: [{ fileIds: ['f1'], preview: null, mapping: null }],
          groupIndex: 0,
          pickedFiles: [],
        })}
      />,
    )
    expectEmptyWorkspaceCopy(['Map fields to columns · your company', 'Supplier details come from your company, not the file.'])
  })

  it.each(MODES)('CreateForm header and footer (%s)', (mode) => {
    render(
      <CreateForm
        ctx={baseCtx(mode, {
          draft: { number: '', items: [{ desc: '', qty: '1', price: '', vat: 'standard' }] },
          filing: false,
          filingError: null,
          handOffReading: null,
        })}
      />,
    )
    expectEmptyWorkspaceCopy(['New invoice · your company', 'Filed as a draft under your company'])
  })

  it('WorkflowsView in-house subtitle', () => {
    render(
      <WorkflowsView
        ctx={baseCtx('inhouse', { policies: [], policiesState: 'empty', policiesError: null, editingPolicyId: null, roles: [], members: [] })}
      />,
    )
    expectEmptyWorkspaceCopy(['Who must sign off before your company transmits an invoice.'])
  })

  it.each(MODES)('RulesView subtitle and custom group header (%s)', (mode) => {
    render(<RulesView ctx={baseCtx(mode, { customRules: [], openRuleKey: null })} />)
    expectEmptyWorkspaceCopy(['your company'])
  })

  it.each(MODES)('CustomersView subtitle and empty copy (%s)', async (mode) => {
    render(<CustomersView ctx={baseCtx(mode)} />)
    await screen.findByText(/Customers appear automatically as you create invoices for your company\./)
    expectEmptyWorkspaceCopy(['No company yet · buyer master data'])
  })

  it.each(MODES)('ReportsView subtitle and empty copy (%s)', async (mode) => {
    render(<ReportsView ctx={baseCtx(mode)} />)
    await waitFor(() => expect(pageText()).toContain('Reports populate once your company has validated invoices in the period.'))
    expectEmptyWorkspaceCopy(['No company yet · tax summary'])
  })

  it.each(MODES)('the add-company task (%s)', (mode) => {
    render(<AddCompanyTask ctx={baseCtx(mode, { refetchEntities: vi.fn(), entitiesError: null })} />)
    expectEmptyWorkspaceCopy(['Invoices are filed for a registered company'])
    // The in-house empty state is itself titled `No company yet`; a firm's is not.
    if (mode === 'firm') {
      expect(pageText()).not.toContain(emptyClient().name)
      expect(pageText()).not.toContain(emptyClient().short)
    }
  })
})
