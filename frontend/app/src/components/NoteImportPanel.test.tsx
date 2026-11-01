// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import type { ExtractionFieldState } from '../lib/extractionReview'
import type { NoteOriginals } from '../lib/importApi'
import { NOTE_COPY } from '../lib/notes'
import type { PlatformCtx } from '../types'
import { NoteImportPanel, SAVE_DISABLED } from './NoteImportPanel'

const DOC = 'doc-1'
const ENTITY = 'ent-1'

const ORIGINALS: NoteOriginals = {
  entity_id: ENTITY,
  filed_invoice_id: null,
  originals: [
    { id: 'o-1', invoice_number: 'INV-1', issue_date: '2026-02-01T00:00:00Z', currency: 'NGN', total: '1075.00', buyer_name: 'Honeywell', note_count: 1 },
    { id: 'o-2', invoice_number: 'INV-2', issue_date: '2026-03-01T00:00:00Z', currency: 'NGN', total: '215.00', buyer_name: 'Dangote', note_count: 0 },
    { id: 'o-9', invoice_number: 'INV-9', issue_date: null, currency: null, total: null, buyer_name: null, note_count: 0 },
  ],
}

const field = (name: string, value: string | null): ExtractionFieldState => ({
  name,
  value,
  region: null,
  reason: '',
  alternatives: [],
  corrected: null,
  rules: [],
})

const READ = [field('invoice_number', 'CN-3401'), field('original_invoice_number', 'INV-2')]

interface Sent {
  url: string
  method?: string
  body: Record<string, unknown>
}

function setup(
  over: {
    fields?: ExtractionFieldState[]
    originals?: NoteOriginals
    reply?: (n: number) => Promise<unknown>
    activeEntity?: unknown
  } = {},
) {
  const sent: Sent[] = []
  const onFiled = vi.fn()
  const enterByHand = vi.fn()
  const authedFetch = async (url: string, opts?: { method?: string; body?: unknown }) => {
    sent.push({ url, method: opts?.method, body: opts?.body as Record<string, unknown> })
    return over.reply ? over.reply(sent.length) : { id: 'b', status: 'completed', ready_invoices: 1, errors: null }
  }
  const ctx = {
    authedFetch,
    enterByHand,
    activeEntity: 'activeEntity' in over ? over.activeEntity : { id: ENTITY },
  } as unknown as PlatformCtx
  render(
    <NoteImportPanel ctx={ctx} base="https://gw" documentId={DOC} noteType="credit_note" fields={over.fields ?? READ} originals={over.originals ?? ORIGINALS} onFiled={onFiled} />,
  )
  return { sent, onFiled, enterByHand }
}

const select = () => screen.getByTestId('extraction-note-original') as HTMLSelectElement
const number = () => screen.getByTestId('extraction-note-number') as HTMLInputElement
const importBtn = () => screen.getByTestId('extraction-note-import') as HTMLButtonElement
const asInvoice = () => screen.getByTestId('extraction-note-as-invoice') as HTMLButtonElement

async function click(el: HTMLElement) {
  await act(async () => {
    fireEvent.click(el)
  })
}

afterEach(cleanup)

describe('NoteImportPanel', () => {
  it('the picker lists and preselects', () => {
    setup()
    const opts = Array.from(select().options)
    expect(opts.map((o) => o.value)).toEqual(['', 'o-1', 'o-2', 'o-9'])
    expect(opts[0].textContent).toBe('Choose an invoice')
    expect(opts[1].textContent).toBe('INV-1 · 2026-02-01 · NGN 1075.00 · Honeywell')
    expect(select().value).toBe('o-2')
  })

  it('no match picks nothing', () => {
    setup({ fields: [field('invoice_number', 'CN-1'), field('original_invoice_number', 'inv-2')] })
    expect(select().value, 'a case-insensitive match preselected').toBe('')
    expect(importBtn().disabled).toBe(true)
    expect(importBtn().title).not.toBe('')
    expect(importBtn().style.cursor).toBe(SAVE_DISABLED.cursor)
  })

  it('an empty list leaves one exit', () => {
    setup({ originals: { ...ORIGINALS, originals: [] } })
    expect(select().disabled).toBe(true)
    expect(screen.getByTestId('extraction-note-empty').textContent).toContain('no accepted invoice with an IRN')
    expect(importBtn().disabled).toBe(true)
    expect(importBtn().title).toContain('no accepted invoice')
    expect(asInvoice().disabled).toBe(false)
  })

  it('the number seeds, re-seeds and yields to typing', () => {
    setup({ fields: [field('original_invoice_number', null)] })
    expect(number().value, 'no original chosen seeds nothing').toBe('')
    fireEvent.change(select(), { target: { value: 'o-1' } })
    expect(number().value).toBe('CN-INV-1-2')
    fireEvent.change(select(), { target: { value: 'o-9' } })
    expect(number().value, 'a new pick did not re-seed').toBe('CN-INV-9-1')
    fireEvent.change(number(), { target: { value: 'MINE-1' } })
    fireEvent.change(select(), { target: { value: 'o-1' } })
    expect(number().value, 'a pick overwrote typed text').toBe('MINE-1')

    cleanup()
    setup()
    expect(number().value, 'the read number is the seed').toBe('CN-3401')
    fireEvent.change(select(), { target: { value: 'o-1' } })
    expect(number().value, 'a pick replaced the read number').toBe('CN-3401')
  })

  it('import sends the choice', async () => {
    const { sent, onFiled } = setup()
    await click(importBtn())
    expect(sent).toHaveLength(1)
    expect(sent[0].url).toBe('https://gw/api/invoice/v1/imports/document')
    expect(sent[0].method).toBe('POST')
    expect(sent[0].body).toEqual({
      entity_id: ENTITY,
      document_id: DOC,
      document_type: 'credit_note',
      original_invoice_id: 'o-2',
      invoice_number: 'CN-3401',
    })
    expect(onFiled).toHaveBeenCalledTimes(1)

    fireEvent.change(number(), { target: { value: '   ' } })
    expect(importBtn().disabled, 'a blank number was importable').toBe(true)
    expect(importBtn().title).not.toBe('')
  })

  it('a clash is a field error; any other refusal is the banner', async () => {
    const refusals = [
      new ApiError('http', NOTE_COPY.numberTaken, 409),
      new ApiError('http', 'the original belongs to another client', 409),
      new ApiError('http', 'Internal error', 500),
    ]
    const { onFiled } = setup({ reply: (n) => Promise.reject(refusals[n - 1]) })

    await click(importBtn())
    expect(screen.getByTestId('extraction-note-number-error').textContent).toBe(NOTE_COPY.numberTaken)
    expect(screen.queryByTestId('extraction-note-error'), 'a clash raised the banner').toBeNull()
    expect(select(), 'a clash closed the panel').toBeTruthy()

    await click(importBtn())
    expect(screen.getByTestId('extraction-note-error').textContent).toBe('the original belongs to another client')
    expect(screen.queryByTestId('extraction-note-number-error')).toBeNull()

    await click(importBtn())
    expect(screen.getByTestId('extraction-note-error').textContent).toBe('Internal error')
    expect(onFiled).not.toHaveBeenCalled()
  })

  it('this is an invoice on a taken number stays on the panel and says why', async () => {
    const message = 'An invoice with this number already exists for this entity.'
    const { onFiled } = setup({
      reply: async () => ({
        id: 'b',
        ready_invoices: 0,
        quarantined_invoices: 1,
        errors: [{ field: 'invoice_number', rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'other', message }],
      }),
    })

    await click(screen.getByTestId('extraction-note-as-invoice'))

    expect(onFiled, 'a quarantine read as filed').not.toHaveBeenCalled()
    expect(screen.getByTestId('extraction-note-error').textContent).toBe(message)
    expect(screen.getByTestId('extraction-note-enter-by-hand')).toBeTruthy()
  })

  it('a double click posts once', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { sent } = setup({ reply: async () => (await gate, { id: 'b', ready_invoices: 1, errors: [] }) })

    fireEvent.click(importBtn())
    fireEvent.click(importBtn())
    expect(sent).toHaveLength(1)

    await act(async () => {
      fireEvent.click(asInvoice())
    })
    expect(sent, 'the other button posted during a send').toHaveLength(1)
    await act(async () => release())

    await click(asInvoice())
    expect(sent, 'the lock never released').toHaveLength(2)
    cleanup()

    // One act batches the renders, so no click sees the busy state: only the ref can refuse.
    let hold!: () => void
    const held = new Promise<void>((r) => (hold = r))
    const same = setup({ reply: async () => (await held, { id: 'b', ready_invoices: 1, errors: [] }) })
    await act(async () => {
      fireEvent.click(importBtn())
      fireEvent.click(asInvoice())
      fireEvent.click(importBtn())
    })
    expect(same.sent, 'clicks in one tick each posted').toHaveLength(1)
    await act(async () => hold())
  })

  it('this is an invoice', async () => {
    const filed = setup()
    await click(asInvoice())
    expect(filed.sent[0].body).toEqual({ entity_id: ENTITY, document_id: DOC, document_type: 'commercial_invoice' })
    expect(filed.onFiled).toHaveBeenCalledTimes(1)
    cleanup()

    const SENTENCE = 'No invoice number was read, so enter this invoice manually.'
    const quarantined = setup({
      reply: async () => ({ id: 'b', status: 'completed', ready_invoices: 0, errors: [{ field: 'invoice_number', message: SENTENCE }] }),
    })
    await click(asInvoice())
    expect(screen.getByTestId('extraction-note-error').textContent).toBe(SENTENCE)
    expect(quarantined.onFiled).not.toHaveBeenCalled()
    await click(screen.getByTestId('extraction-note-enter-by-hand'))
    expect(quarantined.enterByHand).toHaveBeenCalledWith(DOC)
    cleanup()

    const noEntity = setup({
      activeEntity: null,
      reply: async () => ({ id: 'b', ready_invoices: 0, errors: [{ field: 'invoice_number', message: SENTENCE }] }),
    })
    await click(asInvoice())
    const hand = screen.getByTestId('extraction-note-enter-by-hand') as HTMLButtonElement
    expect(hand.disabled).toBe(true)
    expect(hand.title).toBe('Filing needs a linked entity')
    await click(hand)
    expect(noEntity.enterByHand).not.toHaveBeenCalled()
  })
})
