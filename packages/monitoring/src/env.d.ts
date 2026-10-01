// Standalone ambient, same style as packages/api-client/src/env.d.ts (no vite dependency).
interface ImportMetaEnv {
  readonly VITE_SENTRY_DSN?: string
  readonly VITE_RAILWAY_GIT_COMMIT_SHA?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

declare module '*?raw' {
  const content: string
  export default content
}
