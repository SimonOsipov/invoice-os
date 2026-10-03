// @vitest-environment jsdom
// Coverage map (jsdom): image SVG, marker layer, keyboard and click selection.
import { act, createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AFRICA_MAP } from '../africaMap'
import { click, fire, mountView, show, spyConsoleError, unmountView, type View } from './ds/dsDom.test.util'
import { Coverage } from './Coverage'

const ORDER = ['NG', 'KE', 'ZA'] as const
type Id = (typeof ORDER)[number]

const LABEL = 'Map of Africa showing Nigeria, Kenya and South Africa'
const VIEWBOX = '0 0 600 673'
// V783-785 COUNTRIES[*].title.
const TITLES: Record<Id, string> = {
  NG: 'Our starting point. Your next step.',
  KE: 'A country workflow for Kenya.',
  ZA: 'Prepare for what comes next.',
}
// africa-map.js markers; MP-01 pins the same numbers.
const XY: Record<Id, readonly [number, number]> = { NG: [228.9, 264.5], KE: [476.6, 343.8], ZA: [364.3, 610.1] }
// V731-733 label offsets: name at (x, y), tag at (x, y + 15), applied to the marker coordinates.
const LABELS: Record<Id, { name: string; tag: string; x: number; y: number; anchor: string; fill: string }> = {
  NG: { name: 'Nigeria', tag: 'FIRST LAUNCH', x: 194.9, y: 328.5, anchor: 'end', fill: 'var(--accent)' },
  KE: { name: 'Kenya', tag: 'PLANNED', x: 516.6, y: 353.8, anchor: 'start', fill: 'var(--sage)' },
  ZA: { name: 'South Africa', tag: 'PLANNED', x: 442.3, y: 606.1, anchor: 'start', fill: 'var(--sage)' },
}
const HALO: Record<Id, string> = { NG: 'var(--accent)', KE: 'var(--sage)', ZA: 'var(--sage)' }

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(async () => {
  view = mountView()
  consoleError = spyConsoleError()
  await show(view, createElement(Coverage, { onBookDemo: () => undefined }))
})

afterEach(() => {
  expect(consoleError, 'console.error was called').not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

const near = (a: number, b: number, what: string) => expect(Math.abs(a - b), `${what}: ${a} vs ${b}`).toBeLessThanOrEqual(0.01)
const num = (el: Element, attr: string) => Number(el.getAttribute(attr))

/** Inline style first, then the presentation attribute. */
function paint(el: Element, name: string): string {
  return (el as SVGElement).style.getPropertyValue(name).trim() || (el.getAttribute(name) ?? '').trim()
}

/** `paint` of the nearest ancestor-or-self that sets it, up to `stop`. */
function inherited(el: Element, name: string, stop: Element): string {
  for (let e: Element | null = el; e && e !== stop.parentElement; e = e.parentElement) {
    const v = paint(e, name)
    if (v) return v
  }
  return ''
}

function imageSvg(): SVGSVGElement {
  const panels = view.container.querySelectorAll('[data-cov-panel]')
  expect(panels, 'exactly one [data-cov-panel]').toHaveLength(1)
  const svgs = panels[0].querySelectorAll<SVGSVGElement>('svg[role="img"]')
  expect(svgs, 'exactly one svg[role=img] in [data-cov-panel]').toHaveLength(1)
  return svgs[0]
}

function layer(): SVGSVGElement {
  const layers = view.container.querySelectorAll<SVGSVGElement>('[data-cov-markers]')
  expect(layers, 'exactly one [data-cov-markers]').toHaveLength(1)
  return layers[0]
}

function markers(): SVGGElement[] {
  const found = Array.from(layer().querySelectorAll<SVGGElement>('g[role="button"]'))
  expect(found, 'three g[role=button] markers').toHaveLength(3)
  return found
}

function marker(id: Id): SVGGElement {
  return markers()[ORDER.indexOf(id)]
}

function tab(id: Id): HTMLButtonElement {
  const tabs = Array.from(view.container.querySelectorAll<HTMLButtonElement>('button[aria-pressed]'))
  expect(tabs, 'three tab buttons').toHaveLength(3)
  return tabs[ORDER.indexOf(id)]
}

const tabFlags = () => ORDER.map((id) => tab(id).getAttribute('aria-pressed'))
const markerFlags = () => markers().map((m) => m.getAttribute('aria-pressed'))
const ringCounts = () => markers().map((m) => m.querySelectorAll('circle[r="15"]').length)
const cardTitle = () => (view.container.querySelector('[data-cov-card] h3')?.textContent ?? '').replace(/\s+/g, ' ').trim()

/** SVG elements have no `click()`; dispatch the event the way the browser would. */
function clickSvg(el: Element): void {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  })
}

function expectSelected(id: Id): void {
  const want = ORDER.map((o) => String(o === id))
  expect(tabFlags(), `tabs pressed for ${id}`).toEqual(want)
  expect(markerFlags(), `markers pressed for ${id}`).toEqual(want)
  expect(ringCounts(), `r=15 ring under ${id} only`).toEqual(ORDER.map((o) => (o === id ? 1 : 0)))
  expect(layer().querySelectorAll('circle[r="15"]'), 'one r=15 ring in the layer').toHaveLength(1)
  expect(cardTitle(), `card title for ${id}`).toBe(TITLES[id])
}

describe('CoverageMap: the image SVG', () => {
  it('MP-03 the image SVG', () => {
    const svg = imageSvg()
    expect(svg.getAttribute('aria-label')).toBe(LABEL)
    expect(svg.getAttribute('viewBox')).toBe(VIEWBOX)

    const paths = Array.from(svg.querySelectorAll('path'))
    expect(AFRICA_MAP.countries, 'vendored data is non-empty').toHaveLength(51)
    expect(paths, '51 paths').toHaveLength(51)
    expect(paths.map((p) => p.getAttribute('d'))).toEqual(AFRICA_MAP.countries.map((c) => c.d))

    const fills = paths.map((p) => paint(p, 'fill'))
    const want = AFRICA_MAP.countries.map((c) => (c.id === 'NG' ? 'var(--accent)' : c.id === 'KE' || c.id === 'ZA' ? 'var(--sage)' : 'var(--on-dark-10)'))
    expect(fills).toEqual(want)
    expect(fills.filter((f) => f === 'var(--on-dark-10)'), '48 other countries').toHaveLength(48)
    expect(paths.map((p) => paint(p, 'stroke'))).toEqual(paths.map(() => 'var(--surface-panel)'))

    expect(svg.querySelectorAll('circle'), 'no circle in the image SVG (no rings, no markers)').toHaveLength(0)
    expect(svg.querySelectorAll('[role]'), 'no [role] descendant').toHaveLength(0)
    expect(svg.outerHTML, 'only the cov variant: no brown mix, no non-cov fills').not.toMatch(/color-mix|peach-tint|var\(--primary\)|var\(--ink\)/)
  })

  it("MP-04 the labels sit at V731-733's offsets", () => {
    const svg = imageSvg()
    const texts = Array.from(svg.querySelectorAll('text'))
    expect(texts, 'six text elements: a name and a tag per country').toHaveLength(6)
    for (const id of ORDER) {
      const L = LABELS[id]
      const name = texts.filter((t) => t.textContent === L.name)
      const tag = texts.filter((t) => t.textContent === L.tag && Math.abs(num(t, 'y') - (L.y + 15)) < 0.5)
      expect(name, `${L.name} name text`).toHaveLength(1)
      expect(tag, `${L.name} tag text`).toHaveLength(1)
      near(num(name[0], 'x'), L.x, `${L.name} name x`)
      near(num(name[0], 'y'), L.y, `${L.name} name y`)
      near(num(tag[0], 'x'), L.x, `${L.name} tag x`)
      near(num(tag[0], 'y'), L.y + 15, `${L.name} tag y`)
      expect(inherited(name[0], 'text-anchor', svg), `${L.name} name anchor`).toBe(L.anchor)
      expect(inherited(tag[0], 'text-anchor', svg), `${L.name} tag anchor`).toBe(L.anchor)
      expect(inherited(name[0], 'fill', svg), `${L.name} name fill`).toBe(L.fill)
      expect(inherited(tag[0], 'fill', svg), `${L.name} tag fill`).toBe('var(--eyebrow-on-dark)')
      expect(inherited(name[0], 'font-size', svg), `${L.name} name size`).toBe('18px')
      expect(inherited(name[0], 'font-weight', svg), `${L.name} name weight`).toBe('800')
      expect(inherited(tag[0], 'font-size', svg), `${L.name} tag size`).toBe('11px')
      expect(inherited(tag[0], 'font-weight', svg), `${L.name} tag weight`).toBe('700')
      expect(inherited(tag[0], 'letter-spacing', svg), `${L.name} tag tracking`).toBe('0.1em')
      expect(inherited(name[0], 'pointer-events', svg), `${L.name} name pointer-events`).toBe('none')
    }
  })
})

describe('CoverageMap: the marker layer', () => {
  it('MP-05 markers are named controls outside the image', () => {
    const svg = imageSvg()
    const overlay = layer()
    expect(overlay.tagName.toLowerCase()).toBe('svg')
    expect(overlay.getAttribute('viewBox')).toBe(VIEWBOX)
    expect(overlay.hasAttribute('role'), 'overlay has no role').toBe(false)
    expect(svg.contains(overlay), 'overlay sits outside the image SVG').toBe(false)

    const gs = markers()
    expect(gs.map((g) => g.getAttribute('aria-label'))).toEqual(['Nigeria', 'Kenya', 'South Africa'])
    expect(gs.map((g) => g.getAttribute('tabindex'))).toEqual(['0', '0', '0'])
    expect(markerFlags()).toEqual(['true', 'false', 'false'])
    expect(overlay.querySelectorAll('circle[r="15"]'), 'one active ring').toHaveLength(1)
    ORDER.forEach((id, i) => {
      const dots = gs[i].querySelectorAll('circle[r="5"]')
      expect(dots, `${id} dot`).toHaveLength(1)
      near(num(dots[0], 'cx'), XY[id][0], `${id} dot cx`)
      near(num(dots[0], 'cy'), XY[id][1], `${id} dot cy`)
    })
  })

  it('MP-05b each marker holds the Design anatomy: hit area, halo, dot, active ring and focus ring', () => {
    const gs = markers()
    ORDER.forEach((id, i) => {
      const g = gs[i]
      expect(g.classList.contains('cov-marker'), `${id} class cov-marker`).toBe(true)
      expect(paint(g, 'cursor'), `${id} cursor`).toBe('pointer')
      const only = (r: string) => {
        const cs = g.querySelectorAll(`circle[r="${r}"]`)
        expect(cs, `${id} circle r=${r}`).toHaveLength(1)
        near(num(cs[0], 'cx'), XY[id][0], `${id} r=${r} cx`)
        near(num(cs[0], 'cy'), XY[id][1], `${id} r=${r} cy`)
        return cs[0]
      }
      expect(paint(only('20'), 'fill'), `${id} hit circle`).toBe('transparent')
      const halo = only('9')
      expect(paint(halo, 'fill'), `${id} halo fill`).toBe(HALO[id])
      expect(paint(halo, 'opacity'), `${id} halo opacity`).toBe('0.35')
      expect(paint(only('5'), 'fill'), `${id} dot fill`).toBe('var(--surface)')
      const focus = only('19')
      expect(focus.classList.contains('cov-marker-focus'), `${id} focus ring class`).toBe(true)
      expect(paint(focus, 'fill'), `${id} focus ring fill`).toBe('none')
      expect(paint(focus, 'stroke'), `${id} focus ring stroke`).toBe('var(--ring)')
      expect(paint(focus, 'stroke-width'), `${id} focus ring width`).toBe('2')
    })
    const ring = gs[0].querySelector('circle[r="15"]')
    expect(ring, 'the selected marker holds the active ring').not.toBeNull()
    expect(paint(ring as Element, 'fill')).toBe('none')
    expect(paint(ring as Element, 'stroke')).toBe('var(--surface-foreground)')
    expect(paint(ring as Element, 'stroke-width')).toBe('1.5')
  })

  it('MP-11 the box keeps 600:673 by a width cap and the two SVGs stack inside it', () => {
    const svg = imageSvg()
    const overlay = layer()
    const box = svg.parentElement as HTMLElement
    expect(box.hasAttribute('data-cov-map'), 'image SVG sits in [data-cov-map]').toBe(true)
    expect(overlay.parentElement, 'both SVGs share the box').toBe(box)
    expect(Array.from(box.children).indexOf(overlay), 'overlay paints after the image').toBeGreaterThan(Array.from(box.children).indexOf(svg))
    expect(box.style.position).toBe('relative')
    expect(box.style.width).toBe('100%')
    expect(box.style.aspectRatio.replace(/\s+/g, '')).toBe('600/673')
    // jsdom folds calc(600px * 600 / 673) to calc(534.918px); a real browser keeps the expression.
    const px = /^calc\(([\d.]+)px\)$/.exec(box.style.maxWidth)
    if (px) near(Number(px[1]), (600 * 600) / 673, 'max-width')
    else expect(box.style.maxWidth.replace(/\s+/g, '')).toBe('calc(600px*600/673)')
    expect(box.style.maxHeight, 'no maxHeight clamp').toBe('')
    for (const el of [svg, overlay]) {
      expect(el.style.position, 'absolute').toBe('absolute')
      expect(el.style.width).toBe('100%')
      expect(el.style.height).toBe('100%')
      expect(el.style.display).toBe('block')
    }
  })
})

describe('CoverageMap: selection', () => {
  it('MP-06 a marker click selects and syncs the tabs', () => {
    expectSelected('NG')
    clickSvg(marker('KE'))
    expectSelected('KE')
  })

  it('MP-07 Enter and Space select', () => {
    expectSelected('NG')
    const enter = fire(marker('ZA'), 'Enter')
    expectSelected('ZA')
    expect(enter.defaultPrevented, 'Enter is prevented').toBe(true)
    const space = fire(marker('NG'), ' ')
    expectSelected('NG')
    expect(space.defaultPrevented, 'Space is prevented').toBe(true)
  })

  it('MP-08 other keys do nothing', () => {
    expectSelected('NG')
    const kenya = marker('KE')
    const events = ['a', 'Tab', 'ArrowRight'].map((key) => fire(kenya, key))
    expectSelected('NG')
    expect(events.map((e) => e.defaultPrevented)).toEqual([false, false, false])
    // Positive pair: the same marker does select on Enter, so the handler is live.
    fire(kenya, 'Enter')
    expectSelected('KE')
  })

  it('MP-09 a tab click moves the marker state', () => {
    expectSelected('NG')
    click(tab('ZA'))
    expectSelected('ZA')
    expect(markerFlags()).toEqual(['false', 'false', 'true'])
  })
})

describe('CoverageMap: inert on mount', () => {
  it('MP-10 mounting starts no poll and sets no global', async () => {
    unmountView(view)
    const interval = vi.spyOn(window, 'setInterval')
    view = mountView()
    await show(view, createElement(Coverage, { onBookDemo: () => undefined }))
    // Positive pair: the map did mount, so a skeleton cannot pass.
    expect(imageSvg().getAttribute('aria-label')).toBe(LABEL)
    unmountView(view)
    expect(interval).toHaveBeenCalledTimes(0)
    expect('AFRICA_MAP' in window).toBe(false)
    view = mountView()
  })
})
