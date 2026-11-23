// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import type { FieldView, Lookup, LookupsView, MappingView } from '../lib/erpConnectionDetail'
import type { PlatformCtx } from '../types'
import { FieldMappingModal } from './FieldMappingModal'

const MAP_URL = 'https://gw.test/api/portfolio/v1/entities/e1/erp-mappings/fake'
const LOOK_URL = 'https://gw.test/api/portfolio/v1/entities/e1/erp-lookups/fake?external_key=k'

const field = (target: string, o: Partial<FieldView> = {}): FieldView => ({
  target, label: target.toUpperCase(), line: false, source_path: 'a.' + target, default_path: 'a.' + target, origin: 'default', ...o,
})
const lk = (o: Partial<Lookup> = {}): Lookup => ({
  kind: 'customer', erp_key: 'C1', tax_category: null, exemption_code: null, hsn_code: null, service_code: null, tin: '12345678-0001', state: null, lga: null, ...o,
})
const mapping = (): MappingView => ({
  system: 'fake',
  fields: [
    field('invoice_number', { source_path: 'mine.no', origin: 'client' }),
    field('issue_date'),
    field('line_qty', { line: true }),
  ],
})
const lookups = (): LookupsView => ({ system: 'fake', external_key: 'k', lookups: [lk(), lk({ erp_key: 'C2', tin: '12345678-0002' })] })

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((a) => {
    resolve = a
  })
  return { promise, resolve }
}

type Init = { method?: string; body?: { fields?: unknown; lookups?: unknown } }
const puts = (f: ReturnType<typeof vi.fn>) => f.mock.calls.filter((c) => (c[1] as Init | undefined)?.method === 'PUT') as [string, Init][]

function mount(o: { fetch?: (url: string, init: Init) => unknown; externalKey?: string | null } = {}) {
  const f = vi.fn(async (url: string, init: Init) => (o.fetch ? o.fetch(url, init) : url.includes('erp-mappings') ? mapping() : lookups()))
  const ctx = { authedFetch: f, refetchMembers: vi.fn() } as unknown as PlatformCtx
  const onSaved = vi.fn()
  const onClose = vi.fn()
  const externalKey = o.externalKey === undefined ? 'k' : o.externalKey
  const r = render(
    <FieldMappingModal ctx={ctx} entityId="e1" system="fake" systemName="Fake ERP" externalKey={externalKey} mapping={mapping()} lookups={externalKey === null ? null : lookups()} onSaved={onSaved} onClose={onClose} />,
  )
  return { ...r, f, ctx, onSaved, onClose }
}
const row = (t: string) => screen.getAllByTestId('field-mapping-row').find((r) => r.dataset.target === t)!
const save = () => screen.getByTestId('field-mapping-save') as HTMLButtonElement
const edit = (el: HTMLElement, v: string) => fireEvent.change(el, { target: { value: v } })
const clickSave = () => act(async () => { fireEvent.click(save()) })

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('FieldMappingModal', () => {
  it('fieldMappingModal_showsEveryTargetAndTheLookups', () => {
    mount()
    expect(screen.getAllByTestId('field-mapping-row')).toHaveLength(3)
    expect((within(row('invoice_number')).getByLabelText('INVOICE_NUMBER path') as HTMLInputElement).value).toBe('mine.no')
    expect((within(row('issue_date')).getByLabelText('ISSUE_DATE path') as HTMLInputElement).value).toBe('a.issue_date')
    expect(within(row('line_qty')).getByText('LINE')).toBeTruthy()
    expect(screen.getAllByTestId('lookup-row')).toHaveLength(2)
    expect(screen.getAllByTestId('field-mapping-default')).toHaveLength(1)
  })

  it('fieldMappingModal_savesOnlyTheChangedRows', async () => {
    const f = mount({ fetch: (u) => (u.includes('erp-mappings') ? mapping() : lookups()) })
    edit(screen.getByLabelText('ISSUE_DATE path'), 'new.date')
    edit(screen.getAllByLabelText('TIN')[1], '12345678-0009')
    await clickSave()
    const p = puts(f.f)
    expect(p).toHaveLength(2)
    expect(p[0][0]).toBe(MAP_URL)
    expect(p[0][1].body).toEqual({ fields: [{ target: 'issue_date', source_path: 'new.date' }] })
    expect(p[1][0]).toBe(LOOK_URL)
    expect(p[1][1].body).toEqual({ lookups: [lk({ erp_key: 'C2', tin: '12345678-0009' })] })
    expect(f.onClose).toHaveBeenCalledTimes(1)
    expect(f.onSaved).toHaveBeenCalledTimes(2)
  })

  it('fieldMappingModal_useDefaultSendsNull', async () => {
    const f = mount()
    fireEvent.click(screen.getByTestId('field-mapping-default'))
    expect((screen.getByLabelText('INVOICE_NUMBER path') as HTMLInputElement).value).toBe('a.invoice_number')
    await clickSave()
    expect(puts(f.f)[0][1].body).toEqual({ fields: [{ target: 'invoice_number', source_path: null }] })
  })

  it('fieldMappingModal_onlyLookupsChangedSkipsTheFieldPut', async () => {
    const f = mount()
    edit(screen.getAllByLabelText('TIN')[0], '12345678-0008')
    await clickSave()
    const p = puts(f.f)
    expect(p).toHaveLength(1)
    expect(p[0][0]).toBe(LOOK_URL)
  })

  it('fieldMappingModal_saveDisabledUntilAChange', async () => {
    const held = deferred<MappingView>()
    const f = mount({ fetch: () => held.promise })
    expect(save().disabled).toBe(true)
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    expect(save().disabled).toBe(false)
    await clickSave()
    expect(save().disabled).toBe(true)
    expect(save().textContent).toBe('Saving…')
    expect((screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Close' }) as HTMLButtonElement).disabled).toBe(true)
    await clickSave()
    expect(puts(f.f)).toHaveLength(1)
    await act(async () => held.resolve(mapping()))
  })

  it('fieldMappingModal_a400KeepsItOpenWithTheSentence', async () => {
    const f = mount({ fetch: () => { throw new ApiError('http', 'invoice_number: unknown path', 400) } })
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    await clickSave()
    expect(screen.getByTestId('field-mapping-error').textContent).toBe('invoice_number: unknown path')
    expect(f.onClose).not.toHaveBeenCalled()
    expect(f.ctx.refetchMembers).not.toHaveBeenCalled()
    expect(save().disabled).toBe(false)
  })

  it('fieldMappingModal_a403RefetchesTheRoster', async () => {
    const f = mount({ fetch: () => { throw new ApiError('http', 'Only an admin can edit the mapping.', 403) } })
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    await clickSave()
    expect(screen.getByTestId('field-mapping-error').textContent).toContain('admin')
    expect(f.ctx.refetchMembers).toHaveBeenCalledTimes(1)
  })

  it('fieldMappingModal_fieldsLandLookupsFail', async () => {
    const next = { ...mapping(), fields: [field('issue_date', { source_path: 'x', origin: 'client' })] }
    const f = mount({
      fetch: (u) => {
        if (u.includes('erp-mappings')) return next
        throw new ApiError('http', 'customer tin: not a fileable TIN', 400)
      },
    })
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    edit(screen.getAllByLabelText('TIN')[0], 'nope')
    await clickSave()
    expect(f.onSaved).toHaveBeenCalledTimes(1)
    expect(f.onSaved).toHaveBeenCalledWith({ mapping: next })
    expect(f.onClose).not.toHaveBeenCalled()
    expect(screen.getByTestId('field-mapping-error').textContent).toBe('customer tin: not a fileable TIN')
    // The retry sends the lookups alone: the landed field row is no longer a change.
    await clickSave()
    expect(puts(f.f).map((p) => p[0])).toEqual([MAP_URL, LOOK_URL, LOOK_URL])
  })

  it('fieldMappingModal_noCompanyNoLookups', async () => {
    const f = mount({ externalKey: null })
    expect(screen.queryAllByTestId('lookup-row')).toHaveLength(0)
    expect(screen.queryByTestId('lookup-add')).toBeNull()
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    await clickSave()
    expect(puts(f.f).every((p) => p[0] === MAP_URL)).toBe(true)
  })

  it('fieldMappingModal_unmountMidSaveCallsNothing', async () => {
    const held = deferred<MappingView>()
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const f = mount({ fetch: () => held.promise })
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    await clickSave()
    f.unmount()
    await act(async () => held.resolve(mapping()))
    expect(f.onSaved).not.toHaveBeenCalled()
    expect(f.onClose).not.toHaveBeenCalled()
    expect(err).not.toHaveBeenCalled()
  })

  it('fieldMappingModal_a401ShowsTheSentenceOnly', async () => {
    const f = mount({ fetch: () => { throw new ApiError('http', 'Your session ended.', 401) } })
    edit(screen.getByLabelText('ISSUE_DATE path'), 'x')
    await clickSave()
    await waitFor(() => expect(screen.getByTestId('field-mapping-error').textContent).toBe('Your session ended.'))
    expect(f.ctx.refetchMembers).not.toHaveBeenCalled()
    expect(f.onClose).not.toHaveBeenCalled()
  })
})
