// Mirrors gatewayBase()'s null-when-unset contract (@invoice-os/api-client/client,
// C8b/C8c): each PR now deploys to its own ephemeral Railway environment with an
// unpredictable domain suffix (M4-23), so a hardcoded dev-deploy fallback would silently
// route a sign-in to the wrong environment. Return null rather than defaulting.
const resolveBase = (v: string | undefined): string | null => {
  const trimmed = (v ?? '').trim().replace(/\/+$/, '')
  return trimmed || null
}
export const appBase = () => resolveBase(import.meta.env.VITE_APP_URL)
export const opsBase = () => resolveBase(import.meta.env.VITE_OPS_URL)
export const supportBase = () => resolveBase(import.meta.env.VITE_SUPPORT_URL)

export const consoleBase = (target: 'ops' | 'support') => (target === 'ops' ? opsBase() : supportBase())

export const handoffTarget = (base: string, code: string) => `${base}?handoff=${encodeURIComponent(code)}`
