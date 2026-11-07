// Reads a deployed brand mark and checks the rule: vector master, ring on a dark ground only.
import { expect, type Locator } from '@playwright/test'
import { enclosesRect, inflateRect, rectsOverlap, type Rect } from './layout'

// .asc-dark .asc-mark in packages/design-tokens/v2/mark.css: 0 0 0 2.25px #ffffff.
export const MARK_RING = 'rgb(255, 255, 255) 0px 0px 0px 2.25px'
// .asc-mark radius in mark.css (rx / width of mark.svg).
export const MARK_RADIUS = '28.5%'
// .asc-mark scale in mark.css: scale: calc(128 / 144).
export const TILE_RATIO = 128 / 144

const TILE_TOLERANCE = 0.01
// Distinctive content of packages/design-tokens/v2/assets/mark.svg.
export const MASTER_VIEWBOX = '0 0 128 128'
export const MASTER_TILE_RX = '36.5'
const SVG_PATH = /\/mark(-[\w-]+)?\.svg(\?.*)?$/

export type BrandMarkReading = {
  src: string
  boxShadow: string
  radius: string
  ground: string
  box: number
  drawn: number
}

// Ancestor backgrounds from the mark outwards, up to and including the first opaque one.
export async function readBrandMark(mark: Locator): Promise<BrandMarkReading> {
  await expect.poll(() => mark.evaluate((el) => (el as HTMLImageElement).naturalWidth), { message: 'brand mark never loaded' }).toBeGreaterThan(0)
  const read = await mark.evaluate((el) => {
    const cs = getComputedStyle(el)
    const layers: string[] = []
    for (let p = el.parentElement; p; p = p.parentElement) {
      const bg = getComputedStyle(p).backgroundColor
      layers.push(bg)
      const alpha = bg.match(/,\s*([\d.]+)\)$/)
      if (bg !== 'transparent' && (!alpha || +alpha[1] >= 1)) break
    }
    const ground = layers.join(' | ')
    return { src: (el as HTMLImageElement).currentSrc, boxShadow: cs.boxShadow, radius: cs.borderTopLeftRadius, ground, box: parseFloat(cs.width) }
  })
  const drawn = (await mark.boundingBox())?.width
  if (drawn === undefined) throw new Error('brand mark has no box')
  return { ...read, drawn }
}

type Rgba = [number, number, number, number]

function parseColor(color: string): Rgba {
  const m = color.trim().match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/)
  if (!m) throw new Error(`cannot parse the ground colour ${JSON.stringify(color)}`)
  const a = m[4] === undefined ? 1 : m[4].endsWith('%') ? parseFloat(m[4]) / 100 : +m[4]
  return [+m[1], +m[2], +m[3], a]
}

// Composites the layers (innermost first, ' | ' separated) over white; transparent layers add nothing.
function effectiveGround(ground: string): [number, number, number] {
  const layers = ground.split(' | ').map((l) => (l === 'transparent' ? 'rgba(0, 0, 0, 0)' : l)).map(parseColor)
  let out: [number, number, number] = [255, 255, 255]
  for (const [r, g, b, a] of layers.reverse()) out = [r * a + out[0] * (1 - a), g * a + out[1] * (1 - a), b * a + out[2] * (1 - a)]
  return out
}

// WCAG relative luminance of the composited ground.
function luminance(ground: string): number {
  const lin = (c: number): number => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  const [r, g, b] = effectiveGround(ground)
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
}

function isMasterDataUri(src: string): boolean {
  const m = src.match(/^data:image\/svg\+xml[;,]/)
  if (!m) return false
  let body = src.slice(src.indexOf(',') + 1)
  try { body = decodeURIComponent(body) } catch { /* keep raw */ }
  // Vite's inliner rewrites attribute quotes to single quotes.
  const attr = (name: string, value: string) => body.includes(`${name}="${value}"`) || body.includes(`${name}='${value}'`)
  return attr('viewBox', MASTER_VIEWBOX) && attr('rx', MASTER_TILE_RX)
}

export function brandMarkProblems(r: BrandMarkReading): string[] {
  const problems: string[] = []
  if (!isMasterDataUri(r.src) && !SVG_PATH.test(r.src)) problems.push(`mark source is not the SVG master: ${r.src.slice(0, 80)}`)
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
