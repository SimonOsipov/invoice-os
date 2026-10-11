// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Rect, TourState, Win } from './tour'
import { useTourSpot } from './useTourSpot'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let errorSpy: ReturnType<typeof vi.spyOn>
let seen: { rect: Rect | null; win: Win }
const realRect = Element.prototype.getBoundingClientRect
const NAV = { left: 10, top: 100, width: 267, height: 36 }

function Harness({ tour, routeKey }: { tour: TourState | null; routeKey: string }) {
  seen = useTourSpot(tour, routeKey)
  return null
}
const render = (tour: TourState | null, routeKey = '/') => act(() => root.render(<Harness tour={tour} routeKey={routeKey} />))

beforeEach(() => {
  Element.prototype.getBoundingClientRect = function (this: Element) {
    const b = this.id === 'nav-invoices' ? NAV : { left: 0, top: 0, width: 0, height: 0 }
    return { ...b, x: b.left, y: b.top, right: b.left + b.width, bottom: b.top + b.height, toJSON: () => b } as DOMRect
  }
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  Element.prototype.getBoundingClientRect = realRect
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
  const errors = errorSpy.mock.calls.length
  errorSpy.mockRestore()
  expect(errors).toBe(0)
})

describe('useTourSpot', () => {
  it('useTourSpot_missingTargetGivesANullRectAndTheWindow', () => {
    const menu: TourState = { i: 0, phase: 'menu' }
    render(menu)
    expect(seen.rect).toBeNull()
    expect(seen.win).toEqual({ w: window.innerWidth, h: window.innerHeight })
    expect([window.innerWidth, window.innerHeight]).toEqual([1024, 768])

    const nav = document.createElement('nav')
    nav.id = 'nav-invoices'
    document.body.appendChild(nav)
    render(menu, '/next')
    expect(seen.rect).toEqual({ x: 6, y: 97, w: 275, h: 42 })

    render(null)
    expect(seen.rect).toBeNull()

    render({ i: 0, phase: 'card' }, '/next')
    expect(seen.rect).toBeNull()
  })

  it('useTourSpot_remeasuresWhenWebFontsFinishLoading', () => {
    const fonts = new EventTarget() as EventTarget & { ready: Promise<unknown> }
    fonts.ready = new Promise(() => {})
    Object.defineProperty(document, 'fonts', { value: fonts, configurable: true })
    try {
      const nav = document.createElement('nav')
      nav.id = 'nav-invoices'
      document.body.appendChild(nav)
      render({ i: 0, phase: 'menu' })
      expect(seen.rect).toEqual({ x: 6, y: 97, w: 275, h: 42 })

      NAV.top = 106
      NAV.height = 32
      act(() => {
        fonts.dispatchEvent(new Event('loadingdone'))
      })
      expect(seen.rect).toEqual({ x: 6, y: 103, w: 275, h: 38 })
    } finally {
      NAV.top = 100
      NAV.height = 36
      delete (document as { fonts?: unknown }).fonts
    }
  })

  it('useTourSpot_remeasuresWhenDocumentFontsReadyResolvesAfterMount', async () => {
    let ready!: () => void
    const fonts = new EventTarget() as EventTarget & { ready: Promise<unknown> }
    fonts.ready = new Promise<void>((r) => (ready = r))
    Object.defineProperty(document, 'fonts', { value: fonts, configurable: true })
    try {
      const nav = document.createElement('nav')
      nav.id = 'nav-invoices'
      document.body.appendChild(nav)
      render({ i: 0, phase: 'menu' })
      expect(seen.rect).toEqual({ x: 6, y: 97, w: 275, h: 42 })

      NAV.top = 106
      NAV.height = 32
      await act(async () => {
        ready()
        await fonts.ready
      })
      expect(seen.rect).toEqual({ x: 6, y: 103, w: 275, h: 38 })
    } finally {
      NAV.top = 100
      NAV.height = 36
      delete (document as { fonts?: unknown }).fonts
    }
  })
})
