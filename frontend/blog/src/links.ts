const LANDING_FALLBACK = 'https://www.ascomply.com'

export function landingHref(): string {
  const base = (import.meta.env.VITE_LANDING_URL ?? '').trim().replace(/\/+$/, '')
  return base === '' ? LANDING_FALLBACK : base
}

export function privacyHref(): string {
  return `${landingHref()}/privacy`
}
