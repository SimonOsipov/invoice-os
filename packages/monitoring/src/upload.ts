/// <reference types="node" />

export interface SourcemapUploadOptions {
  authToken: string
  project: string
  url: string
  telemetry: false
  release: { name: string; inject: false; setCommits: false }
  sourcemaps: { filesToDeleteAfterUpload: string[] }
}

// QA stub: red until SENTRY-08-01 is implemented.
export function sourcemapUploadOptions(
  _appDir: string,
  _env: Record<string, string | undefined> = process.env,
): SourcemapUploadOptions {
  return {
    authToken: 'stub',
    project: '',
    url: '',
    telemetry: true as never,
    release: { name: '', inject: true as never, setCommits: true as never },
    sourcemaps: { filesToDeleteAfterUpload: [] },
  }
}
