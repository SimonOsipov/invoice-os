/// <reference types="vite/client" />

// Unset or blank means the outbound link falls back to the production landing host.
interface ImportMetaEnv {
  readonly VITE_LANDING_URL?: string
}
