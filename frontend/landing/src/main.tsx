import { forwarding } from './verifyLink'
import './inviteLink'
import './instrument'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { CrashBoundary } from '@invoice-os/monitoring'

// v2 design-system entry (tokens, utilities), then the landing-local bridge that maps
// the old app-layer names onto it. Never import the v1 entry here.
import '@invoice-os/design-tokens/v2/styles.css'
import './styles/bridge.css'
import './styles/ds.css'
// Local page styles ported from the prototype's inline <style> (keyframes + hovers).
import './styles/landing.css'

import App from './App'
import { bootAnalytics, trackEmailVerified } from './analytics'
import { bootChat } from './hubspotChat'
import { BrandMark } from './icons'
import { InvitePage } from './components/InvitePage'
import { inviteToken } from './inviteLink'
import { NOT_FOUND_PAGE, isInvitePath } from './route'
import { readVerifyOutcome } from './verify'

if (!forwarding) {
  const notFound = document.documentElement.dataset.page === NOT_FOUND_PAGE
  // Read before render: the app strips the verify params during render.
  const verified = readVerifyOutcome(location.search) === 'verified'
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <CrashBoundary brand={<BrandMark size={20} />}>
        {!notFound && isInvitePath(location.pathname) ? <InvitePage token={inviteToken()} /> : <App notFound={notFound} />}
      </CrashBoundary>
    </StrictMode>,
  )

  if (verified) trackEmailVerified()

  // Outside React and after render: StrictMode's double-invoked effects cannot reach it,
  // and nothing sits in front of first paint.
  bootAnalytics()
  bootChat()
}
