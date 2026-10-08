import { useEffect, useId, useRef, useState } from 'react'

import { GLYPHS, Icon } from '../icons'
import { Button } from './ds/Button'
import { Logo } from './ds/Logo'
import { activeNavHref } from './activeSection'

// Only sections on the page; App.render.test.tsx AN-01 pins that each href resolves.
export const NAV_LINKS: { label: string; href: string }[] = [
  { label: 'The problem', href: '#problem' },
  { label: 'The solution', href: '#solution' },
  { label: 'Platform', href: '#platform' },
  { label: "Who it's for", href: '#solutions' },
  { label: 'Integrations', href: '#integrations' },
]

const NAV_HREFS = NAV_LINKS.map((l) => l.href)

export function Nav({
  onSignIn,
  onBookDemo,
  hrefPrefix = '',
}: {
  onSignIn: () => void
  onBookDemo: () => void
  hrefPrefix?: string
}) {
  const [activeHref, setActiveHref] = useState<string | null>(null)
  const [menuOpen, setMenuOpen] = useState(false)
  const menuId = useId()
  const burgerRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    // Header height comes from --header-h, the single source of truth (it also
    // drives landing.css's scroll-padding-top).
    // No fallback on purpose: if the token ever went missing, parseFloat('') is
    // NaN, every `top <= NaN` is false and no link lights — the failure is dark,
    // never wrong. Do not "fix" this with a hardcoded pixel fallback.
    const readHeaderH = () => parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--header-h'))
    let headerH = readHeaderH()

    let frame = 0
    const measure = () => {
      frame = 0
      const sections = Array.from(document.querySelectorAll('section[id]')).map((el) => ({
        id: el.id,
        top: el.getBoundingClientRect().top,
      }))
      const next = activeNavHref(sections, NAV_HREFS, headerH + 1)
      setActiveHref((prev) => (prev === next ? prev : next))
    }
    const schedule = () => {
      if (frame) return
      frame = requestAnimationFrame(measure)
    }
    measure()
    window.addEventListener('scroll', schedule, { passive: true })

    // TWO TRIGGERS, ONE DECISION. The observer does not answer "which section is
    // active" itself — it re-runs the same pure activeNavHref measurement. A second
    // writer with its own notion of active would fight the scroll one for the
    // indicator, and activeNavHref is the tested answer (activeSection.test.ts).
    // What it adds is the crossings a scroll listener never sees: a section that
    // changes height under a parked viewport (a tab or toggle swaps content
    // of different heights), and the tail of a smooth anchor jump that settles
    // after the last scroll event.
    //
    // Guarded on a finite token for the same reason the threshold above has no
    // fallback: `-NaNpx` is not a legal rootMargin and would THROW out of this
    // effect, taking the scroll listener down with it. Missing token => no spy.
    let io: IntersectionObserver | null = null
    const observe = () => {
      io?.disconnect()
      io = null
      if (!Number.isFinite(headerH)) return
      io = new IntersectionObserver(schedule, { rootMargin: `-${headerH}px 0px -60% 0px` })
      document.querySelectorAll('section[id]').forEach((el) => io!.observe(el))
    }
    observe()

    // --header-h changes across the breakpoint; re-read it and rebuild the observer.
    const onResize = () => {
      headerH = readHeaderH()
      observe()
      measure()
    }
    window.addEventListener('resize', onResize)

    return () => {
      window.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', onResize)
      io?.disconnect()
      if (frame) cancelAnimationFrame(frame)
    }
  }, [])

  useEffect(() => {
    if (!menuOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      setMenuOpen(false)
      burgerRef.current?.focus()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [menuOpen])

  return (
    <header
      style={{
        position: 'sticky',
        top: 0,
        zIndex: 50,
        height: 'var(--header-h)',
        background: 'var(--header-bg)',
        backdropFilter: 'blur(var(--header-blur))',
        WebkitBackdropFilter: 'blur(var(--header-blur))',
        borderBottom: '1px solid var(--header-border)',
      }}
    >
      <div
        className="container"
        style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 24 }}
      >
        <a href={`${hrefPrefix}#top`} aria-label="ASComply Africa" style={{ display: 'inline-flex', flex: 'none' }}>
          <Logo size={32} />
        </a>
        <nav aria-label="Primary" className="a-nav">
          {NAV_LINKS.map((l) => {
            const active = l.href === activeHref
            return (
              <a
                key={l.href}
                href={`${hrefPrefix}${l.href}`}
                className="ios-nav-link"
                // The spy only answers at scroll time; this moves the indicator with the click.
                onClick={() => setActiveHref(l.href)}
                // 'true' | undefined, never a boolean: React would render aria-current="false".
                aria-current={active ? 'true' : undefined}
                style={{
                  fontSize: 14,
                  fontWeight: 600,
                  color: active ? 'var(--primary)' : 'var(--ink)',
                  borderBottom: `2px solid ${active ? 'var(--primary)' : 'transparent'}`,
                  paddingTop: 6,
                  paddingBottom: 4,
                }}
              >
                {l.label}
              </a>
            )
          })}
        </nav>
        <div style={{ display: 'flex', alignItems: 'center', gap: 24 }}>
          <button type="button" className="a-login a-link" onClick={onSignIn} style={{ fontSize: 14, fontWeight: 600 }}>
            Sign in
          </button>
          <Button
            size="sm"
            onClick={() => {
              setMenuOpen(false)
              onBookDemo()
            }}
          >
            Book a demo
          </Button>
          <button
            type="button"
            ref={burgerRef}
            className="a-burger"
            aria-label="Menu"
            aria-expanded={menuOpen}
            aria-controls={menuId}
            onClick={() => setMenuOpen((o) => !o)}
          >
            <Icon paths={GLYPHS[menuOpen ? 'x' : 'menu']} size={20} strokeWidth={2} />
          </button>
        </div>
      </div>
      {menuOpen && (
        <div id={menuId} className="a-menu">
          {NAV_LINKS.map((l) => (
            <a
              key={l.href}
              href={`${hrefPrefix}${l.href}`}
              className="a-menu-link"
              onClick={() => {
                setActiveHref(l.href)
                setMenuOpen(false)
              }}
              aria-current={l.href === activeHref ? 'true' : undefined}
            >
              {l.label}
            </a>
          ))}
          <button
            type="button"
            className="a-link a-menu-login"
            onClick={() => {
              setMenuOpen(false)
              onSignIn()
            }}
          >
            Sign in
          </button>
        </div>
      )}
    </header>
  )
}
