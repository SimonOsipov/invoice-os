/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URL of the API gateway (mock issuer + /api/*). When set, the sign-in does
  // the real M2-13 round trip (mint a JWT, GET /api/tenancy/v1/me). When unset, the
  // app runs as a pure client-side showcase mock with no backend calls.
  readonly VITE_GATEWAY_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
