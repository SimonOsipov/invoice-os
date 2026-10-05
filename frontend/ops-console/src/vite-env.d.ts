/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URL of the marketing landing page (the real sign-in front door). Sign out
  // sends the browser here. Each PR deploys to its own ephemeral Railway environment
  // with an unpredictable domain suffix (M4-23), so there is no hardcoded default —
  // unset means Sign out stays put rather than routing to the wrong environment
  // (see `landingBase()` in auth.ts). Mirrors frontend/app/src/vite-env.d.ts.
  readonly VITE_LANDING_URL?: string
  readonly VITE_GATEWAY_URL?: string
  readonly VITE_SENTRY_DSN?: string
  readonly VITE_SENTRY_TEST_DIGEST?: string
  readonly VITE_RAILWAY_GIT_COMMIT_SHA?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
