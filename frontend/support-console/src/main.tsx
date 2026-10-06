import './instrument'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { CrashBoundary } from '@invoice-os/monitoring'

// v2 tokens, then the .asc-app layer, then local overrides.
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'
import './styles/support.css'

import App from './App'
import { BrandMark } from './icons'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <CrashBoundary brand={<BrandMark size={20} />}>
      <App />
    </CrashBoundary>
  </StrictMode>,
)
