import { LIBRARY_BASE } from './sitePaths'

// Caddy decodes and collapses the path before matching files; mirror it so a 200 never renders the home page.
const decode = (s: string): string => {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

// Pure/total: trim, decode, lowercase, collapse every slash run, strip one trailing slash, exact-match only.
const normalize = (pathname: string): string => {
  const trimmed = decode(pathname.trim()).toLowerCase().replace(/\/{2,}/g, '/')
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
