// Mirrors releaseName in internal/platform/config.go.
export function releaseName(buildSha: string, platformSha: string): string {
  const build = buildSha.trim()
  const platform = platformSha.trim()
  if (build !== '' && build !== 'dev') return build
  return platform !== '' ? `unstamped-${platform}` : 'unstamped'
}
