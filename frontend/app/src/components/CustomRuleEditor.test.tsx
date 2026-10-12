// @vitest-environment jsdom
// The custom-rule editor, driven through RulesView over a fake authedFetch that keeps a rule list.
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { CustomRule, CustomRuleInput } from '../lib/rulesApi'
import type { PlatformCtx } from '../types'
import { RulesView } from './RulesView'

const GW = 'https://gateway.test'
const A = 'aaaaaaaa-0000-4000-8000-00000000000a'
const B = 'bbbbbbbb-0000-4000-8000-00000000000b'

function rule(id: string, over: Partial<CustomRule> = {}): CustomRule {
  return {
    id,
    key: `custom:${id}`,
    revision: 1,
    revision_id: `rev-${id}`,
    entity_id: null,
    type: 'required',
    target: 'buyer.email',
    params: {},
    severity: 'warning',
    when: null,
    message: `message of ${id}`,
    enabled: true,
    updated_at: '2026-10-12T00:00:00Z',
    updated_by: 'u1',
    ...over,
  }
}

type Init = { method?: string; body?: unknown }

// POST appends and PUT replaces in `store`; `write` overrides the answer to either.
function fakeFetch(store: CustomRule[], write?: (url: string, init: Init) => unknown) {
  return vi.fn(async (url: string, init?: Init) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && url.endsWith('/api/validation/v1/rules')) {
      return { today: '2026-10-12', versions: [], rule_set_version_id: 'v4', version: 4, effective_from: '2026-08-06', rules: [] }
    }
    if (method === 'GET') return { custom_rules: [...store] }
    if (write) return write(url, init!)
    const body = init!.body as CustomRuleInput
    if (method === 'POST') {
      const made = rule(`new-${store.length}`, body)
      store.push(made)
      return made
    }
    const id = url.split('/').pop()!
    const i = store.findIndex((r) => r.id === id)
    store[i] = { ...store[i], ...body }
    return store[i]
  })
}

type F = ReturnType<typeof fakeFetch>
const writes = (f: F) => f.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET')
const listReads = (f: F) => f.mock.calls.filter(([u, init]) => !init?.method && u.endsWith('/custom-rules'))
const bodyOf = (f: F, i = 0) => (writes(f)[i][1] as Init).body

const ADMIN = [{ id: 'u1', isYou: true, role: 'admin', status: 'active', name: 'Ada', initials: 'A', email: null }]

async function mount(f: F, over: Record<string, unknown> = {}) {
  const ctx = {
    mode: 'firm',
    active: { short: 'Adaeze Ventures', initials: 'AV', tin: '', entityId: A },
    activeEntity: { id: A, name: 'Adaeze Ventures Ltd' },
    authedFetch: f,
    members: ADMIN,
    membersState: 'ready',
    openRuleKey: null,
    openRule: vi.fn(),
    closeRule: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
  const view = render(<RulesView ctx={ctx} />)
  await screen.findByText(/^INHERITED/)
  return { ctx, view }
}

const open = async (name = 'New custom rule') => {
  fireEvent.click(screen.getByTestId('rules-new'))
  return screen.findByRole('dialog', { name })
}
const select = (label: string, text: string) => {
  const el = screen.getByLabelText(label) as HTMLSelectElement
  const opt = [...el.options].find((o) => o.textContent === text)
  expect(opt, `${label} offers "${text}"`).toBeTruthy()
  fireEvent.change(el, { target: { value: opt!.value } })
}
const type = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
const save = () => fireEvent.click(screen.getByTestId('rule-save'))
const customMessages = () => [...document.querySelectorAll('.pf-row')].map((r) => r.children[5].textContent)
const options = (label: string) => [...(screen.getByLabelText(label) as HTMLSelectElement).options].map((o) => o.textContent)

beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', GW))
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
})

describe('CustomRuleEditor, create', () => {
  it('customRuleEditor_guidedSavePostsTheTypedRule', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    select('Field', 'Buyer email')
    select('Check', 'matches a pattern')
    type('Pattern', '^[^@]+@[^@]+$')
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value, 'prefilled from the field').toBe('Buyer email failed the check')
    type('Message', 'Buyer email looks wrong')
    fireEvent.click(screen.getByRole('radio', { name: 'Warn' }))
    fireEvent.click(screen.getByRole('radio', { name: 'Adaeze Ventures Ltd only' }))
    save()

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(writes(f)).toHaveLength(1)
    expect(writes(f)[0][0]).toBe(`${GW}/api/invoice/v1/custom-rules`)
    expect(writes(f)[0][1]).toMatchObject({ method: 'POST' })
    expect(bodyOf(f)).toEqual({
      entity_id: A,
      type: 'format/regex',
      target: 'buyer.email',
      params: { pattern: '^[^@]+@[^@]+$' },
      severity: 'warning',
      when: null,
      message: 'Buyer email looks wrong',
      enabled: true,
    })
    await waitFor(() => expect(listReads(f)).toHaveLength(2))
    expect(customMessages()).toEqual(['Buyer email looks wrong'])
  })

  it('the message follows the field until it is edited', async () => {
    await mount(fakeFetch([]))
    await open()
    select('Field', 'Total')
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('Total failed the check')
    type('Message', 'mine')
    select('Field', 'Currency')
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('mine')
  })

  it('customRuleEditor_checksFollowTheFieldType', async () => {
    await mount(fakeFetch([]))
    await open()
    select('Field', 'Total')
    expect(options('Check')).toEqual(['is filled in', 'is between'])
    select('Field', 'Issue date')
    expect(options('Check')).toEqual(['is filled in'])
    select('Field', 'Currency')
    expect(options('Check')).toEqual(['is filled in', 'matches a pattern', 'is one of'])
  })

  it('a check the new field lacks falls back to the first one', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    select('Field', 'Total')
    select('Check', 'is between')
    select('Field', 'Issue date')
    expect((screen.getByLabelText('Check') as HTMLSelectElement).value).toBe('required')
    expect(screen.queryByLabelText('Minimum')).toBeNull()
  })

  it('enum and range send their parameters', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    select('Field', 'Currency')
    select('Check', 'is one of')
    type('Allowed values', 'NGN, USD ,')
    save()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect((bodyOf(f) as CustomRuleInput).params).toEqual({ values: ['NGN', 'USD'] })

    await open()
    select('Field', 'Total')
    select('Check', 'is between')
    type('Maximum', '500000000')
    save()
    await waitFor(() => expect(writes(f)).toHaveLength(2))
    expect((bodyOf(f, 1) as CustomRuleInput).params).toEqual({ max: 500000000 })
  })

  it('customRuleEditor_expressionSavePostsCel', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    fireEvent.click(screen.getByTestId('rule-mode-expression'))
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value, 'the guided prefill does not carry over').toBe('')
    expect(options('Target field')[0]).toBe('Whole invoice')
    select('Target field', 'Buyer email')
    select('Target field', 'Total')
    type('Expression', '!has(invoice.total) || invoice.total < 5.0')
    type('Message', 'Total is too small')
    fireEvent.click(screen.getByRole('radio', { name: 'Block submission' }))
    save()

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(writes(f)).toHaveLength(1)
    expect(bodyOf(f)).toEqual({
      entity_id: A,
      type: 'cel',
      target: 'total',
      params: { expr: '!has(invoice.total) || invoice.total < 5.0' },
      severity: 'error',
      when: null,
      message: 'Total is too small',
      enabled: true,
    })
  })

  it('a prefixed header field becomes its payload path in an expression rule', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    fireEvent.click(screen.getByTestId('rule-mode-expression'))
    select('Target field', 'Buyer email')
    type('Expression', 'has(invoice.buyer) && has(invoice.buyer.email)')
    type('Message', 'Buyer needs an email')
    save()
    await waitFor(() => expect(writes(f)).toHaveLength(1))
    expect((bodyOf(f) as CustomRuleInput).target).toBe('buyer.email')
  })

  it('Warn after Block puts the rule back to a warning', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    fireEvent.click(screen.getByRole('radio', { name: 'Block submission' }))
    fireEvent.click(screen.getByRole('radio', { name: 'Warn' }))
    save()
    await waitFor(() => expect(writes(f)).toHaveLength(1))
    expect((bodyOf(f) as CustomRuleInput).severity).toBe('warning')
  })

  it('the expression tab shows the guard help line', async () => {
    await mount(fakeFetch([]))
    await open()
    expect(screen.queryByTestId('rule-expression-help')).toBeNull()
    fireEvent.click(screen.getByTestId('rule-mode-expression'))
    expect(screen.getByTestId('rule-expression-help').textContent).toBe(
      'Guard each field you read, segment by segment: has(invoice.buyer) && has(invoice.buyer.email).',
    )
  })

  it('customRuleEditor_serverRefusalIsShownVerbatim', async () => {
    const f = fakeFetch([], () => Promise.reject(new ApiError('http', 'the rule does not evaluate: no such key: total', 400)))
    await mount(f)
    await open()
    fireEvent.click(screen.getByTestId('rule-mode-expression'))
    type('Expression', 'invoice.total > 5')
    type('Message', 'too small')
    save()

    expect((await screen.findByTestId('rule-save-error')).textContent).toBe('the rule does not evaluate: no such key: total')
    expect((screen.getByLabelText('Expression') as HTMLTextAreaElement).value).toBe('invoice.total > 5')
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('too small')
    expect(screen.getByRole('dialog', { name: 'New custom rule' })).toBeTruthy()
    expect((screen.getByTestId('rule-save') as HTMLButtonElement).disabled, 'Save works again').toBe(false)
    expect(listReads(f), 'no refetch').toHaveLength(1)
  })

  it('customRuleEditor_incompleteFormSendsNothing', async () => {
    const f = fakeFetch([])
    await mount(f)
    await open()
    select('Field', 'Total')
    select('Check', 'is between')
    save()

    expect(screen.getByTestId('rule-save-error').textContent).toBe('Enter a minimum, a maximum or both.')
    type('Message', '   ')
    save()
    expect(screen.getByTestId('rule-save-error').textContent).toBe('Enter a message.')
    expect(writes(f)).toHaveLength(0)
    expect(screen.getByRole('dialog', { name: 'New custom rule' })).toBeTruthy()
  })

  it('a failed save never touches the shown rules', async () => {
    const f = fakeFetch([rule('r1')], () => Promise.reject(new ApiError('http', 'boom', 502)))
    await mount(f)
    await open()
    save()
    await screen.findByTestId('rule-save-error')
    expect(customMessages()).toEqual(['message of r1'])
  })

  it('customRuleEditor_saveIsDisabledInFlight', async () => {
    let release!: (r: CustomRule) => void
    const f = fakeFetch([], () => new Promise<CustomRule>((res) => (release = res)))
    await mount(f)
    await open()
    const btn = screen.getByTestId('rule-save')

    fireEvent.click(btn)
    fireEvent.click(btn)
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(true))
    expect(writes(f)).toHaveLength(1)
    release(rule('made'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('a create supersedes a list read in flight: the old list cannot remove the new rule', async () => {
    const OLD = rule('old')
    const MADE = rule('made', { message: 'brand new' })
    let reads = 0
    const release: Array<() => void> = []
    const f = vi.fn(async (url: string, init?: Init) => {
      if (init?.method === 'DELETE') return undefined
      if (init?.method === 'POST') return MADE
      if (url.endsWith('/custom-rules')) {
        const n = ++reads
        if (n > 1) await new Promise<void>((res) => release.push(res))
        return { custom_rules: n === 3 ? [OLD, MADE] : [OLD] }
      }
      return { today: '2026-10-12', versions: [], rule_set_version_id: 'v4', version: 4, effective_from: '2026-08-06', rules: [] }
    })
    await mount(f as unknown as F, { openRuleKey: OLD.key })

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    await waitFor(() => expect(reads).toBe(2))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await open()
    save()
    await waitFor(() => expect(reads).toBe(3))
    await waitFor(() => expect(customMessages()).toEqual(['brand new']))

    release[0]() // the read that started before the create answers with the old list
    await new Promise((r) => setTimeout(r, 20))
    expect(customMessages(), 'the old list did not overwrite the new rule').toEqual(['brand new'])
    release[1]()
    await waitFor(() => expect(customMessages()).toEqual(['message of old', 'brand new']))
  })
})

describe('CustomRuleEditor, scope', () => {
  it('customRuleEditor_scopeByMode', async () => {
    const firmWide = fakeFetch([])
    const m1 = await mount(firmWide)
    await open()
    expect((screen.getByRole('radio', { name: 'Adaeze Ventures Ltd only' }) as HTMLInputElement).checked, 'defaults to the client').toBe(true)
    fireEvent.click(screen.getByRole('radio', { name: 'All clients (firm-wide)' }))
    save()
    await waitFor(() => expect(writes(firmWide)).toHaveLength(1))
    expect((bodyOf(firmWide) as CustomRuleInput).entity_id).toBeNull()
    m1.view.unmount()

    const client = fakeFetch([])
    const m2 = await mount(client)
    await open()
    save()
    await waitFor(() => expect(writes(client)).toHaveLength(1))
    expect((bodyOf(client) as CustomRuleInput).entity_id).toBe(A)
    m2.view.unmount()

    const inhouse = fakeFetch([])
    const m3 = await mount(inhouse, { mode: 'inhouse', activeEntity: { id: A, name: 'Adaeze Ventures Ltd' }, active: { short: 'Acme', initials: 'A', tin: '', entityId: null } })
    await open()
    expect(screen.queryByRole('radiogroup', { name: 'Scope' })).toBeNull()
    save()
    await waitFor(() => expect(writes(inhouse)).toHaveLength(1))
    expect((bodyOf(inhouse) as CustomRuleInput).entity_id).toBeNull()
    m3.view.unmount()

    const none = fakeFetch([])
    await mount(none, { activeEntity: null })
    await open()
    expect(screen.queryAllByRole('radio', { name: /only$/ }), 'no client radio').toHaveLength(0)
    expect((screen.getByRole('radio', { name: 'All clients (firm-wide)' }) as HTMLInputElement).checked).toBe(true)
    save()
    await waitFor(() => expect(writes(none)).toHaveLength(1))
    expect((bodyOf(none) as CustomRuleInput).entity_id).toBeNull()
  })
})

describe('CustomRuleEditor, edit', () => {
  const GUIDED = rule('g1', { entity_id: A, type: 'enum', target: 'currency', params: { values: ['NGN', 'USD'] }, severity: 'error', message: 'Currency is wrong' })
  const CEL = rule('c1', { type: 'cel', target: 'total', params: { expr: 'invoice.total > 0.0' }, message: 'Total must be positive' })
  const TAX = rule('t1', { type: 'tax_math', target: '', params: { tolerance: 1 }, message: 'Tax math' })

  async function openEdit(r: CustomRule, f: F) {
    const ctx = await mount(f, { openRuleKey: r.key })
    // The real closeRule clears the key; the mock must, or the detail drawer comes back.
    ctx.ctx.closeRule = vi.fn(() => {
      ;(ctx.ctx as { openRuleKey: string | null }).openRuleKey = null
    })
    fireEvent.click(within(screen.getByRole('dialog')).getByTestId('rule-edit'))
    return { ...ctx, dialog: await screen.findByRole('dialog', { name: 'Edit custom rule' }) }
  }

  it('customRuleEditor_editOpensByShape', async () => {
    const g = await openEdit(GUIDED, fakeFetch([GUIDED]))
    expect(screen.getByTestId('rule-mode-guided').getAttribute('aria-selected')).toBe('true')
    expect((screen.getByLabelText('Field') as HTMLSelectElement).value).toBe('currency')
    expect((screen.getByLabelText('Check') as HTMLSelectElement).value).toBe('enum')
    expect((screen.getByLabelText('Allowed values') as HTMLInputElement).value).toBe('NGN, USD')
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('Currency is wrong')
    expect((screen.getByRole('radio', { name: 'Block submission' }) as HTMLInputElement).checked).toBe(true)
    expect(g.ctx.closeRule, 'the detail drawer makes way').toHaveBeenCalled()
    g.view.unmount()

    const c = await openEdit(CEL, fakeFetch([CEL]))
    expect(screen.getByTestId('rule-mode-expression').getAttribute('aria-selected')).toBe('true')
    expect((screen.getByLabelText('Expression') as HTMLTextAreaElement).value).toBe('invoice.total > 0.0')
    expect((screen.getByLabelText('Target field') as HTMLSelectElement).value).toBe('total')
    expect((screen.getByRole('radio', { name: 'All clients (firm-wide)' }) as HTMLInputElement).checked).toBe(true)
    c.view.unmount()

    await openEdit(TAX, fakeFetch([TAX]))
    expect(screen.getByTestId('rule-readonly')).toBeTruthy()
    expect(screen.queryByTestId('rule-save')).toBeNull()
    expect(screen.queryByTestId('rule-mode-guided')).toBeNull()
  })

  it('a cel rule with a when stays read-only', async () => {
    const c2 = rule('c2', { type: 'cel', params: { expr: 'true' }, when: 'invoice.total > 1.0' })
    await openEdit(c2, fakeFetch([c2]))
    expect(screen.getByTestId('rule-readonly')).toBeTruthy()
  })

  it('customRuleEditor_editPutsAndKeepsEnabled', async () => {
    const OFF = { ...GUIDED, enabled: false }
    const f = fakeFetch([OFF])
    await openEdit(OFF, f)
    type('Message', 'Currency must be NGN or USD')
    save()

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(writes(f)).toHaveLength(1)
    expect(writes(f)[0][0]).toBe(`${GW}/api/invoice/v1/custom-rules/${OFF.id}`)
    expect(writes(f)[0][1]).toMatchObject({ method: 'PUT' })
    expect(bodyOf(f)).toEqual({
      entity_id: A,
      type: 'enum',
      target: 'currency',
      params: { values: ['NGN', 'USD'] },
      severity: 'error',
      when: null,
      message: 'Currency must be NGN or USD',
      enabled: false,
    })
    await waitFor(() => expect(customMessages()).toEqual(['Currency must be NGN or USD']))
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('false')
  })

  it('customRuleEditor_editNotFoundKeepsTheForm', async () => {
    const f = fakeFetch([GUIDED], () => Promise.reject(new ApiError('http', 'custom rule not found', 404)))
    await openEdit(GUIDED, f)
    type('Message', 'edited')
    save()

    expect((await screen.findByTestId('rule-save-error')).textContent).toBe('custom rule not found')
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('edited')
    expect(screen.getByRole('dialog', { name: 'Edit custom rule' })).toBeTruthy()
    expect(listReads(f)).toHaveLength(1)
  })

  it('customRuleEditor_scopeMoveLeavesTheView', async () => {
    const store = [GUIDED]
    const f = fakeFetch(store, () => {
      store[0] = { ...GUIDED, entity_id: B }
      return store[0]
    })
    await openEdit(GUIDED, f)
    expect(customMessages()).toEqual(['Currency is wrong'])
    save()

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(listReads(f)).toHaveLength(2))
    await waitFor(() => expect(customMessages()).toEqual([]))
    expect(screen.getByText(/^No custom rules yet/)).toBeTruthy()
  })

  it('Cancel and the close button leave without a request', async () => {
    const f = fakeFetch([GUIDED])
    await openEdit(GUIDED, f)
    fireEvent.click(screen.getByText('Cancel'))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(writes(f)).toHaveLength(0)

    await open()
    type('Message', 'typed and abandoned')
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    await open()
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value, 'a reopened editor starts clean').toBe('Invoice number failed the check')
    expect(writes(f)).toHaveLength(0)
  })

  it('an editor open when the viewer stops being an admin goes away', async () => {
    const f = fakeFetch([])
    const { ctx, view } = await mount(f)
    await open()
    view.rerender(<RulesView ctx={{ ...ctx, members: [{ ...ADMIN[0], role: 'preparer' }] } as unknown as PlatformCtx} />)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(writes(f)).toHaveLength(0)
  })
})
