// @vitest-environment jsdom
// RESKIN2-06-04: FieldMappingModal against Platform.dc.html:3224-3253, with D-1, D-11 and D-23.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CONNECTOR_DEFS } from '../data'
import { mappingFor } from '../lib/connectors'
import type { PlatformCtx } from '../types'
import { FieldMappingModal } from './FieldMappingModal'

const def = CONNECTOR_DEFS[0]

function mount() {
  const save = vi.fn()
  const onClose = vi.fn()
  const ctx = { connectorMappings: {}, saveConnectorMapping: save } as unknown as PlatformCtx
  const r = render(<FieldMappingModal ctx={ctx} def={def} onClose={onClose} />)
  return { ...r, save, onClose }
}
const panel = () => screen.getByRole('dialog', { name: 'Edit field mapping' })
const scrim = () => panel().parentElement as HTMLElement
const btn = (name: string) => screen.getByRole('button', { name })

afterEach(cleanup)

describe('FieldMappingModal > scrim and panel', () => {
  it('the scrim is the D-1 colour-mix with blur(6px) on both backdrop properties', () => {
    mount()
    const s = scrim()
    expect(s.style.background).toBe('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect(s.style.backdropFilter).toBe('blur(6px)')
    expect((s.style as unknown as Record<string, string>).WebkitBackdropFilter).toBe('blur(6px)')
  })

  it('the panel is 640 wide, --bg-2, a --line-2 border, --radius-lg and --shadow-card', () => {
    mount()
    const p = panel()
    expect(p.style.width).toBe('640px')
    expect(p.style.background).toBe('var(--bg-2)')
    expect(p.style.borderRadius).toBe('var(--radius-lg)')
    expect(p.style.boxShadow).toBe('var(--shadow-card)')
    expect(p.style.border).toContain('var(--line-2)')
  })

  it('no oklch paint remains anywhere in the modal', () => {
    const { container } = mount()
    expect(container.innerHTML.length).toBeGreaterThan(500)
    expect(container.innerHTML).not.toContain('oklch')
  })
})

describe('FieldMappingModal > header', () => {
  it('pads 16px 20px, with a 15/700 title, a mono 10 --fg-3 caption and an --action arrow glyph', () => {
    mount()
    const title = screen.getByText('Edit field mapping', { exact: true })
    expect(title.style.fontSize).toBe('15px')
    expect(title.style.fontWeight).toBe('700')
    const head = title.parentElement!.parentElement!.parentElement as HTMLElement
    expect(head.style.padding).toBe('16px 20px')
    const cap = screen.getByText('ERP FIELD → NRS UBL PATH', { exact: true })
    expect(cap.className).toBe('mono')
    expect(cap.style.fontSize).toBe('10px')
    expect(cap.style.color).toBe('var(--fg-3)')
    const icon = title.parentElement!.previousElementSibling as HTMLElement
    expect(icon.style.color).toBe('var(--action)')
    expect(icon.querySelector('path')?.getAttribute('d')).toBe('M5 12h14M13 6l6 6-6 6')
  })

  it('the close button is 30x30, borderless, transparent, --fg-3, with the 16px close glyph', () => {
    mount()
    const c = btn('Close')
    expect(c.style.width).toBe('30px')
    expect(c.style.height).toBe('30px')
    expect(c.style.border).toMatch(/^(0|0px|none)/)
    expect(c.style.background).toBe('transparent')
    expect(c.style.color).toBe('var(--fg-3)')
    expect(c.querySelector('svg')?.getAttribute('width')).toBe('16')
  })
})

describe('FieldMappingModal > rows (D-11)', () => {
  it('keeps one label per input, so every input has an accessible name', () => {
    const rows = mappingFor(def, {})
    mount()
    expect(rows.length).toBeGreaterThan(0)
    expect(screen.getAllByLabelText('ERP source field')).toHaveLength(rows.length)
    expect(screen.getAllByLabelText('NRS UBL target')).toHaveLength(rows.length)
    expect(document.querySelectorAll('label')).toHaveLength(rows.length * 2)
  })

  it('rows stack with a 10px gap and no row dividers', () => {
    mount()
    const first = screen.getAllByLabelText('ERP source field')[0]
    const row = first.closest('label')!.parentElement as HTMLElement
    expect(row.style.gridTemplateColumns).toBe('minmax(0, 1fr) auto minmax(0, 1fr)')
    expect(row.style.gap).toBe('10px')
    expect(row.style.borderBottom).toBe('')
    const list = row.parentElement as HTMLElement
    expect(list.style.gap).toBe('10px')
    expect(list.style.padding).toBe('16px 20px')
  })

  it('inputs are 34 tall mono 12 on --bg-1 with a --line-2 border and radius-md; the target is --action', () => {
    mount()
    const [erp] = screen.getAllByLabelText('ERP source field') as HTMLInputElement[]
    const [ubl] = screen.getAllByLabelText('NRS UBL target') as HTMLInputElement[]
    for (const i of [erp, ubl]) {
      expect(i.style.height).toBe('34px')
      expect(i.style.fontSize).toBe('12px')
      expect(i.style.padding).toBe('0px 10px')
      expect(i.style.background).toBe('var(--bg-1)')
      expect(i.style.borderRadius).toBe('var(--radius-md)')
      expect(i.style.border).toContain('var(--line-2)')
    }
    expect(erp.style.color).toBe('var(--fg-1)')
    expect(ubl.style.color).toBe('var(--action)')
  })

  it('each row carries the 14px arrow glyph in --fg-4 between its inputs', () => {
    mount()
    const row = screen.getAllByLabelText('ERP source field')[0].closest('label')!.parentElement as HTMLElement
    const arrow = row.children[1] as HTMLElement
    expect(arrow.style.color).toBe('var(--fg-4)')
    expect(arrow.getAttribute('aria-hidden')).toBe('true')
    expect(arrow.querySelector('path')?.getAttribute('d')).toBe('M5 12h14M13 6l6 6-6 6')
  })
})

describe('FieldMappingModal > footer and behaviour', () => {
  it('Cancel and Save are 36 tall pf-btn buttons; Save is --action with --primary-foreground text', () => {
    mount()
    const cancel = btn('Cancel')
    const save = btn('Save mapping')
    for (const b of [cancel, save]) {
      expect(b.className).toBe('pf-btn')
      expect(b.style.height).toBe('36px')
      expect(b.style.borderRadius).toBe('var(--radius-btn)')
    }
    expect(cancel.style.background).toBe('var(--bg-2)')
    expect(cancel.style.color).toBe('var(--fg-2)')
    expect(save.style.background).toBe('var(--action)')
    expect(save.style.color).toBe('var(--primary-foreground)')
  })

  it('Save lifts the edited draft once and closes; Cancel and the scrim discard', () => {
    const { save, onClose } = mount()
    const [erp] = screen.getAllByLabelText('ERP source field') as HTMLInputElement[]
    fireEvent.change(erp, { target: { value: 'ZZ-EDITED' } })
    fireEvent.click(btn('Save mapping'))
    expect(save).toHaveBeenCalledTimes(1)
    const [id, rows] = save.mock.calls[0]
    expect(id).toBe(def.id)
    expect(rows[0].erp).toBe('ZZ-EDITED')
    expect(onClose).toHaveBeenCalledTimes(1)
    onClose.mockClear()
    fireEvent.click(btn('Cancel'))
    fireEvent.click(scrim())
    expect(onClose).toHaveBeenCalledTimes(2)
    expect(save).toHaveBeenCalledTimes(1)
  })

  it('a click inside the panel does not close it', () => {
    const { onClose } = mount()
    fireEvent.click(panel())
    expect(onClose).not.toHaveBeenCalled()
  })
})
