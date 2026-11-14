import './instrument'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { gatewayBase } from '@invoice-os/api-client'
import { CrashBoundary } from '@invoice-os/monitoring'

// v2 design tokens, then the v2 .asc-app product layer (owns the app's look).
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'
import '@invoice-os/design-tokens/v2/mark.css'
// Local app-shell styles ported from the prototype's inline <style> (keyframes + hovers)
// plus the responsive media-query layer.
import './styles/platform.css'

import App from './App'
import { BrandMark } from './icons'
import { settlePendingSignOut } from './lib/revoke'

// An unanswered sign-out is sent again before the app reads its session.
void settlePendingSignOut(gatewayBase() ?? '', window.location.search).then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <CrashBoundary brand={<BrandMark size={20} />}>
        <App />
      </CrashBoundary>
    </StrictMode>,
  )
})
