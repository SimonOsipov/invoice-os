// Pure/total: trim, lowercase, strip one trailing slash, exact-match only.
export function isPrivacyPath(pathname: string): boolean {
  const trimmed = pathname.trim().toLowerCase()
  const normalized = trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed
  return normalized === '/privacy'
}

// Collapses to the two real pages so a visitor-typed path never becomes a transaction name.
export const landingRouteName = (pathname: string): string =>
  pathname === '/' ? '/' : isPrivacyPath(pathname) ? '/privacy' : '<unmatched>'

// STUB (Mode A): the executor replaces this body.
export function isInvitePath(_pathname: string): boolean {
  return false
}
