import { useCallback, useEffect, useState } from 'react'
import { Nav } from './components/Nav'
import { VerifyNotice } from './components/VerifyNotice'
import { SignInModal } from './components/SignInModal'
import { DemoModal } from './components/DemoModal'
import { RegisterModal } from './components/RegisterModal'
import { Hero } from './components/Hero'
import { AudienceStrip } from './components/AudienceStrip'
import { Problem } from './components/Problem'
import { Modules } from './components/Modules'
import { Platform } from './components/Platform'
import { Coverage } from './components/Coverage'
import { Intelligence } from './components/Intelligence'
import { Solutions } from './components/Solutions'
import { Integrations } from './components/Integrations'
import { Api } from './components/Api'
import { Faq } from './components/Faq'
import { ClosingCta } from './components/ClosingCta'
import { Footer } from './components/Footer'
import { Privacy } from './components/Privacy'
import { CookieNotice } from './components/CookieNotice'
import { isScrollable, scrollDepthPercent, trackDemoOpen, trackScrollDepth, type DemoCtaSource } from './analytics'
import { readConsent, type ConsentRecord } from './consent'
import { applyChoice } from './consentActions'
import { isPrivacyPath } from './route'
import { registrationOpen } from './register'
import { readSignInConsole, readSignInState } from './signIn'
import { readVerifyOutcome, VERIFIED_PARAM, VERIFY_PARAM } from './verify'

// Copy for the ?signin= outcome; `ready` opens the modal with no message.
const SIGN_IN_OUTCOMES = new Map<string, string | undefined>([
  ['ready', undefined],
  ['no-workspace', 'This account has no workspace yet.'],
  ['failed', "We couldn't open your workspace. Sign in again."],
  ['not-staff', 'This account cannot open the ASComply consoles.'],
])

// Params the boot reads once and removes from the address bar.
const BOOT_PARAMS = ['state', 'console', 'signin', VERIFIED_PARAM, VERIFY_PARAM]

// Held a minute short of the 10-minute state TTL, so a posted state is still live.
const STATE_HOLD_MS = 9 * 60 * 1000

// Pure read: the strip runs in an effect, so StrictMode's double init sees the same URL.
function readSignInBoot(search: string) {
  const state = readSignInState(search)
  const outcome = new URLSearchParams(search).get('signin') ?? ''
  return { state, consoleTarget: readSignInConsole(search), bootAt: Date.now(), error: SIGN_IN_OUTCOMES.get(outcome), open: SIGN_IN_OUTCOMES.has(outcome) }
}

// Tokens and utility classes (.v2-btn, .label, .mono) are global: v2 plus bridge.css.
export default function App() {
  // The state is held in memory only, never in storage.
  const [signInBoot] = useState(() => readSignInBoot(window.location.search))
  // A bfcache restore drops the state for good.
  const [stateDropped, setStateDropped] = useState(false)
  // Stable until a drop, so the open form re-checks only then.
  const heldState = useCallback(
    () => (!stateDropped && Date.now() - signInBoot.bootAt < STATE_HOLD_MS ? signInBoot.state : null),
    [signInBoot, stateDropped],
  )
  const [signInOpen, setSignInOpen] = useState(signInBoot.open)
  const [signInError, setSignInError] = useState(signInBoot.error)
  // Read once at mount; the strip below removes the params, so a re-read would lose the notice.
  const [verifyOutcome, setVerifyOutcome] = useState(() => readVerifyOutcome(window.location.search))
  const [demoOpen, setDemoOpen] = useState(false)
  const [registerOpen, setRegisterOpen] = useState(false)
  // Read once at mount: a stored choice keeps the notice down until `reopened` flips.
  const [consent, setConsent] = useState<ConsentRecord | null>(() => readConsent())
  // Once a choice is stored the footer control is the only route back to the notice.
  const [reopened, setReopened] = useState(false)
  // Source-bound per call site: the components keep `onBookDemo: () => void`, so this file carries the attribution.
  const book = (source: DemoCtaSource) => () => {
    trackDemoOpen(source)
    setDemoOpen(true)
  }
  const onSignIn = () => setSignInOpen(true)
  const onCreateAccount = registrationOpen()
    ? () => {
        setSignInOpen(false)
        setSignInError(undefined)
        setRegisterOpen(true)
      }
    : undefined
  const privacy = isPrivacyPath(window.location.pathname)

  useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (e.persisted) setStateDropped(true)
    }
    window.addEventListener('pageshow', onShow)
    return () => window.removeEventListener('pageshow', onShow)
  }, [])

  // Strip the boot params for any value; every other param stays.
  useEffect(() => {
    const url = new URL(window.location.href)
    if (!BOOT_PARAMS.some((k) => url.searchParams.has(k))) return
    BOOT_PARAMS.forEach((k) => url.searchParams.delete(k))
    window.history.replaceState(null, '', url.pathname + url.search + url.hash)
  }, [])

  // Page-level depth, deliberately outside Nav.tsx's scroll effect: that one owns the
  // nav indicator, and folding analytics in makes every nav change an analytics change.
  useEffect(() => {
    let frame = 0
    const measure = () => {
      frame = 0
      // documentElement, not body: body.scrollHeight excludes body margins. The height is
      // never cached — a toggle can swap content of different heights.
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
      <Nav onSignIn={onSignIn} onBookDemo={book('nav')} onCreateAccount={onCreateAccount} hrefPrefix={privacy ? '/' : ''} />
      {verifyOutcome && <VerifyNotice outcome={verifyOutcome} onDismiss={() => setVerifyOutcome(null)} />}
      {privacy ? (
        <Privacy />
      ) : (
        <>
          <Hero onBookDemo={book('hero')} />
          <AudienceStrip />
          <Problem />
          <Modules />
          <Platform onBookDemo={book('platform')} />
          <Coverage onBookDemo={book('coverage')} />
          <Intelligence />
          <Solutions onBookDemo={book('audience')} />
          <Integrations onBookDemo={book('integrations')} />
          <Api onBookDemo={book('api')} />
          <Faq onBookDemo={book('faq')} />
          <ClosingCta onBookDemo={book('closing')} />
        </>
      )}
      <Footer onBookDemo={book('footer')} onSignIn={onSignIn} hrefPrefix={privacy ? '/' : ''} onCookieChoices={() => setReopened(true)} />
      {/* Pinned by "the notice mounts after Footer and before the modals": last in flow puts the
          spacer's scroll room at the document end and the tab order after the footer. */}
      {(consent === null || reopened) && (
        <CookieNotice
          current={consent}
          suppressed={signInOpen || demoOpen || registerOpen}
          onChoose={(choice) => {
            setConsent(applyChoice(choice))
            // consent is already non-null on a reopen, so only this closes it again.
            setReopened(false)
          }}
        />
      )}
      {signInOpen && (
        <SignInModal
          heldState={heldState}
          initialError={signInError}
          consoleTarget={signInBoot.consoleTarget ?? undefined}
          onCreateAccount={onCreateAccount}
          onClose={() => {
            setSignInOpen(false)
            setSignInError(undefined)
          }}
        />
      )}
      {demoOpen && <DemoModal onClose={() => setDemoOpen(false)} />}
      {registerOpen && <RegisterModal onClose={() => setRegisterOpen(false)} />}
    </div>
  )
}
