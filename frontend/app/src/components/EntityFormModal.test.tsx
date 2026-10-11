// @vitest-environment jsdom
// AUTH-10-02: the TIN field explains itself before the server enforces it.
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TIN_HINT } from '../lib/entityForm'
import { createAuthedFetch } from '../lib/authedFetch'
import { createEntity, updateEntity, type Entity } from '../lib/portfolio'
import type { PlatformCtx } from '../types'
import { AddCompanyTask } from './AddCompanyTask'
import { ClientsView } from './ClientsView'
import { EntityFormModal } from './EntityFormModal'
import { SettingsView } from './SettingsView'

vi.mock('../lib/portfolio', async (importActual) => ({
  ...(await importActual<typeof import('../lib/portfolio')>()),
  createEntity: vi.fn(),
  updateEntity: vi.fn(),
}))

// internal/portfolio/tin.go TINChecksumMessage.
const TIN_CHECKSUM =
  "This TIN's last digit is a check digit, and it does not match the other digits. Check the number on the tax certificate."

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

function ctxFor(mode: 'inhouse' | 'firm', refetchMembers = vi.fn()): PlatformCtx {
  return { mode, authedFetch: vi.fn(), refetchMembers } as unknown as PlatformCtx
}

function mount(mode: 'create' | 'edit', ctxMode: 'inhouse' | 'firm', refetchMembers = vi.fn()) {
  const utils = render(
    <EntityFormModal
      mode={mode}
      entity={mode === 'edit' ? ENTITY : undefined}
      ctx={ctxFor(ctxMode, refetchMembers)}
      base="https://gateway.test"
      onClose={() => {}}
      onSuccess={() => {}}
    />,
  )
  return { ...utils, dialog: within(screen.getByRole('dialog')) }
}

// A hint that is '' would make every getByText below vacuous.
function expectHintDefined() {
  expect(TIN_HINT.length).toBeGreaterThan(0)
}

describe('EntityFormModal TIN hint (AUTH-10-02)', () => {
  beforeEach(() => {
    vi.mocked(createEntity).mockReset()
    vi.mocked(updateEntity).mockReset()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })

  it('the create modal explains the TIN before anything is submitted', () => {
    for (const ctxMode of ['inhouse', 'firm'] as const) {
      const { dialog, unmount } = mount('create', ctxMode)
      expectHintDefined()
      expect(dialog.getByText(TIN_HINT)).toBeTruthy()
      expect(createEntity).not.toHaveBeenCalled()
      unmount()
    }
  })

  it('the TIN input is described by the hint', () => {
    const { dialog } = mount('create', 'inhouse')
    expectHintDefined()
    const input = dialog.getByPlaceholderText('########-####')
    expect(input.getAttribute('aria-describedby')).toBe('entity-tin-hint')
    expect(document.getElementById('entity-tin-hint')?.textContent).toBe(TIN_HINT)
  })

  it("a refused TIN shows the server's reason beside the hint", async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', TIN_CHECKSUM, 400))
    const { container, dialog } = mount('create', 'inhouse')
    fireEvent.change(container.querySelector('input.pf-input') as HTMLInputElement, { target: { value: 'Acme Ltd' } })
    fireEvent.change(dialog.getByPlaceholderText('########-####'), { target: { value: '1234567890' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Add company' }))

    await waitFor(() => expect(dialog.getByText(TIN_CHECKSUM)).toBeTruthy())
    expect(createEntity).toHaveBeenCalledTimes(1)
    expectHintDefined()
    expect(dialog.getByText(TIN_HINT)).toBeTruthy()
  })

  it('the edit modal explains the TIN too', () => {
    for (const ctxMode of ['inhouse', 'firm'] as const) {
      const { dialog, unmount } = mount('edit', ctxMode)
      expectHintDefined()
      expect(dialog.getByText(TIN_HINT)).toBeTruthy()
      unmount()
    }
  })

  it('the hint names both TIN forms and the supplier TIN check', () => {
    expect(TIN_HINT).toMatch(/12-digit FIRS/)
    expect(TIN_HINT).toMatch(/10-digit JTB/)
    expect(TIN_HINT).toMatch(/supplier TIN check/)
  })
  function fillAndSubmit(dialog: ReturnType<typeof within>, container: HTMLElement, tin: string, label = 'Add company') {
    fireEvent.change(container.querySelector('input.pf-input') as HTMLInputElement, { target: { value: 'Acme Ltd' } })
    fireEvent.change(dialog.getByPlaceholderText('########-####'), { target: { value: tin } })
    fireEvent.click(dialog.getByRole('button', { name: label }))
  }

  function describedHint(dialog: ReturnType<typeof within>): HTMLElement {
    const id = dialog.getByPlaceholderText('########-####').getAttribute('aria-describedby')
    expect(id).toBeTruthy()
    const el = document.getElementById(id as string)
    expect(el).not.toBeNull()
    return el as HTMLElement
  }

  it('the refused reason sits after the hint and is not inside the described element', async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', TIN_CHECKSUM, 400))
    const { container, dialog } = mount('create', 'inhouse')
    fillAndSubmit(dialog, container, '1234567890')
    const reason = await dialog.findByText(TIN_CHECKSUM)

    const hint = describedHint(dialog)
    expect(hint.textContent).toBe(TIN_HINT)
    expect(hint.contains(reason)).toBe(false)
    expect(hint.compareDocumentPosition(reason) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(document.querySelectorAll('#entity-tin-hint')).toHaveLength(1)
  })

  it.each([
    ['400 reason', 400, TIN_CHECKSUM, TIN_CHECKSUM],
    ['400 with an empty body', 400, '', 'Please check the TIN and try again.'],
    ['409 duplicate', 409, 'duplicate', 'This TIN is already registered.'],
  ])('a %s shows its reason and keeps the hint', async (_label, status, serverMsg, shown) => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', serverMsg, status))
    const { container, dialog } = mount('create', 'firm')
    fillAndSubmit(dialog, container, '1234567890', 'Add client')
    expect(await dialog.findByText(shown)).toBeTruthy()
    expect(dialog.getAllByText(TIN_HINT)).toHaveLength(1)
    expect(describedHint(dialog).textContent).toBe(TIN_HINT)
  })

  it('the client-side TIN-required message shows beside the hint without a server call', async () => {
    const { container, dialog } = mount('create', 'inhouse')
    fireEvent.change(container.querySelector('input.pf-input') as HTMLInputElement, { target: { value: 'Acme Ltd' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Add company' }))
    expect(await dialog.findByText('TIN is required')).toBeTruthy()
    expect(createEntity).not.toHaveBeenCalled()
    expect(dialog.getAllByText(TIN_HINT)).toHaveLength(1)
  })

  it('a form-level failure keeps the hint', async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', 'boom', 500))
    const { container, dialog } = mount('create', 'inhouse')
    fillAndSubmit(dialog, container, '1234567890')
    expect(await dialog.findByText('Something went wrong. Please try again.')).toBeTruthy()
    expect(dialog.getAllByText(TIN_HINT)).toHaveLength(1)
  })

  it('the edit modal keeps the hint beside a refused TIN and describes the input', async () => {
    vi.mocked(updateEntity).mockRejectedValue(new ApiError('http', TIN_CHECKSUM, 400))
    const { dialog } = mount('edit', 'firm')
    fireEvent.change(dialog.getByPlaceholderText('########-####'), { target: { value: '1234567890' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save changes' }))
    expect(await dialog.findByText(TIN_CHECKSUM)).toBeTruthy()
    expect(updateEntity).toHaveBeenCalledTimes(1)
    expect(dialog.getAllByText(TIN_HINT)).toHaveLength(1)
    expect(describedHint(dialog).textContent).toBe(TIN_HINT)
  })

  it('only the TIN input points at the hint', () => {
    const { dialog } = mount('create', 'inhouse')
    const inputs = Array.from(dialog.getAllByRole('textbox'))
    expect(inputs).toHaveLength(5)
    const described = inputs.filter((i) => i.getAttribute('aria-describedby') === 'entity-tin-hint')
    expect(described).toHaveLength(1)
    expect((described[0] as HTMLInputElement).placeholder).toBe('########-####')
  })

  it('SettingsView Company tab (in-house) shows the hint in create and edit', () => {
    for (const entity of [undefined, ENTITY]) {
      const ctx = {
        mode: 'inhouse',
        settingsTab: 'company',
        sandbox: false,
        connectors: {},
        connectorMappings: {},
        activeEntity: entity,
        entitiesState: 'ready',
        entitiesError: null,
        refetchEntities: vi.fn(),
        members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
        membersState: 'ready',
        setSettingsTab: vi.fn(),
        authedFetch: vi.fn(),
      } as unknown as PlatformCtx
      const { unmount } = render(<SettingsView ctx={ctx} />)
      fireEvent.click(screen.getByRole('button', { name: entity ? 'Edit company' : 'Add company' }))
      expect(within(screen.getByRole('dialog')).getByText(TIN_HINT)).toBeTruthy()
      unmount()
    }
  })

  it('ClientsView (firm) shows the hint in the Add client and Edit client modals', async () => {
    const rows = [ENTITY]
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        const body = new URL(url).pathname.endsWith('/rollup')
          ? { totals: { counts: {}, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] }, clients: [], top_violations: [] }
          : { entities: rows, pagination: { limit: 200, offset: 0, total: rows.length } }
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
      }),
    )
    const ctx = {
      mode: 'firm',
      authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
      user: { name: 'F', initials: 'F', tenantName: 'Acme', verified: true },
      entities: rows,
      entitiesState: 'ready',
      entitiesError: null,
      refetchEntities: vi.fn(),
      members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
      membersState: 'ready',
    } as unknown as PlatformCtx
    render(<ClientsView ctx={ctx} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Add client' }))
    expect(within(screen.getByRole('dialog')).getByText(TIN_HINT)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()

    fireEvent.click(await screen.findByText(ENTITY.name))
    const edit = within(await screen.findByRole('dialog'))
    expect(edit.getByText('Edit client')).toBeTruthy()
    expect(edit.getByText(TIN_HINT)).toBeTruthy()
  })
})

const ADMIN_ONLY = 'only an admin can add a company' // internal/portfolio ErrNotPermitted
const GENERIC = 'Something went wrong. Please try again.'

describe('EntityFormModal refused create (LOGFIX-06-04)', () => {
  beforeEach(() => {
    vi.mocked(createEntity).mockReset()
    vi.mocked(updateEntity).mockReset()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllEnvs()
  })

  function fillAndSubmit(dialog: ReturnType<typeof within>, container: HTMLElement, tin: string) {
    fillName(container)
    fireEvent.change(dialog.getByPlaceholderText('########-####'), { target: { value: tin } })
    fireEvent.click(dialog.getByRole('button', { name: 'Add company' }))
  }

  function fillName(container: HTMLElement, name = 'Acme Ltd') {
    fireEvent.change(container.querySelector('input.pf-input') as HTMLInputElement, { target: { value: name } })
  }

  it('a refused create shows the reason and re-reads the roster', async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', ADMIN_ONLY, 403))
    const refetchMembers = vi.fn()
    const { container, dialog } = mount('create', 'inhouse', refetchMembers)
    fillAndSubmit(dialog, container, '1234567890')
    expect(await dialog.findByText(ADMIN_ONLY)).toBeTruthy()
    expect(dialog.queryByText(GENERIC)).toBeNull()
    expect(refetchMembers).toHaveBeenCalledTimes(1)
  })

  it('a refused create with an empty 403 message falls back', async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', '', 403))
    const refetchMembers = vi.fn()
    const { container, dialog } = mount('create', 'inhouse', refetchMembers)
    fillAndSubmit(dialog, container, '1234567890')
    expect(await dialog.findByText(GENERIC)).toBeTruthy()
    expect(refetchMembers).toHaveBeenCalledTimes(1)
  })

  it('a refused edit keeps the generic message', async () => {
    vi.mocked(updateEntity).mockRejectedValue(new ApiError('http', 'your membership in this workspace is not active', 403))
    const refetchMembers = vi.fn()
    const { container, dialog } = mount('edit', 'inhouse', refetchMembers)
    fillName(container, 'Renamed Ltd')
    fireEvent.click(dialog.getByRole('button', { name: 'Save changes' }))
    expect(await dialog.findByText(GENERIC)).toBeTruthy()
    expect(dialog.queryByText('your membership in this workspace is not active')).toBeNull()
    expect(refetchMembers).not.toHaveBeenCalled()
  })

  it('a 409 does not re-read the roster', async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', 'duplicate', 409))
    const refetchMembers = vi.fn()
    const { container, dialog } = mount('create', 'inhouse', refetchMembers)
    fillAndSubmit(dialog, container, '1234567890')
    expect(await dialog.findByText('This TIN is already registered.')).toBeTruthy()
    expect(refetchMembers).not.toHaveBeenCalled()
  })

  it("a demoted admin's screen turns into No company created", async () => {
    vi.mocked(createEntity).mockRejectedValue(new ApiError('http', ADMIN_ONLY, 403))
    const member = (role: string) => ({ id: 'u1', name: 'Ada', initials: 'A', email: null, role, status: 'active', isYou: true })
    const refetchMembers = vi.fn()
    const ctxWith = (role: string) =>
      ({
        mode: 'inhouse',
        entitiesState: 'empty',
        entities: [],
        clients: [],
        activeEntity: null,
        entitiesError: null,
        refetchEntities: vi.fn(),
        members: [member(role)],
        membersState: 'ready',
        membersError: null,
        refetchMembers,
        authedFetch: vi.fn(),
        user: { name: 'Ada', initials: 'A', tenantName: 'Acme Ltd', verified: true },
      }) as unknown as PlatformCtx
    const { container, rerender } = render(<AddCompanyTask ctx={ctxWith('admin')} />)
    fireEvent.click(within(screen.getByTestId('add-company-task')).getByRole('button', { name: 'Add company' }))
    const dialog = within(screen.getByRole('dialog'))
    fillAndSubmit(dialog, container, '1234567890')
    expect(await dialog.findByText(ADMIN_ONLY)).toBeTruthy()
    expect(refetchMembers).toHaveBeenCalledTimes(1)

    rerender(<AddCompanyTask ctx={ctxWith('preparer')} />)
    expect(screen.getByTestId('company-setup-waiting')).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByTestId('add-company-task')).toBeNull()
  })
})

// jsdom drops backdrop-filter from the style attribute, so the scrim is read from SSR markup.
function ssrBackdropDecls(): { html: string; decls: Map<string, string> } {
  const html = renderToStaticMarkup(
    <EntityFormModal mode="create" ctx={ctxFor('firm')} base="https://gateway.test" onClose={() => {}} onSuccess={() => {}} />,
  )
  const style = /^<div style="([^"]*)"/.exec(html)?.[1]
  expect(style, 'the outermost element is the backdrop').toBeTruthy()
  return { html, decls: new Map(style!.split(';').filter(Boolean).map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)])) }
}

describe('PR-01 the entity modal sits 10px over the v2 scrim', () => {
  afterEach(cleanup)

  it('the panel is radius-lg on shadow-card', () => {
    mount('create', 'firm')
    const panel = screen.getByRole('dialog')

    expect(panel.style.width, 'control: the panel style is read').toBe('480px')
    expect(panel.style.borderRadius).toBe('var(--radius-lg)')
    expect(panel.style.boxShadow).toBe('var(--shadow-card)')
  })

  it('the scrim is a token mix with both blur properties, and no oklch anywhere', () => {
    const { html, decls } = ssrBackdropDecls()

    expect(decls.get('position'), 'control: the backdrop declarations are read').toBe('fixed')
    expect(decls.get('background')).toBe('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect(decls.get('backdrop-filter')).toBe('blur(6px)')
    expect(decls.get('-webkit-backdrop-filter')).toBe('blur(6px)')
    expect(html).not.toContain('oklch')
  })

  describe('every caller opens the v2 panel', () => {
    beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test'))
    afterEach(() => {
      vi.unstubAllEnvs()
      vi.unstubAllGlobals()
    })

    function expectV2Panel() {
      const panel = screen.getByRole('dialog')
      expect(panel.style.width, 'control: the panel style is read').toBe('480px')
      expect(panel.style.borderRadius).toBe('var(--radius-lg)')
      expect(panel.style.boxShadow).toBe('var(--shadow-card)')
      expect(panel.parentElement?.style.position, 'the panel sits in the fixed backdrop').toBe('fixed')
    }

    it('ClientsView Add client', async () => {
      const rows = [ENTITY]
      vi.stubGlobal(
        'fetch',
        vi.fn((url: string) => {
          const body = new URL(url).pathname.endsWith('/rollup')
            ? { totals: { counts: {}, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] }, clients: [], top_violations: [] }
            : { entities: rows, pagination: { limit: 200, offset: 0, total: rows.length } }
          return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
        }),
      )
      const ctx = {
        mode: 'firm',
        authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
        user: { name: 'F', initials: 'F', tenantName: 'Acme', verified: true },
        entities: rows,
        entitiesState: 'ready',
        entitiesError: null,
        refetchEntities: vi.fn(),
        members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
        membersState: 'ready',
      } as unknown as PlatformCtx
      render(<ClientsView ctx={ctx} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Add client' }))
      expectV2Panel()
    })

    it('AddCompanyTask Add company', () => {
      const ctx = {
        mode: 'inhouse',
        entitiesState: 'empty',
        entities: [],
        clients: [],
        activeEntity: null,
        entitiesError: null,
        refetchEntities: vi.fn(),
        members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
        membersState: 'ready',
        membersError: null,
        refetchMembers: vi.fn(),
        authedFetch: vi.fn(),
        user: { name: 'Ada', initials: 'A', tenantName: 'Acme Ltd', verified: true },
      } as unknown as PlatformCtx
      render(<AddCompanyTask ctx={ctx} />)

      fireEvent.click(within(screen.getByTestId('add-company-task')).getByRole('button', { name: 'Add company' }))
      expectV2Panel()
    })

    it('SettingsView Company tab', () => {
      const ctx = {
        mode: 'inhouse',
        settingsTab: 'company',
        sandbox: false,
        connectors: {},
        connectorMappings: {},
        activeEntity: null,
        entitiesState: 'ready',
        entitiesError: null,
        refetchEntities: vi.fn(),
        members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
        membersState: 'ready',
        setSettingsTab: vi.fn(),
        authedFetch: vi.fn(),
      } as unknown as PlatformCtx
      render(<SettingsView ctx={ctx} />)

      fireEvent.click(screen.getByRole('button', { name: 'Add company' }))
      expectV2Panel()
    })
  })

  // An inline border-color or box-shadow on the input would beat the class :focus ring.
  it('the inputs leave the focus ring to the class (pin, green at write)', () => {
    const { container } = mount('create', 'firm')
    const inputs = Array.from(container.querySelectorAll<HTMLInputElement>('input.pf-input'))

    expect(inputs, 'the five fields render').toHaveLength(5)
    for (const input of inputs) {
      expect(input.style.borderColor, 'inline border-color').toBe('')
      expect(input.style.boxShadow, 'inline box-shadow').toBe('')
      expect(input.style.border, 'inline border').toBe('')
    }
  })
})
