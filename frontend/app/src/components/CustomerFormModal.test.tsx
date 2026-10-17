// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Customer } from '../lib/buyerDirectory'
import type { PlatformCtx } from '../types'
import { CustomerFormModal } from './CustomerFormModal'

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
  render(<CustomerFormModal mode="edit" customer={CUSTOMER} ctx={{ authedFetch } as unknown as PlatformCtx} base="https://gateway.test" onClose={onClose} onSuccess={onSuccess} />)
  return { authedFetch, onClose, onSuccess }
}

const save = () => fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
const editEmail = () => fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'b@x.ng' } })

describe('CustomerFormModal', () => {
  afterEach(cleanup)

  it('shows the TIN as text, not an input', () => {
    mount()
    expect(screen.getByText(CUSTOMER.tin)).toBeTruthy()
    expect(screen.queryByDisplayValue(CUSTOMER.tin)).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Edit customer' })).toBeTruthy()
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
        const r = render(<CustomerFormModal mode="edit" customer={CUSTOMER} ctx={{ authedFetch: vi.fn().mockRejectedValue(err) } as unknown as PlatformCtx} base="https://gateway.test" onClose={vi.fn()} onSuccess={vi.fn()} />)
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

describe('CustomerFormModal per-field diff', () => {
  afterEach(cleanup)

  function open(authedFetch = vi.fn().mockResolvedValue(FILLED)) {
    render(<CustomerFormModal mode="edit" customer={FILLED} ctx={{ authedFetch } as unknown as PlatformCtx} base="https://gateway.test" onClose={vi.fn()} onSuccess={vi.fn()} />)
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

describe('CustomerFormModal create mode', () => {
  afterEach(cleanup)

  function mountCreate(authedFetch = vi.fn().mockResolvedValue(CUSTOMER), onOpenExisting = vi.fn().mockResolvedValue(undefined)) {
    const onClose = vi.fn()
    const onSuccess = vi.fn()
    render(<CustomerFormModal mode="create" ctx={{ authedFetch } as unknown as PlatformCtx} base="https://gateway.test" onClose={onClose} onSuccess={onSuccess} onOpenExisting={onOpenExisting} />)
    return { authedFetch, onClose, onSuccess, onOpenExisting }
  }
  const tin = () => screen.getByRole('textbox', { name: 'TIN' }) as HTMLInputElement
  const add = () => fireEvent.click(screen.getByRole('button', { name: 'Add customer' }))
  const typeTin = (v: string) => fireEvent.change(tin(), { target: { value: v } })
  const generic = 'Something went wrong. Please try again.'
  const http = (status: number, msg: string) => new ApiError('http', msg, status)

  it('CFM-01 create mode: the TIN is an input with its hint; edit mode keeps the TIN as text', () => {
    mountCreate()
    expect(screen.getByRole('dialog', { name: 'Add customer' })).toBeTruthy()
    expect(tin().getAttribute('aria-describedby')).toBe('customer-tin-hint')
    expect(document.getElementById('customer-tin-hint')?.textContent).toBe('12-digit FIRS TIN as 12345678-0001, or RN- followed by digits.')
  })

  it('CFM-02 an unfileable TIN shows a field error and sends nothing', () => {
    const { authedFetch } = mountCreate()
    const shape = 'Enter the TIN as 12345678-0001, or RN- followed by digits.'
    for (const [v, msg] of [['', 'TIN is required'], ['   ', 'TIN is required'], ['12345678', shape], ['rn-12', shape], ['RN-', shape], ['12345678-00012', shape]]) {
      typeTin(v)
      add()
      expect(screen.getByText(msg), JSON.stringify(v)).toBeTruthy()
    }
    expect(authedFetch).not.toHaveBeenCalled()
  })

  it('CFM-03 a fileable TIN is sent trimmed with the hyphen kept (boundary)', async () => {
    for (const [typed, sent] of [[' 12345678-0001 ', '12345678-0001'], ['RN-1', 'RN-1']]) {
      const { authedFetch, onSuccess } = mountCreate()
      typeTin(typed)
      add()
      await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
      expect(authedFetch).toHaveBeenCalledTimes(1)
      expect(authedFetch).toHaveBeenCalledWith('https://gateway.test/api/invoice/v1/customers', { method: 'POST', body: { tin: sent } })
      cleanup()
    }
  })

  it('CFM-04 a server 400 shows under the field it names', async () => {
    const af = vi.fn()
    mountCreate(af)
    typeTin('RN-1')
    af.mockRejectedValueOnce(http(400, 'tin must be NNNNNNNN-NNNN or RN- followed by digits'))
    add()
    const tinMsg = await screen.findByText('tin must be NNNNNNNN-NNNN or RN- followed by digits')
    expect(tinMsg.previousElementSibling?.id).toBe('customer-tin-hint')
    af.mockRejectedValueOnce(http(400, 'buyer_type must be B2B, B2G or B2C'))
    add()
    const typeMsg = await screen.findByText('buyer_type must be B2B, B2G or B2C')
    expect(typeMsg.previousElementSibling?.contains(screen.getByLabelText('Buyer type'))).toBe(true)
    af.mockRejectedValueOnce(http(400, 'invalid request body'))
    add()
    const banner = await screen.findByText('invalid request body')
    expect(banner.previousElementSibling).toBeNull()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('CFM-05 a 409 offers to open the held customer', async () => {
    const { onOpenExisting } = mountCreate(vi.fn().mockRejectedValue(http(409, 'duplicate tin')))
    typeTin(' RN-7 ')
    add()
    expect(await screen.findByText('A customer with this TIN already exists')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Open customer' }))
    expect(onOpenExisting).toHaveBeenCalledTimes(1)
    expect(onOpenExisting).toHaveBeenCalledWith('RN-7')
  })

  it('CFM-05b a rejected Open customer shows the generic banner; a pending one ignores a second click', async () => {
    let reject!: (e: unknown) => void
    const pending = new Promise<void>((_, r) => { reject = r })
    const { onOpenExisting } = mountCreate(vi.fn().mockRejectedValue(http(409, 'duplicate tin')), vi.fn().mockReturnValue(pending))
    typeTin('RN-7')
    add()
    const open = await screen.findByRole('button', { name: 'Open customer' })
    fireEvent.click(open)
    fireEvent.click(open)
    expect(onOpenExisting).toHaveBeenCalledTimes(1)
    expect((open as HTMLButtonElement).disabled).toBe(true)
    reject(new Error('lookup failed'))
    expect(await screen.findByText(generic)).toBeTruthy()
    expect(screen.getByText('A customer with this TIN already exists')).toBeTruthy()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Open customer' }) as HTMLButtonElement).disabled).toBe(false))
  })

  it('CFM-05c Cancel, Close and the backdrop do nothing while Open customer is pending', async () => {
    const { onClose } = mountCreate(vi.fn().mockRejectedValue(http(409, 'duplicate tin')), vi.fn().mockReturnValue(new Promise(() => {})))
    typeTin('RN-7')
    add()
    fireEvent.click(await screen.findByRole('button', { name: 'Open customer' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    fireEvent.click(screen.getByRole('dialog').parentElement as HTMLElement)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('CFM-06 401 shows nothing; 500, 403 and network show the generic banner', async () => {
    for (const err of [http(500, 'internal'), http(403, 'forbidden'), new TypeError('Failed to fetch')]) {
      const { authedFetch } = mountCreate(vi.fn().mockRejectedValue(err))
      typeTin('RN-1')
      add()
      expect(await screen.findByText(generic)).toBeTruthy()
      expect(screen.getByRole('dialog')).toBeTruthy()
      expect(authedFetch).toHaveBeenCalledTimes(1)
      cleanup()
    }
    const { authedFetch } = mountCreate(vi.fn().mockRejectedValue(http(401, 'unauthorized')))
    typeTin('RN-1')
    add()
    await waitFor(() => expect(authedFetch).toHaveBeenCalledTimes(1))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Add customer' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByText(/unauthorized|Something went wrong/)).toBeNull()
  })

  it('CFM-07 a double submit posts once', () => {
    const { authedFetch } = mountCreate(vi.fn().mockReturnValue(new Promise(() => {})))
    typeTin('RN-1')
    const form = screen.getByRole('dialog').querySelector('form') as HTMLFormElement
    fireEvent.submit(form)
    fireEvent.submit(form)
    expect(authedFetch).toHaveBeenCalledTimes(1)
  })

  it('CFM-08 editing the TIN after a 409 clears the message and the Open customer offer', async () => {
    mountCreate(vi.fn().mockRejectedValue(http(409, 'duplicate tin')))
    typeTin('RN-7')
    add()
    expect(await screen.findByText('A customer with this TIN already exists')).toBeTruthy()
    typeTin('RN-8')
    expect(screen.queryByText('A customer with this TIN already exists')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Open customer' })).toBeNull()
  })
})
