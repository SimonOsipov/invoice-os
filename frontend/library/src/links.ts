import { APP_PATHS } from './appPaths.ts'
import type { Feature, Group } from './types.ts'

const resolveBase = (v: string | undefined): string | null => {
  const t = (v ?? '').trim().replace(/\/+$/, '')
  return t === '' ? null : t
}

export const appBase = () => resolveBase(import.meta.env.VITE_APP_URL)
export const landingBase = () => resolveBase(import.meta.env.VITE_LANDING_URL)

export function platformHref(path: string): string | null {
  const base = appBase()
  return base === null ? null : `${base}${path}?via=library`
}

export function featurePlatformHref(f: Feature): string | null {
  return f.status === 'soon' ? null : platformHref(f.path)
}

export function groupPlatformHref(g: Group): string | null {
  return g.feats.some((f) => f.status === 'shipped') ? platformHref(APP_PATHS[g.view]) : null
}

export function demoHref(): string | null {
  const base = landingBase()
  return base === null ? null : `${base}/?demo`
}
