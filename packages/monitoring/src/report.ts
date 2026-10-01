export interface ApiFailureInput {
  kind: 'network' | 'http' | 'malformed'
  status: number | null
  method: string
  url: string
  error: unknown
}

// Red stub: SENTRY-06-03 replaces this.
export function captureApiFailure(_f: ApiFailureInput): void {}
