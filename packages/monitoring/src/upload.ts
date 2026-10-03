/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { releaseName } from './release-name'

export interface SourcemapUploadOptions {
  authToken: string
  project: string
  url: string
  telemetry: false
  release: { name: string; inject: false; setCommits: false }
  sourcemaps: { filesToDeleteAfterUpload: string[] }
}

// Node-only: SPA vite configs import this by relative path; keep it out of index.ts.
export function sourcemapUploadOptions(
  appDir: string,
  env: Record<string, string | undefined> = process.env,
): SourcemapUploadOptions {
  const stamp = readFileSync(join(appDir, '../../internal/platform/buildsha.txt'), 'utf8')
  return {
    authToken: (env.SENTRY_AUTH_TOKEN ?? '').trim(),
    project: 'asc-frontend',
    url: 'https://de.sentry.io',
    telemetry: false,
    release: {
      name: releaseName(stamp, env.VITE_RAILWAY_GIT_COMMIT_SHA ?? ''),
      inject: false,
      setCommits: false,
    },
    sourcemaps: { filesToDeleteAfterUpload: [join(appDir, 'dist', '**', '*.map')] },
  }
}
