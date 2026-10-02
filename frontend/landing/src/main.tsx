import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

// v2 design-system entry (tokens, utilities), then the landing-local bridge that maps
// the old app-layer names onto it. Never import the v1 entry here.
import '@invoice-os/design-tokens/v2/styles.css'
import './styles/bridge.css'
import './styles/ds.css'
// Local page styles ported from the prototype's inline <style> (keyframes + hovers).
import './styles/landing.css'

import App from './App'
import { bootAnalytics } from './analytics'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)

// Outside React and after render: StrictMode's double-invoked effects cannot reach it,
// and nothing sits in front of first paint.
bootAnalytics()
