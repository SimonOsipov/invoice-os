// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { InvoiceDetailRecord, InvoiceLineItem } from '../lib/invoices'
import type { NoteType } from '../lib/notes'
import type { PlatformCtx } from '../types'
import { NoteForm } from './NoteForm'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const LINES: InvoiceLineItem[] = [
  { id: 'l1', line_no: 1, description: 'Widget', quantity: '2.000', unit_price: '10.00', line_total: '20.00', line_tax: null },
  { id: 'l2', line_no: 2, description: 'Gadget', quantity: '1.000', unit_price: '5.00', line_total: '5.00', line_tax: null },
  { id: 'l3', line_no: 3, description: 'Gizmo', quantity: '4.000', unit_price: '3.00', line_total: '12.00', line_tax: null },
]

function inv(): InvoiceDetailRecord {
  return { id: 'inv-1', invoice_number: 'INV-1', line_items: LINES } as unknown as InvoiceDetailRecord
}

interface Reply {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function setup(opts: { type?: NoteType; noteCount?: number | null; reply?: Reply; slow?: boolean } = {}) {
  const posts: { url: string; body: { document_type: string; invoice_number: string; issue_date: string; lines: { line_id: string; quantity: string; unit_price: string }[] } }[] = []
  const fetchMock = vi.fn((url: string, init: RequestInit = {}) => {
    posts.push({ url, body: JSON.parse(String(init.body)) })
    return Promise.resolve(opts.reply ?? { ok: true, status: 201, json: () => Promise.resolve({ id: 'note-1' }) })
  })
  vi.stubGlobal('fetch', fetchMock)
  const openImportedInvoice = vi.fn()
  const ctx = { authedFetch: createAuthedFetch(() => 'tok', vi.fn()), openImportedInvoice } as unknown as PlatformCtx
  const props = { ctx, base: 'https://gw', invoiceId: 'inv-1', inv: inv(), documentType: opts.type ?? 'credit_note', onCancel: vi.fn() }
  const view = render(<NoteForm {...props} noteCount={opts.noteCount === undefined ? 1 : opts.noteCount} />)
  return { posts, openImportedInvoice, view, props }
}

const ticks = () => screen.getAllByTestId('note-line-check') as HTMLInputElement[]
const submit = () => screen.getByTestId('note-form-submit') as HTMLButtonElement
const tick = (i: number) => fireEvent.click(ticks()[i])
const num = () => (screen.getByTestId('note-number') as HTMLInputElement).value
const total = () => screen.getByTestId('note-total').textContent

describe('NoteForm', () => {
  it('the form lists every line unticked and seeds the number', () => {
    const { view, props } = setup({ noteCount: 1 })
    expect(screen.getAllByTestId('note-line-row')).toHaveLength(3)
    expect(ticks().map((t) => t.checked)).toEqual([false, false, false])
    expect((screen.getAllByTestId('note-line-qty') as HTMLInputElement[]).map((e) => e.value)).toEqual(['2.000', '1.000', '4.000'])
    expect((screen.getAllByTestId('note-line-price') as HTMLInputElement[]).map((e) => e.value)).toEqual(['10.00', '5.00', '3.00'])
    expect(num()).toBe('CN-INV-1-2')
    view.unmount()

    // Notes still loading: blank, then the suggestion arrives; typed text is never overwritten.
    const loading = render(<NoteForm {...props} noteCount={null} />)
    expect(num()).toBe('')
    loading.rerender(<NoteForm {...props} noteCount={0} />)
    expect(num()).toBe('CN-INV-1-1')
    loading.unmount()
    const typed = render(<NoteForm {...props} noteCount={null} />)
    fireEvent.change(screen.getByTestId('note-number'), { target: { value: 'MINE' } })
    typed.rerender(<NoteForm {...props} noteCount={5} />)
    expect(num()).toBe('MINE')
  })

  it('a credit note caps a line; a debit note does not', () => {
    setup({ type: 'credit_note' })
    tick(0)
    fireEvent.change(screen.getAllByTestId('note-line-qty')[0], { target: { value: '3' } })
    expect(screen.getAllByTestId('note-line-error')).toHaveLength(1)
    expect(submit().disabled).toBe(true)
    cleanup()

    setup({ type: 'debit_note' })
    tick(0)
    fireEvent.change(screen.getAllByTestId('note-line-qty')[0], { target: { value: '3' } })
    expect(screen.queryByTestId('note-line-error')).toBeNull()
    expect(submit().disabled).toBe(false)
  })

  it('the total follows ticks and keystrokes', () => {
    setup({ type: 'debit_note' })
    expect(total()).toBe('—')
    tick(0) // 20.00 + 7.5% = 21.50
    expect(total()).toBe('21.50')
    tick(2) // 32.00 + 2.40 = 34.40
    expect(total()).toBe('34.40')
    fireEvent.change(screen.getAllByTestId('note-line-qty')[2], { target: { value: '1' } }) // 23.00 + 1.73 (1.725 half-up) = 24.73
    expect(screen.getByTestId('note-subtotal').textContent).toBe('23.00')
    expect(total()).toBe('24.73')
    tick(0) // 3.00 + 0.23 (0.225 half-up) = 3.23
    expect(total()).toBe('3.23')
  })

  it('submit sends only ticked lines and opens the note', async () => {
    const { posts, openImportedInvoice } = setup()
    tick(0)
    tick(2)
    fireEvent.change(screen.getByTestId('note-issue-date'), { target: { value: '2026-10-30' } })
    fireEvent.click(submit())

    await waitFor(() => expect(openImportedInvoice).toHaveBeenCalledWith('note-1'))
    expect(posts).toHaveLength(1)
    expect(posts[0].url).toBe('https://gw/api/invoice/v1/invoices/inv-1/notes')
    expect(posts[0].body).toEqual({
      document_type: 'credit_note',
      invoice_number: 'CN-INV-1-2',
      issue_date: '2026-10-30T00:00:00Z',
      lines: [
        { line_id: 'l1', quantity: '2.000', unit_price: '10.00' },
        { line_id: 'l3', quantity: '4.000', unit_price: '3.00' },
      ],
    })
  })

  it('submit stays disabled until the form is complete', () => {
    setup()
    expect(submit().disabled, 'no tick').toBe(true)
    tick(0)
    expect(submit().disabled, 'complete').toBe(false)
    fireEvent.change(screen.getByTestId('note-number'), { target: { value: '  ' } })
    expect(submit().disabled, 'blank number').toBe(true)
    fireEvent.change(screen.getByTestId('note-number'), { target: { value: 'CN-9' } })
    expect(submit().disabled).toBe(false)
    fireEvent.change(screen.getByTestId('note-issue-date'), { target: { value: '' } })
    expect(submit().disabled, 'blank date').toBe(true)
  })

  it('a number clash is a field error', async () => {
    setup({ reply: { ok: false, status: 409, json: () => Promise.resolve({ error: 'duplicate invoice number' }) } })
    tick(1)
    fireEvent.change(screen.getByTestId('note-number'), { target: { value: 'CN-TAKEN' } })
    fireEvent.click(submit())

    const err = await screen.findByTestId('note-number-error')
    expect(err.textContent).toContain('already in the register')
    expect(screen.queryByTestId('note-form-error')).toBeNull()
    expect(num()).toBe('CN-TAKEN')
    expect(ticks()[1].checked).toBe(true)
  })

  it('any other refusal goes to the banner', async () => {
    for (const [status, error] of [
      [409, 'a note needs an accepted invoice with an IRN'],
      [500, 'internal error'],
      [500, 'duplicate invoice number'],
      [400, 'invoice: validation failed: quantity has more than 3 decimals'],
    ] as const) {
      setup({ reply: { ok: false, status, json: () => Promise.resolve({ error }) } })
      tick(0)
      fireEvent.click(submit())
      const banner = await screen.findByTestId('note-form-error')
      expect(within(banner).getByText(/\S/)).toBeTruthy()
      expect(screen.queryByTestId('note-number-error')).toBeNull()
      cleanup()
    }
  })

  it('a double click posts once', async () => {
    const { posts } = setup()
    tick(0)
    const btn = submit()
    fireEvent.click(btn)
    fireEvent.click(btn)
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts).toHaveLength(1)
  })
  it('a repeated submit event posts once', async () => {
    const { posts } = setup()
    tick(0)
    const form = screen.getByTestId('note-form')
    fireEvent.submit(form)
    fireEvent.submit(form)
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts).toHaveLength(1)
  })

  it('submit sends the edited quantity and price of a ticked line', async () => {
    const { posts } = setup()
    tick(0)
    fireEvent.change(screen.getAllByTestId('note-line-qty')[0], { target: { value: '1' } })
    fireEvent.change(screen.getAllByTestId('note-line-price')[0], { target: { value: '9.50' } })
    fireEvent.click(submit())
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0].body.lines).toEqual([{ line_id: 'l1', quantity: '1', unit_price: '9.50' }])
  })

  it('an over-cap line blocks Submit only while it is ticked', () => {
    setup({ type: 'credit_note' })
    tick(1)
    tick(0)
    fireEvent.change(screen.getAllByTestId('note-line-qty')[0], { target: { value: '9' } })
    expect(submit().disabled).toBe(true)
    tick(0)
    expect(screen.getAllByTestId('note-line-error')).toHaveLength(1)
    expect(submit().disabled).toBe(false)
  })

  it('a ticked line with quantity 0 or a negative price blocks Submit on either note type', () => {
    for (const type of ['credit_note', 'debit_note'] as const) {
      setup({ type })
      tick(0)
      fireEvent.change(screen.getAllByTestId('note-line-qty')[0], { target: { value: '0' } })
      expect(screen.getAllByTestId('note-line-error'), `${type} qty 0`).toHaveLength(1)
      expect(submit().disabled).toBe(true)
      fireEvent.change(screen.getAllByTestId('note-line-qty')[0], { target: { value: '1' } })
      fireEvent.change(screen.getAllByTestId('note-line-price')[0], { target: { value: '-1' } })
      expect(screen.getAllByTestId('note-line-error'), `${type} price -1`).toHaveLength(1)
      expect(submit().disabled).toBe(true)
      cleanup()
    }
  })

  it('a credit note takes a quantity equal to the original and caps one unit price above it', () => {
    setup({ type: 'credit_note' })
    tick(0)
    expect(screen.queryByTestId('note-line-error')).toBeNull()
    fireEvent.change(screen.getAllByTestId('note-line-price')[0], { target: { value: '10.01' } })
    expect(screen.getAllByTestId('note-line-error')).toHaveLength(1)
    fireEvent.change(screen.getAllByTestId('note-line-price')[0], { target: { value: '10.00' } })
    expect(screen.queryByTestId('note-line-error')).toBeNull()
  })

  it('a server 400 shows its message in the banner and keeps the form editable', async () => {
    setup({ reply: { ok: false, status: 400, json: () => Promise.resolve({ error: 'quantity "1e5" is not a decimal' }) } })
    tick(0)
    fireEvent.click(submit())
    const banner = await screen.findByTestId('note-form-error')
    expect(banner.textContent).toContain('1e5')
    await waitFor(() => expect(submit().disabled).toBe(false))
    expect((screen.getByTestId('note-number') as HTMLInputElement).disabled).toBe(false)
  })

  it('typing a new number clears the number clash', async () => {
    setup({ reply: { ok: false, status: 409, json: () => Promise.resolve({ error: 'duplicate invoice number' }) } })
    tick(0)
    fireEvent.click(submit())
    await screen.findByTestId('note-number-error')
    fireEvent.change(screen.getByTestId('note-number'), { target: { value: 'CN-NEW' } })
    expect(screen.queryByTestId('note-number-error')).toBeNull()
  })
})
