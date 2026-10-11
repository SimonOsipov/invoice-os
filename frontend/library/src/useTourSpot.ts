import { useCallback, useEffect, useLayoutEffect, useState } from 'react'
import { TOUR } from './content'
import { cardScrollDelta, spotRect, type Rect, type TourState, type Win } from './tour'

const readWin = (): Win => ({ w: window.innerWidth, h: window.innerHeight })

export function useTourSpot(tour: TourState | null, routeKey: string): { rect: Rect | null; win: Win } {
  const [rect, setRect] = useState<Rect | null>(null)
  const [win, setWin] = useState<Win>(readWin)
  const i = tour?.i
  const phase = tour?.phase

  // Scroll only on step change and resize; a scroll event re-reads the rectangle so keyboard scrolling is not fought.
  const measure = useCallback(
    (scroll: boolean) => {
      if (i === undefined || phase === undefined) return
      setWin(readWin())
      const stop = TOUR[i]
      if (phase === 'menu') {
        const target = document.getElementById(`nav-${stop.g}`)
        const nav = document.querySelector('aside nav')
        if (target && nav && scroll) {
          const t = target.getBoundingClientRect()
          const n = nav.getBoundingClientRect()
          if (t.top < n.top + 8 || t.bottom > n.bottom - 8) nav.scrollTop += t.top - n.top - 80
        }
        setRect(target ? spotRect('menu', target.getBoundingClientRect()) : null)
        return
      }
      const target = document.getElementById(`fc-${stop.f}`)
      const main = document.getElementById('lib-main')
      if (target && main && scroll) {
        const bar = main.firstElementChild?.getBoundingClientRect().height ?? 0
        main.scrollTop += cardScrollDelta(target.getBoundingClientRect().top, main.getBoundingClientRect().top, bar, window.innerHeight)
      }
      setRect(target && main ? spotRect('card', target.getBoundingClientRect()) : null)
    },
    [i, phase],
  )

  useLayoutEffect(() => {
    measure(true)
  }, [measure, routeKey])

  useEffect(() => {
    if (i === undefined) return
    const onResize = () => measure(true)
    const onScroll = () => measure(false)
    const main = document.getElementById('lib-main')
    const nav = document.querySelector('aside nav')
    // Web fonts swap in after the first measure and resize the nav row; re-read then.
    const fonts = document.fonts
    let live = true
    const onFonts = () => live && measure(false)
    void fonts?.ready.then(onFonts)
    fonts?.addEventListener('loadingdone', onFonts)
    window.addEventListener('resize', onResize)
    main?.addEventListener('scroll', onScroll)
    nav?.addEventListener('scroll', onScroll)
    // Re-measure when the target resizes with no font, resize or scroll event.
    const stop = TOUR[i]
    const target = document.getElementById(phase === 'menu' ? `nav-${stop.g}` : `fc-${stop.f}`)
    const ro = target && typeof ResizeObserver !== 'undefined' ? new ResizeObserver(() => measure(false)) : null
    if (target) ro?.observe(target)
    return () => {
      ro?.disconnect()
      live = false
      fonts?.removeEventListener('loadingdone', onFonts)
      window.removeEventListener('resize', onResize)
      main?.removeEventListener('scroll', onScroll)
      nav?.removeEventListener('scroll', onScroll)
    }
  }, [i, phase, measure])

  return { rect: tour ? rect : null, win }
}
