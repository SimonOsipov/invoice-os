import sha from '../../../internal/platform/buildsha.txt?raw'

// Mirrors releaseName in internal/platform/config.go.
export function releaseName(buildSha: string, railwaySha: string): string {
  const build = buildSha.trim()
  const railway = railwaySha.trim()
  if (build !== '' && build !== 'dev') return build
  return railway !== '' ? `unstamped-${railway}` : 'unstamped'
}

export const RELEASE = releaseName(sha, import.meta.env.VITE_RAILWAY_GIT_COMMIT_SHA ?? '')
