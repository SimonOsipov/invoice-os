/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URL of the API gateway (mock issuer + /api/*). When set, the sign-in does
  // the real M2-13 round trip (mint a JWT, GET /api/tenancy/v1/me). When unset, the
  // app runs as a pure client-side showcase mock with no backend calls.
  readonly VITE_GATEWAY_URL?: string
  // Base URL of the marketing landing page (the real sign-in front door). Sign out
  // sends the browser here instead of the app's own minimal persona-picker. Each PR now
  // deploys to its own ephemeral Railway environment with an unpredictable domain suffix
  // (M4-21), so there is no hardcoded default — unset means Sign out stays put rather
  // than routing to the wrong environment (see `landingBase()` in auth.ts).
  readonly VITE_LANDING_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
