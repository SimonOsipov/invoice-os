import { useState } from 'react'
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
import { trackDemoOpen, type DemoCtaSource } from './analytics'

// The whole page lives under `.asc-app` — that scope defines the design-system
// tokens (--accent, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label,
// .mono, .grid-bg, .dot-bg) that every section relies on.
export default function App() {
  const [signInOpen, setSignInOpen] = useState(false)
  const [demoOpen, setDemoOpen] = useState(false)
  // Source-bound per call site: the six components keep `onBookDemo: () => void`
  // and stay untouched, so one file carries the attribution instead of seven.
  const book = (source: DemoCtaSource) => () => {
    trackDemoOpen(source)
    setDemoOpen(true)
  }
  const onSignIn = () => setSignInOpen(true)
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
      <Nav onSignIn={onSignIn} onBookDemo={book('nav')} />
      <Hero onBookDemo={book('hero')} onSignIn={onSignIn} />
      <TrustStrip />
      <Problem />
      <Modules />
      <HowItWorks />
      <Compliance />
      <Audience onBookDemo={book('audience')} />
      <Developers />
      <Pricing onBookDemo={book('pricing')} />
      <DemoCta onBookDemo={book('demo_cta')} />
      <Footer onBookDemo={book('footer')} />
      {signInOpen && <SignInModal onClose={() => setSignInOpen(false)} />}
      {demoOpen && <DemoModal onClose={() => setDemoOpen(false)} />}
    </div>
  )
}
