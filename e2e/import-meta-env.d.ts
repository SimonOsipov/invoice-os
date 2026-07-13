// M3-14-01: typecheck-only ambient shim, needed ONLY because `tsc` type-checks
// the full source of any file it pulls into its program — including
// packages/api-client/src/client.ts, which this package's `api/client.ts`
// imports the (unused-by-us) `gatewayBase()` function from. That function
// reads `import.meta.env.VITE_GATEWAY_URL`, which needs a global
// `ImportMeta.env` augmentation. api-client declares that augmentation itself
// in its own src/env.d.ts — but that file is only part of api-client's OWN
// tsc program (its tsconfig `include` is "src"); this package's tsconfig
// never references it, so without a matching global declaration here, `tsc
// --noEmit` fails on that unrelated line with TS2339 even though we never
// call gatewayBase(). frontend/app and frontend/landing hit the same shape
// via their own real `vite-env.d.ts` (which additionally references
// `vite/client`); this package has no `vite` dependency (it is a Node-only
// Playwright suite), so `/// <reference types="vite/client" />` does not
// resolve here — this mirrors just the ambient interface shape instead.
// Zero runtime effect; this file only exists to satisfy `tsc`.
interface ImportMetaEnv {
  readonly VITE_GATEWAY_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
