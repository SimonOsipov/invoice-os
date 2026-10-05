// Ambient for the api-client source this package type-checks through; no vite dependency.
interface ImportMetaEnv {
  readonly VITE_GATEWAY_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
