// @vitest-environment jsdom
// AUTH-10-02: the TIN field explains itself before the server enforces it.
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TIN_HINT } from '../lib/entityForm'
import { createEntity, type Entity } from '../lib/portfolio'
import type { PlatformCtx } from '../types'
import { EntityFormModal } from './EntityFormModal'

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

function ctxFor(mode: 'inhouse' | 'firm'): PlatformCtx {
  return { mode, authedFetch: vi.fn() } as unknown as PlatformCtx
}

function mount(mode: 'create' | 'edit', ctxMode: 'inhouse' | 'firm') {
  const utils = render(
    <EntityFormModal
      mode={mode}
      entity={mode === 'edit' ? ENTITY : undefined}
      ctx={ctxFor(ctxMode)}
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
  })
  afterEach(cleanup)

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
})
