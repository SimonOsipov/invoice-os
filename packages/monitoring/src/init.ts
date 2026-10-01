import type { Service } from './options'

// Red stub: SENTRY-06-03 replaces this.
export function initMonitoring(_service: Service, _opts?: { gateway?: string | null; routeName?: (p: string) => string }): boolean {
  return false
}
