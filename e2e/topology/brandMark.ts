// Reads a deployed brand mark and checks the LAND-09 rule: vector master, ring on a dark ground only.
import { expect, type Locator } from '@playwright/test'
import { enclosesRect, inflateRect, rectsOverlap, type Rect } from './layout'

// .asc-dark .asc-mark in packages/design-tokens/v2/mark.css: 0 0 0 2.25px #ffffff.
export const MARK_RING = 'rgb(255, 255, 255) 0px 0px 0px 2.25px'
// .asc-mark radius in mark.css (rx / width of mark.svg).
export const MARK_RADIUS = '28.5%'
// .asc-mark scale in mark.css: scale: calc(128 / 144).
export const TILE_RATIO = 128 / 144

const TILE_TOLERANCE = 0.01
const SVG_PATH = /\/mark(-[\w-]+)?\.svg(\?.*)?$/

export type BrandMarkReading = {
  src: string
  boxShadow: string
  radius: string
  ground: string
  box: number
  drawn: number
}

// First ancestor background that is not fully transparent, else the html colour, else the white canvas.
export async function readBrandMark(mark: Locator): Promise<BrandMarkReading> {
  await expect.poll(() => mark.evaluate((el) => (el as HTMLImageElement).naturalWidth), { message: 'brand mark never loaded' }).toBeGreaterThan(0)
  const read = await mark.evaluate((el) => {
    const cs = getComputedStyle(el)
    const clear = (c: string) => c === 'rgba(0, 0, 0, 0)' || c === 'transparent'
    let ground = ''
    for (let p = el.parentElement; p; p = p.parentElement) {
      const bg = getComputedStyle(p).backgroundColor
      if (!clear(bg)) { ground = bg; break }
    }
    if (!ground) ground = 'rgb(255, 255, 255)'
    return { src: (el as HTMLImageElement).currentSrc, boxShadow: cs.boxShadow, radius: cs.borderTopLeftRadius, ground, box: parseFloat(cs.width) }
  })
  const drawn = (await mark.boundingBox())?.width
  if (drawn === undefined) throw new Error('brand mark has no box')
  return { ...read, drawn }
}

// WCAG relative luminance of an rgb()/rgba() colour; alpha is ignored.
function luminance(color: string): number {
  const m = color.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/)
  if (!m) throw new Error(`cannot parse the ground colour ${JSON.stringify(color)}`)
  const lin = (c: number): number => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * lin(+m[1]) + 0.7152 * lin(+m[2]) + 0.0722 * lin(+m[3])
}

export function brandMarkProblems(r: BrandMarkReading): string[] {
  const problems: string[] = []
  if (!/^data:image\/svg\+xml,/.test(r.src) && !SVG_PATH.test(r.src)) problems.push(`mark source is not the SVG master: ${r.src.slice(0, 80)}`)
  const dark = luminance(r.ground) < 0.5
  if (dark && r.boxShadow !== MARK_RING) problems.push(`mark on the dark ground ${r.ground} needs the ring ${MARK_RING}, has ${r.boxShadow}`)
  if (!dark && r.boxShadow !== 'none') problems.push(`mark on the light ground ${r.ground} has a ring: ${r.boxShadow}`)
  if (r.radius !== MARK_RADIUS) problems.push(`mark radius is ${r.radius}, want ${MARK_RADIUS}`)
  const ratio = r.drawn / r.box
  if (Math.abs(ratio - TILE_RATIO) > TILE_TOLERANCE) problems.push(`mark tile size ratio ${ratio.toFixed(4)}, want ${TILE_RATIO.toFixed(4)} (drawn ${r.drawn} of box ${r.box})`)
  return problems
}

// The drawn ring width: the box-shadow spread scaled like the tile. 0 for `none`.
export function ringWidth(r: Pick<BrandMarkReading, 'boxShadow' | 'box' | 'drawn'>): number {
  if (r.boxShadow === 'none') return 0
  const spread = r.boxShadow.match(/([\d.]+)px\s*$/)
  if (!spread) throw new Error(`cannot read the ring spread from ${JSON.stringify(r.boxShadow)}`)
  return parseFloat(spread[1]) * (r.drawn / r.box)
}

// The ring rectangle must stay inside `within` and clear every rectangle in `clear`.
export function ringProblems(markRect: Rect, ring: number, within: Rect, clear: Rect[]): string[] {
  const problems: string[] = []
  const ringRect = inflateRect(markRect, ring)
  if (!enclosesRect(within, ringRect)) problems.push(`ring ${JSON.stringify(ringRect)} sticks out of ${JSON.stringify(within)}`)
  clear.forEach((c, i) => {
    if (rectsOverlap(ringRect, c)) problems.push(`ring overlaps lockup text #${i} ${JSON.stringify(c)}`)
  })
  return problems
}
