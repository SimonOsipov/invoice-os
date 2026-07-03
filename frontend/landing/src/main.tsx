import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

// Design-system tokens (copied verbatim from the FiscalBridge Africa Design System,
// project 999b7034-9f23-43d4-9229-51af7dde9f62). Order matters: the theme-agnostic
// base first, then the v2 visual identity that layers colors/components on top.
import './styles/colors_and_type.css'
import './styles/v2.css'
// Local page styles ported from the prototype's inline <style> (keyframes + hovers).
import './styles/landing.css'

import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
