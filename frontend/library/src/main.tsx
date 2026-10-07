import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

// v2 tokens, then the .asc-app layer.
import '@invoice-os/design-tokens/v2/styles.css'
import '@invoice-os/design-tokens/v2/app-layer.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>{null}</StrictMode>,
)
