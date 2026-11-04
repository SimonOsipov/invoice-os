import { LIBRARY_BASE } from './sitePaths'

// Pure/total: trim, lowercase, strip one trailing slash, exact-match only.
const normalize = (pathname: string): string => {
  const trimmed = pathname.trim().toLowerCase()
  return trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed
}

export const PRIVACY_PATH = '/privacy'
export const INVITE_PATH = '/invite'
export const NOT_FOUND_PAGE = 'not-found'

export const isPrivacyPath = (pathname: string): boolean => normalize(pathname) === PRIVACY_PATH

export const isLibraryPath = (p: string): boolean => p === LIBRARY_BASE || p.startsWith(LIBRARY_BASE + '/')

export const isInvitePath = (pathname: string): boolean => normalize(pathname) === INVITE_PATH

// Collapses to the three real pages so a visitor-typed path never becomes a transaction name.
export const landingRouteName = (pathname: string): string =>
  pathname === '/' ? '/' : isPrivacyPath(pathname) ? '/privacy' : isInvitePath(pathname) ? '/invite' : '<unmatched>'
