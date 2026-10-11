// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Customer } from '../lib/buyerDirectory'
import type { PlatformCtx } from '../types'
import { BuyersView } from './BuyersView'

function customer(over: Partial<Customer> = {}): Customer {
  return {
    id: 'c1',
    tin: '20184412-0001',
    name: 'Acme Ltd',
    contact_person: null,
    buyer_type: 'B2B',
    email: 'a@x.ng',
    telephone: '0801',
    street: null,
    city: null,
    postal_zone: null,
    country: null,
    state: 'NG-LA',
    lga: null,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function page(customers: Customer[], total = customers.length, offset = 0) {
  return { customers, pagination: { limit: 50, offset, total } }
}

function mount(authedFetch: ReturnType<typeof vi.fn>, role = 'admin') {
  const ctx = { authedFetch, members: [{ id: 'u1', isYou: true, role }] } as unknown as PlatformCtx
  return render(<BuyersView ctx={ctx} />)
}

const rows = () => document.querySelectorAll('.pf-list-row')

describe('BuyersView', () => {
  beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test'))
  afterEach(() => {
    cleanup()
    vi.unstubAllEnvs()
  })

  it('renders one row per record with — for no name', async () => {
    mount(vi.fn().mockResolvedValue(page([customer({ name: null }), customer({ id: 'c2', tin: '20184412-0002' })])))
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(within(rows()[0] as HTMLElement).getAllByText('—').length).toBeGreaterThan(0)
    expect((rows()[0].children[0] as HTMLElement).textContent).toBe('—')
    expect((rows()[1].children[0] as HTMLElement).textContent).toBe('Acme Ltd')
    expect(rows()[0].children).toHaveLength(6)
  })

  it('renders — for a null email, phone and state', async () => {
    mount(vi.fn().mockResolvedValue(page([customer({ email: null, telephone: null, state: null })])))
    await waitFor(() => expect(rows()).toHaveLength(1))
    const cells = Array.from(rows()[0].children).map((c) => c.textContent)
    expect(cells).toEqual(['Acme Ltd', '20184412-0001', 'B2B', '—', '—', '—'])
  })

  it('shows the empty state', async () => {
    mount(vi.fn().mockResolvedValue(page([])))
    expect(await screen.findByText('No buyers yet')).toBeTruthy()
    expect(screen.getByText('Buyers are added when you save an invoice with a buyer TIN.')).toBeTruthy()
  })

  it('retries after an error', async () => {
    const af = vi.fn().mockRejectedValueOnce(new ApiError('http', 'boom', 500)).mockResolvedValue(page([customer()]))
    mount(af)
    const retry = await screen.findByRole('button', { name: /retry/i })
    fireEvent.click(retry)
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(af).toHaveBeenCalledTimes(2)
  })

  it('shows the pager only above one page', async () => {
    const { unmount } = mount(vi.fn().mockResolvedValue(page([customer()], 51)))
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(screen.queryByTestId('buyers-pager')).toBeTruthy()
    unmount()
    mount(vi.fn().mockResolvedValue(page([customer()], 50)))
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(screen.queryByTestId('buyers-pager')).toBeNull()
  })

  it("the pager's Next requests the next page", async () => {
    const af = vi.fn().mockResolvedValue(page([customer()], 120))
    mount(af)
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))
    await waitFor(() => expect(af).toHaveBeenCalledTimes(2))
    expect(af.mock.calls[1][0]).toBe('https://gateway.test/api/invoice/v1/customers?limit=50&offset=50')
  })

  it('a preparer can open and save', async () => {
    const af = vi.fn((_url: string, opts?: { method?: string }) => Promise.resolve(opts?.method === 'PATCH' ? customer({ city: 'Ikeja' }) : page([customer()])))
    mount(af, 'preparer')
    await waitFor(() => expect(rows()).toHaveLength(1))
    fireEvent.click(rows()[0])
    const dialog = within(screen.getByRole('dialog', { name: 'Edit buyer' }))
    fireEvent.change(dialog.getByLabelText('City'), { target: { value: 'Ikeja' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(af).toHaveBeenCalledWith('https://gateway.test/api/invoice/v1/customers/c1', { method: 'PATCH', body: { city: 'Ikeja' } }))
    // success refetches the list and closes the modal
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(af.mock.calls.filter(([, o]) => o?.method !== 'PATCH')).toHaveLength(2)
  })
})
