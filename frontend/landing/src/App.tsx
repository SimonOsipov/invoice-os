import { useState } from 'react'
import { Nav } from './components/Nav'
import { SignInModal } from './components/SignInModal'
import { DemoModal } from './components/DemoModal'
import { Hero } from './components/Hero'
import { TrustStrip } from './components/TrustStrip'
import { HowItWorks } from './components/HowItWorks'
import { Modules } from './components/Modules'
import { Compliance } from './components/Compliance'
import { Audience } from './components/Audience'
import { Developers } from './components/Developers'
import { Pricing } from './components/Pricing'
import { DemoCta } from './components/DemoCta'
import { Footer } from './components/Footer'

// The whole page lives under `.if-v2` — that scope defines the design-system
// tokens (--accent, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label,
// .mono, .grid-bg, .dot-bg) that every section relies on.
export default function App() {
  const [signInOpen, setSignInOpen] = useState(false)
  const [demoOpen, setDemoOpen] = useState(false)
  const onBookDemo = () => setDemoOpen(true)
  return (
    <div
      className="if-v2"
      style={{
        minHeight: '100vh',
        background: 'var(--bg-1)',
        fontFamily: 'var(--font-sans)',
        color: 'var(--fg-1)',
        overflowX: 'hidden',
      }}
    >
      <Nav onSignIn={() => setSignInOpen(true)} onBookDemo={onBookDemo} />
      <Hero onBookDemo={onBookDemo} />
      <TrustStrip />
      <HowItWorks />
      <Modules />
      <Compliance />
      <Audience onBookDemo={onBookDemo} />
      <Developers />
      <Pricing onBookDemo={onBookDemo} />
      <DemoCta onBookDemo={onBookDemo} />
      <Footer onBookDemo={onBookDemo} />
      {signInOpen && <SignInModal onClose={() => setSignInOpen(false)} />}
      {demoOpen && <DemoModal onClose={() => setDemoOpen(false)} />}
    </div>
  )
}
