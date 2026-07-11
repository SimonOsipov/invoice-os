// Standalone ambient — no `vite/client` reference (no vite dependency in this package).
// The built-in ImportMeta has no `env`, so declaring only ImportMetaEnv leaves
// `import.meta.env` as TS2339 (verified against tsc@6.0.3). Mirrors both halves
// of frontend/app/src/vite-env.d.ts.
interface ImportMetaEnv {
  readonly VITE_GATEWAY_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
