import './instrument'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { CrashBoundary } from '@invoice-os/monitoring'

// v2 design tokens, then the v2 .asc-app product layer.
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'
// Local app-shell styles ported from the prototype's inline <style> (keyframes + hovers).
import './styles/ops.css'

import App from './App'
import { BrandMark } from './icons'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <CrashBoundary brand={<BrandMark size={20} />}>
      <App />
    </CrashBoundary>
  </StrictMode>,
)
