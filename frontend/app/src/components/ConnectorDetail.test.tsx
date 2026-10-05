// @vitest-environment jsdom
// RESKIN2-06-04: ConnectorDetail resolved paint against Platform.dc.html:2724-2850.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CONNECTOR_DEFS } from '../data'
import { connectorDetail } from '../lib/connectors'
import type { PlatformCtx } from '../types'
import { ConnectorDetail } from './ConnectorDetail'

const ctx = { connectorMappings: {} } as unknown as PlatformCtx

function mount(def = CONNECTOR_DEFS[0]) {
  return render(<ConnectorDetail ctx={ctx} def={def} env="SANDBOX" onBack={vi.fn()} onEditMapping={vi.fn()} />)
}
const card = (heading: string) => {
  let el: HTMLElement | null = screen.getByText(heading, { exact: true })
  while (el && el.style.borderRadius === '') el = el.parentElement
  return el as HTMLElement
}
const sandbox = () => screen.getByText('SANDBOX', { exact: false }).closest('button') as HTMLButtonElement
const live = () => screen.getByTestId('connector-env-pill-live') as HTMLButtonElement

afterEach(cleanup)

describe('ConnectorDetail > env pill and DRIFT', () => {
  it('the env pill is radius-md and both segments are radius-sm, 24 tall, 9.5/700, with 50% dots', () => {
    mount()
    expect(screen.getByTestId('connector-env-pill').style.borderRadius).toBe('var(--radius-md)')
    for (const b of [sandbox(), live()]) {
      expect(b.style.borderRadius).toBe('var(--radius-sm)')
      expect(b.style.height).toBe('24px')
      expect(b.style.fontSize).toBe('9.5px')
      expect(b.style.fontWeight).toBe('700')
      expect((b.querySelector('span') as HTMLElement).style.borderRadius).toBe('50%')
    }
  })

  // .pf-btn / .v2-btn force `border-radius: 7px !important`, which beats the inline radius.
  it('neither segment wears a button class that forces the 7px radius over radius 4', () => {
    mount()
    for (const b of [sandbox(), live()]) {
      expect(b.className).not.toMatch(/\b(pf-btn|v2-btn)\b/)
    }
  })

  it('LIVE is disabled with the D-3 paint', () => {
    mount()
    expect(live().disabled).toBe(true)
    expect(live().style.opacity).toBe('0.45')
    expect(live().style.cursor).toBe('not-allowed')
    expect(live().style.filter).toBe('none')
  })

  // The prototype's `button:disabled` rule dims every disabled button, SANDBOX included (Platform.dc.html:51).
  it('SANDBOX is a disabled control too and takes the same D-3 paint', () => {
    mount()
    expect(sandbox().disabled).toBe(true)
    expect(sandbox().style.opacity).toBe('0.45')
    expect(sandbox().style.cursor).toBe('not-allowed')
  })

  it('the DRIFT pill is radius-sm in the amber triplet when drift is non-zero and green when zero', () => {
    const drifts = CONNECTOR_DEFS.map((d) => ({ d, n: connectorDetail(d).funnel.drift }))
    const dirty = drifts.find((x) => x.n > 0)
    const clean = drifts.find((x) => x.n === 0)
    expect(dirty, 'a fixture with drift').toBeTruthy()
    expect(clean, 'a fixture without drift').toBeTruthy()
    mount(dirty!.d)
    const pill = screen.getByText(/^DRIFT /)
    expect(pill.style.borderRadius).toBe('var(--radius-sm)')
    expect(pill.style.color).toBe('var(--status-amber-text)')
    cleanup()
    mount(clean!.d)
    expect(screen.getByText(/^DRIFT /).style.color).toBe('var(--status-green-text)')
  })
})

describe('ConnectorDetail > header and health strip', () => {
  it('the header title is 16/700 and the category chip is radius-sm', () => {
    mount()
    const name = screen.getByText(CONNECTOR_DEFS[0].name, { exact: true })
    expect(name.style.fontSize).toBe('16px')
    expect(name.style.fontWeight).toBe('700')
    expect((name.nextElementSibling as HTMLElement).style.borderRadius).toBe('var(--radius-sm)')
  })

  it('the monogram tile and SANDBOX text are white (--primary-foreground), as the prototype #fff', () => {
    mount()
    const tile = screen.getByText(CONNECTOR_DEFS[0].mono, { exact: true })
    expect(tile.style.color).toBe('var(--primary-foreground)')
    expect(sandbox().style.color).toBe('var(--primary-foreground)')
  })

  it('the back link is 12.5px with a 5px gap', () => {
    mount()
    const back = screen.getByText('All connectors', { exact: false }).closest('button') as HTMLButtonElement
    expect(back.style.fontSize).toBe('12.5px')
    expect(back.style.gap).toBe('5px')
  })

  it('the back link glyph is the 15px chevron-left', () => {
    mount()
    const svg = screen.getByText('All connectors', { exact: false }).closest('button')!.querySelector('svg') as SVGElement
    expect(svg.getAttribute('width')).toBe('15')
    expect(svg.querySelector('path')?.getAttribute('d')).toBe('M15 18l-6-6 6-6')
  })

  it('the five health tiles pad 13px 15px and show a 15/700 value', () => {
    mount()
    const tiles = document.querySelectorAll('.pf-health > div')
    expect(tiles).toHaveLength(5)
    tiles.forEach((t) => expect((t as HTMLElement).style.padding).toBe('13px 15px'))
    const value = screen.getByText(connectorDetail(CONNECTOR_DEFS[0]).frequency, { exact: true })
    expect(value.style.fontSize).toBe('15px')
    expect(value.style.fontWeight).toBe('700')
  })
})

describe('ConnectorDetail > funnel, volume', () => {
  it('card heads pad 13px 20px with 15/700 titles on the funnel and volume cards', () => {
    mount()
    for (const h of ['Reconciliation · ERP ↔ clearance', 'Documents pulled']) {
      const t = screen.getByText(h, { exact: true })
      expect(t.style.fontSize).toBe('15px')
      expect(t.style.fontWeight).toBe('700')
      expect((t.parentElement as HTMLElement).style.padding).toBe('13px 20px')
    }
  })

  it('funnel steps are 26/700 mono numbers over a 12.5/600 label and an 11px sub', () => {
    mount()
    const n = screen.getByText('Validated', { exact: true })
    expect(n.style.fontSize).toBe('12.5px')
    expect(n.style.fontWeight).toBe('600')
    expect((n.nextElementSibling as HTMLElement).style.fontSize).toBe('11px')
    const num = n.previousElementSibling as HTMLElement
    expect(num.style.fontSize).toBe('26px')
    expect(num.style.fontWeight).toBe('700')
  })

  it('the three funnel arrows are the arrow glyph with a 10px top margin, not a text →', () => {
    mount()
    const funnel = document.querySelector('.pf-funnel') as HTMLElement
    const arrows = [...funnel.children].filter((c) => c.querySelector('svg') && !c.textContent?.trim())
    expect(arrows).toHaveLength(3)
    arrows.forEach((a) => {
      expect(a.querySelector('svg path')?.getAttribute('d')).toBe('M5 12h14M13 6l6 6-6 6')
      expect((a as HTMLElement).style.marginTop).toBe('10px')
    })
    expect(funnel.textContent).not.toContain('→')
  })

  it('volume bars are 2px radius and there is one per day', () => {
    mount()
    const bars = document.querySelectorAll<HTMLElement>('[title$=" documents"]')
    expect(bars).toHaveLength(connectorDetail(CONNECTOR_DEFS[0]).volume.length)
    bars.forEach((b) => expect(b.style.borderRadius).toBe('2px'))
  })
})

describe('ConnectorDetail > feed, master data, write-back, held', () => {
  it('feed dots are 50% circles', () => {
    mount()
    const dots = [...card('Sync activity').querySelectorAll<HTMLElement>('span')].filter((s) => s.style.width === '5px')
    expect(dots.length).toBeGreaterThan(0)
    dots.forEach((d) => expect(d.style.borderRadius).toBe('50%'))
  })

  it('master tiles are unboxed mono 20/700 and tax-code rows rule from above at 7px padding', () => {
    mount()
    const c = card('Master data mirror')
    const tile = screen.getByText('Customers', { exact: true })
    expect(tile.style.marginTop).toBe('3px')
    expect((tile.parentElement as HTMLElement).style.border).toBe('')
    expect((tile.parentElement as HTMLElement).style.background).toBe('')
    const num = tile.previousElementSibling as HTMLElement
    expect(num.style.fontSize).toBe('20px')
    expect(num.style.fontWeight).toBe('700')
    const row = screen.getByText('Standard rated VAT', { exact: false }).parentElement as HTMLElement
    expect(row.style.padding).toBe('7px 0px')
    expect(row.style.borderTop).toBe('1px solid var(--line-1)')
    expect(row.style.borderBottom).toBe('')
    expect(c.querySelectorAll('code').length).toBeGreaterThan(0)
  })

  it('write-back is a 3-column grid of 22/700 numbers over a radius-sm track with an unrounded fill', () => {
    mount()
    const stamped = screen.getByText('Stamped back', { exact: true })
    const grid = stamped.parentElement!.parentElement as HTMLElement
    expect(grid.style.gridTemplateColumns).toBe('repeat(3, 1fr)')
    expect(grid.style.gap).toBe('12px')
    const num = stamped.previousElementSibling as HTMLElement
    expect(num.style.fontSize).toBe('22px')
    expect(num.style.fontWeight).toBe('700')
    const pct = screen.getByText('IRN + CSID stamped', { exact: true }).nextElementSibling as HTMLElement
    expect(pct.style.fontSize).toBe('12px')
    expect(pct.style.fontWeight).toBe('600')
    expect(pct.style.color).toBe('var(--fg-1)')
    const track = pct.parentElement!.nextElementSibling as HTMLElement
    expect(track.style.borderRadius).toBe('var(--radius-sm)')
    expect(track.style.marginBottom).toBe('12px')
    expect((track.firstElementChild as HTMLElement).style.borderRadius).toBe('')
  })

  it('held-document dots are 50% circles and the card keeps one Review per row', () => {
    mount()
    const reviews = screen.getAllByText('Review', { exact: true })
    expect(reviews.length).toBeGreaterThan(0)
    reviews.forEach((r) => {
      const dot = (r.parentElement as HTMLElement).firstElementChild as HTMLElement
      expect(dot.style.borderRadius).toBe('50%')
    })
  })
})

describe('ConnectorDetail > no pill radius', () => {
  it('no element paints a 99 or 999 radius', () => {
    const { container } = mount()
    const all = [...container.querySelectorAll<HTMLElement>('*')]
    expect(all.filter((e) => e.style.borderRadius !== '').length).toBeGreaterThan(10)
    expect(all.filter((e) => /^(99|999)(px)?$/.test(e.style.borderRadius))).toHaveLength(0)
  })
})
