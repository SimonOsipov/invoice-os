import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

// Design-system tokens, sourced from the shared @invoice-os/design-tokens workspace
// package (single source of truth; DS project 999b7034-9f23-43d4-9229-51af7dde9f62).
// Order matters: the theme-agnostic base first, then the v2 visual identity on top.
import '@invoice-os/design-tokens/colors_and_type.css'
import '@invoice-os/design-tokens/v2.css'
// Local app-shell styles ported from the prototype's inline <style> (keyframes + hovers)
// plus the responsive media-query layer.
import './styles/platform.css'

import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
