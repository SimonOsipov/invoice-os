// @vitest-environment jsdom
// ENGI-06-06: App-level glue of the create form's NRS fields -- supplier seeding at every
// reseed, and the filing body. Harness is App.handOff.test.tsx's.
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => void store.set(key, value)),
    removeItem: vi.fn((key: string) => void store.delete(key)),
    clear: vi.fn(() => store.clear()),
  }
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const HANDOFF_SESSION = JSON.stringify({
  v: 1,
  personaId: 'firm',
  token: 'tok',
  me: {
    tenant: { id: '11111111-1111-4111-8111-111111111111', name: 'Firm', kind: 'firm' },
    user: { id: 'u-1', role: 'admin', display_name: 'Ada', email: 'ada@firm.test' },
  },
  verified: true,
  handoff: true,
})
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_B = 'bbbbbbbb-0000-4000-8000-000000000002'
const CREATED_ID = 'cccccccc-0000-4000-8000-00000000000c'

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))
vi.mock('./components/InvoiceDetail', () => ({ InvoiceDetail: () => null }))

let createBodies: Record<string, unknown>[] = []

beforeEach(() => {
  capturedCtx = undefined
  createBodies = []
  vi.stubGlobal('localStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
})
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

function entityRow(id: string, name: string, tin: string, over: Record<string, unknown>) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z', ...over }
}

const ENTITIES = [
  entityRow(ENTITY_A, 'Co A', '12345678-0001', { email: 'a@co.test', street: '1 A St', state: 'NG-LA' }),
  entityRow(ENTITY_B, 'Co B', '12345678-0002', { email: 'b@co.test', street: '2 B St', state: 'NG-KN' }),
]

function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; body?: string }) => {
      const json = (body: unknown, status = 200) => Promise.resolve({ ok: true, status, json: () => Promise.resolve(body) })
      if (url.endsWith('/api/invoice/v1/invoices') && init?.method === 'POST') {
        createBodies.push(JSON.parse(init.body ?? '{}') as Record<string, unknown>)
        return json({ id: CREATED_ID, invoice_number: 'INV-1', status: 'draft' }, 201)
      }
      if (url.includes('/portfolio/v1/entities')) {
        return json({ entities: ENTITIES, pagination: { limit: 200, offset: 0, total: 2 } })
      }
      if (url.includes('/code-lists/tax-categories')) return json({ codes: [{ code: 'STANDARD_VAT', label: 'Standard VAT', percent: '7.5' }] })
      if (url.includes('/code-lists/hs-codes')) return json({ codes: [{ code: '8471', label: 'Computers', percent: null }] })
      if (url.includes('/code-lists/')) return json({ codes: [] })
      return json({
        entities: [], policies: [], members: [], roles: [], invoices: [], total: 0, clients: [],
        totals: EMPTY_BUCKET, rejection_reasons: [],
      })
    }),
  )
}

async function renderApp(stored: string = serializeSession(SEAT_SESSION)) {
  routeFetch()
  localStorage.setItem(SESSION_KEY, stored)
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

const requireCtx = (): PlatformCtx => capturedCtx!
const supplierEmail = () => (screen.getByLabelText('Supplier email') as HTMLInputElement).value
const supplierStreet = () => (screen.getByLabelText('Supplier street') as HTMLInputElement).value
const supplierState = () => (screen.getByLabelText('Supplier state') as HTMLInputElement).value

async function openForm(entityId: string = ENTITY_A) {
  await waitFor(() => expect(requireCtx().activeEntity?.id).toBe(entityId))
  await act(async () => requireCtx().openCreate())
  await act(async () => requireCtx().skipUpload())
  await screen.findByLabelText('Supplier email')
}

describe('supplier pre-fill', () => {
  it('app_supplierPrefillsFromTheActiveCompany', async () => {
    await renderApp()
    await openForm()
    expect(supplierEmail()).toBe('a@co.test')
    expect(supplierStreet()).toBe('1 A St')
    expect(supplierState()).toBe('NG-LA')

    await act(async () => requireCtx().switchClient(ENTITY_B))
    await act(async () => requireCtx().nav('create'))
    expect(requireCtx().createStep).toBe('form')
    expect(supplierEmail()).toBe('b@co.test')
    expect(supplierStreet()).toBe('2 B St')
  })

  it('app_supplierPrefillsAgainOnNewInvoice', async () => {
    await renderApp()
    await openForm()
    fireEvent.change(screen.getByLabelText('Supplier email'), { target: { value: 'typed@co.test' } })
    await act(async () => requireCtx().fileDraft())
    await waitFor(() => expect(requireCtx().importedInvoiceId).toBe(CREATED_ID))

    await act(async () => requireCtx().openCreate())
    await act(async () => requireCtx().skipUpload())
    expect(supplierEmail()).toBe('a@co.test')
    expect(supplierStreet()).toBe('1 A St')
  })

  it('app_supplierPrefillsAtAHandOffStart', async () => {
    await renderApp(HANDOFF_SESSION)
    await waitFor(() => expect(requireCtx().activeEntity?.id).toBe(ENTITY_A))
    await waitFor(() => expect(requireCtx().draft.supplier_email, 'the late entity list fills the seed').toBe('a@co.test'))
    expect(requireCtx().draft.supplier_street).toBe('1 A St')
    expect(requireCtx().draft.number, 'a hand-off draft starts blank').toBe('')

    await act(async () => requireCtx().openCreate())
    expect(requireCtx().draft.supplier_email).toBe('a@co.test')
    expect(requireCtx().draft.number).toBe('')
  })
})

describe('filing', () => {
  it('app_fileSendsTheNrsFields', async () => {
    await renderApp()
    await openForm()
    const typeInto = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
    typeInto('Invoice kind', 'B2B')
    typeInto('Due date', '2026-07-01')
    typeInto('Buyer email', 'buyer@x.test')
    for (const [label, code] of [['Tax category 1', 'STANDARD_VAT'], ['HSN code 1', '8471']] as const) {
      fireEvent.focus(screen.getByLabelText(label))
      await act(async () => {})
      typeInto(label, code)
      await act(async () => {})
    }
    await act(async () => requireCtx().fileDraft())
    await waitFor(() => expect(createBodies).toHaveLength(1))

    const body = createBodies[0]!
    expect(body).toMatchObject({
      invoice_kind: 'B2B',
      tax_currency_code: 'NGN',
      due_date: '2026-07-01T00:00:00Z',
      buyer_email: 'buyer@x.test',
      supplier_email: 'a@co.test',
    })
    const lines = body.line_items as Record<string, unknown>[]
    expect(lines[0]).toMatchObject({ tax_category: 'STANDARD_VAT', tax_percent: '7.5', hsn_code: '8471', base_quantity: '1', line_tax: '187500.00' })
    expect(lines[1]).toMatchObject({ tax_category: null, line_tax: null, base_quantity: '1' })
  })
})

describe('supplier pre-fill adversarial', () => {
  it('app_fileSendsATypedSupplierValueAndNullForABlankOne', async () => {
    await renderApp()
    await openForm()
    fireEvent.change(screen.getByLabelText('Supplier email'), { target: { value: 'typed@co.test' } })
    fireEvent.change(screen.getByLabelText('Supplier street'), { target: { value: '' } })
    await act(async () => requireCtx().fileDraft())
    await waitFor(() => expect(createBodies).toHaveLength(1))
    expect(createBodies[0]!.supplier_email).toBe('typed@co.test')
    expect(createBodies[0]!.supplier_street).toBeNull()
  })

  it('app_aSecondCompanySwitchReseedsFromThatCompanyNotTheTypedValue', async () => {
    await renderApp()
    await openForm()
    fireEvent.change(screen.getByLabelText('Supplier email'), { target: { value: 'typed@co.test' } })
    await act(async () => requireCtx().switchClient(ENTITY_B))
    await act(async () => requireCtx().nav('create'))
    expect(supplierEmail()).toBe('b@co.test')
  })
})
