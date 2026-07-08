/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URLs of the sibling SPAs the sign-in routes to after a persona + OTP. Default
  // to the shared dev deployments (the same canonical URLs the e2e smoke uses); a
  // production build overrides them.
  readonly VITE_APP_URL?: string
  readonly VITE_OPS_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
