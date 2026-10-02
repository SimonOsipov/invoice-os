import { gatewayBase } from '@invoice-os/api-client'
import { signOutConsole } from '@invoice-os/console-session'

export const SESSION_KEY = 'invoice-os.ops-session'

// Mirrors the app's landingBase() (frontend/app/src/auth.ts) deliberately, including
// the null-when-unset contract: each PR deploys to its own ephemeral Railway
// environment with an unpredictable domain suffix, so a hardcoded fallback would
// silently sign users out into the WRONG environment. Returns null rather than
// defaulting; the caller must handle that.
//
// VITE_LANDING_URL is baked at image build time (frontend/ops-console/Dockerfile ARG)
// from the per-environment Railway service variable set by
// scripts/ci/railway-env.sh reconcile_url_variables.
export const landingBase = (): string | null => {
  const v = (import.meta.env.VITE_LANDING_URL ?? '').trim().replace(/\/+$/, '')
  return v || null
}

export const signOut = () => signOutConsole({ storageKey: SESSION_KEY, gateway: gatewayBase(), landing: landingBase() })
