// Pure/total: trim, lowercase, strip one trailing slash, exact-match only.
const normalize = (pathname: string): string => {
  const trimmed = pathname.trim().toLowerCase()
  return trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed
}

export const isPrivacyPath = (pathname: string): boolean => normalize(pathname) === '/privacy'

export const isInvitePath = (pathname: string): boolean => normalize(pathname) === '/invite'

// Collapses to the three real pages so a visitor-typed path never becomes a transaction name.
export const landingRouteName = (pathname: string): string =>
  pathname === '/' ? '/' : isPrivacyPath(pathname) ? '/privacy' : isInvitePath(pathname) ? '/invite' : '<unmatched>'
