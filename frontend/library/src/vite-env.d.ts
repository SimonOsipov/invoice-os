/// <reference types="vite/client" />

// Unset or blank means the outbound button is not rendered.
interface ImportMetaEnv {
  readonly VITE_APP_URL?: string
  readonly VITE_LANDING_URL?: string
}
