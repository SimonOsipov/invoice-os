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
  status: 'active',
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

  it('a 404 and a network error show the generic message', async () => {
    for (const err of [new ApiError('http', 'not found', 404), new TypeError('Failed to fetch')]) {
      const { unmount } = (() => {
        const r = render(<BuyerFormModal customer={CUSTOMER} ctx={{ authedFetch: vi.fn().mockRejectedValue(err) } as unknown as PlatformCtx} base="https://gateway.test" onClose={vi.fn()} onSuccess={vi.fn()} />)
        return r
      })()
      editEmail()
      save()
      expect(await screen.findByText('Something went wrong. Please try again.')).toBeTruthy()
      unmount()
    }
  })

  it('Cancel closes without a request even after an edit', () => {
    const { authedFetch, onClose } = mount()
    editEmail()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(authedFetch).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

const FILLED: Customer = {
  ...CUSTOMER,
  contact_person: 'Ada',
  buyer_type: 'B2B',
  telephone: '0801',
  street: '1 Road',
  city: 'Ikeja',
  postal_zone: '100001',
  country: 'NG',
  state: 'NG-LA',
  lga: 'Ikeja',
}

const EDITABLE: Array<[label: string, wire: string]> = [
  ['Name', 'name'],
  ['Contact person', 'contact_person'],
  ['Email', 'email'],
  ['Phone', 'telephone'],
  ['Street', 'street'],
  ['City', 'city'],
  ['Postal zone', 'postal_zone'],
  ['Country', 'country'],
  ['State', 'state'],
  ['LGA', 'lga'],
]

describe('BuyerFormModal per-field diff', () => {
  afterEach(cleanup)

  function open(authedFetch = vi.fn().mockResolvedValue(FILLED)) {
    render(<BuyerFormModal customer={FILLED} ctx={{ authedFetch } as unknown as PlatformCtx} base="https://gateway.test" onClose={vi.fn()} onSuccess={vi.fn()} />)
    return authedFetch
  }

  it.each(EDITABLE)('%s: a changed value is sent trimmed, as %s only', async (label, wire) => {
    expect(EDITABLE).toHaveLength(10)
    const af = open()
    fireEvent.change(screen.getByLabelText(label), { target: { value: '  changed  ' } })
    save()
    await waitFor(() => expect(af).toHaveBeenCalledTimes(1))
    expect(af.mock.calls[0][1]).toEqual({ method: 'PATCH', body: { [wire]: 'changed' } })
  })

  it.each(EDITABLE)('%s: a cleared value is sent as an empty string for %s', async (label, wire) => {
    const af = open()
    fireEvent.change(screen.getByLabelText(label), { target: { value: '' } })
    save()
    await waitFor(() => expect(af).toHaveBeenCalledTimes(1))
    expect(af.mock.calls[0][1]).toEqual({ method: 'PATCH', body: { [wire]: '' } })
  })

  it('Buyer type: a changed type is sent, and choosing — clears it to an empty string', async () => {
    const af = open()
    fireEvent.change(screen.getByLabelText('Buyer type'), { target: { value: 'B2G' } })
    save()
    await waitFor(() => expect(af).toHaveBeenCalledTimes(1))
    expect(af.mock.calls[0][1]).toEqual({ method: 'PATCH', body: { buyer_type: 'B2G' } })
    cleanup()
    const af2 = open()
    fireEvent.change(screen.getByLabelText('Buyer type'), { target: { value: '' } })
    save()
    await waitFor(() => expect(af2).toHaveBeenCalledTimes(1))
    expect(af2.mock.calls[0][1]).toEqual({ method: 'PATCH', body: { buyer_type: '' } })
  })
})
