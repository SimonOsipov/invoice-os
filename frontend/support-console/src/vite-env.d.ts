/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URL of the marketing landing page (the real sign-in front door). Sign out
  // sends the browser here. Each PR deploys its own preview on per-PR hostnames,
  // so there is no hardcoded default
  // (see `landingBase()` in auth.ts). Mirrors frontend/ops-console/src/vite-env.d.ts.
  readonly VITE_LANDING_URL?: string
  // Base URL of the app; a 401 on a borrowed session and ?auth=app go through it (appBase() in auth.ts).
  readonly VITE_APP_URL?: string
  readonly VITE_GATEWAY_URL?: string
  readonly VITE_SENTRY_DSN?: string
  readonly VITE_SENTRY_TEST_DIGEST?: string
  readonly VITE_BUILD_SHA?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
