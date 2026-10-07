import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'

// v2 tokens, then the .asc-app layer, then the library rules.
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'
import './styles/library.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
