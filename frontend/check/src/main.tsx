import './instrument'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { CrashBoundary } from '@invoice-os/monitoring'
import markUrl from '@invoice-os/design-tokens/v2/assets/mark.svg'
// Tokens load before App so the landing ds.css (via ds.ts) follows them, then the check rules.
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'
import '@invoice-os/design-tokens/v2/mark.css'
import { App } from './App'
import { bootCheckAnalytics, bootCheckChat } from './analytics'
import './styles/check.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <CrashBoundary brand={<img className="asc-mark" src={markUrl} alt="" aria-hidden="true" width={20} height={20} />}>
      <App />
    </CrashBoundary>
  </StrictMode>,
)

// After render, outside React: StrictMode's double effects cannot reach it.
bootCheckAnalytics()
bootCheckChat()
