import { useEffect, useState } from 'react'

import { BrandMark } from '../icons'
import { activeNavHref } from './activeSection'

// Page order, not feature order. Two deliberate absences:
//   - `Platform` is gone; its #modules target is now carried by `The Solution`.
//   - `How it works` has no link at all. The section stays on the page (between
//     The Solution and Compliance), it just isn't a nav destination.
// `shed` names the class that drops the link below a breakpoint (landing.css). The
// nav must never wrap or clip, and it has no room for six links under ~1240px, so
// the tail is shed rather than allowed to collide with the lockup or the CTAs.
// Annotated rather than inferred: without it the element type is a union of "has
// shed" and "has no shed", and whether `l.shed` is even readable then depends on
// TypeScript's object-literal normalisation rather than on anything stated here.
const NAV_LINKS: { label: string; href: string; shed?: string }[] = [
  { label: 'The Problem', href: '#problem' },
  { label: 'The Solution', href: '#modules' },
  { label: 'Compliance', href: '#compliance', shed: 'ios-nav-shed-1080' },
  { label: "Who it's for", href: '#accountants', shed: 'ios-nav-shed-1080' },
  { label: 'Developers', href: '#developers', shed: 'ios-nav-shed-1240' },
  { label: 'Pricing', href: '#pricing', shed: 'ios-nav-shed-1240' },
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

  useEffect(() => {
    // Header height comes from --header-h, the single source of truth (it also
    // drives this file's calc() below and landing.css's scroll-padding-top).
    // No fallback on purpose: if the token ever went missing, parseFloat('') is
    // NaN, every `top <= NaN` is false and no link lights — the failure is dark,
    // never wrong. Do not "fix" this with a hardcoded pixel fallback.
    const headerH = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--header-h'))

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
    // changes height under a parked viewport (the Who-it's-for toggle swaps mocks
    // of different heights), and the tail of a smooth anchor jump that settles
    // after the last scroll event.
    //
    // Guarded on a finite token for the same reason the threshold above has no
    // fallback: `-NaNpx` is not a legal rootMargin and would THROW out of this
    // effect, taking the scroll listener down with it. Missing token => no spy.
    let io: IntersectionObserver | null = null
    if (Number.isFinite(headerH)) {
      io = new IntersectionObserver(schedule, { rootMargin: `-${headerH}px 0px -60% 0px` })
      document.querySelectorAll('section[id]').forEach((el) => io!.observe(el))
    }

    return () => {
      window.removeEventListener('scroll', schedule)
      io?.disconnect()
      if (frame) cancelAnimationFrame(frame)
    }
  }, [])

  return (
    <header
      style={{
        position: 'sticky',
        top: 0,
        zIndex: 50,
        background: 'oklch(98.5% .008 85 / .82)',
        backdropFilter: 'blur(16px)',
        WebkitBackdropFilter: 'blur(16px)',
        borderBottom: '1px solid var(--line-1)',
      }}
    >
      <div
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '0 32px',
          height: 'calc(var(--header-h) - 1px)',
          // auto | minmax(0,1fr) | auto — lockup, nav, actions. The nav owning its
          // OWN track is what stops it colliding with either neighbour: both outer
          // tracks size to their content and the middle one absorbs whatever is
          // left. minmax(0,…) rather than a bare 1fr so the track may shrink below
          // its content width instead of forcing the row wider than the container.
          display: 'grid',
          gridTemplateColumns: 'auto minmax(0, 1fr) auto',
          gap: 28,
          alignItems: 'center',
        }}
      >
        <a href={`${hrefPrefix}#top`} style={{ display: 'flex', alignItems: 'center', gap: 10, color: 'var(--fg-1)' }}>
          <BrandMark size={22} />
          <span style={{ fontWeight: 600, fontSize: 16, letterSpacing: '-0.02em' }}>ASComply</span>
          <span
            className="mono"
            style={{
              fontSize: 10,
              fontWeight: 500,
              letterSpacing: '0.08em',
              color: 'var(--fg-3)',
              border: '1px solid var(--line-2)',
              borderRadius: 'var(--radius-sm)',
              padding: '2px 5px',
            }}
          >
            AFRICA
          </span>
        </a>
        <nav
          aria-label="Primary"
          className="ios-hide-mobile"
          style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 30, minWidth: 0 }}
        >
          {NAV_LINKS.map((l) => {
            const active = l.href === activeHref
            return (
              <a
                key={l.href}
                href={`${hrefPrefix}${l.href}`}
                // Not .ios-link: that rule's hover resolves to amber (the prototype's
                // generic link behaviour, still what the footer wants). The nav's own
                // state machine is teal-on-hover, teal-when-current.
                className={l.shed ? `ios-nav-link ${l.shed}` : 'ios-nav-link'}
                // The spy is a scroll-time answer, so it cannot light the destination
                // until the jump lands. Setting it here means the indicator moves with
                // the click; the next measurement then confirms (or corrects) it.
                onClick={() => setActiveHref(l.href)}
                // Must be 'true' | undefined, never a boolean: React passes a
                // boolean straight to setAttribute for aria-* names, which would
                // render aria-current="false" on every inactive link.
                aria-current={active ? 'true' : undefined}
                style={{
                  fontSize: 14,
                  color: active ? 'var(--action)' : 'var(--fg-2)',
                  borderBottom: `2px solid ${active ? 'var(--action)' : 'transparent'}`,
                  // 6 above vs 4 + 2px border below keeps the text centred on the
                  // row alongside the brand lockup and both CTA buttons.
                  paddingTop: 6,
                  paddingBottom: 4,
                }}
              >
                {l.label}
              </a>
            )
          })}
        </nav>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {/* Opens the mock persona picker → OTP → routes to the workspace the
              chosen role may open (task-21). */}
          <button onClick={onSignIn} className="v2-btn v2-btn-ghost" style={{ height: 38, cursor: 'pointer' }}>
            Explore the platform
          </button>
          <button onClick={onBookDemo} className="v2-btn v2-btn-primary" style={{ height: 38, cursor: 'pointer' }}>
            Book a demo
          </button>
        </div>
      </div>
    </header>
  )
}
