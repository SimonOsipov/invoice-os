// @vitest-environment jsdom
// AUTH-10-03: a workspace with no company lands on the add-company task.
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import type { AsyncStatus } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { emptyClient } from '../lib/clients'
import { createEntity } from '../lib/portfolio'
import type { Entity } from '../lib/portfolio'
import type { Mode, PlatformCtx } from '../types'
import { ADD_COMPANY_COPY, AddCompanyTask } from './AddCompanyTask'

vi.mock('../lib/portfolio', async (importActual) => ({
  ...(await importActual<typeof import('../lib/portfolio')>()),
  createEntity: vi.fn(),
}))

const ENTITY: Entity = {
  id: 'e1',
  name: 'Lagos Freight',
  tin: '20184412-0001',
  registration: null,
  sector: null,
  address: null,
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
}

interface CtxOpts {
  mode?: Mode
  entitiesState?: AsyncStatus
  entities?: Entity[]
  clientsCount?: number
  tenantName?: string | null
  entitiesError?: ApiError | null
}

function mkCtx(opts: CtxOpts = {}) {
  const refetchEntities = vi.fn()
  const ctx = {
    mode: opts.mode ?? 'inhouse',
    entitiesState: opts.entitiesState ?? 'empty',
    entities: opts.entities ?? [],
    clients: Array.from({ length: opts.clientsCount ?? 0 }, () => emptyClient()),
    activeEntity: null,
    active: emptyClient(),
    entitiesError: opts.entitiesError ?? null,
    refetchEntities,
    authedFetch: vi.fn(),
    user: { name: 'Ada', initials: 'A', tenantName: opts.tenantName === undefined ? 'Acme Ltd' : opts.tenantName, verified: true },
  } as unknown as PlatformCtx
  return { ctx, refetchEntities }
}

const trigger = () => within(screen.getByTestId('add-company-task')).getByRole('button')

describe('AddCompanyTask (AUTH-10-03)', () => {
  beforeEach(() => {
    vi.mocked(createEntity).mockReset()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllEnvs()
  })

  it('an in-house workspace with no company lands on Add your company', () => {
    render(<AddCompanyTask ctx={mkCtx({ mode: 'inhouse' }).ctx} />)
    const copy = ADD_COMPANY_COPY.inhouse
    expect(screen.getByRole('heading', { level: 1, name: 'Add your company' })).toBeTruthy()
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe(copy.h1)
    expect(screen.getByText('OVERVIEW')).toBeTruthy()
    expect(screen.getByText(copy.emptyTitle)).toBeTruthy()
    expect(screen.getByText(copy.emptyMessage)).toBeTruthy()
    expect(trigger().textContent?.trim()).toBe(copy.button)
    expect(screen.getByText('No company yet')).toBeTruthy()
    expect(screen.getByText("Invoices are filed for a registered company. Add yours — you'll need its name and its TIN.")).toBeTruthy()
    expect(trigger().textContent?.trim()).toBe('Add company')
    expect(screen.getByText('Acme Ltd · invoices are filed for a registered company.')).toBeTruthy()
    expect(screen.queryByText(/No client/)).toBeNull()
    expect(screen.queryByText(/COMPLIANCE OVERVIEW/)).toBeNull()
  })

  it('a firm with no clients lands on Add your first client', () => {
    render(<AddCompanyTask ctx={mkCtx({ mode: 'firm' }).ctx} />)
    const copy = ADD_COMPANY_COPY.firm
    expect(screen.getByRole('heading', { level: 1, name: 'Add your first client' })).toBeTruthy()
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe(copy.h1)
    expect(screen.getByText('OVERVIEW')).toBeTruthy()
    expect(screen.getByText(copy.emptyTitle)).toBeTruthy()
    expect(screen.getByText(copy.emptyMessage)).toBeTruthy()
    expect(trigger().textContent?.trim()).toBe(copy.button)
    expect(screen.getByText('No clients yet')).toBeTruthy()
    expect(screen.getByText("Invoices are filed for a registered company. Add the first client you file for — you'll need its name and its TIN.")).toBeTruthy()
    expect(trigger().textContent?.trim()).toBe('Add client')
    expect(screen.queryByText(/COMPLIANCE OVERVIEW/)).toBeNull()
    expect(screen.queryByText(emptyClient().name)).toBeNull()
  })

  it("the task's button opens the shared entity form", () => {
    for (const [mode, title] of [['inhouse', 'Add company'], ['firm', 'Add client']] as const) {
      const { unmount } = render(<AddCompanyTask ctx={mkCtx({ mode }).ctx} />)
      expect(screen.queryByRole('dialog')).toBeNull()
      fireEvent.click(trigger())
      const dialog = screen.getByRole('dialog', { name: title })
      expect(within(dialog).getByPlaceholderText('########-####')).toBeTruthy()
      unmount()
    }
  })

  it('adding the company refetches the roster and closes the form', async () => {
    vi.mocked(createEntity).mockResolvedValue(ENTITY)
    const { ctx, refetchEntities } = mkCtx({ mode: 'inhouse' })
    render(<AddCompanyTask ctx={ctx} />)
    fireEvent.click(trigger())
    const dialog = within(screen.getByRole('dialog', { name: 'Add company' }))
    fireEvent.change(document.querySelector('[role="dialog"] input.pf-input') as HTMLInputElement, { target: { value: 'Acme Ltd' } })
    fireEvent.change(dialog.getByPlaceholderText('########-####'), { target: { value: '1234567897' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Add company' }))

    await waitFor(() => expect(refetchEntities).toHaveBeenCalledTimes(1))
    expect(createEntity).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(refetchEntities).toHaveBeenCalledTimes(1)
  })

  it('a loading roster shows Loading, never the task', () => {
    const cases: CtxOpts[] = [
      { entitiesState: 'loading' },
      { entitiesState: 'ready', entities: [ENTITY, { ...ENTITY, id: 'e2' }], clientsCount: 0 },
    ]
    for (const opts of cases) {
      const { unmount } = render(<AddCompanyTask ctx={mkCtx(opts).ctx} />)
      expect(screen.getByText('Loading your workspace…')).toBeTruthy()
      expect(screen.queryByTestId('add-company-task')).toBeNull()
      expect(screen.queryByRole('button', { name: ADD_COMPANY_COPY.inhouse.button })).toBeNull()
      unmount()
    }
  })

  it('a failed roster offers a retry', () => {
    const { ctx, refetchEntities } = mkCtx({ entitiesState: 'error', entitiesError: new ApiError('http', 'boom', 500) })
    render(<AddCompanyTask ctx={ctx} />)
    expect(screen.getByText('boom')).toBeTruthy()
    expect(screen.queryByTestId('add-company-task')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(refetchEntities).toHaveBeenCalledTimes(1)
  })

  it("with no gateway the task's button is disabled", () => {
    const { unmount } = render(<AddCompanyTask ctx={mkCtx().ctx} />)
    expect((trigger() as HTMLButtonElement).disabled).toBe(false)
    unmount()

    vi.stubEnv('VITE_GATEWAY_URL', '')
    render(<AddCompanyTask ctx={mkCtx({ entitiesState: 'idle' }).ctx} />)
    expect((trigger() as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(trigger())
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('a firm whose roster has entities but no clients yet shows Loading, never the task', () => {
    render(<AddCompanyTask ctx={mkCtx({ mode: 'firm', entitiesState: 'ready', entities: [ENTITY], clientsCount: 0 }).ctx} />)
    expect(screen.getByText('Loading your workspace…')).toBeTruthy()
    expect(screen.queryByTestId('add-company-task')).toBeNull()
    expect(screen.queryByRole('button', { name: ADD_COMPANY_COPY.firm.button })).toBeNull()
  })

  it('the task gives way to Loading when a roster refetch starts', () => {
    const { ctx } = mkCtx({ mode: 'inhouse' })
    const { rerender } = render(<AddCompanyTask ctx={ctx} />)
    expect(screen.getByTestId('add-company-task')).toBeTruthy()
    rerender(<AddCompanyTask ctx={{ ...ctx, entitiesState: 'loading' } as PlatformCtx} />)
    expect(screen.queryByTestId('add-company-task')).toBeNull()
    expect(screen.getByText('Loading your workspace…')).toBeTruthy()
  })

  it('with no tenant name the subtitle says Your workspace', () => {
    render(<AddCompanyTask ctx={mkCtx({ tenantName: null }).ctx} />)
    expect(screen.getByTestId('add-company-task')).toBeTruthy()
    expect(screen.getByText(/^Your workspace ·/)).toBeTruthy()
  })
})

// inline-token reads; geometry (left edges equal) is OV-04's, deployed.
describe('AddCompanyTask Overview look (D-28, D-37)', () => {
  const PAD = '30px 36px 56px'

  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllEnvs()
  })

  it('the header is a plain 26px block with a 28px / -0.03em h1 and no inline weight', () => {
    for (const mode of ['inhouse', 'firm'] as const) {
      const { container, unmount } = render(<AddCompanyTask ctx={mkCtx({ mode }).ctx} />)
      const wrapper = container.firstElementChild as HTMLElement
      const h1 = screen.getByRole('heading', { level: 1 })
      const header = h1.parentElement as HTMLElement

      expect(wrapper.style.padding).toBe(PAD)
      expect(header.parentElement).toBe(wrapper)
      expect(header.style.marginBottom).toBe('26px')
      expect(header.style.display).toBe('')
      expect([...header.children].map((c) => c.tagName)).toEqual(['DIV', 'H1', 'P'])
      expect((header.children[0] as HTMLElement).style.marginBottom).toBe('10px')
      expect(h1.style.fontSize).toBe('28px')
      expect(h1.style.letterSpacing).toBe('-0.03em')
      expect(h1.style.margin).toBe('0px 0px 5px')
      expect(h1.style.fontWeight).toBe('')
      const sub = header.children[2] as HTMLElement
      expect(sub.style.fontSize).toBe('14px')
      expect(sub.style.overflowWrap).toBe('anywhere')
      unmount()
    }
  })

  it('the task card holds the Add button (dense, in-card)', () => {
    render(<AddCompanyTask ctx={mkCtx().ctx} />)

    const task = screen.getByTestId('add-company-task')
    const header = screen.getByRole('heading', { level: 1 }).parentElement
    expect(header!.nextElementSibling).toBe(task)
    expect(task.children, 'one card, no button row beside it').toHaveLength(1)
    const card = task.firstElementChild as HTMLElement
    const buttons = within(card).getAllByRole('button')
    expect(buttons).toHaveLength(1)
    expect(card.lastElementChild).toBe(buttons[0])
    expect(card.style.padding).toBe('48px')
    expect(card.style.background).toBe('transparent')
    const message = within(card).getByText(ADD_COMPANY_COPY.inhouse.emptyMessage)
    expect(message.style.maxWidth).toBe('460px')
    expect(message.style.margin).toBe('0px 0px 20px')
  })

  it('QA: both modes draw one dense card at width 460, the Add button its last child, also with no gateway', () => {
    for (const [mode, gateway] of [['inhouse', 'https://gateway.test'], ['firm', 'https://gateway.test'], ['firm', '']] as const) {
      vi.stubEnv('VITE_GATEWAY_URL', gateway)
      const { unmount } = render(<AddCompanyTask ctx={mkCtx({ mode }).ctx} />)
      const task = screen.getByTestId('add-company-task')
      expect(task.children, `${mode}/${gateway}: one card`).toHaveLength(1)
      const card = task.firstElementChild as HTMLElement
      const button = within(card).getByRole('button')
      expect(card.lastElementChild, `${mode}/${gateway}: the button is the card's last child`).toBe(button)
      expect(card.style.padding).toBe('48px')
      expect(within(card).getByText(ADD_COMPANY_COPY[mode].emptyMessage).style.maxWidth).toBe('460px')
      expect((button as HTMLButtonElement).disabled, `${mode}/${gateway}: disabled only without a gateway`).toBe(gateway === '')
      unmount()
    }
  })

  it('loading renders inside the same padded wrapper as the task', () => {
    for (const opts of [{ entitiesState: 'loading' }, { mode: 'firm', entitiesState: 'ready', entities: [ENTITY], clientsCount: 0 }] as CtxOpts[]) {
      const { container, unmount } = render(<AddCompanyTask ctx={mkCtx(opts).ctx} />)
      const wrapper = container.firstElementChild as HTMLElement
      expect(wrapper.style.padding).toBe(PAD)
      expect(wrapper.contains(screen.getByText('Loading your workspace…'))).toBe(true)
      expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
      unmount()
    }
  })

  it('error renders its message and Retry inside the same padded wrapper', () => {
    const { ctx } = mkCtx({ entitiesState: 'error', entitiesError: new ApiError('http', 'boom', 500) })
    const { container } = render(<AddCompanyTask ctx={ctx} />)

    const wrapper = container.firstElementChild as HTMLElement
    expect(wrapper.style.padding).toBe(PAD)
    expect(wrapper.contains(screen.getByText('boom'))).toBe(true)
    expect(wrapper.contains(screen.getByRole('button', { name: 'Retry' }))).toBe(true)
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
  })
})
