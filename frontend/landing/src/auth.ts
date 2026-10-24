// Mirrors gatewayBase()'s null-when-unset contract (@invoice-os/api-client/client,
// C8b/C8c): each PR deploys its own preview on per-PR hostnames, so a hardcoded
// fallback would silently route a sign-in to the wrong environment. Return null rather than defaulting.
const resolveBase = (v: string | undefined): string | null => {
  const trimmed = (v ?? '').trim().replace(/\/+$/, '')
  return trimmed || null
}
export const appBase = () => resolveBase(import.meta.env.VITE_APP_URL)
export const opsBase = () => resolveBase(import.meta.env.VITE_OPS_URL)
export const supportBase = () => resolveBase(import.meta.env.VITE_SUPPORT_URL)
export const libraryBase = () => resolveBase(import.meta.env.VITE_LIBRARY_URL)
export const blogBase = () => resolveBase(import.meta.env.VITE_BLOG_URL)

export const consoleBase = (target: 'ops' | 'support') => (target === 'ops' ? opsBase() : supportBase())

export const handoffTarget = (base: string, code: string) => `${base}?handoff=${encodeURIComponent(code)}`
