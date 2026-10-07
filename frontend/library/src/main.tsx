import './instrument'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { CrashBoundary } from '@invoice-os/monitoring'
import markUrl from '@invoice-os/design-tokens/v2/assets/mark.png'

// v2 tokens, then the .asc-app layer.
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <CrashBoundary brand={<img src={markUrl} alt="" aria-hidden="true" width={20} height={20} />}>{null}</CrashBoundary>
  </StrictMode>,
)
