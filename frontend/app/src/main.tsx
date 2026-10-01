import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { gatewayBase } from '@invoice-os/api-client'
import { CrashBoundary, initMonitoring } from '@invoice-os/monitoring'

// Design-system tokens, sourced from the shared @invoice-os/design-tokens workspace
// package (single source of truth; DS project 999b7034-9f23-43d4-9229-51af7dde9f62).
// Single entry: tokens -> utilities -> .asc-app product layer.
import '@invoice-os/design-tokens/styles.css'
// Local app-shell styles ported from the prototype's inline <style> (keyframes + hovers)
// plus the responsive media-query layer.
import './styles/platform.css'

import App from './App'
import { BrandMark } from './icons'
import { routeName } from './lib/route'

initMonitoring('app', { gateway: gatewayBase(), routeName })

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <CrashBoundary brand={<BrandMark size={20} />}>
      <App />
    </CrashBoundary>
  </StrictMode>,
)
