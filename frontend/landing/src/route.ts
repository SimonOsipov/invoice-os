// Pure/total: trim, lowercase, strip one trailing slash, exact-match only.
export function isPrivacyPath(pathname: string): boolean {
  const trimmed = pathname.trim().toLowerCase()
  const normalized = trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed
  return normalized === '/privacy'
}
