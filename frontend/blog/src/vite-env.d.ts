/// <reference types="vite/client" />

// Unset or blank means the outbound link falls back to the production landing host.
interface ImportMetaEnv {
  readonly VITE_LANDING_URL?: string
  readonly VITE_GATEWAY_URL?: string
  readonly VITE_GA_MEASUREMENT_ID?: string
}

declare const __COOKIE_NOTICE_CSS__: string
