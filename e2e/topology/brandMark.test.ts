import { describe, expect, it } from 'vitest'

import { brandMarkProblems, MARK_RING, ringProblems, ringWidth, type BrandMarkReading } from './brandMark'

const DARK = 'rgb(8, 47, 49)'
const LIGHT = 'rgb(250, 248, 242)'
const good: BrandMarkReading = { src: 'data:image/svg+xml,x', boxShadow: MARK_RING, radius: '28.5%', ground: DARK, box: 28, drawn: 24.89 }

describe('brandMarkProblems', () => {
  it('a correct dark mark and a correct light mark are clean', () => {
    expect(brandMarkProblems(good)).toEqual([])
    expect(brandMarkProblems({ ...good, ground: LIGHT, boxShadow: 'none' })).toEqual([])
  })

  it('a dark ground needs the ring', () => {
    const p = brandMarkProblems({ ...good, boxShadow: 'none' })
    expect(p).toHaveLength(1)
    expect(p[0]).toContain('needs the ring')
  })

  it('a light ground forbids a ring', () => {
    const p = brandMarkProblems({ ...good, ground: LIGHT })
    expect(p).toHaveLength(1)
    expect(p[0]).toContain('has a ring')
  })

  it('a translucent light ground is light', () => {
    expect(brandMarkProblems({ ...good, ground: 'rgba(250, 248, 242, 0.95)', boxShadow: 'none' })).toEqual([])
  })

  it('a tile that fills the box is a problem', () => {
    const p = brandMarkProblems({ ...good, drawn: 28 })
    expect(p).toHaveLength(1)
    expect(p[0]).toContain('tile size')
  })

  it('a raster source is a problem; a path or data URL master is not', () => {
    expect(brandMarkProblems({ ...good, src: '/assets/mark-UgrtOohT.png' })).toHaveLength(1)
    expect(brandMarkProblems({ ...good, src: 'https://x.test/blog/assets/mark-Ab1.svg' })).toEqual([])
    expect(brandMarkProblems({ ...good, src: 'data:image/svg+xml,%3csvg' })).toEqual([])
  })

  it('a wrong corner radius is a problem', () => {
    expect(brandMarkProblems({ ...good, radius: '6px' })).toHaveLength(1)
  })
})

describe('ringWidth', () => {
  it('the spread is scaled to the drawn ring', () => {
    expect(Math.abs(ringWidth({ boxShadow: MARK_RING, box: 28, drawn: 24.89 }) - 2)).toBeLessThan(0.01)
    expect(ringWidth({ boxShadow: 'none', box: 28, drawn: 24.89 })).toBe(0)
  })
})

describe('ringProblems', () => {
  const within = { x: 0, y: 0, width: 252, height: 900 }
  const mark = { x: 10, y: 10, width: 28, height: 28 }

  it('a ring over the wordmark is a problem', () => {
    const hit = ringProblems(mark, 2, within, [{ x: 39, y: 10, width: 60, height: 14 }])
    expect(hit).toHaveLength(1)
    expect(hit[0]).toContain('overlaps')
    expect(ringProblems(mark, 2, within, [{ x: 41, y: 10, width: 60, height: 14 }])).toEqual([])
  })

  it('a ring that leaves the sidebar is a problem', () => {
    expect(ringProblems({ x: 1, y: 10, width: 28, height: 28 }, 2, within, [])).toHaveLength(1)
  })
})
