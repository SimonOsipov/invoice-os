import { useEffect, useState } from 'react'
import { Nav } from './components/Nav'
import { SignInModal } from './components/SignInModal'
import { DemoModal } from './components/DemoModal'
import { Hero } from './components/Hero'
import { TrustStrip } from './components/TrustStrip'
import { Problem } from './components/Problem'
import { Modules } from './components/Modules'
import { HowItWorks } from './components/HowItWorks'
import { Compliance } from './components/Compliance'
import { Audience } from './components/Audience'
import { Developers } from './components/Developers'
import { Pricing } from './components/Pricing'
import { DemoCta } from './components/DemoCta'
import { Footer } from './components/Footer'
import { Privacy } from './components/Privacy'
import { CookieNotice } from './components/CookieNotice'
import { isScrollable, scrollDepthPercent, trackDemoOpen, trackScrollDepth, type DemoCtaSource } from './analytics'
import { readConsent, type ConsentRecord } from './consent'
import { applyChoice } from './consentActions'
import { isPrivacyPath } from './route'
import { readSignInState } from './signIn'

// D23 copy for the app's ?signin= outcome; `ready` opens the modal with no message.
const SIGN_IN_OUTCOMES = new Map<string, string | undefined>([
  ['ready', undefined],
  ['no-workspace', 'This account has no workspace yet.'],
  ['failed', "We couldn't open your workspace. Sign in again."],
])

// Pure read: the strip runs in an effect, so StrictMode's double init sees the same URL.
function readSignInBoot(search: string) {
  const state = readSignInState(search)
  const outcome = new URLSearchParams(search).get('signin') ?? ''
  return { state, error: SIGN_IN_OUTCOMES.get(outcome), open: SIGN_IN_OUTCOMES.has(outcome) }
}

// The whole page lives under `.asc-app` — that scope defines the design-system
// tokens (--accent, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label,
// .mono, .grid-bg, .dot-bg) that every section relies on.
export default function App() {
  // The state is held in memory only (D25 step 3), never in storage.
  const [signInBoot] = useState(() => readSignInBoot(window.location.search))
  const [signInOpen, setSignInOpen] = useState(signInBoot.open)
  const [signInError, setSignInError] = useState(signInBoot.error)
  const [demoOpen, setDemoOpen] = useState(false)
  // Read once at mount: a stored choice keeps the notice down until `reopened` flips.
  const [consent, setConsent] = useState<ConsentRecord | null>(() => readConsent())
  // Once a choice is stored the footer control is the only route back to the notice.
  const [reopened, setReopened] = useState(false)
  // Source-bound per call site: the five components keep `onBookDemo: () => void`
  // and stay untouched, so one file carries the attribution instead of six.
  const book = (source: DemoCtaSource) => () => {
    trackDemoOpen(source)
    setDemoOpen(true)
  }
  const onSignIn = () => setSignInOpen(true)
  const privacy = isPrivacyPath(window.location.pathname)

  // Strip `state` and `signin` for any value; every other param stays.
  useEffect(() => {
    const url = new URL(window.location.href)
    if (!url.searchParams.has('state') && !url.searchParams.has('signin')) return
    url.searchParams.delete('state')
    url.searchParams.delete('signin')
    window.history.replaceState(null, '', url.pathname + url.search + url.hash)
  }, [])

  // Page-level depth, deliberately outside Nav.tsx's scroll effect: that one owns the
  // nav indicator, and folding analytics in makes every nav change an analytics change.
  useEffect(() => {
    let frame = 0
    const measure = () => {
      frame = 0
      // documentElement, not body: body.scrollHeight excludes body margins. The height is
      // never cached — the Who-it's-for toggle swaps mocks of different heights.
      const documentH = document.documentElement.scrollHeight
      // A page that fits the viewport is 100% seen but nothing was scrolled; reporting it
      // at mount would burn all four milestones. Pinned by "guards the mount-time measurement".
      if (!isScrollable(window.innerHeight, documentH)) return
      trackScrollDepth(scrollDepthPercent(window.scrollY, window.innerHeight, documentH))
    }
    const schedule = () => {
      if (frame) return
      frame = requestAnimationFrame(measure)
    }
    measure()
    window.addEventListener('scroll', schedule, { passive: true })
    return () => {
      window.removeEventListener('scroll', schedule)
      if (frame) cancelAnimationFrame(frame)
    }
  }, [])

  return (
    <div
      className="asc-app"
      style={{
        minHeight: '100vh',
        background: 'var(--bg-1)',
        fontFamily: 'var(--font-sans)',
        color: 'var(--fg-1)',
        // `clip` is load-bearing, not a typo for `hidden`. `overflow-x: hidden` forces
        // overflow-y to compute to `auto`, making this div the sticky header's scroll
        // container — and it never scrolls, so the header rides the page away. `clip`
        // clips identically but establishes no scroll container. Guarded by e2e/smoke/landing-nav.spec.ts (E4).
        overflowX: 'clip',
      }}
    >
      <Nav onSignIn={onSignIn} onBookDemo={book('nav')} hrefPrefix={privacy ? '/' : ''} />
      {privacy ? (
        <Privacy />
      ) : (
        <>
          <Hero onBookDemo={book('hero')} onSignIn={onSignIn} />
          <TrustStrip />
          <Problem />
          <Modules />
          <HowItWorks />
          <Compliance />
          <Audience onBookDemo={book('audience')} />
          <Developers />
          <Pricing onBookDemo={book('pricing')} />
          <DemoCta />
        </>
      )}
      <Footer onBookDemo={book('footer')} hrefPrefix={privacy ? '/' : ''} onCookieChoices={() => setReopened(true)} />
      {/* Pinned by "the notice mounts after Footer and before the modals": last in flow puts the
          spacer's scroll room at the document end and the tab order after the footer. */}
      {(consent === null || reopened) && (
        <CookieNotice
          current={consent}
          suppressed={signInOpen || demoOpen}
          onChoose={(choice) => {
            setConsent(applyChoice(choice))
            // consent is already non-null on a reopen, so only this closes it again.
            setReopened(false)
          }}
        />
      )}
      {signInOpen && (
        <SignInModal
          state={signInBoot.state}
          initialError={signInError}
          onClose={() => {
            setSignInOpen(false)
            setSignInError(undefined)
          }}
        />
      )}
      {demoOpen && <DemoModal onClose={() => setDemoOpen(false)} />}
    </div>
  )
}
