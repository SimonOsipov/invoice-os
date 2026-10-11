// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Customer } from '../lib/buyerDirectory'
import type { PlatformCtx } from '../types'
import { BuyerFormModal } from './BuyerFormModal'

const CUSTOMER: Customer = {
  id: 'c1',
  tin: '20184412-0001',
  name: 'Acme Ltd',
  contact_person: null,
  buyer_type: null,
  email: 'a@x.ng',
  telephone: null,
  street: null,
  city: null,
  postal_zone: null,
  country: null,
  state: null,
  lga: null,
  created_at: '2026-01-01T00:00:00Z',
}

function mount(authedFetch = vi.fn()) {
  const onClose = vi.fn()
  const onSuccess = vi.fn()
  render(<BuyerFormModal customer={CUSTOMER} ctx={{ authedFetch } as unknown as PlatformCtx} base="https://gateway.test" onClose={onClose} onSuccess={onSuccess} />)
  return { authedFetch, onClose, onSuccess }
}

const save = () => fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
const editEmail = () => fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'b@x.ng' } })

describe('BuyerFormModal', () => {
  afterEach(cleanup)

  it('shows the TIN as text, not an input', () => {
    mount()
    expect(screen.getByText(CUSTOMER.tin)).toBeTruthy()
    expect(screen.queryByDisplayValue(CUSTOMER.tin)).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Edit buyer' })).toBeTruthy()
  })

  it('closes without a request when nothing changed', () => {
    const { authedFetch, onClose, onSuccess } = mount()
    save()
    expect(authedFetch).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('the buyer type select offers —, B2B, B2G, B2C', () => {
    mount()
    const options = Array.from((screen.getByLabelText('Buyer type') as HTMLSelectElement).options).map((o) => o.textContent)
    expect(options).toEqual(['—', 'B2B', 'B2G', 'B2C'])
  })

  it('a 400 shows the server message', async () => {
    const { onSuccess } = mount(vi.fn().mockRejectedValue(new ApiError('http', 'buyer_type must be B2B, B2G or B2C', 400)))
    editEmail()
    save()
    expect(await screen.findByText('buyer_type must be B2B, B2G or B2C')).toBeTruthy()
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('a 500 shows the generic message with the modal open', async () => {
    mount(vi.fn().mockRejectedValue(new ApiError('http', 'internal', 500)))
    editEmail()
    save()
    expect(await screen.findByText('Something went wrong. Please try again.')).toBeTruthy()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('a 401 shows no message', async () => {
    const { authedFetch, onSuccess } = mount(vi.fn().mockRejectedValue(new ApiError('http', 'unauthorized', 401)))
    editEmail()
    save()
    await waitFor(() => expect(authedFetch).toHaveBeenCalledTimes(1))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByText(/unauthorized|Something went wrong/)).toBeNull()
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('success calls onSuccess once', async () => {
    const { authedFetch, onSuccess } = mount(vi.fn().mockResolvedValue(CUSTOMER))
    editEmail()
    save()
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
    expect(authedFetch).toHaveBeenCalledWith('https://gateway.test/api/invoice/v1/customers/c1', { method: 'PATCH', body: { email: 'b@x.ng' } })
  })
})
