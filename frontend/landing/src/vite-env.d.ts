/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URLs of the sibling SPAs the sign-in routes to after a persona + OTP. Each PR
  // now deploys to its own ephemeral Railway environment with an unpredictable domain
  // suffix (M4-21), so there is no hardcoded default — unset means destUrl() returns null
  // (see auth.ts) rather than routing to the wrong environment.
  readonly VITE_APP_URL?: string
  readonly VITE_OPS_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
